// Package generic is the cloud-hosts adapter for hosts without a standard API: free machines and small hosted
// servers. Each host is a line of configuration: webhooks the user wired up for power and renewal, a TCP or HTTP
// probe that says whether it is up, a console link, and the dates its service ends or renews.
package generic

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nwverse/persona-extensions/extensions/cloud-hosts/internal/core"
	"github.com/nwverse/persona/sdk/go/wire"
)

// The custom actions of this provider, as the manifest declares them: renew (confirm) and probe (read).
const (
	CustomRenew wire.CloudCustomActionID = "renew"
	CustomProbe wire.CloudCustomActionID = "probe"
)

// Account is the one account of the custom hosts.
const Account wire.CloudAccountID = "custom"

// listBudget bounds the probes of one cloud.instances (whose own bound is 20 s): a host not probed by then is
// answered as unknown.
const listBudget = 16 * time.Second

// hookTimeout bounds one webhook call, under cloud.action's 30 s.
const hookTimeout = 25 * time.Second

// minToken is the shortest webhook token accepted: a shorter one could not be told apart from ordinary words, so the
// answers could not be kept free of it.
const minToken = 8

// Config is the custom hosts' configuration.
type Config struct {
	// Hosts are the hosts, one per string: semicolon-separated key=value pairs (keys: see parseHost),
	// e.g. "name=free-vps; address=203.0.113.10; probe=tcp:22; start=/hooks/free-vps/start".
	Hosts []string
	// WebhookBase is an optional https address every relative webhook path is resolved against. It is what the
	// sandbox must let the program reach. A plain http base is the user's explicit choice and also allows http
	// webhooks.
	WebhookBase string
	// WebhookToken is an optional secret sent as "Authorization: Bearer <token>" to every webhook: at least
	// minToken characters, so the redaction of every answer can recognise it.
	WebhookToken string
	// DataDir is the directory of the state file (renewed dates, last successful probe); "" keeps it in memory.
	DataDir string
	// HTTP is the client of the webhooks (nil = core.NewHTTPClient). HTTP probes keep its transport's TLS settings.
	HTTP *http.Client
	// Now is the clock (nil = time.Now).
	Now func() time.Time
	// Dial opens the probes' connections. nil is the default: through the HTTP proxy that HTTPS_PROXY / HTTP_PROXY
	// name (an HTTP CONNECT, honouring NO_PROXY), directly when none is set. Tests inject their own.
	Dial func(ctx context.Context, network, addr string) (net.Conn, error)
}

type adapter struct {
	hosts       []*host
	byName      map[string]*host
	token       string
	http        *http.Client
	probeClient *http.Client
	dial        DialFunc
	now         func() time.Time
	store       *store
	secrets     []string
	renewSlot   map[string]chan struct{} // one renewal of a host at a time: a slot of capacity 1 per host
}

// renewWait is how long a renewal waits for another renewal of the same host to finish before it answers
// invalid_state: short, so a second renewal never runs past the hub's bound waiting for a slow first one.
const renewWait = time.Second

// New is the custom hosts' adapter. A host that cannot be read is a configuration error naming the host and the
// key; no hosts at all is not_signed_in (nothing is set up).
func New(c Config) (core.Adapter, error) {
	base, err := parseBase(c.WebhookBase)
	if err != nil {
		return nil, err
	}
	var specs []string
	for _, s := range c.Hosts {
		if strings.TrimSpace(s) != "" {
			specs = append(specs, s)
		}
	}
	if len(specs) == 0 {
		return nil, core.NotSignedIn("custom hosts: no hosts are configured")
	}
	if len(specs) > MaxHosts {
		return nil, core.Failed("custom hosts: at most %d hosts can be configured", MaxHosts)
	}
	a := &adapter{byName: map[string]*host{}, renewSlot: map[string]chan struct{}{}, token: strings.TrimSpace(c.WebhookToken), http: c.HTTP, now: c.Now}
	if strings.ContainsFunc(a.token, func(r rune) bool { return r < ' ' || r == 0x7f }) {
		return nil, core.NotSignedIn("custom hosts: the webhook token is not usable")
	}
	if a.token != "" && len(a.token) < minToken {
		return nil, core.Failed("custom hosts: the webhook token must be at least %d characters long", minToken)
	}
	for i, s := range specs {
		h, err := parseHost(i+1, s, base)
		if err != nil {
			return nil, err
		}
		if a.byName[h.name] != nil {
			return nil, cfgError(fmt.Sprintf("%q", h.name), "name", "is used by another host")
		}
		a.byName[h.name] = h
		a.renewSlot[h.name] = make(chan struct{}, 1)
		a.hosts = append(a.hosts, h)
	}
	pub := public(a.hosts)
	for _, h := range a.hosts {
		for _, u := range h.hooks {
			a.secrets = append(a.secrets, urlSecrets(u, pub)...)
		}
		a.secrets = append(a.secrets, urlSecrets(h.renewHook, pub)...)
		if h.probe != nil && !h.probe.tcp {
			if u, err := url.Parse(h.probe.target); err == nil && u.RawQuery != "" {
				a.secrets = append(a.secrets, urlSecrets(u, pub)...)
			}
		}
	}
	if a.token != "" {
		a.secrets = append(a.secrets, a.token)
	}
	a.secrets = core.CleanList(a.secrets)
	sort.SliceStable(a.secrets, func(i, j int) bool { return len(a.secrets[i]) > len(a.secrets[j]) })
	if a.http == nil {
		a.http = core.NewHTTPClient(core.HTTPOptions{Timeout: hookTimeout})
	}
	if a.now == nil {
		a.now = time.Now
	}
	if c.Dial != nil {
		a.dial = c.Dial
	} else {
		a.dial = proxyDialer(http.ProxyFromEnvironment)
	}
	a.probeClient = newProbeClient(a.http, a.dial)
	a.store = openStore(c.DataDir)
	return a, nil
}

// SecretValues are the webhook token and every webhook URL with its path, query values of at least 8 characters and
// token-like path segments (a webhook's URL may carry its own token), and a probe URL's when it has a query.
func (a *adapter) SecretValues() []string { return append([]string(nil), a.secrets...) }

// lookup is the host ref names. An id that is no host's name (one with a slash, a percent sign or a space cannot be)
// is not_found without any request.
func (a *adapter) lookup(ref wire.CloudInstanceRef, locale string) (*host, error) {
	if ref.Account != Account {
		return nil, core.NotFound("%s", core.T(locale, "There is no such account among the custom hosts.", "自定义主机中没有这个账号。"))
	}
	h := a.byName[string(ref.ID)]
	if h == nil {
		return nil, core.NotFound("%s", core.T(locale, "There is no such custom host.", "没有这台自定义主机。"))
	}
	return h, nil
}

// Instances probes every host now, 16 at a time.
func (a *adapter) Instances(ctx context.Context) ([]wire.CloudInstance, []string, error) {
	loc := core.Locale(ctx)
	ctx, cancel := context.WithTimeout(ctx, listBudget)
	defer cancel()
	out := make([]wire.CloudInstance, len(a.hosts))
	sem := make(chan struct{}, probeConcurrency)
	var wg sync.WaitGroup
	var skipped atomic.Int32
	for i, h := range a.hosts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var r probeResult
			select {
			case sem <- struct{}{}:
				r = a.probe(ctx, h)
				<-sem
			case <-ctx.Done():
				r = probeResult{outcome: probeSkipped}
				if h.probe == nil {
					r.outcome = probeNone
				}
			}
			if r.outcome == probeSkipped {
				skipped.Add(1)
			}
			out[i] = a.instance(loc, h, r)
		}()
	}
	wg.Wait()
	a.store.flush()
	// Hosts the budget left unprobed are listed as unknown; the list says it is incomplete, so it is not kept.
	if n := skipped.Load(); n > 0 {
		return out, []string{core.T(loc, fmt.Sprintf("%s (%d hosts not probed in time)", Account, n),
			fmt.Sprintf("%s（%d 台主机未能及时探测）", Account, n))}, nil
	}
	return out, nil, nil
}

// skippedDetail is the state detail of a host the call's budget left unprobed.
const skippedDetail = "It was not probed in time."

// Instance probes one host now.
func (a *adapter) Instance(ctx context.Context, ref wire.CloudInstanceRef) (wire.CloudInstance, error) {
	loc := core.Locale(ctx)
	h, err := a.lookup(ref, loc)
	if err != nil {
		return wire.CloudInstance{}, err
	}
	in := a.instance(loc, h, a.probe(ctx, h))
	a.store.flush()
	return in, nil
}

// Snapshots: custom hosts have none.
func (a *adapter) Snapshots(ctx context.Context, ref wire.CloudInstanceRef) ([]wire.CloudSnapshot, error) {
	loc := core.Locale(ctx)
	if _, err := a.lookup(ref, loc); err != nil {
		return nil, err
	}
	return nil, core.Unsupported("%s", core.T(loc, "Custom hosts have no snapshots.", "自定义主机没有快照。"))
}

// dates are h's end and renewal dates: the configured ones, or the state file's when later.
func (a *adapter) dates(h *host) (expires, renews time.Time, st hostState) {
	st = a.store.get(h.name)
	expires, renews = h.expires, h.renews
	if st.Expires.After(expires) {
		expires = st.Expires
	}
	if st.Renews.After(renews) {
		renews = st.Renews
	}
	return expires, renews, st
}

func (h *host) canRenew(expires, renews time.Time) bool {
	return h.renewHook != nil || !expires.IsZero() || !renews.IsZero()
}

// instance is h as the probe r found it, its state detail in loc's language.
func (a *adapter) instance(loc string, h *host, r probeResult) wire.CloudInstance {
	if r.outcome == probeAlive || r.outcome == probeAnswered {
		a.store.alive(h.name, r.at, h.keepaliveDays > 0)
	}
	expires, renews, st := a.dates(h)
	in := wire.CloudInstance{
		Account:     Account,
		ID:          wire.CloudInstanceID(h.name),
		DisplayName: h.title,
		Region:      h.region,
		Zone:        h.zone,
		ConsoleURL:  h.console,
		ObservedAt:  a.now(),
	}
	if in.DisplayName == "" {
		in.DisplayName = h.name
	}
	target := h.address
	if h.probe != nil {
		target = h.probe.host
	}
	switch r.outcome {
	case probeAlive:
		in.State = wire.CloudInstanceStateRunning
	case probeAnswered:
		in.State = wire.CloudInstanceStateRunning
		if r.tlsBad {
			in.StateDetail = core.T(loc, "It answered the probe, but its TLS certificate was not accepted.",
				"它响应了探测，但它的 TLS 证书未通过验证。")
		} else {
			in.StateDetail = core.T(loc, "It accepted the probe's connection, but gave no usable HTTP answer.",
				"它接受了探测的连接，但没有给出可用的 HTTP 响应。")
		}
	case probeHTTPError:
		in.State = wire.CloudInstanceStateUnknown
		in.StateDetail = core.T(loc, fmt.Sprintf("The probe got HTTP %d: something answers, but whether the host itself is up is not known.", r.status),
			fmt.Sprintf("探测收到 HTTP %d：有服务在响应，但无法确定主机本身是否正常。", r.status))
	case probeRefused:
		in.State = wire.CloudInstanceStateStopped
		in.StateDetail = core.T(loc, "It did not answer the probe: the connection was refused.", "它没有响应探测：连接被拒绝。")
	case probeSlow:
		in.State = wire.CloudInstanceStateStopped
		in.StateDetail = core.T(loc, "It did not answer the probe in time.", "它没有及时响应探测。")
	case probeForbidden:
		in.State = wire.CloudInstanceStateUnknown
		in.StateDetail = core.T(loc, "The sandbox does not let this extension reach "+target+": allow that host in the extension's network permissions.",
			"沙盒不允许这个扩展访问 "+target+"，请在扩展的网络权限中允许该主机。")
	case probeNoProxy:
		in.State = wire.CloudInstanceStateUnknown
		in.StateDetail = core.T(loc, "It could not be probed: the network proxy could not be used.", "无法探测：网络代理不可用。")
	case probeSkipped:
		in.State = wire.CloudInstanceStateUnknown
		in.StateDetail = core.T(loc, skippedDetail, "未能及时探测。")
	default:
		in.State = wire.CloudInstanceStateUnknown
		in.StateDetail = core.T(loc, "No probe is configured, so its state is not known.", "没有配置探测，所以不知道它的状态。")
	}
	if ip := core.PlainIP(h.address); ip != "" {
		if isPrivate(ip) {
			in.PrivateIps = append(in.PrivateIps, ip)
		} else {
			in.PublicIps = append(in.PublicIps, ip)
		}
	}
	if h.private != "" {
		in.PrivateIps = append(in.PrivateIps, h.private)
	}
	spec := wire.CloudSpec{Size: h.size, Cpu: h.cpu, MemoryMb: h.memoryMB, DiskGb: h.diskGB, Arch: h.arch, OS: h.os}
	if spec != (wire.CloudSpec{}) {
		in.Spec = &spec
	}
	lc := wire.CloudLifecycle{Spot: h.spot, AutoRenew: h.autoRenew, ExpiresAt: expires, RenewsAt: renews}
	if h.keepaliveDays > 0 && !st.LastAlive.IsZero() {
		lc.KeepaliveAt = st.LastAlive.AddDate(0, 0, h.keepaliveDays)
	}
	if lc != (wire.CloudLifecycle{}) {
		in.Lifecycle = &lc
	}
	for _, k := range powerKinds {
		if h.hooks[k] == nil {
			continue
		}
		switch in.State {
		case wire.CloudInstanceStateRunning:
			if k == wire.CloudActionKindStart {
				continue
			}
		case wire.CloudInstanceStateStopped:
			if k != wire.CloudActionKindStart {
				continue
			}
		}
		in.Actions = append(in.Actions, k)
	}
	in.Actions = append(in.Actions, wire.CloudActionKindSyncIp)
	if h.console != "" {
		in.Actions = append(in.Actions, wire.CloudActionKindConsole)
	}
	if h.probe != nil {
		in.CustomActions = append(in.CustomActions, CustomProbe)
	}
	if h.canRenew(expires, renews) {
		in.CustomActions = append(in.CustomActions, CustomRenew)
	}
	return in
}

// Action calls the webhook of a power action, renews, or probes.
func (a *adapter) Action(ctx context.Context, act core.Action) (wire.CloudActionResult, error) {
	h, err := a.lookup(act.Ref, act.Locale)
	if err != nil {
		return wire.CloudActionResult{}, err
	}
	switch act.Kind {
	case wire.CloudActionKindStart, wire.CloudActionKindStop, wire.CloudActionKindReboot,
		wire.CloudActionKindHardReset, wire.CloudActionKindForceStop:
		u := h.hooks[act.Kind]
		if u == nil {
			return wire.CloudActionResult{}, core.Unsupported("%s", core.T(act.Locale,
				"No webhook is configured for this action.", "这个操作没有配置 Webhook。"))
		}
		ans, err := a.callHook(ctx, h, string(act.Kind), u, act.RequestID, act.Locale)
		if err != nil {
			return wire.CloudActionResult{}, err
		}
		if ans.refused {
			return wire.CloudActionResult{Status: wire.CloudActionStatusRefused, Message: ans.message}, nil
		}
		return wire.CloudActionResult{Status: wire.CloudActionStatusAccepted, Message: ans.message}, nil
	case wire.CloudActionKindCustom:
		switch act.Custom {
		case CustomRenew:
			return a.renew(ctx, h, act)
		case CustomProbe:
			return a.probeAction(ctx, h, act)
		}
	}
	return wire.CloudActionResult{}, core.Unsupported("%s", core.T(act.Locale,
		"Custom hosts do not support this action.", "自定义主机不支持这个操作。"))
}

// probeAction probes now and says what it found.
func (a *adapter) probeAction(ctx context.Context, h *host, act core.Action) (wire.CloudActionResult, error) {
	if h.probe == nil {
		return wire.CloudActionResult{}, core.Unsupported("%s", core.T(act.Locale,
			"No probe is configured for this host.", "这台主机没有配置探测。"))
	}
	r := a.probe(ctx, h)
	in := a.instance(act.Locale, h, r)
	a.store.flush()
	ms := r.rtt.Milliseconds()
	var msg string
	switch r.outcome {
	case probeAlive:
		msg = core.T(act.Locale, fmt.Sprintf("Reachable: it answered in %d ms.", ms), fmt.Sprintf("可以连通：%d 毫秒内响应。", ms))
	case probeAnswered:
		if r.tlsBad {
			msg = core.T(act.Locale, fmt.Sprintf("Reachable: it answered in %d ms, but its TLS certificate was not accepted.", ms),
				fmt.Sprintf("可以连通：%d 毫秒内响应，但它的 TLS 证书未通过验证。", ms))
		} else {
			msg = core.T(act.Locale, fmt.Sprintf("Reachable: it accepted the connection in %d ms, but gave no usable HTTP answer.", ms),
				fmt.Sprintf("可以连通：%d 毫秒内接受了连接，但没有给出可用的 HTTP 响应。", ms))
		}
	case probeHTTPError:
		msg = core.T(act.Locale, fmt.Sprintf("It answered HTTP %d after %d ms: something answers, but whether the host itself is up is not known.", r.status, ms),
			fmt.Sprintf("%d 毫秒后返回 HTTP %d：有服务在响应，但无法确定主机本身是否正常。", ms, r.status))
	case probeRefused:
		msg = core.T(act.Locale, "Not reachable: the connection was refused.", "无法连通：连接被拒绝。")
	case probeSlow:
		msg = core.T(act.Locale, fmt.Sprintf("Not reachable: no answer within %d s.", int(probeTimeout/time.Second)),
			fmt.Sprintf("无法连通：%d 秒内没有响应。", int(probeTimeout/time.Second)))
	case probeForbidden:
		msg = core.T(act.Locale, "Not probed: the sandbox does not let this extension reach "+h.probe.host+"; allow that host in the extension's network permissions.",
			"未能探测：沙盒不允许这个扩展访问 "+h.probe.host+"，请在扩展的网络权限中允许该主机。")
	case probeSkipped:
		msg = core.T(act.Locale, "Not probed: the request ended before the probe could run.", "未能探测：请求在探测完成前已结束。")
	default:
		msg = core.T(act.Locale, "Not probed: the network proxy could not be used.", "未能探测：网络代理不可用。")
	}
	return wire.CloudActionResult{Status: wire.CloudActionStatusDone, Instance: &in, Message: msg}, nil
}

// renew calls the renew webhook (when there is one), then moves the dates forward by the host's term. Renewals of a
// host run one at a time (one that cannot start within renewWait answers invalid_state before calling the webhook
// or moving a date), and the state file keeps the request id of the last one applied: the same request id
// again (after the extension restarted and the Provider forgot it) answers the dates it left without moving them or
// calling the webhook again.
func (a *adapter) renew(ctx context.Context, h *host, act core.Action) (wire.CloudActionResult, error) {
	slot := a.renewSlot[h.name]
	wait, cancel := context.WithTimeout(ctx, renewWait)
	select {
	case slot <- struct{}{}:
		cancel()
	case <-wait.Done():
		cancel()
		return wire.CloudActionResult{}, core.InvalidState("%s", core.T(act.Locale,
			"Another renewal of this server is still running.", "这台服务器的另一次续期还在进行。"))
	}
	defer func() { <-slot }()
	expires, renews, st := a.dates(h)
	if !h.canRenew(expires, renews) {
		return wire.CloudActionResult{}, core.Unsupported("%s", core.T(act.Locale,
			"This host has neither dates nor a renew webhook.", "这台主机既没有日期，也没有续期 Webhook。"))
	}
	if act.RequestID != "" && st.LastRenewal == string(act.RequestID) {
		msg := renewedMessage(act.Locale, expires, renews)
		in := a.instance(act.Locale, h, a.probe(ctx, h))
		a.store.flush()
		return wire.CloudActionResult{Status: wire.CloudActionStatusDone, Instance: &in, Message: msg}, nil
	}
	var hookMsg string
	if h.renewHook != nil {
		ans, err := a.callHook(ctx, h, string(CustomRenew), h.renewHook, act.RequestID, act.Locale)
		if err != nil {
			return wire.CloudActionResult{}, err
		}
		if ans.refused {
			return wire.CloudActionResult{Status: wire.CloudActionStatusRefused, Message: ans.message}, nil
		}
		hookMsg = ans.message
	}
	now := a.now().UTC()
	move := func(t time.Time) time.Time {
		if t.IsZero() {
			return t
		}
		if now.After(t) {
			t = now
		}
		return t.AddDate(0, 0, h.termDays)
	}
	newExp, newRen := move(expires), move(renews)
	saveErr := a.store.update(h.name, func(st *hostState) {
		if newExp.After(st.Expires) {
			st.Expires = newExp
		}
		if newRen.After(st.Renews) {
			st.Renews = newRen
		}
		st.LastRenewal = string(act.RequestID)
	})
	msg := renewedMessage(act.Locale, newExp, newRen)
	if saveErr != nil {
		msg += core.T(act.Locale, " The new dates could not be saved and are kept until the extension restarts.",
			"新日期未能保存，扩展重启前有效。")
	}
	if hookMsg != "" {
		msg += " " + hookMsg
	}
	in := a.instance(act.Locale, h, a.probe(ctx, h))
	a.store.flush()
	return wire.CloudActionResult{Status: wire.CloudActionStatusDone, Instance: &in, Message: msg}, nil
}

// renewedMessage names the end date a renewal left (or the next renewal, without an end date).
func renewedMessage(locale string, expires, renews time.Time) string {
	if expires.IsZero() && renews.IsZero() {
		return core.T(locale, "The renewal was requested.", "已请求续期。")
	}
	if expires.IsZero() {
		day := renews.UTC().Format("2006-01-02")
		return core.T(locale, "Renewed: the next renewal is on "+day+".", "已续期：下次续费日为 "+day+"。")
	}
	day := expires.UTC().Format("2006-01-02")
	return core.T(locale, "Renewed: the service now ends on "+day+".", "已续期：服务到期日为 "+day+"。")
}

// hookAnswer is what a webhook said in a 2xx answer.
type hookAnswer struct {
	message string
	refused bool
}

// callHook asks a webhook. Its URL never appears in what it answers: errors name the action and the status, plus
// the webhook's own words (unclipped: the Provider removes the adapter's secrets from them, then clips) except when
// it refused the token or said the address does not exist, where its words are the likeliest to echo the URL or the
// token back.
//
// Only a 429 (and a connection that was never made, as core.Do says) is transient: after a 5xx or a timeout the
// webhook may have acted, so the Provider keeps the failure as the request id's answer.
func (a *adapter) callHook(ctx context.Context, h *host, action string, u *url.URL, reqID wire.CloudRequestID, locale string) (hookAnswer, error) {
	ctx, cancel := context.WithTimeout(ctx, hookTimeout)
	defer cancel()
	var body []byte
	if h.method != http.MethodGet {
		body, _ = json.Marshal(struct {
			Action    string `json:"action"`
			Host      string `json:"host"`
			RequestID string `json:"request_id"`
		}{action, h.name, string(reqID)})
	}
	req, err := http.NewRequestWithContext(ctx, h.method, u.String(), bytes.NewReader(body))
	if err != nil {
		return hookAnswer{}, core.Failed("%s", core.T(locale, fmt.Sprintf("The %s webhook is not a usable URL.", action),
			fmt.Sprintf("%s Webhook 不是可用的 URL。", action)))
	}
	if body == nil {
		req.Body, req.ContentLength = http.NoBody, 0
	} else {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	if reqID != "" {
		req.Header.Set("Idempotency-Key", string(reqID))
	}
	if a.token != "" {
		req.Header.Set("Authorization", "Bearer "+a.token)
	}
	resp, err := core.Do(ctx, a.http, req)
	if err != nil {
		return hookAnswer{}, err
	}
	var j struct {
		Message any `json:"message"`
		Error   any `json:"error"`
		Status  any `json:"status"`
	}
	_ = json.Unmarshal(resp.Body, &j)
	str := func(v any) string {
		s, _ := v.(string)
		return core.OneLine(s, 0)
	}
	if resp.OK() {
		ans := hookAnswer{message: str(j.Message)}
		if strings.EqualFold(str(j.Status), "refused") {
			ans.refused = true
			if ans.message == "" {
				ans.message = core.T(locale, "The webhook refused the request.", "Webhook 拒绝了这个请求。")
			}
		}
		return ans, nil
	}
	detail := str(j.Message)
	if detail == "" {
		detail = str(j.Error)
	}
	switch resp.Status {
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusGone:
		detail = ""
	}
	// say is the adapter's own words for what happened (en and zh, with %s the action and %d the status), followed
	// by the webhook's own words as it wrote them.
	say := func(en, zh string) string {
		msg := core.T(locale, fmt.Sprintf(en, action, resp.Status), fmt.Sprintf(zh, action, resp.Status))
		if detail != "" {
			msg += " " + detail
		}
		return msg
	}
	switch s := resp.Status; {
	case s == http.StatusUnauthorized || s == http.StatusForbidden:
		return hookAnswer{}, core.NotSignedIn("%s", say("The %s webhook did not accept the webhook token (HTTP %d).",
			"%s Webhook 不接受 Webhook 令牌（HTTP %d）。"))
	case s == http.StatusNotFound || s == http.StatusGone:
		return hookAnswer{}, core.Failed("%s", say("The %s webhook does not exist at the configured address (HTTP %d).",
			"%s Webhook 在配置的地址上不存在（HTTP %d）。"))
	case s == http.StatusConflict || s == http.StatusLocked:
		return hookAnswer{}, core.InvalidState("%s", say("The %s webhook refused it in the host's current state (HTTP %d).",
			"%s Webhook 因主机当前的状态拒绝了请求（HTTP %d）。"))
	case s == http.StatusTooManyRequests:
		return hookAnswer{}, core.RateLimited("%s", say("The %s webhook asked to slow down (HTTP %d).",
			"%s Webhook 要求放慢请求（HTTP %d）。"))
	case s == http.StatusServiceUnavailable && strings.Contains(strings.ToLower(string(resp.Body)), "capacity"):
		return hookAnswer{}, core.Capacity("%s", say("The %s webhook has no capacity for it now (HTTP %d).",
			"%s Webhook 当前没有容量（HTTP %d）。"))
	}
	return hookAnswer{}, core.Failed("%s", say("The %s webhook failed (HTTP %d).", "%s Webhook 调用失败（HTTP %d）。"))
}
