// Package virtualizor speaks for Virtualizor panels through their end-user API: the panel login's servers, their
// power and their addresses.
//
// The end-user API takes its key and password in the query string, by the panel's design. So no request address
// ever goes into an error or a message here (core.Do names the host only), and both are registered as secrets.
package virtualizor

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/nwverse/persona-extensions/extensions/cloud-hosts/internal/core"
	"github.com/nwverse/persona/sdk/go/wire"
)

// The list is read in pages of listPage, at most listPages of them.
const (
	listPage  = 50
	listPages = 10
)

// Config configures the Virtualizor end-user API adapter.
type Config struct {
	// BaseURL is the end-user panel's address with its port, for example https://panel.example.com:4083. The API is
	// <BaseURL>/index.php, and console links open <BaseURL> itself (when it is https).
	BaseURL string
	// Credentials is the secret text: "apikey:apipass" for one panel login (the account "default"), or one
	// "name=apikey:apipass" line per login.
	Credentials string
	// PinSHA256 is the panel certificate's SHA-256 fingerprint, for a self-signed panel. Used only when HTTP is nil.
	PinSHA256 string
	// HTTP is the client (core.NewHTTPClient when nil). Tests point it at an httptest server.
	HTTP *http.Client
	// Now is the clock (time.Now when nil).
	Now func() time.Time
}

type account struct {
	name, key, pass string
}

type adapter struct {
	base     string
	console  string
	accounts []account
	byName   map[string]account
	http     *http.Client
	now      func() time.Time
	redact   *core.Redactor
}

// New is the Virtualizor adapter for the logins in c.Credentials.
func New(c Config) (core.Adapter, error) {
	tokens := core.ParseNamedTokens(c.Credentials)
	if len(tokens) == 0 {
		return nil, core.NotSignedIn("Virtualizor: no API key and password are set up (\"apikey:apipass\")")
	}
	a := &adapter{base: c.BaseURL, console: core.ConsoleBase(c.BaseURL), byName: map[string]account{},
		http: c.HTTP, now: c.Now, redact: &core.Redactor{}}
	for _, t := range tokens {
		key, pass, ok := strings.Cut(t.Token, ":")
		key, pass = strings.TrimSpace(key), strings.TrimSpace(pass)
		if !ok || key == "" || pass == "" {
			return nil, core.NotSignedIn("Virtualizor: the credentials of %s need both an API key and an API password (\"apikey:apipass\")", t.Account)
		}
		acct := account{name: t.Account, key: key, pass: pass}
		a.accounts = append(a.accounts, acct)
		a.byName[acct.name] = acct
	}
	if _, err := core.JoinURL(c.BaseURL, "/index.php", nil); err != nil {
		return nil, core.Failed("the Virtualizor panel address is not a usable URL")
	}
	if a.http == nil {
		pin := strings.TrimSpace(c.PinSHA256)
		if pin != "" && !validPin(pin) {
			return nil, core.Failed("The certificate fingerprint is not a SHA-256 fingerprint (64 hex digits).")
		}
		a.http = core.NewHTTPClient(core.HTTPOptions{PinSHA256: pin})
	}
	if a.now == nil {
		a.now = time.Now
	}
	a.redact.Add(a.SecretValues()...)
	return a, nil
}

// SecretValues are every API key and password.
func (a *adapter) SecretValues() []string {
	var out []string
	for _, acct := range a.accounts {
		out = append(out, acct.key, acct.pass)
	}
	return out
}

// call is one end-user API request: its JSON answer's fields, or the panel's error. A write is POSTed.
func (a *adapter) call(ctx context.Context, acct account, act string, write bool, extra url.Values) (map[string]json.RawMessage, error) {
	q := url.Values{"act": {act}, "api": {"json"}, "apikey": {acct.key}, "apipass": {acct.pass}}
	for k, v := range extra {
		q[k] = v
	}
	loc := core.Locale(ctx)
	u, err := core.JoinURL(a.base, "/index.php", q) // the ids go in the query (url.Values), never in the path
	if err != nil {
		return nil, err
	}
	method := http.MethodGet
	if write {
		method = http.MethodPost
	}
	req, err := http.NewRequest(method, u.String(), nil)
	if err != nil {
		return nil, core.Failed("%s", core.T(loc, "The Virtualizor panel address is not a usable URL.",
			"Virtualizor 面板地址不是可用的网址。"))
	}
	req.Header.Set("Accept", "application/json")
	resp, err := core.Do(ctx, a.http, req)
	if err != nil {
		return nil, err
	}
	var body map[string]json.RawMessage
	if json.Unmarshal(resp.Body, &body) != nil || body == nil {
		lower := strings.ToLower(string(resp.Body))
		switch {
		case resp.Status == http.StatusUnauthorized || resp.Status == http.StatusForbidden ||
			hasAny(lower, "login", "sign in", "password"):
			return nil, signedOut(loc)
		case !resp.OK():
			return nil, core.StatusError("The Virtualizor panel", resp.Status, resp.Reason)
		}
		return nil, core.Failed("%s", core.T(loc, "The Virtualizor panel's answer could not be read.",
			"无法读取 Virtualizor 面板的回复。"))
	}
	// The panel says "not logged in" with act=login, or with a negative uid (its own client script turns uid < 0
	// into the login page), whatever act it was asked.
	if s := text(body["act"]); strings.EqualFold(s, "login") || negative(text(body["uid"])) {
		return nil, signedOut(loc)
	}
	if msgs := messages(body["error"]); len(msgs) > 0 {
		e := vzError(strings.Join(msgs, " "), write)
		e.Message = a.redact.Redact(e.Message)
		return nil, e
	}
	if !resp.OK() {
		return nil, core.StatusError("The Virtualizor panel", resp.Status, resp.Reason)
	}
	return body, nil
}

func signedOut(locale string) *core.Error {
	return core.NotSignedIn("%s", core.T(locale, "Virtualizor did not accept the API key and password.",
		"Virtualizor 不接受这个 API 密钥和密码。"))
}

func noServer(locale string) *core.Error {
	return core.NotFound("%s", core.T(locale, "No such Virtualizor server.", "没有这台 Virtualizor 服务器。"))
}

// text is a JSON string or number as text ("" otherwise).
func text(raw json.RawMessage) string {
	var f flexString
	if len(raw) == 0 || json.Unmarshal(raw, &f) != nil {
		return ""
	}
	return strings.TrimSpace(string(f))
}

// negative reports whether s is a number below zero.
func negative(s string) bool {
	v, err := strconv.ParseFloat(s, 64)
	return err == nil && v < 0
}

// messages are the texts of the panel's "error" field: a list or a map of messages (in key order), or one message.
func messages(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}
	var out []string
	var list []json.RawMessage
	var obj map[string]json.RawMessage
	switch {
	case json.Unmarshal(raw, &list) == nil:
		for _, r := range list {
			if s := text(r); s != "" {
				out = append(out, s)
			}
		}
	case json.Unmarshal(raw, &obj) == nil:
		keys := make([]string, 0, len(obj))
		for k := range obj {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if s := text(obj[k]); s != "" {
				out = append(out, s)
			}
		}
	default:
		if s := text(raw); s != "" && s != "0" && !strings.EqualFold(s, "false") {
			out = append(out, s)
		}
	}
	return out
}

// truthy reports whether a JSON value says yes: true, a number or text other than 0, or a non-empty object or list.
func truthy(raw json.RawMessage) bool {
	s := strings.TrimSpace(string(raw))
	switch s {
	case "", "null", "false", "0", `"0"`, `""`, "[]", "{}", `"false"`:
		return false
	}
	return true
}

// vzError is the panel's error list as a coded error. The vendor's words are not clipped here: the Provider redacts
// them first and clips after. Rate limiting is the one answer after which the panel certainly did not act
// (core.RateLimited marks it transient).
func vzError(msg string, write bool) *core.Error {
	msg = core.OneLine(msg, 0)
	t := "Virtualizor: " + msg
	l := strings.ToLower(msg)
	switch {
	case hasAny(l, "api key", "apikey", "api pass", "apipass", "authenticat", "login", "not logged", "unauthori"):
		return core.NotSignedIn("%s", t)
	case hasAny(l, "too many", "rate limit", "slow down"):
		return core.RateLimited("%s", t)
	case hasAny(l, "insufficient", "not enough", "no resources", "resources are not available", "quota"):
		return core.Capacity("%s", t)
	case hasAny(l, "already", "suspended", "locked", "is not running", "is offline", "is not online", "is stopped", "in progress"):
		return core.InvalidState("%s", t)
	case hasAny(l, "not found", "does not exist", "invalid vps", "no vps", "not belong", "no such"):
		return core.NotFound("%s", t)
	case write:
		return core.Refused("%s", t)
	}
	return core.Failed("%s", t)
}

// vs is one server as listvs (and vpsmanage's info) describe it.
type vs struct {
	VPSID       flexString      `json:"vpsid"`
	VPSName     flexString      `json:"vps_name"`
	Hostname    flexString      `json:"hostname"`
	OSName      flexString      `json:"os_name"`
	RAM         flexNum         `json:"ram"`   // MB
	Cores       flexNum         `json:"cores"` // virtual CPUs
	Space       flexNum         `json:"space"` // GB
	IPs         json.RawMessage `json:"ips"`
	IP          json.RawMessage `json:"ip"`     // the primary address (or a list), used when ips is empty
	Status      flexString      `json:"status"` // 1 running, 0 off, 2 suspended
	Suspended   flexString      `json:"suspended"`
	NwSuspended flexString      `json:"nw_suspended"`
	Virt        flexString      `json:"virt"`
	Region      flexString      `json:"region"`
}

// overlay is v with the fields o has set.
func (v vs) overlay(o vs) vs {
	set := func(dst *flexString, src flexString) {
		if src != "" {
			*dst = src
		}
	}
	set(&v.VPSID, o.VPSID)
	set(&v.VPSName, o.VPSName)
	set(&v.Hostname, o.Hostname)
	set(&v.OSName, o.OSName)
	set(&v.Status, o.Status)
	set(&v.Suspended, o.Suspended)
	set(&v.NwSuspended, o.NwSuspended)
	set(&v.Virt, o.Virt)
	set(&v.Region, o.Region)
	if o.RAM > 0 {
		v.RAM = o.RAM
	}
	if o.Cores > 0 {
		v.Cores = o.Cores
	}
	if o.Space > 0 {
		v.Space = o.Space
	}
	if len(ipList(o.IPs)) > 0 {
		v.IPs = o.IPs
	}
	if len(ipList(o.IP)) > 0 {
		v.IP = o.IP
	}
	return v
}

// addresses are v's addresses: its ips, else its ip.
func (v vs) addresses() []string {
	if ips := ipList(v.IPs); len(ips) > 0 {
		return ips
	}
	return ipList(v.IP)
}

// ipList are the addresses of an ips field: a list, a map of id to address in id order, or one address.
func ipList(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}
	var one string
	if json.Unmarshal(raw, &one) == nil {
		if one = strings.TrimSpace(one); one == "" {
			return nil
		}
		return []string{one}
	}
	var list []flexString
	if json.Unmarshal(raw, &list) == nil {
		out := make([]string, 0, len(list))
		for _, s := range list {
			out = append(out, string(s))
		}
		return out
	}
	var obj map[string]flexString
	if json.Unmarshal(raw, &obj) != nil {
		return nil
	}
	keys := make([]string, 0, len(obj))
	for k := range obj {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		a, ea := strconv.Atoi(keys[i])
		b, eb := strconv.Atoi(keys[j])
		if ea == nil && eb == nil {
			return a < b
		}
		return keys[i] < keys[j]
	})
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, string(obj[k]))
	}
	return out
}

// flagged reports whether a suspension flag is set.
func flagged(f flexString) bool {
	s := strings.TrimSpace(string(f))
	return s != "" && s != "0" && !strings.EqualFold(s, "false")
}

func (a *adapter) instance(locale string, acct account, v vs) wire.CloudInstance {
	in := wire.CloudInstance{Account: wire.CloudAccountID(acct.name), ID: wire.CloudInstanceID(v.VPSID),
		DisplayName: string(v.Hostname), Region: string(v.Region), ConsoleURL: a.console, ObservedAt: a.now()}
	if in.DisplayName == "" {
		in.DisplayName = string(v.VPSName)
	}
	in.PublicIps, in.PrivateIps = splitIPs(v.addresses())
	spec := wire.CloudSpec{Cpu: float64(v.Cores), MemoryMb: int64(v.RAM), DiskGb: int64(v.Space), OS: string(v.OSName)}
	if spec != (wire.CloudSpec{}) {
		in.Spec = &spec
	}

	sync := wire.CloudActionKindSyncIp
	// 1 online, 0 offline, 2 suspended (the panel's own status list); the newer documentation spells them out.
	status := strings.ToLower(strings.TrimSpace(string(v.Status)))
	suspended := status == "2" || status == "suspended"
	switch status {
	case "1", "online", "running":
		in.State = wire.CloudInstanceStateRunning
	case "0", "offline", "stopped", "2", "suspended":
		in.State = wire.CloudInstanceStateStopped
	default:
		in.State = wire.CloudInstanceStateUnknown
	}
	switch {
	case flagged(v.Suspended) || suspended:
		in.StateDetail = core.T(locale, "Suspended at the panel: it cannot be powered on or off from here.",
			"已在面板上被暂停：无法在这里开机或关机。")
		in.Actions = []wire.CloudActionKind{sync}
	case flagged(v.NwSuspended):
		in.StateDetail = core.T(locale, "Its network is suspended at the panel: it cannot be powered on or off from here.",
			"它的网络已在面板上被暂停：无法在这里开机或关机。")
		in.Actions = []wire.CloudActionKind{sync}
	case in.State == wire.CloudInstanceStateRunning:
		in.Actions = []wire.CloudActionKind{wire.CloudActionKindStop, wire.CloudActionKindReboot, wire.CloudActionKindForceStop, sync}
	case in.State == wire.CloudInstanceStateStopped:
		in.Actions = []wire.CloudActionKind{wire.CloudActionKindStart, sync}
	default:
		if s := string(v.Status); s != "" {
			in.StateDetail = core.T(locale, "The panel reports the state: ", "面板报告的状态：") + core.OneLine(s, 0)
		}
		in.Actions = []wire.CloudActionKind{sync}
	}
	in.Actions = withConsole(in.Actions, in.ConsoleURL)
	return in
}

// servers are the entries of a vs field: a map of vpsid to server, or a list (an empty one is [] in the panel's PHP).
func servers(locale string, raw json.RawMessage) ([]vs, error) {
	if len(raw) == 0 || !truthy(raw) {
		return nil, nil
	}
	var list []vs
	if json.Unmarshal(raw, &list) == nil {
		return list, nil
	}
	var obj map[string]vs
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil, core.Failed("%s", core.T(locale, "The Virtualizor panel's server list could not be read.",
			"无法读取 Virtualizor 面板的服务器列表。"))
	}
	keys := make([]string, 0, len(obj))
	for k := range obj {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		a, _ := strconv.Atoi(keys[i])
		b, _ := strconv.Atoi(keys[j])
		return a < b || (a == b && keys[i] < keys[j])
	})
	for _, k := range keys {
		v := obj[k]
		if v.VPSID == "" {
			v.VPSID = flexString(k)
		}
		list = append(list, v)
	}
	return list, nil
}

// list is every server of acct, at most 500.
func (a *adapter) list(ctx context.Context, acct account) ([]wire.CloudInstance, error) {
	var out []wire.CloudInstance
	seen := map[string]bool{}
	for page := 1; page <= listPages && len(out) < core.MaxInstances; page++ {
		body, err := a.call(ctx, acct, "listvs", false, url.Values{"page": {strconv.Itoa(page)}, "reslen": {strconv.Itoa(listPage)}})
		if err != nil {
			return nil, err
		}
		list, err := servers(core.Locale(ctx), body["vs"])
		if err != nil {
			return nil, err
		}
		fresh := 0
		for _, v := range list {
			id := strings.TrimSpace(string(v.VPSID))
			if !allDigits(id) || seen[id] || len(out) == core.MaxInstances {
				continue
			}
			v.VPSID = flexString(id)
			seen[id] = true
			fresh++
			out = append(out, a.instance(core.Locale(ctx), acct, v))
		}
		if fresh == 0 || !morePages(body["page"], page, len(list)) {
			break
		}
	}
	return out, nil
}

// morePages reports whether listvs has a page after page, which held got entries. The panel's page object
// ({"start", "len", "maxNum"}: offset, page length and number of matching servers) decides when it is there;
// without it, a page shorter than the one asked for is the last.
func morePages(raw json.RawMessage, page, got int) bool {
	var p struct {
		Start  flexNum `json:"start"`
		Len    flexNum `json:"len"`
		MaxNum flexNum `json:"maxNum"`
	}
	if len(raw) > 0 && json.Unmarshal(raw, &p) == nil && p.Len > 0 {
		if p.MaxNum > 0 {
			start := float64(p.Start)
			if start <= 0 {
				start = float64(page-1) * float64(p.Len)
			}
			return start+float64(p.Len) < float64(p.MaxNum)
		}
		return float64(got) >= float64(p.Len)
	}
	return got >= listPage
}

// Instances lists every login's servers, at most eight logins at once.
func (a *adapter) Instances(ctx context.Context) ([]wire.CloudInstance, []string, error) {
	ctx, cancel := budget(ctx, listBudget)
	defer cancel()
	lists := make([][]wire.CloudInstance, len(a.accounts))
	errs := make([]error, len(a.accounts))
	each(len(a.accounts), func(i int) { lists[i], errs[i] = a.list(ctx, a.accounts[i]) })
	var out []wire.CloudInstance
	var problems []string
	var first error
	ok := 0
	for i, err := range errs {
		if err != nil {
			problems = append(problems, a.accounts[i].name)
			if first == nil {
				first = err
			}
			continue
		}
		ok++
		for _, in := range lists[i] {
			if len(out) < core.MaxInstances {
				out = append(out, in)
			}
		}
	}
	if ok == 0 && first != nil {
		return nil, nil, first
	}
	return out, problems, nil
}

// account is the login of ref. A Virtualizor vpsid is a decimal number: any other id (one with a slash, a percent
// sign or a space) is not_found without a request.
func (a *adapter) account(locale string, ref wire.CloudInstanceRef) (account, error) {
	acct, ok := a.byName[string(ref.Account)]
	if !ok || !allDigits(string(ref.ID)) {
		return account{}, noServer(locale)
	}
	return acct, nil
}

// Instance reads one server now (act=vpsmanage).
func (a *adapter) Instance(ctx context.Context, ref wire.CloudInstanceRef) (wire.CloudInstance, error) {
	loc := core.Locale(ctx)
	acct, err := a.account(loc, ref)
	if err != nil {
		return wire.CloudInstance{}, err
	}
	ctx, cancel := budget(ctx, readBudget)
	defer cancel()
	body, err := a.call(ctx, acct, "vpsmanage", false, url.Values{"svs": {string(ref.ID)}})
	if err != nil {
		return wire.CloudInstance{}, err
	}
	var info struct {
		vs
		VPS *vs `json:"vps"`
	}
	raw := body["info"]
	if !truthy(raw) {
		raw = body["vpsinfo"] // the shape of the panel's newer documentation
	}
	if !truthy(raw) || json.Unmarshal(raw, &info) != nil {
		return wire.CloudInstance{}, noServer(loc)
	}
	v := vs{}
	if info.VPS != nil {
		v = *info.VPS
	}
	v = v.overlay(info.vs)
	if id := strings.TrimSpace(string(v.VPSID)); id == "" {
		v.VPSID = flexString(ref.ID)
	} else if id != string(ref.ID) {
		return wire.CloudInstance{}, noServer(loc)
	}
	return a.instance(loc, acct, v), nil
}

// commands are the end-user API's power acts.
var commands = map[wire.CloudActionKind]string{
	wire.CloudActionKindStart:     "start",
	wire.CloudActionKindStop:      "stop",
	wire.CloudActionKindReboot:    "restart",
	wire.CloudActionKindForceStop: "poweroff",
}

// Action sends a power act. The panel's "done" only says it took the request, so it is accepted.
func (a *adapter) Action(ctx context.Context, act core.Action) (wire.CloudActionResult, error) {
	cmd, ok := commands[act.Kind]
	if !ok {
		return wire.CloudActionResult{}, core.Unsupported("%s", core.T(act.Locale,
			"Virtualizor's end-user API cannot do this.", "Virtualizor 用户 API 不支持这个操作。"))
	}
	acct, err := a.account(act.Locale, act.Ref)
	if err != nil {
		return wire.CloudActionResult{}, err
	}
	ctx, cancel := budget(ctx, actionBudget)
	defer cancel()
	body, err := a.call(ctx, acct, cmd, true, url.Values{"svs": {string(act.Ref.ID)}, "do": {"1"}})
	if err != nil {
		return wire.CloudActionResult{}, err
	}
	if !truthy(body["done"]) {
		return wire.CloudActionResult{}, core.Failed("%s", core.T(act.Locale,
			"The Virtualizor panel did not say it took the request.", "Virtualizor 面板没有确认收到这个请求。"))
	}
	return wire.CloudActionResult{Status: wire.CloudActionStatusAccepted}, nil
}

// Snapshots: the end-user API has none this adapter offers.
func (a *adapter) Snapshots(ctx context.Context, _ wire.CloudInstanceRef) ([]wire.CloudSnapshot, error) {
	return nil, core.Unsupported("%s", core.T(core.Locale(ctx), "Virtualizor snapshots are not supported.",
		"不支持 Virtualizor 快照。"))
}
