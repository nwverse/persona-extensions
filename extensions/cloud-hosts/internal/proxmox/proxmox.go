// Package proxmox is the cloud-hosts adapter for Proxmox VE: the QEMU virtual machines and LXC containers of one
// cluster (or single node), read and powered through the node's REST API (/api2/json) with API tokens.
//
// An account is one API token ("default" for a secret that holds one token). An instance id is "<type>/<vmid>",
// type qemu or lxc, for example "qemu/100": a vmid is unique in its cluster, and the node a guest runs on is not part
// of its identity (a migration or an HA relocation moves it), so every call looks the node up in /cluster/resources
// first. The node is the instance's zone.
package proxmox

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona-extensions/extensions/cloud-hosts/internal/core"
	"github.com/nwverse/persona/sdk/go/wire"
)

// vendor names Proxmox VE in messages for people.
const vendor = "Proxmox VE"

// Budgets under the hub's bounds (cloud.instances 20 s, cloud.instance 15 s, cloud.action 30 s).
const (
	listBudget     = 19 * time.Second
	instanceBudget = 14 * time.Second
	actionBudget   = 28 * time.Second
	addressBudget  = 8 * time.Second // the list's address reads, within listBudget
	parallel       = 8               // accounts, and guests' address reads, asked at once
)

// Config is the adapter's setup.
type Config struct {
	// BaseURL is the node's or cluster's https address with its port, e.g. https://pve.example.com:8006. It is also
	// the base of the console links, so it must be https. (Tests point it at an httptest TLS server.)
	BaseURL string
	// Token is the secret text: one API token "user@realm!tokenid=secret", or "name=user@realm!tokenid=secret" lines
	// for several accounts (blank lines and # comments skipped). A leading "PVEAPIToken=" is accepted and dropped.
	Token string
	// PinSHA256 is the SHA-256 fingerprint (hex, colons allowed) of the node's certificate, for a self-signed one:
	// the connection is then accepted for that certificate only. Ignored when HTTP is set.
	PinSHA256 string
	// HTTP is the client (nil: core.NewHTTPClient with PinSHA256). Redirects are never followed either way.
	HTTP *http.Client
	// Now is the clock (nil: time.Now).
	Now func() time.Time
}

// account is one API token.
type account struct {
	name    string
	tokenID string // user@realm!tokenid
	secret  string // the token's uuid secret
}

func (a *account) authorization() string { return "PVEAPIToken=" + a.tokenID + "=" + a.secret }

type adapter struct {
	base     string // BaseURL, trimmed
	console  string // core.ConsoleBase(BaseURL)
	http     *http.Client
	now      func() time.Time
	accounts []*account
	byName   map[string]*account
}

// New is the Proxmox VE adapter for cfg.
func New(cfg Config) (core.Adapter, error) {
	accounts, err := parseTokens(cfg.Token)
	if err != nil {
		return nil, err
	}
	base := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	if base == "" {
		return nil, core.Failed("The Proxmox VE address is missing: set it as https://<node>:8006.")
	}
	u, err := url.Parse(base)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, core.Failed("The Proxmox VE address must be https://<node>:8006, without a user, query or fragment.")
	}
	console := core.ConsoleBase(base)
	if console == "" {
		return nil, core.Failed("The Proxmox VE address must be https.")
	}
	client := cfg.HTTP
	if client == nil {
		pin := strings.TrimSpace(cfg.PinSHA256)
		if pin != "" && !validPin(pin) {
			return nil, core.Failed("The certificate pin is not a SHA-256 fingerprint (64 hex digits).")
		}
		client = core.NewHTTPClient(core.HTTPOptions{PinSHA256: pin})
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	a := &adapter{base: base, console: console, http: client, now: now, accounts: accounts, byName: map[string]*account{}}
	for _, acc := range accounts {
		a.byName[acc.name] = acc
	}
	return a, nil
}

func validPin(s string) bool {
	s = strings.ToLower(strings.NewReplacer(":", "", " ", "", "sha256", "", "=", "").Replace(s))
	if len(s) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}

// parseTokens reads the secret. A line is "name=user@realm!tokenid=secret" when what follows the first "=" still
// holds a "!" and a "=" (an API token itself holds both, so "user@realm!tokenid=secret" alone is never read as a
// name), and the account "default" otherwise.
func parseTokens(secret string) ([]*account, error) {
	var out []*account
	seen := map[string]bool{}
	n := 0
	for _, line := range strings.FieldsFunc(secret, func(c rune) bool { return c == '\n' || c == '\r' }) {
		n++
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		name, token := core.DefaultAccount, line
		if i := strings.IndexByte(line, '='); i > 0 {
			prefix, rest := strings.TrimSpace(line[:i]), strings.TrimSpace(line[i+1:])
			if !strings.EqualFold(prefix, "PVEAPIToken") && core.ValidAccountName(prefix) &&
				strings.Contains(rest, "!") && strings.Contains(rest, "=") {
				name, token = prefix, rest
			}
		}
		token = strings.TrimSpace(token)
		if len(token) > len("PVEAPIToken=") && strings.EqualFold(token[:len("PVEAPIToken=")], "PVEAPIToken=") {
			token = strings.TrimSpace(token[len("PVEAPIToken="):])
		}
		id, sec, ok := splitToken(token)
		if !ok {
			return nil, core.NotSignedIn("Line %d of the Proxmox VE token is not in the form user@realm!tokenid=secret.", n)
		}
		if seen[name] {
			return nil, core.NotSignedIn("The Proxmox VE token names the account %q twice.", name)
		}
		seen[name] = true
		out = append(out, &account{name: name, tokenID: id, secret: sec})
	}
	if len(out) == 0 {
		return nil, core.NotSignedIn("The Proxmox VE API token is missing: set it as user@realm!tokenid=secret.")
	}
	return out, nil
}

// splitToken is "user@realm!tokenid=secret" as its id and secret.
func splitToken(t string) (id, secret string, ok bool) {
	bang := strings.IndexByte(t, '!')
	if bang <= 0 {
		return "", "", false
	}
	eq := strings.IndexByte(t[bang:], '=')
	if eq < 0 {
		return "", "", false
	}
	id, secret = t[:bang+eq], t[bang+eq+1:]
	user, tokenName := id[:bang], id[bang+1:]
	at := strings.IndexByte(user, '@')
	if at <= 0 || at == len(user)-1 || tokenName == "" || secret == "" ||
		strings.ContainsFunc(t, func(c rune) bool { return c <= ' ' || c == 0x7f }) {
		return "", "", false
	}
	return id, secret, true
}

// SecretValues are the credentials the adapter holds, for redaction.
func (a *adapter) SecretValues() []string {
	var out []string
	for _, acc := range a.accounts {
		out = append(out, acc.secret, acc.tokenID+"="+acc.secret, acc.authorization(), acc.tokenID)
	}
	return out
}

// scrub is s without any credential the adapter holds: a node or a proxy in front of it may echo the Authorization
// header back in its error text.
func (a *adapter) scrub(s string) string {
	vals := a.SecretValues()
	sort.Slice(vals, func(i, j int) bool { return len(vals[i]) > len(vals[j]) })
	for _, v := range vals {
		if v != "" {
			s = strings.ReplaceAll(s, v, "[redacted]")
		}
	}
	return s
}

// call asks the API (path below /api2/json) and decodes the envelope's data into out (when not nil).
func (a *adapter) call(ctx context.Context, acc *account, method, path string, query, form url.Values, out any) error {
	u, err := core.JoinURL(a.base, "/api2/json/"+strings.TrimLeft(path, "/"), query)
	if err != nil {
		return err
	}
	var body io.Reader
	if method == http.MethodPost {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return core.Failed("%s", core.T(core.Locale(ctx), "The request to Proxmox VE could not be made.", "无法构造发往 Proxmox VE 的请求。"))
	}
	req.Header.Set("Authorization", acc.authorization())
	req.Header.Set("Accept", "application/json")
	if method == http.MethodPost {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	resp, err := core.Do(ctx, a.http, req)
	if err != nil {
		return err
	}
	if !resp.OK() {
		e := pveError(resp, core.Locale(ctx))
		e.Message = a.scrub(e.Message)
		return e
	}
	var env struct {
		Data json.RawMessage `json:"data"`
	}
	if err := resp.JSON(&env); err != nil {
		return err
	}
	if out == nil || len(env.Data) == 0 || string(env.Data) == "null" {
		return nil
	}
	if err := json.Unmarshal(env.Data, out); err != nil {
		return core.Failed("%s", core.T(core.Locale(ctx), "The answer of Proxmox VE could not be read.", "无法读取 Proxmox VE 的回复。"))
	}
	return nil
}

// pveError maps a failed answer: Proxmox VE says what went wrong in the status line's reason phrase and in the body's
// "message" and "errors". No answer of it is transient: a 5xx (595 and 596 included, where the node that was asked
// could not reach the guest's node) does not prove the request was not carried out; only a 429 is.
func pveError(resp *core.Response, loc string) *core.Error {
	var words []string
	reason := strings.TrimSpace(resp.Reason)
	if reason == http.StatusText(resp.Status) {
		reason = "" // the standard phrase says nothing of Proxmox VE's own
	}
	if reason != "" {
		words = append(words, reason)
	}
	var body struct {
		Message string            `json:"message"`
		Errors  map[string]string `json:"errors"`
	}
	if json.Unmarshal(resp.Body, &body) == nil {
		if m := strings.TrimSpace(body.Message); m != "" && (len(words) == 0 || !strings.Contains(words[0], m)) {
			words = append(words, m)
		}
		keys := make([]string, 0, len(body.Errors))
		for k := range body.Errors {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			words = append(words, k+": "+strings.TrimSpace(body.Errors[k]))
		}
	}
	// Not clipped here: the Provider redacts first and clips after.
	detail := core.OneLine(strings.Join(words, "; "), 0)
	msg := vendor + ": " + detail
	if detail == "" {
		msg = core.T(loc, "Proxmox VE answered an error (HTTP "+strconv.Itoa(resp.Status)+").",
			"Proxmox VE 返回了错误（HTTP "+strconv.Itoa(resp.Status)+"）。")
	}
	lower := strings.ToLower(detail)
	has := func(subs ...string) bool {
		for _, s := range subs {
			if strings.Contains(lower, s) {
				return true
			}
		}
		return false
	}
	switch {
	case resp.Status == http.StatusUnauthorized || resp.Status == http.StatusForbidden:
		return core.NotSignedIn("%s", msg)
	case resp.Status >= 300 && resp.Status < 400:
		return core.Failed("%s", core.T(loc, "Proxmox VE answered with a redirect, which this extension does not follow.",
			"Proxmox VE 返回了重定向，本扩展不会跟随。"))
	// Only the guest itself or its node being gone is not_found: "does not exist" alone also names a missing
	// storage or volume of a guest that is there.
	case has("configuration file") && has("does not exist"),
		has("no such vm", "no such ct", "no such container", "no such guest", "no such node"):
		return core.NotFound("%s", msg)
	case has("not enough memory", "cannot allocate memory", "out of memory", "no space left", "not enough space"):
		return core.Capacity("%s", msg)
	case has("not running", "already running", "is locked", "vm is locked", "is running", "is a template", "is paused",
		"is suspended", "cannot shutdown"):
		return core.InvalidState("%s", msg)
	case has("already used", "reserved name"):
		return core.Refused("%s", msg)
	}
	e := core.StatusError(vendor, resp.Status, detail) // 429 rate_limited (transient), 404, 409, 423; else failed
	if detail == "" {
		e.Message = msg
	}
	return e
}

// ref is a guest: its type and vmid (the id, "<type>/<vmid>") and the node it is on now (looked up, not in the id).
type ref struct {
	node string
	typ  string // qemu or lxc
	vmid int
}

func (r ref) id() wire.CloudInstanceID {
	return wire.CloudInstanceID(r.typ + "/" + strconv.Itoa(r.vmid))
}

func (r ref) path(rest string) string {
	return "nodes/" + url.PathEscape(r.node) + "/" + r.typ + "/" + strconv.Itoa(r.vmid) + rest
}

// parseRef reads an id "<type>/<vmid>"; the node is left for locate.
func parseRef(id wire.CloudInstanceID) (ref, bool) {
	parts := strings.Split(string(id), "/")
	if len(parts) != 2 || (parts[0] != "qemu" && parts[0] != "lxc") {
		return ref{}, false
	}
	if parts[1] == "" || len(parts[1]) > 10 || strings.TrimLeft(parts[1], "0123456789") != "" || parts[1][0] == '0' {
		return ref{}, false
	}
	vmid, err := strconv.Atoi(parts[1])
	if err != nil || vmid <= 0 {
		return ref{}, false
	}
	return ref{typ: parts[0], vmid: vmid}, true
}

// validNode is a Proxmox VE node name: a host name label, letters, digits and "-", not starting or ending in "-".
func validNode(s string) bool {
	if s == "" || len(s) > 63 || s[0] == '-' || s[len(s)-1] == '-' {
		return false
	}
	for _, c := range s {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-':
		default:
			return false
		}
	}
	return true
}

// lookup is the account and parsed id of an instance reference: not_found when either is not one of this adapter.
// An id of another shape (one with a "/", "%" or space in its vmid, say) is answered without any request.
func (a *adapter) lookup(in wire.CloudInstanceRef, loc string) (*account, ref, error) {
	acc := a.byName[string(in.Account)]
	if acc == nil {
		return nil, ref{}, core.NotFound("%s", core.T(loc, "There is no Proxmox VE account "+strconv.Quote(string(in.Account))+".",
			"没有名为 "+strconv.Quote(string(in.Account))+" 的 Proxmox VE 账户。"))
	}
	r, ok := parseRef(in.ID)
	if !ok {
		return nil, ref{}, noGuest(loc)
	}
	return acc, r, nil
}

// locate is the guest r as the cluster lists it now, with the node it is on: not_found when the cluster has no such
// guest of that type, or only a template.
func (a *adapter) locate(ctx context.Context, acc *account, r ref) (ref, *resource, error) {
	var res []resource
	if err := a.call(ctx, acc, http.MethodGet, "cluster/resources", url.Values{"type": {"vm"}}, nil, &res); err != nil {
		return r, nil, err
	}
	for i := range res {
		e := &res[i]
		if int(e.VMID) != r.vmid || e.Type != r.typ {
			continue
		}
		if e.Template != 0 {
			return r, nil, isTemplate(core.Locale(ctx), r.id())
		}
		if !validNode(e.Node) {
			break
		}
		r.node = e.Node
		return r, e, nil
	}
	return r, nil, noGuest(core.Locale(ctx))
}

func noGuest(loc string) *core.Error {
	return core.NotFound("%s", core.T(loc, "There is no such Proxmox VE guest.", "Proxmox VE 中没有这台虚拟机或容器。"))
}

func isTemplate(loc string, id wire.CloudInstanceID) *core.Error {
	return core.NotFound("%s", core.T(loc, string(id)+" is a template, not a guest that runs.",
		string(id)+" 是模板，不是可运行的虚拟机或容器。"))
}

// onNode looks the guest up and runs fn on the node it is on. When that node answers that the guest is not there
// (it moved between the lookup and the call), the guest is looked up once more and fn runs again on its new node.
func (a *adapter) onNode(ctx context.Context, acc *account, r ref, fn func(ref) error) (ref, *resource, error) {
	loc, res, err := a.locate(ctx, acc, r)
	if err != nil {
		return loc, res, err
	}
	err = fn(loc)
	if e := core.AsError(err); e != nil && e.Code == wire.CloudErrorCodeNotFound {
		again, res2, lerr := a.locate(ctx, acc, r)
		if lerr != nil {
			return again, nil, lerr
		}
		if again.node != loc.node {
			loc, res = again, res2
			err = fn(loc)
		}
	}
	return loc, res, err
}

// num is a JSON number that may come as a string.
type num float64

func (n *num) UnmarshalJSON(b []byte) error {
	s := strings.Trim(strings.TrimSpace(string(b)), `"`)
	if s == "" || s == "null" {
		*n = 0
		return nil
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		*n = 0
		return nil
	}
	*n = num(f)
	return nil
}

// resource is one entry of /cluster/resources?type=vm.
type resource struct {
	ID       string `json:"id"`
	VMID     num    `json:"vmid"`
	Name     string `json:"name"`
	Node     string `json:"node"`
	Type     string `json:"type"`
	Status   string `json:"status"`
	MaxCPU   num    `json:"maxcpu"`
	MaxMem   num    `json:"maxmem"`
	MaxDisk  num    `json:"maxdisk"`
	Template num    `json:"template"`
	Lock     string `json:"lock"`
}

// current is /nodes/{node}/{type}/{vmid}/status/current.
type current struct {
	Status    string `json:"status"`
	QMPStatus string `json:"qmpstatus"`
	Lock      string `json:"lock"`
	Name      string `json:"name"`
	CPUs      num    `json:"cpus"`
	MaxMem    num    `json:"maxmem"`
	MaxDisk   num    `json:"maxdisk"`
	Template  num    `json:"template"`
}

// guest is what the adapter knows of one guest before it becomes an instance.
type guest struct {
	ref
	name                 string
	status, qmp, lock    string
	cpus                 float64
	maxmem, maxdisk      int64
	publicIPs, privateIP []string
	addrNote             string // why its addresses could not be read, when that is worth saying
}

// Instances lists every guest (templates left out) of every account.
func (a *adapter) Instances(ctx context.Context) ([]wire.CloudInstance, []string, error) {
	ctx, cancel := context.WithTimeout(ctx, listBudget)
	defer cancel()

	type result struct {
		guests []*guest
		err    error
	}
	results := make([]result, len(a.accounts))
	sem := make(chan struct{}, parallel)
	var wg sync.WaitGroup
	for i, acc := range a.accounts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			gs, err := a.listAccount(ctx, acc)
			results[i] = result{gs, err}
		}()
	}
	wg.Wait()

	// The addresses of the running guests, read at once within what is left of the budget; a guest whose read
	// fails is listed without them. /cluster/resources does not carry QEMU's own status, so a running QEMU guest's
	// status/current is read first, in the same bounded fan-out: a paused or suspended one is then shown as Instance
	// shows it, and its guest agent (which cannot answer while QEMU is paused) is not asked.
	actx, acancel := context.WithTimeout(ctx, addressBudget)
	defer acancel()
	for i, acc := range a.accounts {
		for _, g := range results[i].guests {
			if g.status != "running" {
				continue
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()
				if g.typ == "qemu" {
					var cur current
					if a.call(actx, acc, http.MethodGet, g.path("/status/current"), nil, nil, &cur) == nil && cur.Status != "" {
						g.status, g.qmp = cur.Status, cur.QMPStatus
						if cur.Lock != "" {
							g.lock = cur.Lock
						}
					}
					if g.status != "running" || (g.qmp != "" && g.qmp != "running") {
						return
					}
				}
				g.publicIPs, g.privateIP, g.addrNote = a.addresses(actx, acc, g.ref)
			}()
		}
	}
	wg.Wait()

	var list []wire.CloudInstance
	var problems []string
	var firstErr error
	now, loc := a.now(), core.Locale(ctx)
	for i, acc := range a.accounts {
		if results[i].err != nil {
			problems = append(problems, acc.name)
			if firstErr == nil {
				firstErr = results[i].err
			}
			continue
		}
		for _, g := range results[i].guests {
			list = append(list, a.instance(loc, acc, g, now))
		}
	}
	if len(problems) == len(a.accounts) {
		return nil, nil, firstErr
	}
	return list, problems, nil
}

func (a *adapter) listAccount(ctx context.Context, acc *account) ([]*guest, error) {
	var res []resource
	if err := a.call(ctx, acc, http.MethodGet, "cluster/resources", url.Values{"type": {"vm"}}, nil, &res); err != nil {
		return nil, err
	}
	var out []*guest
	for _, r := range res {
		if r.Template != 0 || (r.Type != "qemu" && r.Type != "lxc") || !validNode(r.Node) || r.VMID <= 0 {
			continue
		}
		out = append(out, &guest{ref: ref{node: r.Node, typ: r.Type, vmid: int(r.VMID)}, name: r.Name,
			status: r.Status, lock: r.Lock, cpus: float64(r.MaxCPU), maxmem: int64(r.MaxMem), maxdisk: int64(r.MaxDisk)})
		if len(out) == core.MaxInstances {
			break
		}
	}
	return out, nil
}

// Instance reads one guest now: where it is from the cluster, then its status and, when it runs, its addresses
// from its node. A guest whose node the cluster cannot reach is answered from the cluster's list (unknown).
func (a *adapter) Instance(ctx context.Context, in wire.CloudInstanceRef) (wire.CloudInstance, error) {
	ctx, cancel := context.WithTimeout(ctx, instanceBudget)
	defer cancel()
	acc, r, err := a.lookup(in, core.Locale(ctx))
	if err != nil {
		return wire.CloudInstance{}, err
	}
	loc, res, err := a.locate(ctx, acc, r)
	if err != nil {
		return wire.CloudInstance{}, err
	}
	if res.Status == "unknown" || res.Status == "" {
		g := &guest{ref: loc, name: res.Name, status: res.Status, lock: res.Lock,
			cpus: float64(res.MaxCPU), maxmem: int64(res.MaxMem), maxdisk: int64(res.MaxDisk)}
		return a.instance(core.Locale(ctx), acc, g, a.now()), nil
	}
	var cur current
	loc, _, err = a.onNode(ctx, acc, loc, func(at ref) error {
		cur = current{}
		return a.call(ctx, acc, http.MethodGet, at.path("/status/current"), nil, nil, &cur)
	})
	if err != nil {
		return wire.CloudInstance{}, err
	}
	if cur.Template != 0 {
		return wire.CloudInstance{}, isTemplate(core.Locale(ctx), in.ID)
	}
	g := &guest{ref: loc, name: cur.Name, status: cur.Status, qmp: cur.QMPStatus, lock: cur.Lock,
		cpus: float64(cur.CPUs), maxmem: int64(cur.MaxMem), maxdisk: int64(cur.MaxDisk)}
	if g.status == "running" && (g.typ != "qemu" || g.qmp == "" || g.qmp == "running") {
		g.publicIPs, g.privateIP, g.addrNote = a.addresses(ctx, acc, loc)
	}
	return a.instance(core.Locale(ctx), acc, g, a.now()), nil
}

// Action powers a guest or takes a snapshot. Proxmox VE answers each with a task (UPID) it runs on: accepted.
// Its API takes no client token, so the request id is not passed on (the Provider's record keeps retries single).
func (a *adapter) Action(ctx context.Context, act core.Action) (wire.CloudActionResult, error) {
	ctx, cancel := context.WithTimeout(ctx, actionBudget)
	defer cancel()
	loc := act.Locale
	if loc == "" {
		loc = core.Locale(ctx)
	}
	acc, r, err := a.lookup(act.Ref, loc)
	if err != nil {
		return wire.CloudActionResult{}, err
	}
	unsupported := func() (wire.CloudActionResult, error) {
		return wire.CloudActionResult{}, core.Unsupported("%s", core.T(loc,
			"Proxmox VE does not offer this action here.", "Proxmox VE 不支持这个操作。"))
	}
	var op string
	switch act.Kind {
	case wire.CloudActionKindStart:
		op = "start"
	case wire.CloudActionKindStop:
		op = "shutdown"
	case wire.CloudActionKindReboot:
		op = "reboot"
	case wire.CloudActionKindHardReset:
		if r.typ != "qemu" {
			return unsupported()
		}
		op = "reset"
	case wire.CloudActionKindForceStop:
		op = "stop"
	case wire.CloudActionKindSnapshotCreate:
		return a.snapshotCreate(ctx, acc, r, act)
	default:
		return unsupported()
	}
	var upid string
	if _, _, err := a.onNode(ctx, acc, r, func(at ref) error {
		return a.call(ctx, acc, http.MethodPost, at.path("/status/"+op), nil, url.Values{}, &upid)
	}); err != nil {
		return wire.CloudActionResult{}, err
	}
	return wire.CloudActionResult{Status: wire.CloudActionStatusAccepted}, nil
}

func (a *adapter) snapshotCreate(ctx context.Context, acc *account, r ref, act core.Action) (wire.CloudActionResult, error) {
	name := snapName(act.Name, a.now())
	desc := strings.TrimSpace(act.Name)
	if desc == "" {
		desc = "Taken by Persona"
	}
	form := url.Values{"snapname": {name}, "description": {desc}}
	var upid string
	if _, _, err := a.onNode(ctx, acc, r, func(at ref) error {
		return a.call(ctx, acc, http.MethodPost, at.path("/snapshot"), nil, form, &upid)
	}); err != nil {
		return wire.CloudActionResult{}, err
	}
	return wire.CloudActionResult{Status: wire.CloudActionStatusAccepted, Snapshot: &wire.CloudSnapshot{
		ID: wire.CloudSnapshotID(name), Name: name, State: wire.CloudSnapshotStateCreating, CreatedAt: a.now().UTC()}}, nil
}

// snapName is a valid Proxmox VE snapshot name from what the user asked for: it starts with a letter, holds only
// letters, digits, "_" and "-", and is 2 to 40 characters (pve-common's configuration id); "persona-<time>" when
// nothing usable of the request is left or what is left is a name Proxmox VE reserves (current, pending, vzdump).
func snapName(asked string, now time.Time) string {
	var b strings.Builder
	dash := false
	for _, c := range strings.TrimSpace(asked) {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '_', c == '-':
			b.WriteRune(c)
			dash = false
		default:
			if !dash && b.Len() > 0 {
				b.WriteByte('-')
				dash = true
			}
		}
	}
	s := strings.Trim(b.String(), "-_")
	if s != "" {
		if c := s[0]; !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z') {
			s = "s-" + s
		}
		if len(s) > 40 {
			s = strings.TrimRight(s[:40], "-_")
		}
	}
	switch strings.ToLower(s) {
	case "", "current", "pending", "vzdump":
		return "persona-" + now.UTC().Format("20060102-150405")
	}
	if len(s) < 2 {
		return "persona-" + now.UTC().Format("20060102-150405")
	}
	return s
}

// snapshot is one entry of /nodes/{node}/{type}/{vmid}/snapshot.
type snapshot struct {
	Name      string `json:"name"`
	SnapTime  num    `json:"snaptime"`
	SnapState string `json:"snapstate"`
}

// Snapshots lists a guest's snapshots (the pseudo entry "current", the running state, left out).
func (a *adapter) Snapshots(ctx context.Context, in wire.CloudInstanceRef) ([]wire.CloudSnapshot, error) {
	ctx, cancel := context.WithTimeout(ctx, instanceBudget)
	defer cancel()
	acc, r, err := a.lookup(in, core.Locale(ctx))
	if err != nil {
		return nil, err
	}
	var list []snapshot
	if _, _, err := a.onNode(ctx, acc, r, func(at ref) error {
		list = nil
		return a.call(ctx, acc, http.MethodGet, at.path("/snapshot"), nil, nil, &list)
	}); err != nil {
		return nil, err
	}
	out := make([]wire.CloudSnapshot, 0, len(list))
	for _, s := range list {
		if s.Name == "" || s.Name == "current" {
			continue
		}
		state := wire.CloudSnapshotStateReady
		switch s.SnapState {
		case "prepare":
			state = wire.CloudSnapshotStateCreating
		case "delete":
			state = wire.CloudSnapshotStateDeleting
		}
		snap := wire.CloudSnapshot{ID: wire.CloudSnapshotID(s.Name), Name: s.Name, State: state}
		if s.SnapTime > 0 {
			snap.CreatedAt = time.Unix(int64(s.SnapTime), 0).UTC()
		}
		out = append(out, snap)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}
