// Package pterodactyl is the cloud-hosts adapter for game-server panels that speak the Pterodactyl Client API:
// Pterodactyl Panel itself and Pelican Panel. A panel login (one client API key) is an account; a server, named by
// its short identifier, is an instance. Power signals are the standard power actions and backups are snapshots.
package pterodactyl

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nwverse/persona-extensions/extensions/cloud-hosts/internal/core"
	"github.com/nwverse/persona/sdk/go/wire"
)

// vendor names the panel in messages.
const vendor = "The panel"

// acceptHeader is the media type the panel documents for its API (Pelican accepts it too).
const acceptHeader = "Application/vnd.pterodactyl.v1+json"

// Bounds of the work one call does.
const (
	listBudget     = 18 * time.Second // cloud.instances has 20 s for every account
	instanceBudget = 13 * time.Second // cloud.instance has 15 s
	actionBudget   = 27 * time.Second // cloud.action has 30 s
	accountWorkers = 8                // accounts read at once
	statsWorkers   = 8                // resources calls at once, across all accounts
	// statsPerAccount is the most resources calls one listing makes for an account. The panel's client API allows
	// 256 requests a minute per user by default; a listing must leave room for the reads and actions that follow.
	statsPerAccount = 100
	serversPerPage  = 100 // the panel's largest page of servers
	backupsPerPage  = 50  // the panel's largest page of backups
	maxPages        = 50
)

// Config is the adapter's setup.
type Config struct {
	// BaseURL is the panel's address, e.g. https://panel.example.com (a path prefix is kept; a trailing /api/client
	// is dropped). Console links are only made for an https address.
	BaseURL string
	// APIKey is the secret text: one client (account) API key (ptlc_… on Pterodactyl, pacc_… on Pelican), or "name=key" lines for several logins on the same
	// panel (core.ParseNamedTokens). A single key is the account "default".
	APIKey string
	// HTTP is the client to use (nil: core.NewHTTPClient(core.HTTPOptions{})). Tests point it at an httptest server.
	HTTP *http.Client
	// Now is the clock (nil: time.Now).
	Now func() time.Time
}

type adapter struct {
	base     string // the panel's address without a trailing slash
	console  string // the https address console links start with, "" for none
	accounts []core.NamedToken
	tokens   map[string]string
	redact   *core.Redactor // the keys, removed from the panel's error details
	http     *http.Client
	now      func() time.Time
}

// New is the adapter for the panel and keys of c. It answers not_signed_in when the address or the keys are missing
// or cannot be used; the message names what is wrong, never the key.
func New(c Config) (core.Adapter, error) {
	base, err := normalizeBase(c.BaseURL)
	if err != nil {
		return nil, err
	}
	accounts := core.ParseNamedTokens(c.APIKey)
	if len(accounts) == 0 {
		return nil, core.NotSignedIn("No panel API key is set up: add a client / account API key of the panel (" + keyKinds + ").")
	}
	tokens := map[string]string{}
	for _, t := range accounts {
		if !usableToken(t.Token) {
			return nil, core.NotSignedIn("The API key of account %s is not usable: it holds spaces or characters a key cannot have.", t.Account)
		}
		if strings.HasPrefix(t.Token, "ptla_") || strings.HasPrefix(t.Token, "papp_") {
			return nil, core.NotSignedIn("The API key of account %s is an application API key: the panel's client API needs a client / account API key ("+keyKinds+") from the account page.", t.Account)
		}
		tokens[t.Account] = t.Token
	}
	a := &adapter{base: base, console: core.ConsoleBase(base), accounts: accounts, tokens: tokens, redact: &core.Redactor{}, http: c.HTTP, now: c.Now}
	a.redact.Add(a.SecretValues()...)
	if a.http == nil {
		a.http = core.NewHTTPClient(core.HTTPOptions{})
	}
	if a.now == nil {
		a.now = time.Now
	}
	return a, nil
}

// keyKinds names the client API keys the panels issue.
const keyKinds = "ptlc_… on Pterodactyl, pacc_… on Pelican"

func normalizeBase(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", core.NotSignedIn("The panel's address is not set up.")
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.RawQuery != "" || u.Opaque != "" {
		return "", core.NotSignedIn("The panel's address is not a usable http(s) address without credentials or a query.")
	}
	u.Fragment, u.RawPath = "", ""
	p := strings.TrimRight(u.Path, "/")
	for _, suffix := range []string{"/api/client", "/api"} {
		p = strings.TrimSuffix(p, suffix)
	}
	u.Path = p
	return u.String(), nil
}

func usableToken(t string) bool {
	if t == "" || len(t) > 512 {
		return false
	}
	for _, c := range t {
		if c <= ' ' || c >= 0x7f {
			return false
		}
	}
	return true
}

// SecretValues are the keys the adapter holds.
func (a *adapter) SecretValues() []string {
	out := make([]string, 0, len(a.accounts))
	for _, t := range a.accounts {
		out = append(out, t.Token)
	}
	return out
}

// validID is a server identifier (the 8-character short one, or a uuid): nothing that could leave the path.
var validID = regexp.MustCompile(`^[A-Za-z0-9-]{1,64}$`)

// token is the key of ref's account. An id of another shape (a slash, a percent sign, a space) is not_found without
// any request: it could not stay one path segment of the panel's API, and the panel issues none like it.
func (a *adapter) token(ctx context.Context, ref wire.CloudInstanceRef) (string, error) {
	loc := core.Locale(ctx)
	tok, ok := a.tokens[string(ref.Account)]
	if !ok {
		return "", core.NotFound(core.T(loc, "There is no panel account named %s.", "没有名为 %s 的面板账号。"), ref.Account)
	}
	if !validID.MatchString(string(ref.ID)) {
		return "", noServer(loc)
	}
	return tok, nil
}

func noServer(loc string) *core.Error {
	return core.NotFound("%s", core.T(loc, "The panel has no such server.", "面板上没有这台服务器。"))
}

// --- the panel's answers ---

type allocation struct {
	IP        string `json:"ip"`
	IPAlias   string `json:"ip_alias"`
	Port      int    `json:"port"`
	IsDefault bool   `json:"is_default"`
}

type server struct {
	Identifier             string `json:"identifier"`
	UUID                   string `json:"uuid"`
	Name                   string `json:"name"`
	Node                   string `json:"node"`
	Description            string `json:"description"`
	Status                 string `json:"status"` // null, installing, install_failed, reinstall_failed, suspended, restoring_backup
	IsSuspended            bool   `json:"is_suspended"`
	IsInstalling           bool   `json:"is_installing"`
	IsTransferring         bool   `json:"is_transferring"`
	IsNodeUnderMaintenance bool   `json:"is_node_under_maintenance"`
	Limits                 struct {
		Memory float64 `json:"memory"`
		Disk   float64 `json:"disk"`
		CPU    float64 `json:"cpu"`
	} `json:"limits"`
	FeatureLimits struct {
		Backups *float64 `json:"backups"`
	} `json:"feature_limits"`
	Relationships struct {
		Allocations struct {
			Data []struct {
				Attributes allocation `json:"attributes"`
			} `json:"data"`
		} `json:"allocations"`
	} `json:"relationships"`
}

type serverObject struct {
	Attributes server `json:"attributes"`
	Meta       struct {
		// UserPermissions are what the key's user may do on the server ("*" for its owner and the panel's admins).
		// Only the single-server answer has them; nil means unknown.
		UserPermissions []string `json:"user_permissions"`
	} `json:"meta"`
}

// permissions are a user's permissions on one server; nil allows everything (they are not known, the panel decides).
type permissions []string

// can tells whether p allows the permission want (e.g. control.start).
func (p permissions) can(want string) bool {
	if p == nil {
		return true
	}
	for _, have := range p {
		if have == "*" || have == want {
			return true
		}
		if group, ok := strings.CutSuffix(have, ".*"); ok && strings.HasPrefix(want, group+".") {
			return true
		}
	}
	return false
}

type pagination struct {
	Total       int `json:"total"`
	CurrentPage int `json:"current_page"`
	TotalPages  int `json:"total_pages"`
}

type serverList struct {
	Data []serverObject `json:"data"`
	Meta struct {
		Pagination pagination `json:"pagination"`
	} `json:"meta"`
}

type stats struct {
	Attributes struct {
		CurrentState string `json:"current_state"`
		IsSuspended  bool   `json:"is_suspended"`
	} `json:"attributes"`
}

type backup struct {
	UUID         string     `json:"uuid"`
	Name         string     `json:"name"`
	IsSuccessful bool       `json:"is_successful"`
	Bytes        float64    `json:"bytes"`
	CreatedAt    *time.Time `json:"created_at"`
	CompletedAt  *time.Time `json:"completed_at"`
}

type backupObject struct {
	Attributes backup `json:"attributes"`
}

type backupList struct {
	Data []backupObject `json:"data"`
	Meta struct {
		Pagination pagination `json:"pagination"`
	} `json:"meta"`
}

type apiErrors struct {
	Errors []struct {
		Code   string `json:"code"`
		Status string `json:"status"`
		Detail string `json:"detail"`
	} `json:"errors"`
}

// --- requests ---

// call sends one request with the account's key and decodes a 2xx answer into out (when not nil).
func (a *adapter) call(ctx context.Context, token, method, path string, query url.Values, body, out any) error {
	u, err := core.JoinURL(a.base, path, query)
	if err != nil {
		return err
	}
	loc := core.Locale(ctx)
	var rd *bytes.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return core.Failed("%s", core.T(loc, "The request could not be written.", "无法写出请求。"))
		}
		rd = bytes.NewReader(b)
	}
	var req *http.Request
	if rd != nil {
		req, err = http.NewRequestWithContext(ctx, method, u.String(), rd)
	} else {
		req, err = http.NewRequestWithContext(ctx, method, u.String(), nil)
	}
	if err != nil {
		return core.Failed("%s", core.T(loc, "The request could not be made.", "无法发出请求。"))
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", acceptHeader)
	if rd != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := core.Do(ctx, a.http, req)
	if err != nil {
		return err
	}
	if !resp.OK() {
		return panelError(loc, resp.Status, resp.Body, a.redact)
	}
	if out != nil {
		if len(bytes.TrimSpace(resp.Body)) == 0 {
			return core.Failed("%s", core.T(loc, "The panel sent an empty answer.", "面板返回了空的应答。"))
		}
		return resp.JSON(out)
	}
	return nil
}

// panelError maps the panel's error answer ({"errors":[{"code","status","detail"}]}) to the closed codes. The detail
// is the panel's own words, with the keys taken out and not clipped (the Provider clips after it redacts). None of
// these is transient but the rate limit: after a 5xx or a gateway's answer the panel may still have acted.
func panelError(loc string, status int, body []byte, keys *core.Redactor) *core.Error {
	var e apiErrors
	_ = json.Unmarshal(body, &e)
	code, detail := "", ""
	if len(e.Errors) > 0 {
		code, detail = e.Errors[0].Code, core.OneLine(keys.Redact(e.Errors[0].Detail), 0)
	}
	msg := core.T(loc, fmt.Sprintf("The panel answered HTTP %d.", status), fmt.Sprintf("面板返回了 HTTP %d。", status))
	if detail != "" {
		msg = vendor + ": " + detail
	}
	lower := strings.ToLower(detail + " " + code)
	backupLimit := strings.Contains(lower, "toomanybackups") ||
		(strings.Contains(lower, "backup") && strings.Contains(lower, "limit"))
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return core.NotSignedIn("%s", msg)
	case status == http.StatusNotFound:
		return core.NotFound("%s", msg)
	case status == http.StatusConflict:
		return core.InvalidState("%s", msg)
	case status == http.StatusTooManyRequests:
		return core.RateLimited("%s", msg)
	case (status == http.StatusBadRequest || status == http.StatusUnprocessableEntity) && backupLimit:
		return core.Refused("%s", msg)
	case status == http.StatusBadGateway || status == http.StatusGatewayTimeout:
		if detail == "" {
			msg = core.T(loc, "The panel could not reach the server's node (its daemon is down or unreachable).",
				"面板连不上这台服务器所在的节点（节点守护进程已停止或无法访问）。")
		}
		return core.Failed("%s", msg) // a gateway's answer does not prove the node never got the request
	}
	err := core.StatusError(vendor, status, detail)
	err.Message = msg
	return err
}

func serverPath(id wire.CloudInstanceID, rest string) string {
	return "/api/client/servers/" + url.PathEscape(string(id)) + rest
}

// --- reading ---

// stateRead is what /resources said of a server: its current_state, or why it could not be read.
type stateRead struct {
	state     string
	suspended bool
	err       error
	detail    string // why it was not read, when that is not the panel's answer
}

// errNotAsked marks a state the listing did not ask the panel for.
var errNotAsked = core.Failed("the server's state was not asked for")

func (a *adapter) resources(ctx context.Context, token string, id string) stateRead {
	var st stats
	if err := a.call(ctx, token, http.MethodGet, serverPath(wire.CloudInstanceID(id), "/resources"), nil, nil, &st); err != nil {
		return stateRead{err: err}
	}
	return stateRead{state: st.Attributes.CurrentState, suspended: st.Attributes.IsSuspended}
}

// listServers pages through the servers the key can see, at most core.MaxInstances: the Provider treats a list that
// long as possibly incomplete.
func (a *adapter) listServers(ctx context.Context, token string) ([]server, error) {
	var out []server
	for page := 1; page <= maxPages; page++ {
		q := url.Values{"page": {strconv.Itoa(page)}, "per_page": {strconv.Itoa(serversPerPage)}}
		var l serverList
		if err := a.call(ctx, token, http.MethodGet, "/api/client", q, nil, &l); err != nil {
			return nil, err
		}
		for _, s := range l.Data {
			if s.Attributes.Identifier == "" {
				continue
			}
			out = append(out, s.Attributes)
			if len(out) >= core.MaxInstances {
				return out, nil
			}
		}
		p := l.Meta.Pagination
		if len(l.Data) == 0 || p.TotalPages <= page || p.CurrentPage >= p.TotalPages {
			break
		}
	}
	return out, nil
}

// Instances lists every server of every account, with the state of each read from its resources.
func (a *adapter) Instances(ctx context.Context) ([]wire.CloudInstance, []string, error) {
	ctx, cancel := context.WithTimeout(ctx, listBudget)
	defer cancel()
	loc := core.Locale(ctx)
	type result struct {
		list []wire.CloudInstance
		err  error
	}
	results := make([]result, len(a.accounts))
	accSem := make(chan struct{}, accountWorkers)
	statSem := make(chan struct{}, statsWorkers)
	// throttled is set at the panel's first rate-limit answer: no more resources calls are made in this listing, so
	// the reads and actions that follow are not refused for the rest of the minute.
	var throttled atomic.Bool
	var wg sync.WaitGroup
	for i, acc := range a.accounts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case accSem <- struct{}{}:
			case <-ctx.Done():
				results[i].err = ctx.Err()
				return
			}
			defer func() { <-accSem }()
			servers, err := a.listServers(ctx, acc.Token)
			if err != nil {
				results[i].err = err
				return
			}
			reads := make([]stateRead, len(servers))
			var swg sync.WaitGroup
			asked := 0
			for j, s := range servers {
				if blocked(s) != "" {
					continue // its state is in the list's own answer; /resources would be refused
				}
				if asked >= statsPerAccount {
					reads[j] = stateRead{err: errNotAsked, detail: core.T(loc,
						"The panel was not asked for this server's state: the listing has too many servers to read them all.",
						"服务器太多，没有逐台向面板查询状态，这台的状态未查询。")}
					continue
				}
				asked++
				swg.Add(1)
				go func() {
					defer swg.Done()
					select {
					case statSem <- struct{}{}:
					case <-ctx.Done():
						reads[j] = stateRead{err: ctx.Err()}
						return
					}
					defer func() { <-statSem }()
					if throttled.Load() {
						reads[j] = stateRead{err: errNotAsked, detail: core.T(loc,
							"The panel's rate limit was reached, so the server's state was not read.",
							"已达到面板的请求频率上限，没有读取这台服务器的状态。")}
						return
					}
					reads[j] = a.resources(ctx, acc.Token, s.Identifier)
					if ce := core.AsError(reads[j].err); ce != nil && ce.Code == wire.CloudErrorCodeRateLimited {
						throttled.Store(true)
					}
				}()
			}
			swg.Wait()
			now := a.now()
			for j, s := range servers {
				results[i].list = append(results[i].list, a.instance(loc, acc.Account, s, reads[j], now))
			}
		}()
	}
	wg.Wait()

	var list []wire.CloudInstance
	var problems []string
	var firstErr error
	for i, r := range results {
		if r.err != nil {
			problems = append(problems, a.accounts[i].Account)
			if firstErr == nil {
				firstErr = r.err
			}
			continue
		}
		list = append(list, r.list...)
	}
	if len(problems) == len(a.accounts) {
		return nil, nil, firstErr
	}
	return list, problems, nil
}

// Instance reads one server and its resources from the panel now.
func (a *adapter) Instance(ctx context.Context, ref wire.CloudInstanceRef) (wire.CloudInstance, error) {
	tok, err := a.token(ctx, ref)
	if err != nil {
		return wire.CloudInstance{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, instanceBudget)
	defer cancel()
	var (
		obj     serverObject
		viewErr error
		read    stateRead
		wg      sync.WaitGroup
	)
	wg.Add(2)
	go func() {
		defer wg.Done()
		viewErr = a.call(ctx, tok, http.MethodGet, serverPath(ref.ID, ""), nil, nil, &obj)
	}()
	go func() {
		defer wg.Done()
		read = a.resources(ctx, tok, string(ref.ID))
	}()
	wg.Wait()
	if viewErr != nil {
		return wire.CloudInstance{}, viewErr
	}
	if obj.Attributes.Identifier != string(ref.ID) && obj.Attributes.UUID != string(ref.ID) {
		return wire.CloudInstance{}, noServer(core.Locale(ctx))
	}
	in := a.instance(core.Locale(ctx), string(ref.Account), obj.Attributes, read, a.now())
	in.ID = ref.ID // asked by uuid: answered by the same name
	if obj.Meta.UserPermissions != nil {
		in.Actions = allowed(in.Actions, permissions(obj.Meta.UserPermissions))
	}
	return in, nil
}

// needs is the permission each action asks of a subuser (Pterodactyl's and Pelican's permission names).
var needs = map[wire.CloudActionKind]string{
	wire.CloudActionKindStart:          "control.start",
	wire.CloudActionKindStop:           "control.stop",
	wire.CloudActionKindForceStop:      "control.stop",
	wire.CloudActionKindReboot:         "control.restart",
	wire.CloudActionKindSnapshotList:   "backup.read",
	wire.CloudActionKindSnapshotCreate: "backup.create",
}

// allowed are the actions of kinds that p allows: the panel refuses the others with 403.
func allowed(kinds []wire.CloudActionKind, p permissions) []wire.CloudActionKind {
	out := kinds[:0:0]
	for _, k := range kinds {
		if need, ok := needs[k]; !ok || p.can(need) {
			out = append(out, k)
		}
	}
	return out
}

// blocked is why the panel refuses every request to the server but reading it ("" when it does not). The status
// comes first: the panel also sets is_installing for a failed installation (and Pelican for a failed reinstallation).
func blocked(s server) string {
	switch {
	case s.IsSuspended || s.Status == "suspended":
		return "suspended"
	case s.IsNodeUnderMaintenance:
		return "maintenance"
	case s.Status == "install_failed":
		return "install_failed"
	case s.Status == "reinstall_failed" && s.IsInstalling: // Pelican: not installed, so locked
		return "reinstall_failed"
	case s.IsInstalling || s.Status == "installing":
		return "installing"
	case s.Status == "restoring_backup":
		return "restoring"
	case s.IsTransferring:
		return "transferring"
	}
	return ""
}

// instance is s as the hub's CloudInstance, its state from read.
func (a *adapter) instance(loc, account string, s server, read stateRead, now time.Time) wire.CloudInstance {
	in := wire.CloudInstance{
		Account:     wire.CloudAccountID(account),
		ID:          wire.CloudInstanceID(s.Identifier),
		DisplayName: s.Name,
		Region:      s.Node,
		ObservedAt:  now,
	}
	if in.DisplayName == "" {
		in.DisplayName = s.Identifier
	}
	in.PublicIps, in.PrivateIps = addresses(s)
	in.Spec = spec(s)
	if a.console != "" {
		in.ConsoleURL = a.console + "/server/" + url.PathEscape(s.Identifier)
	}

	power := true
	switch blocked(s) {
	case "suspended":
		in.State, in.StateDetail, power = wire.CloudInstanceStateStopped, core.T(loc, "The server is suspended by the panel's administrator.", "这台服务器已被面板管理员暂停。"), false
	case "maintenance":
		in.State, in.StateDetail, power = wire.CloudInstanceStateUnknown, core.T(loc, "The server's node is under maintenance.", "这台服务器所在的节点正在维护。"), false
	case "installing":
		in.State, in.StateDetail, power = wire.CloudInstanceStateUnknown, core.T(loc, "The server is being installed.", "这台服务器正在安装。"), false
	case "install_failed":
		in.State, in.StateDetail, power = wire.CloudInstanceStateUnknown, core.T(loc, "The server's installation failed.", "这台服务器安装失败。"), false
	case "reinstall_failed":
		in.State, in.StateDetail, power = wire.CloudInstanceStateUnknown, core.T(loc, "The server's reinstallation failed.", "这台服务器重装失败。"), false
	case "restoring":
		in.State, in.StateDetail, power = wire.CloudInstanceStateUnknown, core.T(loc, "The server is being restored from a backup.", "这台服务器正在从备份恢复。"), false
	case "transferring":
		in.State, in.StateDetail, power = wire.CloudInstanceStateUnknown, core.T(loc, "The server is being moved to another node.", "这台服务器正在迁移到另一个节点。"), false
	default:
		switch {
		case read.err == nil && read.suspended:
			in.State, in.StateDetail, power = wire.CloudInstanceStateStopped, core.T(loc, "The server is suspended by the panel's administrator.", "这台服务器已被面板管理员暂停。"), false
		case read.err != nil:
			in.State, in.StateDetail = wire.CloudInstanceStateUnknown, core.T(loc, "The panel could not say whether the server is running.", "面板无法确认这台服务器是否在运行。")
			if read.detail != "" {
				in.StateDetail = read.detail
			}
			if ce := core.AsError(read.err); ce.Code == wire.CloudErrorCodeInvalidState {
				in.StateDetail, power = core.T(loc, "The panel does not allow actions on the server now.", "面板现在不允许操作这台服务器。"), false
			}
		default:
			switch read.state {
			case "running":
				in.State = wire.CloudInstanceStateRunning
			case "starting":
				in.State = wire.CloudInstanceStateStarting
			case "stopping":
				in.State = wire.CloudInstanceStateStopping
			case "offline", "stopped", "exited": // "stopped" is the panel's own word when the daemon gave none
				in.State = wire.CloudInstanceStateStopped
			default:
				in.State, in.StateDetail = wire.CloudInstanceStateUnknown, core.T(loc, "The panel reported a state this extension does not know.", "面板报告了一个本扩展不认识的状态。")
			}
		}
		if s.Status == "reinstall_failed" && in.StateDetail == "" {
			in.StateDetail = core.T(loc, "The server's last reinstallation failed.", "这台服务器上次重装失败。")
		}
	}

	var actions []wire.CloudActionKind
	if power {
		switch in.State {
		case wire.CloudInstanceStateRunning, wire.CloudInstanceStateStarting:
			actions = append(actions, wire.CloudActionKindStop, wire.CloudActionKindReboot, wire.CloudActionKindForceStop)
		case wire.CloudInstanceStateStopping:
			actions = append(actions, wire.CloudActionKindForceStop)
		case wire.CloudInstanceStateStopped:
			actions = append(actions, wire.CloudActionKindStart)
		default: // the state could not be read: let the panel decide
			actions = append(actions, wire.CloudActionKindStart, wire.CloudActionKindStop, wire.CloudActionKindReboot, wire.CloudActionKindForceStop)
		}
	}
	actions = append(actions, wire.CloudActionKindSyncIp)
	if power && s.FeatureLimits.Backups != nil && *s.FeatureLimits.Backups > 0 {
		actions = append(actions, wire.CloudActionKindSnapshotList, wire.CloudActionKindSnapshotCreate)
	}
	if in.ConsoleURL != "" {
		actions = append(actions, wire.CloudActionKindConsole)
	}
	in.Actions = actions
	return in
}

// addresses are the default allocation's: its alias, then its address, each as public or private; unspecified
// addresses (0.0.0.0) and host names are dropped.
func addresses(s server) (public, private []string) {
	data := s.Relationships.Allocations.Data
	if len(data) == 0 {
		return nil, nil
	}
	alloc := data[0].Attributes
	for _, d := range data {
		if d.Attributes.IsDefault {
			alloc = d.Attributes
			break
		}
	}
	seen := map[string]bool{}
	for _, cand := range []string{alloc.IPAlias, alloc.IP} {
		ip := core.PlainIP(cand)
		if ip == "" || seen[ip] {
			continue
		}
		seen[ip] = true
		if isPrivate(ip) {
			private = append(private, ip)
		} else {
			public = append(public, ip)
		}
	}
	return public, private
}

var cgnat = netip.MustParsePrefix("100.64.0.0/10")

func isPrivate(ip string) bool {
	a, err := netip.ParseAddr(ip)
	if err != nil {
		return true
	}
	return a.IsPrivate() || a.IsLoopback() || a.IsLinkLocalUnicast() || cgnat.Contains(a)
}

// spec is the server's limits: cpu in percent (100 = one cpu), memory and disk in MiB; 0 is unlimited.
func spec(s server) *wire.CloudSpec {
	var sp wire.CloudSpec
	if s.Limits.CPU > 0 {
		sp.Cpu = math.Round(s.Limits.CPU) / 100
	}
	if s.Limits.Memory > 0 {
		sp.MemoryMb = int64(math.Round(s.Limits.Memory))
	}
	if s.Limits.Disk > 0 {
		sp.DiskGb = int64(math.Max(1, math.Round(s.Limits.Disk/1024)))
	}
	if sp == (wire.CloudSpec{}) {
		return nil
	}
	return &sp
}

// --- writing ---

var signals = map[wire.CloudActionKind]string{
	wire.CloudActionKindStart:     "start",
	wire.CloudActionKindStop:      "stop",
	wire.CloudActionKindReboot:    "restart",
	wire.CloudActionKindForceStop: "kill",
}

// Action sends a power signal or starts a backup. The panel has no idempotency key of its own, so request_id is not
// passed on (the Provider's record answers a repeated one).
func (a *adapter) Action(ctx context.Context, act core.Action) (wire.CloudActionResult, error) {
	signal, isPower := signals[act.Kind]
	if !isPower && act.Kind != wire.CloudActionKindSnapshotCreate {
		return wire.CloudActionResult{}, core.Unsupported("%s", core.T(act.Locale, "The panel does not offer this action.", "面板不提供这个操作。"))
	}
	if core.Locale(ctx) == "" {
		ctx = core.WithLocale(ctx, act.Locale)
	}
	tok, err := a.token(ctx, act.Ref)
	if err != nil {
		return wire.CloudActionResult{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, actionBudget)
	defer cancel()
	if isPower {
		if err := a.call(ctx, tok, http.MethodPost, serverPath(act.Ref.ID, "/power"), nil, map[string]string{"signal": signal}, nil); err != nil {
			return wire.CloudActionResult{}, err
		}
		return wire.CloudActionResult{Status: wire.CloudActionStatusAccepted}, nil
	}
	body := map[string]any{}
	if act.Name != "" {
		body["name"] = act.Name
	}
	var b backupObject
	if err := a.call(ctx, tok, http.MethodPost, serverPath(act.Ref.ID, "/backups"), nil, body, &b); err != nil {
		return wire.CloudActionResult{}, err
	}
	res := wire.CloudActionResult{Status: wire.CloudActionStatusAccepted}
	if b.Attributes.UUID != "" {
		snap := snapshot(b.Attributes)
		res.Snapshot = &snap
		if snap.State == wire.CloudSnapshotStateReady {
			res.Status = wire.CloudActionStatusDone
		}
	}
	return res, nil
}

// Snapshots are the server's backups, at most core.MaxSnapshots.
func (a *adapter) Snapshots(ctx context.Context, ref wire.CloudInstanceRef) ([]wire.CloudSnapshot, error) {
	tok, err := a.token(ctx, ref)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, instanceBudget)
	defer cancel()
	var out []wire.CloudSnapshot
	for page := 1; page <= maxPages && len(out) < core.MaxSnapshots; page++ {
		q := url.Values{"page": {strconv.Itoa(page)}, "per_page": {strconv.Itoa(backupsPerPage)}}
		var l backupList
		if err := a.call(ctx, tok, http.MethodGet, serverPath(ref.ID, "/backups"), q, nil, &l); err != nil {
			return nil, err
		}
		for _, b := range l.Data {
			if b.Attributes.UUID != "" {
				out = append(out, snapshot(b.Attributes))
			}
		}
		p := l.Meta.Pagination
		if len(l.Data) == 0 || p.TotalPages <= page || p.CurrentPage >= p.TotalPages {
			break
		}
	}
	if len(out) > core.MaxSnapshots {
		out = out[:core.MaxSnapshots]
	}
	return out, nil
}

func snapshot(b backup) wire.CloudSnapshot {
	s := wire.CloudSnapshot{ID: wire.CloudSnapshotID(b.UUID), Name: b.Name}
	switch {
	case b.CompletedAt == nil:
		s.State = wire.CloudSnapshotStateCreating
	case b.IsSuccessful:
		s.State = wire.CloudSnapshotStateReady
	default:
		s.State = wire.CloudSnapshotStateFailed
	}
	if b.CreatedAt != nil {
		s.CreatedAt = *b.CreatedAt
	}
	if b.Bytes > 0 {
		s.SizeGb = math.Round(b.Bytes/(1<<30)*100) / 100
	}
	return s
}

var _ core.SecretHolder = (*adapter)(nil)

// String never shows a key.
func (a *adapter) String() string { return fmt.Sprintf("pterodactyl(%d accounts)", len(a.accounts)) }
