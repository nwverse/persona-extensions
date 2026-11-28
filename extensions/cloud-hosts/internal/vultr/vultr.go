// Package vultr is the cloud-hosts adapter for Vultr (API v2): its instances, their power actions, and taking a
// snapshot of one.
//
// An account is one API key (a Vultr account): the secret holds one key, or "name=key" lines for several accounts.
// An instance id is Vultr's instance id (a UUID) as given.
//
// Vultr has no graceful stop or graceful reboot: its halt is a hard power off (force_stop) and its reboot a hard
// reboot (hard_reset), so stop and reboot are never offered. Its snapshot list is account-wide and does not say which
// instance a snapshot was taken of, so snapshot_list is not offered either; snapshot_create is.
package vultr

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona-extensions/extensions/cloud-hosts/internal/core"
	"github.com/nwverse/persona/sdk/go/wire"
)

// DefaultBaseURL is the Vultr API.
const DefaultBaseURL = "https://api.vultr.com/v2"

// consoleBase is the customer portal's page of one instance; the instance id follows.
const consoleBase = "https://my.vultr.com/subs/?id="

const (
	vendor      = "Vultr"
	perPage     = 500
	maxPages    = 10
	parallel    = 8
	listBudget  = 18 * time.Second
	readBudget  = 14 * time.Second
	writeBudget = 28 * time.Second
)

// Config is the adapter's setup.
type Config struct {
	// Tokens is the secret: one API key, or "name=key" lines for several accounts (core.ParseNamedTokens).
	Tokens string
	// BaseURL overrides the API address, for tests only (DefaultBaseURL when empty).
	BaseURL string
	// HTTP is the client (core.NewHTTPClient when nil).
	HTTP *http.Client
	// Now is the clock (time.Now when nil).
	Now func() time.Time
}

type adapter struct {
	base     string
	client   *http.Client
	now      func() time.Time
	accounts []core.NamedToken
	byName   map[string]string
}

// New is the adapter for the accounts of c.Tokens: not_signed_in when there is no usable key.
func New(c Config) (core.Adapter, error) {
	base := strings.TrimSpace(c.BaseURL)
	if base == "" {
		base = DefaultBaseURL
	}
	if _, err := core.JoinURL(base, "/instances", nil); err != nil {
		return nil, err
	}
	accounts := core.ParseNamedTokens(c.Tokens)
	if len(accounts) == 0 {
		return nil, core.NotSignedIn("No Vultr API key is set up.")
	}
	a := &adapter{base: base, client: c.HTTP, now: c.Now, accounts: accounts, byName: map[string]string{}}
	for _, t := range accounts {
		if strings.ContainsFunc(t.Token, func(r rune) bool { return r <= ' ' || r == 0x7f }) {
			return nil, core.NotSignedIn("The Vultr API key of account %s is not usable.", t.Account)
		}
		a.byName[t.Account] = t.Token
	}
	if a.client == nil {
		a.client = core.NewHTTPClient(core.HTTPOptions{})
	}
	if a.now == nil {
		a.now = time.Now
	}
	return a, nil
}

// SecretValues are the API keys.
func (a *adapter) SecretValues() []string {
	out := make([]string, 0, len(a.accounts))
	for _, t := range a.accounts {
		out = append(out, t.Token)
	}
	return out
}

// --- the vendor's JSON ---

// instance is Vultr's. Its "kvm" field is a console address that carries a token of its own: it is never read.
type instance struct {
	ID           string  `json:"id"`
	Label        string  `json:"label"`
	Hostname     string  `json:"hostname"`
	OS           string  `json:"os"`
	RAM          int64   `json:"ram"`
	Disk         int64   `json:"disk"`
	MainIP       string  `json:"main_ip"`
	V6MainIP     string  `json:"v6_main_ip"`
	InternalIP   string  `json:"internal_ip"`
	VCPUCount    float64 `json:"vcpu_count"`
	Region       string  `json:"region"`
	Plan         string  `json:"plan"`
	Status       string  `json:"status"`
	PowerStatus  string  `json:"power_status"`
	ServerStatus string  `json:"server_status"`
}

type snapshot struct {
	ID          string  `json:"id"`
	DateCreated string  `json:"date_created"`
	Description string  `json:"description"`
	Size        float64 `json:"size"`
	Status      string  `json:"status"`
}

// --- requests ---

func (a *adapter) call(ctx context.Context, token, method, path string, q url.Values, body any, out any) error {
	u, err := core.JoinURL(a.base, path, q)
	if err != nil {
		return err
	}
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return core.Failed("the request could not be made")
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), rd)
	if err != nil {
		return core.Failed("the request could not be made")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := core.Do(ctx, a.client, req)
	if err != nil {
		return err
	}
	if !resp.OK() {
		var e struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(resp.Body, &e)
		return vendorError(resp.Status, e.Error, token, core.Locale(ctx))
	}
	if out != nil {
		return resp.JSON(out)
	}
	return nil
}

// scrub is s without the key, for a vendor that echoes it back. It is not clipped: the Provider redacts every
// credential first and clips after.
func scrub(s, token string) string {
	if token != "" {
		s = strings.ReplaceAll(s, token, "[redacted]")
	}
	return core.OneLine(s, 0)
}

func containsAny(s string, words ...string) bool {
	for _, w := range words {
		if strings.Contains(s, w) {
			return true
		}
	}
	return false
}

// vendorError is Vultr's error in the hub's vocabulary: Vultr answers a status and a sentence, so the sentence's
// wording decides between the codes a status can mean. Only a rate limit is transient: after a 5xx the request may
// still have been carried out.
func vendorError(status int, message, token, loc string) *core.Error {
	msg := scrub(message, token)
	low := strings.ToLower(msg)
	text := vendor + ": " + msg
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		if msg == "" {
			return core.NotSignedIn("%s", core.T(loc, "Vultr did not accept the API key.", "Vultr 未接受这个 API 密钥。"))
		}
		return core.NotSignedIn("%s", text)
	case status == http.StatusNotFound:
		return core.StatusError(vendor, status, msg)
	case status == http.StatusTooManyRequests:
		return core.RateLimited("%s", text)
	case (status == http.StatusBadRequest || status == http.StatusPreconditionFailed) &&
		containsAny(low, "invalid instance", "instance not found", "unable to find instance"):
		return core.NotFound("%s", text)
	case containsAny(low, "capacity", "out of stock", "sold out") ||
		(status == http.StatusServiceUnavailable && containsAny(low, "not available", "unavailable")):
		return core.Capacity("%s", text)
	case (status == http.StatusBadRequest || status == http.StatusPreconditionFailed || status == http.StatusConflict || status == http.StatusUnprocessableEntity) &&
		containsAny(low, "already", "not running", "is running", "is stopped", "powered", "locked", "pending", "state",
			"must be", "halted", "installing", "in progress", "booting"):
		return core.InvalidState("%s", text)
	}
	return core.StatusError(vendor, status, msg)
}

// validID reports whether id can be a Vultr instance id (a UUID: hex digits and dashes).
func validID(id wire.CloudInstanceID) bool {
	s := string(id)
	if s == "" || len(s) > 64 {
		return false
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F' || c == '-') {
			return false
		}
	}
	return true
}

// --- instances ---

func (a *adapter) Instances(ctx context.Context) ([]wire.CloudInstance, []string, error) {
	ctx, cancel := context.WithTimeout(ctx, listBudget)
	defer cancel()
	lists := make([][]wire.CloudInstance, len(a.accounts))
	errs := make([]error, len(a.accounts))
	sem := make(chan struct{}, parallel)
	var wg sync.WaitGroup
	for i, acct := range a.accounts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			lists[i], errs[i] = a.list(ctx, acct, core.Locale(ctx))
		}()
	}
	wg.Wait()
	var out []wire.CloudInstance
	var problems []string
	failed := 0
	for i, acct := range a.accounts {
		if errs[i] != nil {
			failed++
			problems = append(problems, acct.Account)
			continue
		}
		out = append(out, lists[i]...)
	}
	if failed == len(a.accounts) {
		if len(a.accounts) == 1 {
			return nil, nil, errs[0]
		}
		e := core.AsError(errs[0])
		return nil, nil, &core.Error{Code: e.Code, Message: a.accounts[0].Account + ": " + e.Message, Transient: e.Transient}
	}
	// The Provider takes a list of MaxInstances as one that may have more, and says so.
	if len(out) > core.MaxInstances {
		out = out[:core.MaxInstances]
	}
	return out, problems, nil
}

func (a *adapter) list(ctx context.Context, acct core.NamedToken, loc string) ([]wire.CloudInstance, error) {
	var out []wire.CloudInstance
	cursor := ""
	for n := 0; n < maxPages && len(out) < core.MaxInstances; n++ {
		var resp struct {
			Instances []instance `json:"instances"`
			Meta      struct {
				Links struct {
					Next string `json:"next"`
				} `json:"links"`
			} `json:"meta"`
		}
		q := url.Values{"per_page": {"500"}}
		if cursor != "" {
			q.Set("cursor", cursor)
		}
		if err := a.call(ctx, acct.Token, http.MethodGet, "/instances", q, nil, &resp); err != nil {
			return nil, err
		}
		for _, in := range resp.Instances {
			if in.ID != "" && len(out) < core.MaxInstances {
				out = append(out, a.instance(acct.Account, in, loc))
			}
		}
		next := resp.Meta.Links.Next
		if next == "" || next == cursor || len(resp.Instances) == 0 {
			break
		}
		cursor = next
	}
	return out, nil
}

func (a *adapter) Instance(ctx context.Context, ref wire.CloudInstanceRef) (wire.CloudInstance, error) {
	ctx, cancel := context.WithTimeout(ctx, readBudget)
	defer cancel()
	token, ok := a.byName[string(ref.Account)]
	if !ok || !validID(ref.ID) {
		return wire.CloudInstance{}, noInstance(ctx)
	}
	var resp struct {
		Instance *instance `json:"instance"`
	}
	if err := a.call(ctx, token, http.MethodGet, "/instances/"+url.PathEscape(string(ref.ID)), nil, nil, &resp); err != nil {
		return wire.CloudInstance{}, err
	}
	if resp.Instance == nil || resp.Instance.ID != string(ref.ID) {
		return wire.CloudInstance{}, noInstance(ctx)
	}
	return a.instance(string(ref.Account), *resp.Instance, core.Locale(ctx)), nil
}

// noInstance is the not_found of an instance that is not there (or an id that cannot be one).
func noInstance(ctx context.Context) *core.Error {
	return core.NotFound("%s", core.T(core.Locale(ctx), "Vultr: no such instance.", "Vultr：没有这台实例。"))
}

// state is the hub's state for a Vultr instance, a detail in loc's language, and whether its power can be switched
// now.
func state(in instance, loc string) (wire.CloudInstanceState, string, bool) {
	st, detail, power := wire.CloudInstanceStateUnknown, "", false
	switch {
	case in.Status == "suspended":
		st, detail = wire.CloudInstanceStateStopped, core.T(loc, "Vultr has suspended the instance.", "Vultr 已暂停这台实例。")
	case in.Status == "resizing":
		detail = core.T(loc, "The instance is being resized.", "实例正在调整配置。")
	case in.Status == "pending":
		st, detail = wire.CloudInstanceStateStarting, core.T(loc, "The instance is being set up.", "实例正在创建。")
	case in.ServerStatus == "installingbooting":
		st, detail = wire.CloudInstanceStateStarting, core.T(loc, "The instance is installing or booting.", "实例正在安装系统或启动。")
	case in.PowerStatus == "running":
		st, power = wire.CloudInstanceStateRunning, true
	case in.PowerStatus == "stopped":
		st, power = wire.CloudInstanceStateStopped, true
	default:
		word := core.OneLine(in.PowerStatus, 0) // Vultr's own word, passed on as it is
		detail = core.T(loc, "Vultr reports the power status "+word+".", "Vultr 报告的电源状态为 "+word+"。")
	}
	if in.ServerStatus == "locked" {
		detail = strings.TrimSpace(detail + " " + core.T(loc,
			"Vultr is working on the instance; power actions are possible once it is done.",
			"Vultr 正在处理这台实例，完成后才能进行开关机操作。"))
		power = false
	}
	return st, detail, power
}

func actions(st wire.CloudInstanceState, power bool) []wire.CloudActionKind {
	sync, cons := wire.CloudActionKindSyncIp, wire.CloudActionKindConsole
	create := wire.CloudActionKindSnapshotCreate
	switch {
	case power && st == wire.CloudInstanceStateRunning:
		return []wire.CloudActionKind{wire.CloudActionKindHardReset, wire.CloudActionKindForceStop, sync, create, cons}
	case power && st == wire.CloudInstanceStateStopped:
		return []wire.CloudActionKind{wire.CloudActionKindStart, sync, create, cons}
	}
	return []wire.CloudActionKind{sync, cons}
}

func (a *adapter) instance(account string, v instance, loc string) wire.CloudInstance {
	st, detail, power := state(v, loc)
	name := v.Label
	if name == "" {
		name = v.Hostname
	}
	in := wire.CloudInstance{
		Account:     wire.CloudAccountID(account),
		ID:          wire.CloudInstanceID(v.ID),
		DisplayName: name,
		State:       st,
		StateDetail: detail,
		Region:      v.Region,
		PublicIps:   core.PlainIPs(v.MainIP, v.V6MainIP),
		PrivateIps:  core.PlainIPs(v.InternalIP),
		Actions:     actions(st, power),
		ObservedAt:  a.now(),
	}
	if validID(in.ID) {
		in.ConsoleURL = consoleBase + url.QueryEscape(v.ID)
	}
	spec := wire.CloudSpec{Size: v.Plan, Cpu: v.VCPUCount, MemoryMb: v.RAM, DiskGb: v.Disk, OS: v.OS}
	if spec != (wire.CloudSpec{}) {
		in.Spec = &spec
	}
	return in
}

// --- actions ---

func (a *adapter) Action(ctx context.Context, act core.Action) (wire.CloudActionResult, error) {
	ctx, cancel := context.WithTimeout(ctx, writeBudget)
	defer cancel()
	var verb string
	switch act.Kind {
	case wire.CloudActionKindStart:
		verb = "start"
	case wire.CloudActionKindForceStop:
		verb = "halt"
	case wire.CloudActionKindHardReset:
		verb = "reboot"
	case wire.CloudActionKindSnapshotCreate:
	default:
		return wire.CloudActionResult{}, core.Unsupported("%s", core.T(act.Locale, "Vultr instances do not offer this action.", "Vultr 实例不支持这个操作。"))
	}
	token, ok := a.byName[string(act.Ref.Account)]
	if !ok || !validID(act.Ref.ID) {
		return wire.CloudActionResult{}, noInstance(ctx)
	}
	id := string(act.Ref.ID)
	if verb != "" {
		if err := a.call(ctx, token, http.MethodPost, "/instances/"+url.PathEscape(id)+"/"+verb, nil, nil, nil); err != nil {
			return wire.CloudActionResult{}, err
		}
		return wire.CloudActionResult{Status: wire.CloudActionStatusAccepted}, nil
	}
	body := map[string]string{"instance_id": id}
	if act.Name != "" {
		body["description"] = act.Name
	}
	var resp struct {
		Snapshot *snapshot `json:"snapshot"`
	}
	if err := a.call(ctx, token, http.MethodPost, "/snapshots", nil, body, &resp); err != nil {
		return wire.CloudActionResult{}, err
	}
	res := wire.CloudActionResult{Status: wire.CloudActionStatusAccepted}
	if resp.Snapshot != nil && resp.Snapshot.ID != "" {
		s := wire.CloudSnapshot{ID: wire.CloudSnapshotID(resp.Snapshot.ID), Name: resp.Snapshot.Description}
		switch resp.Snapshot.Status {
		case "complete":
			s.State = wire.CloudSnapshotStateReady
		case "deleted", "deleting":
			s.State = wire.CloudSnapshotStateDeleting
		case "failed":
			s.State = wire.CloudSnapshotStateFailed
		default:
			s.State = wire.CloudSnapshotStateCreating
		}
		if t, err := time.Parse(time.RFC3339, resp.Snapshot.DateCreated); err == nil {
			s.CreatedAt = t.UTC()
		}
		if resp.Snapshot.Size > 0 {
			s.SizeGb = resp.Snapshot.Size / (1 << 30)
		}
		if s.Name == "" {
			s.Name = act.Name
		}
		res.Snapshot = &s
	}
	return res, nil
}

// Snapshots is unsupported: Vultr's snapshot list does not say which instance a snapshot was taken of.
func (a *adapter) Snapshots(ctx context.Context, _ wire.CloudInstanceRef) ([]wire.CloudSnapshot, error) {
	return nil, core.Unsupported("%s", core.T(core.Locale(ctx), "Vultr does not list snapshots by instance.", "Vultr 无法按实例列出快照。"))
}
