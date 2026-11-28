package solusvm

import (
	"context"
	"html"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/nwverse/persona-extensions/extensions/cloud-hosts/internal/core"
	"github.com/nwverse/persona/sdk/go/wire"
)

// V1Account is the account every SolusVM 1 server is listed under: the client API has no login, only a key and hash
// per server.
const V1Account = "v1"

// v1DefaultLabel is the id of the one server of a secret that holds a single "key:hash" line.
const v1DefaultLabel = "vps"

// V1Config configures the SolusVM 1 client API adapter.
type V1Config struct {
	// BaseURL is the panel's address, for example https://panel.example.com:5656. The client API is
	// <BaseURL>/api/client/command.php, and console links open <BaseURL> itself (when it is https).
	BaseURL string
	// Credentials is the secret text: one server per line as "label=key:hash", where the label (1-40 characters of
	// a-z, A-Z, 0-9, dot, underscore and dash) becomes the instance id. A secret of a single "key:hash" line is the
	// instance "vps". Blank lines and lines starting with # are skipped.
	Credentials string
	// PinSHA256 is the panel certificate's SHA-256 fingerprint, for a self-signed panel. Used only when HTTP is nil.
	PinSHA256 string
	// HTTP is the client (core.NewHTTPClient when nil). Tests point it at an httptest server.
	HTTP *http.Client
	// Now is the clock (time.Now when nil).
	Now func() time.Time
}

type v1Server struct {
	label, key, hash string
}

type v1Adapter struct {
	endpoint string // <base>/api/client/command.php
	console  string // the panel's https base, "" when it has none
	servers  []v1Server
	byLabel  map[string]v1Server
	http     *http.Client
	now      func() time.Time
	redact   *core.Redactor
}

// NewV1 is the SolusVM 1 client API adapter for the servers in c.Credentials.
func NewV1(c V1Config) (core.Adapter, error) {
	servers, err := parseV1Credentials(c.Credentials)
	if err != nil {
		return nil, err
	}
	endpoint, err := core.JoinURL(c.BaseURL, "/api/client/command.php", nil)
	if err != nil {
		return nil, core.Failed("the SolusVM panel address is not a usable URL")
	}
	a := &v1Adapter{endpoint: endpoint.String(), console: core.ConsoleBase(c.BaseURL), servers: servers,
		byLabel: map[string]v1Server{}, http: c.HTTP, now: c.Now, redact: &core.Redactor{}}
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
	for _, s := range servers {
		a.byLabel[s.label] = s
	}
	a.redact.Add(a.SecretValues()...)
	return a, nil
}

func parseV1Credentials(secret string) ([]v1Server, error) {
	var lines []string
	for _, l := range strings.FieldsFunc(secret, func(c rune) bool { return c == '\n' || c == '\r' }) {
		if l = strings.TrimSpace(l); l != "" && !strings.HasPrefix(l, "#") {
			lines = append(lines, l)
		}
	}
	if len(lines) == 0 {
		return nil, core.NotSignedIn("SolusVM: no server key and hash are set up (one \"label=key:hash\" line per server)")
	}
	var out []v1Server
	seen := map[string]bool{}
	for n, l := range lines {
		label, cred := "", l
		if i := strings.IndexByte(l, '='); i > 0 && core.ValidAccountName(strings.TrimSpace(l[:i])) {
			label, cred = strings.TrimSpace(l[:i]), strings.TrimSpace(l[i+1:])
		} else if len(lines) == 1 {
			label = v1DefaultLabel
		} else {
			return nil, core.NotSignedIn("SolusVM: line %d of the credentials has no label (\"label=key:hash\")", n+1)
		}
		key, hash, ok := strings.Cut(cred, ":")
		key, hash = strings.TrimSpace(key), strings.TrimSpace(hash)
		if !ok || key == "" || hash == "" {
			return nil, core.NotSignedIn("SolusVM: the credentials of %s need both a key and a hash (\"key:hash\")", label)
		}
		if seen[label] {
			return nil, core.NotSignedIn("SolusVM: the label %s is used twice in the credentials", label)
		}
		seen[label] = true
		out = append(out, v1Server{label: label, key: key, hash: hash})
	}
	return out, nil
}

// SecretValues are every key and hash.
func (a *v1Adapter) SecretValues() []string {
	var out []string
	for _, s := range a.servers {
		out = append(out, s.key, s.hash)
	}
	return out
}

// call is one client API command; its answer's tags, or the panel's error.
func (a *v1Adapter) call(ctx context.Context, s v1Server, action string, write bool, extra url.Values) (map[string]string, error) {
	form := url.Values{"key": {s.key}, "hash": {s.hash}, "action": {action}}
	for k, v := range extra {
		form[k] = v
	}
	req, err := http.NewRequest(http.MethodPost, a.endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, core.Failed("%s", core.T(core.Locale(ctx), "The SolusVM panel address is not a usable URL.",
			"SolusVM 面板地址不是可用的网址。"))
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "text/xml, text/plain, */*")
	resp, err := core.Do(ctx, a.http, req)
	if err != nil {
		return nil, err
	}
	tags := scanTags(string(resp.Body))
	switch strings.ToLower(tags["status"]) {
	case "success":
		if !resp.OK() {
			return nil, a.clean(core.StatusError("The SolusVM panel", resp.Status, resp.Reason))
		}
		return tags, nil
	case "error":
		return nil, a.clean(v1Error(core.Locale(ctx), tags["statusmsg"], write))
	}
	if !resp.OK() {
		return nil, a.clean(core.StatusError("The SolusVM panel", resp.Status, resp.Reason))
	}
	return nil, core.Failed("%s", core.T(core.Locale(ctx), "The SolusVM panel's answer could not be read.",
		"无法读取 SolusVM 面板的回复。"))
}

// clean is e without any credential in its message.
func (a *v1Adapter) clean(e *core.Error) *core.Error {
	e.Message = a.redact.Redact(e.Message)
	return e
}

// v1Error is the panel's status=error answer as a coded error. The vendor's words are not clipped here: the
// Provider redacts them first and clips after. Rate limiting is the one answer after which the panel certainly did
// not run the command (core.RateLimited marks it transient).
func v1Error(locale, msg string, write bool) *core.Error {
	msg = core.OneLine(msg, 0)
	text := "SolusVM: " + msg
	if msg == "" {
		text = core.T(locale, "The SolusVM panel answered an error.", "SolusVM 面板返回了错误。")
	}
	l := strings.ToLower(msg)
	switch {
	case hasAny(l, "invalid key", "invalid hash", "key/hash", "key or hash", "key and hash", "unauthori", "authenticat"):
		return core.NotSignedIn("%s", text)
	case hasAny(l, "too many", "rate limit", "flood", "slow down"):
		return core.RateLimited("%s", text)
	case hasAny(l, "already", "suspended", "disabled", "locked", "in progress"):
		return core.InvalidState("%s", text)
	case hasAny(l, "not found", "no such", "does not exist", "terminated", "invalid vserver"):
		return core.NotFound("%s", text)
	case write:
		return core.Refused("%s", text)
	}
	return core.Failed("%s", text)
}

// scanTags reads the client API's pseudo-XML (<name>value</name> fragments, not a document): the first value of
// each leaf tag (one whose content holds no other tag), lowercased names, entities decoded. A wrapper such as the
// <?xml ...?><ctrl>...</ctrl> envelope of real panels is read into, not stored, as the vendor's own example parser
// does. Anything it cannot read is skipped.
func scanTags(body string) map[string]string {
	out := map[string]string{}
	lower := asciiLower(body)
	for i := 0; i < len(body); {
		open := strings.IndexByte(body[i:], '<')
		if open < 0 {
			break
		}
		open += i
		j := open + 1
		for j < len(body) && isTagChar(body[j]) {
			j++
		}
		if j == open+1 || j >= len(body) || body[j] != '>' {
			i = open + 1
			continue
		}
		name := lower[open+1 : j]
		closing := "</" + name + ">"
		end := strings.Index(lower[j+1:], closing)
		if end < 0 {
			i = j + 1
			continue
		}
		end += j + 1
		content := body[j+1 : end]
		if strings.IndexByte(content, '<') >= 0 {
			// A wrapper: read the tags inside it.
			i = j + 1
			continue
		}
		if _, ok := out[name]; !ok {
			out[name] = strings.TrimSpace(html.UnescapeString(content))
		}
		i = end + len(closing)
	}
	return out
}

func isTagChar(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-'
}

// asciiLower is s with ASCII letters lowercased, byte for byte (offsets stay the same).
func asciiLower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + 'a' - 'A'
		}
	}
	return string(b)
}

// read is one server as the panel says it is now.
func (a *v1Adapter) read(ctx context.Context, s v1Server) (wire.CloudInstance, error) {
	tags, err := a.call(ctx, s, "info", false, url.Values{"status": {"true"}, "ipaddr": {"true"}, "hdd": {"true"},
		"mem": {"true"}, "bw": {"true"}})
	if err != nil {
		return wire.CloudInstance{}, err
	}
	if tags["vmstat"] == "" { // a panel that leaves vmstat empty even with status=true

		st, err := a.call(ctx, s, "status", false, nil)
		if err != nil {
			return wire.CloudInstance{}, err
		}
		tags["vmstat"] = st["vmstat"]
		if tags["vmstat"] == "" {
			tags["vmstat"] = st["statusmsg"]
		}
	}
	return a.instance(core.Locale(ctx), s, tags), nil
}

func (a *v1Adapter) instance(locale string, s v1Server, tags map[string]string) wire.CloudInstance {
	in := wire.CloudInstance{Account: V1Account, ID: wire.CloudInstanceID(s.label), DisplayName: tags["hostname"],
		ConsoleURL: a.console, ObservedAt: a.now()}
	if in.DisplayName == "" {
		in.DisplayName = s.label
	}
	ips := []string{tags["ipaddress"]}
	ips = append(ips, strings.FieldsFunc(tags["ipaddr"], func(c rune) bool { return c == ',' || c == ' ' || c == '\n' || c == '\r' || c == '\t' })...)
	in.PublicIps, in.PrivateIps = splitIPs(ips)

	spec := wire.CloudSpec{}
	if total := firstInt(tags["mem"]); total > 0 {
		spec.MemoryMb = total / (1 << 20)
	}
	if total := firstInt(tags["hdd"]); total > 0 {
		spec.DiskGb = total / (1 << 30)
	}
	if spec != (wire.CloudSpec{}) {
		in.Spec = &spec
	}

	sync := wire.CloudActionKindSyncIp
	switch st := strings.ToLower(strings.TrimSpace(tags["vmstat"])); st {
	case "online", "running", "started":
		in.State = wire.CloudInstanceStateRunning
		in.Actions = []wire.CloudActionKind{wire.CloudActionKindStop, wire.CloudActionKindReboot, sync}
	case "offline", "stopped", "shutdown":
		in.State = wire.CloudInstanceStateStopped
		in.Actions = []wire.CloudActionKind{wire.CloudActionKindStart, sync}
	case "disabled", "suspended":
		in.State = wire.CloudInstanceStateStopped
		in.StateDetail = core.T(locale, "Suspended at the panel: it cannot be powered on or off from here.",
			"已在面板上被暂停：无法在这里开机或关机。")
		in.Actions = []wire.CloudActionKind{sync}
	default:
		in.State = wire.CloudInstanceStateUnknown
		if st != "" {
			in.StateDetail = core.T(locale, "The panel reports the state: ", "面板报告的状态：") + core.OneLine(tags["vmstat"], 0)
		}
		in.Actions = []wire.CloudActionKind{sync}
	}
	in.Actions = withConsole(in.Actions, in.ConsoleURL)
	return in
}

// firstInt is the first comma-separated field of s as a whole number (0 when it is not one).
func firstInt(s string) int64 {
	f, _, _ := strings.Cut(s, ",")
	v, err := strconv.ParseInt(strings.TrimSpace(f), 10, 64)
	if err != nil || v < 0 {
		return 0
	}
	return v
}

func (a *v1Adapter) server(locale string, ref wire.CloudInstanceRef) (v1Server, error) {
	s, ok := a.byLabel[string(ref.ID)]
	if ref.Account != V1Account || !ok {
		return v1Server{}, core.NotFound("%s", core.T(locale, "No such SolusVM server is set up.", "没有设置这台 SolusVM 服务器。"))
	}
	return s, nil
}

// Instances reads every server, at most eight at once.
func (a *v1Adapter) Instances(ctx context.Context) ([]wire.CloudInstance, []string, error) {
	ctx, cancel := budget(ctx, listBudget)
	defer cancel()
	ins := make([]wire.CloudInstance, len(a.servers))
	errs := make([]error, len(a.servers))
	each(len(a.servers), func(i int) { ins[i], errs[i] = a.read(ctx, a.servers[i]) })
	var out []wire.CloudInstance
	var problems []string
	var first error
	for i, err := range errs {
		if err != nil {
			problems = append(problems, a.servers[i].label)
			if first == nil {
				first = err
			}
			continue
		}
		if len(out) < core.MaxInstances {
			out = append(out, ins[i])
		}
	}
	if len(out) == 0 && first != nil {
		return nil, nil, first
	}
	return out, problems, nil
}

// Instance reads one server now.
func (a *v1Adapter) Instance(ctx context.Context, ref wire.CloudInstanceRef) (wire.CloudInstance, error) {
	s, err := a.server(core.Locale(ctx), ref)
	if err != nil {
		return wire.CloudInstance{}, err
	}
	ctx, cancel := budget(ctx, readBudget)
	defer cancel()
	return a.read(ctx, s)
}

// v1Commands are the client API's power commands.
var v1Commands = map[wire.CloudActionKind]string{
	wire.CloudActionKindStart:  "boot",
	wire.CloudActionKindStop:   "shutdown",
	wire.CloudActionKindReboot: "reboot",
}

// Action sends a power command. The panel's answer only says it took the command, so it is accepted.
func (a *v1Adapter) Action(ctx context.Context, act core.Action) (wire.CloudActionResult, error) {
	cmd, ok := v1Commands[act.Kind]
	if !ok {
		return wire.CloudActionResult{}, core.Unsupported("%s", core.T(act.Locale,
			"The SolusVM client API cannot do this.", "SolusVM 客户端 API 不支持这个操作。"))
	}
	s, err := a.server(act.Locale, act.Ref)
	if err != nil {
		return wire.CloudActionResult{}, err
	}
	ctx, cancel := budget(ctx, actionBudget)
	defer cancel()
	if _, err := a.call(ctx, s, cmd, true, nil); err != nil {
		return wire.CloudActionResult{}, err
	}
	return wire.CloudActionResult{Status: wire.CloudActionStatusAccepted}, nil
}

// Snapshots: the client API has none.
func (a *v1Adapter) Snapshots(ctx context.Context, _ wire.CloudInstanceRef) ([]wire.CloudSnapshot, error) {
	return nil, core.Unsupported("%s", core.T(core.Locale(ctx), "The SolusVM client API has no snapshots.",
		"SolusVM 客户端 API 没有快照功能。"))
}
