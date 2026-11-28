package solusvm

import (
	"bytes"
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

// The most pages one list reads.
const (
	v2MaxPages         = 50
	v2MaxSnapshotPages = 10
)

// V2Config configures the SolusVM 2 REST API adapter.
type V2Config struct {
	// BaseURL is the panel's address, for example https://panel.example.com. The API is <BaseURL>/api/v1, and
	// console links open <BaseURL>/servers/<id> (when it is https).
	BaseURL string
	// Token is the secret text: one API token, or several panel logins as one "name=token" per line (the name is
	// the account). A single token is the account "default".
	Token string
	// PinSHA256 is the panel certificate's SHA-256 fingerprint, for a self-signed panel. Used only when HTTP is nil.
	PinSHA256 string
	// HTTP is the client (core.NewHTTPClient when nil). Tests point it at an httptest server.
	HTTP *http.Client
	// Now is the clock (time.Now when nil).
	Now func() time.Time
}

type v2Adapter struct {
	base     string
	console  string
	accounts []core.NamedToken
	byName   map[string]core.NamedToken
	http     *http.Client
	now      func() time.Time
	redact   *core.Redactor
}

// NewV2 is the SolusVM 2 adapter for the tokens in c.Token.
func NewV2(c V2Config) (core.Adapter, error) {
	accounts := core.ParseNamedTokens(c.Token)
	if len(accounts) == 0 {
		return nil, core.NotSignedIn("SolusVM 2: no API token is set up")
	}
	if _, err := core.JoinURL(c.BaseURL, "/api/v1", nil); err != nil {
		return nil, core.Failed("the SolusVM panel address is not a usable URL")
	}
	a := &v2Adapter{base: c.BaseURL, console: core.ConsoleBase(c.BaseURL), accounts: accounts,
		byName: map[string]core.NamedToken{}, http: c.HTTP, now: c.Now, redact: &core.Redactor{}}
	if a.http == nil {
		a.http = core.NewHTTPClient(core.HTTPOptions{PinSHA256: c.PinSHA256})
	}
	if a.now == nil {
		a.now = time.Now
	}
	for _, t := range accounts {
		a.byName[t.Account] = t
	}
	a.redact.Add(a.SecretValues()...)
	return a, nil
}

// SecretValues are the tokens.
func (a *v2Adapter) SecretValues() []string {
	var out []string
	for _, t := range a.accounts {
		out = append(out, t.Token)
	}
	return out
}

// do is one API request; a non-2xx answer is its coded error. path is in its escaped form: every id in it went
// through url.PathEscape.
func (a *v2Adapter) do(ctx context.Context, acct core.NamedToken, method, path string, query url.Values, body any) (*core.Response, error) {
	u, err := core.JoinURL(a.base, "/api/v1/"+strings.TrimLeft(path, "/"), query)
	if err != nil {
		return nil, err
	}
	var rd *bytes.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, core.Failed("%s", core.T(core.Locale(ctx), "The request could not be written.", "无法生成请求。"))
		}
		rd = bytes.NewReader(b)
	}
	var req *http.Request
	if rd != nil {
		req, err = http.NewRequest(method, u.String(), rd)
	} else {
		req, err = http.NewRequest(method, u.String(), nil)
	}
	if err != nil {
		return nil, core.Failed("%s", core.T(core.Locale(ctx), "The SolusVM panel address is not a usable URL.",
			"SolusVM 面板地址不是可用的网址。"))
	}
	req.Header.Set("Authorization", "Bearer "+acct.Token)
	req.Header.Set("Accept", "application/json")
	if rd != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := core.Do(ctx, a.http, req)
	if err != nil {
		return nil, err
	}
	if !resp.OK() {
		e := v2Error(core.Locale(ctx), resp)
		e.Message = a.redact.Redact(e.Message)
		return nil, e
	}
	return resp, nil
}

// v2Error is a non-2xx answer as a coded error, read from the panel's {"message", "errors"} body. The vendor's words
// are not clipped here: the Provider redacts them first and clips after. Only a 429 is transient (core.RateLimited):
// after any other answer the panel may have queued the task.
func v2Error(locale string, resp *core.Response) *core.Error {
	var body struct {
		Message string                     `json:"message"`
		Errors  map[string]json.RawMessage `json:"errors"`
	}
	_ = json.Unmarshal(resp.Body, &body)
	msg := body.Message
	keys := make([]string, 0, len(body.Errors))
	for k := range body.Errors {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		var list []string
		var one string
		if json.Unmarshal(body.Errors[k], &list) == nil && len(list) > 0 {
			one = list[0]
		} else if json.Unmarshal(body.Errors[k], &one) != nil {
			continue
		}
		if one != "" && !strings.Contains(msg, one) {
			msg = strings.TrimSpace(msg + " " + one)
		}
	}
	msg = core.OneLine(msg, 0)
	text := "SolusVM: " + msg
	if msg == "" {
		text = core.T(locale, "SolusVM answered with the error ", "SolusVM 返回了错误 ") + strconv.Itoa(resp.Status)
		if resp.Reason != "" {
			text += " " + resp.Reason
		}
	}
	l := strings.ToLower(msg)
	capacity := hasAny(l, "quota", "limit reached", "limit exceeded", "limit is reached", "not enough", "insufficient", "no free", "no available")
	// Only power and status wording: a 422 is mostly a field validation ("The name has already been taken.", "The
	// selected plan is not valid."), which is refused, not a state.
	stateish := hasAny(l, "state", "status", "already started", "already stopped", "already running",
		"already in progress", "is running", "is started", "is stopped", "is suspended", "is locked", "is busy",
		"is paused", "is processing", "in progress", "is not running", "is not started", "is not stopped")
	switch resp.Status {
	case http.StatusUnauthorized, http.StatusForbidden:
		return core.NotSignedIn("%s", text)
	case http.StatusNotFound, http.StatusGone:
		return core.NotFound("%s", text)
	case http.StatusTooManyRequests:
		return core.RateLimited("%s", text)
	case http.StatusConflict, http.StatusLocked:
		if capacity {
			return core.Capacity("%s", text)
		}
		return core.InvalidState("%s", text)
	case http.StatusUnprocessableEntity:
		switch {
		case capacity:
			return core.Capacity("%s", text)
		case stateish:
			return core.InvalidState("%s", text)
		}
		return core.Refused("%s", text)
	}
	if capacity {
		return core.Capacity("%s", text)
	}
	if msg == "" {
		msg = resp.Reason
	}
	return core.StatusError("SolusVM", resp.Status, msg)
}

type v2IPs struct {
	IPv4 []struct {
		IP        string     `json:"ip"`
		IsPrimary flexString `json:"is_primary"`
	} `json:"ipv4"`
	// IPv6 is one {"primary_ip", "range"} object on SolusVM 2 (what its WHMCS module reads), or a list of them on
	// some versions; see v6.
	IPv6 json.RawMessage `json:"ipv6"`
}

type v2IPv6 struct {
	PrimaryIP string `json:"primary_ip"`
	Range     string `json:"range"`
}

// v6 are the IPv6 entries, whichever shape the panel sent (none when it is neither).
func (ips v2IPs) v6() []v2IPv6 {
	raw := bytes.TrimSpace(ips.IPv6)
	if len(raw) == 0 {
		return nil
	}
	var one v2IPv6
	if raw[0] == '{' && json.Unmarshal(raw, &one) == nil {
		return []v2IPv6{one}
	}
	var list []v2IPv6
	if raw[0] == '[' && json.Unmarshal(raw, &list) == nil {
		return list
	}
	return nil
}

// addresses are the plain addresses, the primary IPv4 first, then the other IPv4s, then the IPv6 ones. An IPv6
// range is taken only when it is a plain address, never a CIDR block.
func (ips v2IPs) addresses() []string {
	var primary, other []string
	for _, v := range ips.IPv4 {
		if flagged(v.IsPrimary) {
			primary = append(primary, v.IP)
		} else {
			other = append(other, v.IP)
		}
	}
	out := append(primary, other...)
	for _, v := range ips.v6() {
		switch {
		case core.PlainIP(v.PrimaryIP) != "":
			out = append(out, v.PrimaryIP)
		case core.PlainIP(v.Range) != "":
			out = append(out, v.Range)
		}
	}
	return out
}

// flagged reports whether a JSON flag says yes (true, 1, "1", "true").
func flagged(f flexString) bool {
	s := strings.ToLower(strings.TrimSpace(string(f)))
	return s != "" && s != "0" && s != "false"
}

type v2Server struct {
	ID             flexString `json:"id"`
	Name           string     `json:"name"`
	Status         string     `json:"status"`
	IsSuspended    bool       `json:"is_suspended"`
	IsProcessing   flexString `json:"is_processing"` // a task is running for it
	IPAddresses    v2IPs      `json:"ip_addresses"`
	Specifications struct {
		VCPU flexNum `json:"vcpu"`
		RAM  flexNum `json:"ram"`  // bytes
		Disk flexNum `json:"disk"` // GiB
	} `json:"specifications"`
	Plan *struct {
		Name string `json:"name"`
	} `json:"plan"`
	Location *struct {
		Name string `json:"name"`
	} `json:"location"`
}

func (a *v2Adapter) instance(locale string, acct core.NamedToken, s v2Server) wire.CloudInstance {
	id := string(s.ID)
	in := wire.CloudInstance{Account: wire.CloudAccountID(acct.Account), ID: wire.CloudInstanceID(id),
		DisplayName: s.Name, ObservedAt: a.now()}
	if a.console != "" && allDigits(id) {
		in.ConsoleURL = a.console + "/servers/" + url.PathEscape(id)
	}
	if s.Location != nil {
		in.Region = s.Location.Name
	}
	in.PublicIps, in.PrivateIps = splitIPs(s.IPAddresses.addresses())

	spec := wire.CloudSpec{Cpu: float64(s.Specifications.VCPU), MemoryMb: int64(s.Specifications.RAM) / (1 << 20),
		DiskGb: int64(s.Specifications.Disk)}
	if s.Plan != nil {
		spec.Size = s.Plan.Name
	}
	if spec != (wire.CloudSpec{}) {
		in.Spec = &spec
	}

	// The statuses of the SolusVM 2 API (its Go SDK: started, stopped, processing, paused, not exists, unavailable),
	// plus the transitional words some versions send.
	sync, list := wire.CloudActionKindSyncIp, wire.CloudActionKindSnapshotList
	status := strings.ToLower(strings.TrimSpace(s.Status))
	busy := flagged(s.IsProcessing)
	switch {
	case status == "not exists" || status == "not_exists" || status == "deleting" || status == "deleted" || status == "removing":
		in.State = wire.CloudInstanceStateTerminated
		in.Actions = []wire.CloudActionKind{sync}
		in.ConsoleURL = ""
	case s.IsSuspended || status == "suspended":
		in.State = wire.CloudInstanceStateStopped
		in.StateDetail = core.T(locale, "Suspended at the panel: it cannot be powered on or off from here.",
			"已在面板上被暂停：无法在这里开机或关机。")
		in.Actions = []wire.CloudActionKind{sync, list}
	case status == "processing" || (busy && (status == "started" || status == "running" || status == "stopped")):
		// A task (an install, a reinstall, a backup, a power change) is running at the panel; it refuses power
		// actions until it ends. The state stays what it was when the panel says so, else it is on its way up.
		switch status {
		case "started", "running":
			in.State = wire.CloudInstanceStateRunning
		case "stopped":
			in.State = wire.CloudInstanceStateStopped
		default:
			in.State = wire.CloudInstanceStateStarting
		}
		in.StateDetail = core.T(locale, "A task is running for it at the panel: power actions wait until it ends.",
			"面板上正在为它执行任务：任务结束前无法开关机。")
		in.Actions = []wire.CloudActionKind{sync, list}
	case status == "paused":
		in.State = wire.CloudInstanceStateStopped
		in.StateDetail = core.T(locale, "Paused at the panel: it is frozen in memory, not running.",
			"已在面板上被挂起：它冻结在内存中，没有运行。")
		in.Actions = []wire.CloudActionKind{sync, list}
	case status == "started" || status == "running":
		in.State = wire.CloudInstanceStateRunning
		in.Actions = []wire.CloudActionKind{wire.CloudActionKindStop, wire.CloudActionKindReboot,
			wire.CloudActionKindForceStop, sync, list, wire.CloudActionKindSnapshotCreate}
	case status == "stopped":
		in.State = wire.CloudInstanceStateStopped
		in.Actions = []wire.CloudActionKind{wire.CloudActionKindStart, sync, list, wire.CloudActionKindSnapshotCreate}
	case status == "starting" || status == "stopping" || status == "restarting":
		in.State = map[string]wire.CloudInstanceState{"starting": wire.CloudInstanceStateStarting,
			"stopping": wire.CloudInstanceStateStopping, "restarting": wire.CloudInstanceStateRebooting}[status]
		in.Actions = []wire.CloudActionKind{wire.CloudActionKindForceStop, sync, list}
	default: // unavailable (the panel cannot reach its compute resource), not created, or unreadable
		in.State = wire.CloudInstanceStateUnknown
		if status != "" {
			in.StateDetail = core.T(locale, "The panel reports the state: ", "面板报告的状态：") + core.OneLine(s.Status, 0)
		}
		in.Actions = []wire.CloudActionKind{sync, list}
	}
	in.Actions = withConsole(in.Actions, in.ConsoleURL)
	return in
}

type v2Page struct {
	Data  json.RawMessage `json:"data"`
	Links struct {
		Next *string `json:"next"`
	} `json:"links"`
	Meta struct {
		CurrentPage flexNum `json:"current_page"`
		LastPage    flexNum `json:"last_page"`
	} `json:"meta"`
}

// more reports whether a page after page exists.
func (p v2Page) more(page int) bool {
	if p.Meta.LastPage > 0 {
		return float64(page) < float64(p.Meta.LastPage)
	}
	return p.Links.Next != nil && *p.Links.Next != ""
}

// list is every server of acct, at most 500.
func (a *v2Adapter) list(ctx context.Context, acct core.NamedToken) ([]wire.CloudInstance, error) {
	var out []wire.CloudInstance
	seen := map[string]bool{}
	for page := 1; page <= v2MaxPages && len(out) < core.MaxInstances; page++ {
		resp, err := a.do(ctx, acct, http.MethodGet, "servers", url.Values{"page": {strconv.Itoa(page)}}, nil)
		if err != nil {
			return nil, err
		}
		var p v2Page
		var data []v2Server
		if err := resp.JSON(&p); err != nil {
			return nil, err
		}
		if len(p.Data) > 0 && json.Unmarshal(p.Data, &data) != nil {
			return nil, core.Failed("%s", core.T(core.Locale(ctx), "The SolusVM panel's server list could not be read.",
				"无法读取 SolusVM 面板的服务器列表。"))
		}
		fresh := 0
		for _, s := range data {
			if s.ID == "" || seen[string(s.ID)] || len(out) == core.MaxInstances {
				continue
			}
			seen[string(s.ID)] = true
			fresh++
			out = append(out, a.instance(core.Locale(ctx), acct, s))
		}
		if fresh == 0 || !p.more(page) {
			break
		}
	}
	return out, nil
}

// Instances lists every account's servers, at most eight accounts at once.
func (a *v2Adapter) Instances(ctx context.Context) ([]wire.CloudInstance, []string, error) {
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
			problems = append(problems, a.accounts[i].Account)
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

// account is the login of ref. A SolusVM 2 server id is a decimal number: any other id (one with a slash, a percent
// sign or a space) is not_found without a request.
func (a *v2Adapter) account(locale string, ref wire.CloudInstanceRef) (core.NamedToken, error) {
	t, ok := a.byName[string(ref.Account)]
	if !ok || !allDigits(string(ref.ID)) {
		return core.NamedToken{}, noServer(locale)
	}
	return t, nil
}

// Instance reads one server now.
func (a *v2Adapter) Instance(ctx context.Context, ref wire.CloudInstanceRef) (wire.CloudInstance, error) {
	acct, err := a.account(core.Locale(ctx), ref)
	if err != nil {
		return wire.CloudInstance{}, err
	}
	ctx, cancel := budget(ctx, readBudget)
	defer cancel()
	resp, err := a.do(ctx, acct, http.MethodGet, "servers/"+url.PathEscape(string(ref.ID)), nil, nil)
	if err != nil {
		return wire.CloudInstance{}, err
	}
	var body struct {
		Data *v2Server `json:"data"`
	}
	if err := resp.JSON(&body); err != nil {
		return wire.CloudInstance{}, err
	}
	if body.Data == nil || string(body.Data.ID) != string(ref.ID) {
		return wire.CloudInstance{}, noServer(core.Locale(ctx))
	}
	return a.instance(core.Locale(ctx), acct, *body.Data), nil
}

func noServer(locale string) *core.Error {
	return core.NotFound("%s", core.T(locale, "No such SolusVM server.", "没有这台 SolusVM 服务器。"))
}

// Action powers a server on or off, restarts it or takes a snapshot. The panel answers with a task it queued, so a
// power action is accepted.
func (a *v2Adapter) Action(ctx context.Context, act core.Action) (wire.CloudActionResult, error) {
	var path string
	var body any
	switch act.Kind {
	case wire.CloudActionKindStart:
		path = "start"
	case wire.CloudActionKindStop:
		path = "stop"
	case wire.CloudActionKindForceStop:
		path, body = "stop", map[string]bool{"force": true}
	case wire.CloudActionKindReboot:
		path = "restart"
	case wire.CloudActionKindSnapshotCreate:
		name := act.Name
		if name == "" {
			name = "persona-" + a.now().UTC().Format("20060102-150405")
		}
		path, body = "snapshots", map[string]string{"name": name}
	default:
		return wire.CloudActionResult{}, core.Unsupported("%s", core.T(act.Locale,
			"SolusVM cannot do this.", "SolusVM 不支持这个操作。"))
	}
	acct, err := a.account(act.Locale, act.Ref)
	if err != nil {
		return wire.CloudActionResult{}, err
	}
	ctx, cancel := budget(ctx, actionBudget)
	defer cancel()
	resp, err := a.do(ctx, acct, http.MethodPost, "servers/"+url.PathEscape(string(act.Ref.ID))+"/"+path, nil, body)
	if err != nil {
		return wire.CloudActionResult{}, err
	}
	res := wire.CloudActionResult{Status: wire.CloudActionStatusAccepted}
	if act.Kind == wire.CloudActionKindSnapshotCreate {
		var out struct {
			Data *v2Snapshot `json:"data"`
		}
		if resp.JSON(&out) == nil && out.Data != nil && out.Data.ID != "" {
			s := out.Data.snapshot()
			if s.Name == "" {
				s.Name = act.Name
			}
			res.Snapshot = &s
		}
	}
	return res, nil
}

type v2Snapshot struct {
	ID        flexString `json:"id"`
	Name      string     `json:"name"`
	Status    string     `json:"status"`
	Size      flexNum    `json:"size"` // GiB
	CreatedAt string     `json:"created_at"`
}

func (s v2Snapshot) snapshot() wire.CloudSnapshot {
	out := wire.CloudSnapshot{ID: wire.CloudSnapshotID(s.ID), Name: s.Name, SizeGb: float64(s.Size),
		CreatedAt: parseTime(s.CreatedAt)}
	switch strings.ToLower(strings.TrimSpace(s.Status)) {
	case "available", "created", "ready", "active", "success", "completed":
		out.State = wire.CloudSnapshotStateReady
	case "failed", "error":
		out.State = wire.CloudSnapshotStateFailed
	case "deleting", "removing":
		out.State = wire.CloudSnapshotStateDeleting
	default:
		out.State = wire.CloudSnapshotStateCreating
	}
	return out
}

// parseTime reads the panel's time stamps (RFC 3339, or "2006-01-02 15:04:05" in UTC); zero when unreadable.
func parseTime(s string) time.Time {
	s = strings.TrimSpace(s)
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05", "2006-01-02T15:04:05"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}

// Snapshots lists a server's snapshots.
func (a *v2Adapter) Snapshots(ctx context.Context, ref wire.CloudInstanceRef) ([]wire.CloudSnapshot, error) {
	acct, err := a.account(core.Locale(ctx), ref)
	if err != nil {
		return nil, err
	}
	ctx, cancel := budget(ctx, readBudget)
	defer cancel()
	var out []wire.CloudSnapshot
	seen := map[string]bool{}
	for page := 1; page <= v2MaxSnapshotPages && len(out) < core.MaxSnapshots; page++ {
		resp, err := a.do(ctx, acct, http.MethodGet, "servers/"+url.PathEscape(string(ref.ID))+"/snapshots",
			url.Values{"page": {strconv.Itoa(page)}}, nil)
		if err != nil {
			return nil, err
		}
		var p v2Page
		var data []v2Snapshot
		if err := resp.JSON(&p); err != nil {
			return nil, err
		}
		if len(p.Data) > 0 && json.Unmarshal(p.Data, &data) != nil {
			return nil, core.Failed("%s", core.T(core.Locale(ctx), "The SolusVM panel's snapshot list could not be read.",
				"无法读取 SolusVM 面板的快照列表。"))
		}
		fresh := 0
		for _, s := range data {
			if s.ID == "" || seen[string(s.ID)] || len(out) == core.MaxSnapshots {
				continue
			}
			seen[string(s.ID)] = true
			fresh++
			out = append(out, s.snapshot())
		}
		if fresh == 0 || !p.more(page) {
			break
		}
	}
	return out, nil
}
