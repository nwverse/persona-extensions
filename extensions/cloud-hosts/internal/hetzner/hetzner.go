// Package hetzner is the cloud-hosts adapter for Hetzner Cloud (API v1): its servers as instances, their power
// actions, and the snapshot images taken of them.
//
// An account is one API token (a Hetzner Cloud project): the secret holds one token, or "name=token" lines for
// several projects. An instance id is the server's numeric id as a decimal string.
package hetzner

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona-extensions/extensions/cloud-hosts/internal/core"
	"github.com/nwverse/persona/sdk/go/wire"
)

// DefaultBaseURL is the Hetzner Cloud API.
const DefaultBaseURL = "https://api.hetzner.cloud/v1"

// ConsoleURL is the console every server links to: the API does not say which project a token belongs to, so the
// link is the console's start page rather than the server's own page.
const ConsoleURL = "https://console.hetzner.cloud/"

const (
	vendor      = "Hetzner"
	perPage     = 50
	maxPages    = 20 // of images, newest first: 1000 snapshots looked through at most
	parallel    = 8  // accounts read at once
	listBudget  = 18 * time.Second
	readBudget  = 14 * time.Second
	writeBudget = 28 * time.Second
)

// Config is the adapter's setup.
type Config struct {
	// Tokens is the secret: one API token, or "name=token" lines for several accounts (core.ParseNamedTokens).
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

// New is the adapter for the accounts of c.Tokens: not_signed_in when there is no usable token.
func New(c Config) (core.Adapter, error) {
	base := strings.TrimSpace(c.BaseURL)
	if base == "" {
		base = DefaultBaseURL
	}
	if _, err := core.JoinURL(base, "/servers", nil); err != nil {
		return nil, err
	}
	accounts := core.ParseNamedTokens(c.Tokens)
	if len(accounts) == 0 {
		return nil, core.NotSignedIn("No Hetzner Cloud API token is set up.")
	}
	a := &adapter{base: base, client: c.HTTP, now: c.Now, accounts: accounts, byName: map[string]string{}}
	for _, t := range accounts {
		if strings.ContainsFunc(t.Token, func(r rune) bool { return r <= ' ' || r == 0x7f }) {
			return nil, core.NotSignedIn("The Hetzner Cloud API token of account %s is not usable.", t.Account)
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

// SecretValues are the tokens.
func (a *adapter) SecretValues() []string {
	out := make([]string, 0, len(a.accounts))
	for _, t := range a.accounts {
		out = append(out, t.Token)
	}
	return out
}

// --- the vendor's JSON ---

type apiError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type server struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	Status    string `json:"status"`
	PublicNet struct {
		IPv4 *struct {
			IP string `json:"ip"`
		} `json:"ipv4"`
		IPv6 *struct {
			IP string `json:"ip"`
		} `json:"ipv6"`
	} `json:"public_net"`
	PrivateNet []struct {
		IP string `json:"ip"`
	} `json:"private_net"`
	Locked     bool `json:"locked"` // an action is running on the server: it takes no other until that one is done
	ServerType *struct {
		Name         string  `json:"name"`
		Cores        float64 `json:"cores"`
		Memory       float64 `json:"memory"`
		Disk         float64 `json:"disk"`
		Architecture string  `json:"architecture"`
	} `json:"server_type"`
	Location *struct {
		Name string `json:"name"`
	} `json:"location"`
	Image *struct {
		Description string `json:"description"`
	} `json:"image"`
}

type pagination struct {
	Meta struct {
		Pagination struct {
			NextPage *int `json:"next_page"`
		} `json:"pagination"`
	} `json:"meta"`
}

type image struct {
	ID          int64    `json:"id"`
	Type        string   `json:"type"`
	Status      string   `json:"status"`
	Description string   `json:"description"`
	ImageSize   *float64 `json:"image_size"`
	Created     string   `json:"created"`
	Deleted     *string  `json:"deleted"`
	CreatedFrom *struct {
		ID int64 `json:"id"`
	} `json:"created_from"`
}

type action struct {
	ID     int64     `json:"id"`
	Status string    `json:"status"`
	Error  *apiError `json:"error"`
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
			Error apiError `json:"error"`
		}
		_ = json.Unmarshal(resp.Body, &e)
		return vendorError(resp.Status, e.Error, token)
	}
	if out != nil {
		return resp.JSON(out)
	}
	return nil
}

// scrub is s without the token, for a vendor that echoes it back. It is not clipped: the Provider redacts every
// credential first and clips after.
func scrub(s, token string) string {
	if token != "" {
		s = strings.ReplaceAll(s, token, "[redacted]")
	}
	return core.OneLine(s, 0)
}

// vendorError is Hetzner's error code (the "Error Codes" table of the API reference) in the hub's vocabulary. Every
// documented code is classified here; the HTTP status decides only for an empty code or one Hetzner adds later. Only
// a rate limit is transient (Hetzner certainly did not take the request): after a conflict, a maintenance answer or
// a 5xx the request may still have been carried out.
func vendorError(status int, e apiError, token string) *core.Error {
	msg := scrub(e.Message, token)
	text := vendor + ": " + msg
	if msg == "" {
		text = vendor + " answered " + strings.ReplaceAll(e.Code, "_", " ")
	}
	code := strings.ToLower(strings.TrimSpace(e.Code))
	switch code {
	case "unauthorized", "forbidden":
		return core.NotSignedIn("%s", text)
	case "not_found":
		return core.NotFound("%s", text)
	case "locked": // 423: an action is already running on the server
		return core.InvalidState("%s", text)
	case "conflict": // 409: the server changed during the request
		return core.InvalidState("%s", text)
	case "protected": // 423: the server's protection forbids this action
		return core.Refused("%s", text)
	case "unsupported_error": // 422: the server does not support the action
		return core.Unsupported("%s", text)
	case "rate_limit_exceeded":
		return core.RateLimited("%s", text)
	case "resource_unavailable", "resource_limit_exceeded": // 412 not available for order, 403 account quota
		return core.Capacity("%s", text)
	case "maintenance", "unavailable", "timeout", "bad_gateway", "server_error": // 403, 503, 504, 502, 500
		return core.Failed("%s", text)
	case "invalid_input", "json_error", "service_error", "uniqueness_error", "deprecated_api_endpoint", "method_not_allowed", "action_failed":
		return core.Failed("%s", text)
	}
	if code == "" || status == http.StatusUnauthorized || status == http.StatusNotFound || status == http.StatusTooManyRequests || status >= 500 {
		return core.StatusError(vendor, status, msg)
	}
	// An unknown code on another status (a 403 among them) is not taken to mean the token is bad.
	return core.Failed("%s", text)
}

func (a *adapter) token(account wire.CloudAccountID) (string, bool) {
	t, ok := a.byName[string(account)]
	return t, ok
}

// serverID is id as Hetzner's numeric server id.
func serverID(id wire.CloudInstanceID) (int64, bool) {
	n, err := strconv.ParseInt(string(id), 10, 64)
	return n, err == nil && n > 0 && strconv.FormatInt(n, 10) == string(id)
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
	for page := 1; len(out) < core.MaxInstances; {
		var resp struct {
			Servers []server `json:"servers"`
			pagination
		}
		q := url.Values{"page": {strconv.Itoa(page)}, "per_page": {strconv.Itoa(perPage)}}
		if err := a.call(ctx, acct.Token, http.MethodGet, "/servers", q, nil, &resp); err != nil {
			return nil, err
		}
		for _, s := range resp.Servers {
			if s.ID > 0 && len(out) < core.MaxInstances {
				out = append(out, a.instance(acct.Account, s, loc))
			}
		}
		next := resp.Meta.Pagination.NextPage
		if next == nil || *next <= page || len(resp.Servers) == 0 {
			break
		}
		page = *next
	}
	return out, nil
}

func (a *adapter) Instance(ctx context.Context, ref wire.CloudInstanceRef) (wire.CloudInstance, error) {
	ctx, cancel := context.WithTimeout(ctx, readBudget)
	defer cancel()
	s, err := a.server(ctx, ref)
	if err != nil {
		return wire.CloudInstance{}, err
	}
	return a.instance(string(ref.Account), s, core.Locale(ctx)), nil
}

// server reads one server now.
func (a *adapter) server(ctx context.Context, ref wire.CloudInstanceRef) (server, error) {
	token, ok := a.token(ref.Account)
	id, okID := serverID(ref.ID)
	if !ok || !okID {
		return server{}, noServer(ctx)
	}
	var resp struct {
		Server *server `json:"server"`
	}
	if err := a.call(ctx, token, http.MethodGet, "/servers/"+strconv.FormatInt(id, 10), nil, nil, &resp); err != nil {
		return server{}, err
	}
	if resp.Server == nil || resp.Server.ID != id {
		return server{}, noServer(ctx)
	}
	return *resp.Server, nil
}

// noServer is the not_found of a server that is not there (or an id that cannot be one).
func noServer(ctx context.Context) *core.Error {
	return core.NotFound("%s", core.T(core.Locale(ctx), "Hetzner: no such server.", "Hetzner：没有这台服务器。"))
}

// state is the hub's state for Hetzner's status, with a detail in loc's language where the status says more.
func state(status, loc string) (wire.CloudInstanceState, string) {
	switch status {
	case "initializing", "starting":
		return wire.CloudInstanceStateStarting, ""
	case "running":
		return wire.CloudInstanceStateRunning, ""
	case "stopping":
		return wire.CloudInstanceStateStopping, ""
	case "off":
		return wire.CloudInstanceStateStopped, ""
	case "deleting":
		return wire.CloudInstanceStateTerminated, core.T(loc, "The server is being deleted.", "服务器正在删除。")
	case "migrating":
		return wire.CloudInstanceStateUnknown, core.T(loc, "The server is being moved to another host.", "服务器正在迁移到另一台主机。")
	case "rebuilding":
		return wire.CloudInstanceStateUnknown, core.T(loc, "The server is being reinstalled.", "服务器正在重装系统。")
	case "unknown":
		return wire.CloudInstanceStateUnknown, core.T(loc, "Hetzner does not know the server's state.", "Hetzner 不清楚这台服务器的状态。")
	}
	word := core.OneLine(status, 0) // Hetzner's own word, passed on as it is
	return wire.CloudInstanceStateUnknown, core.T(loc, "Hetzner reports the status "+word+".", "Hetzner 报告的状态为 "+word+"。")
}

// actions are what a server in st can be asked now; a locked one (an action of its own still running) takes no new
// action until that one is done, so it offers only what reads.
func actions(st wire.CloudInstanceState, locked bool) []wire.CloudActionKind {
	const (
		start  = wire.CloudActionKindStart
		stop   = wire.CloudActionKindStop
		reboot = wire.CloudActionKindReboot
		reset  = wire.CloudActionKindHardReset
		off    = wire.CloudActionKindForceStop
		sync   = wire.CloudActionKindSyncIp
		list   = wire.CloudActionKindSnapshotList
		create = wire.CloudActionKindSnapshotCreate
		cons   = wire.CloudActionKindConsole
	)
	if locked && st != wire.CloudInstanceStateTerminated {
		return []wire.CloudActionKind{sync, list, cons}
	}
	switch st {
	case wire.CloudInstanceStateRunning:
		return []wire.CloudActionKind{stop, reboot, reset, off, sync, list, create, cons}
	case wire.CloudInstanceStateStopped:
		return []wire.CloudActionKind{start, sync, list, create, cons}
	case wire.CloudInstanceStateStopping:
		return []wire.CloudActionKind{off, sync, list, cons}
	case wire.CloudInstanceStateTerminated:
		return []wire.CloudActionKind{sync}
	}
	return []wire.CloudActionKind{sync, list, cons}
}

// firstOf64 is the first address (::1) of a Hetzner IPv6 network such as "2001:db8::/64", "" when it is not one.
func firstOf64(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if !strings.Contains(s, "/") {
		return core.PlainIP(s)
	}
	p, err := netip.ParsePrefix(s)
	if err != nil || !p.Addr().Is6() {
		return ""
	}
	return core.PlainIP(p.Masked().Addr().Next().String())
}

func (a *adapter) instance(account string, s server, loc string) wire.CloudInstance {
	st, detail := state(s.Status, loc)
	if s.Locked && st != wire.CloudInstanceStateTerminated {
		detail = strings.TrimSpace(detail + " " + core.T(loc,
			"A task is still running on the server; other actions are possible once it is done.",
			"服务器上还有任务在进行，完成后才能执行其他操作。"))
	}
	in := wire.CloudInstance{
		Account:     wire.CloudAccountID(account),
		ID:          wire.CloudInstanceID(strconv.FormatInt(s.ID, 10)),
		DisplayName: s.Name,
		State:       st,
		StateDetail: detail,
		ConsoleURL:  ConsoleURL,
		Actions:     actions(st, s.Locked),
		ObservedAt:  a.now(),
	}
	// Servers carry their location (fsn1, nbg1, ash, ...) as the region; Hetzner has no zone below it since the
	// datacenter property was removed from server answers.
	if s.Location != nil {
		in.Region = s.Location.Name
	}
	var public []string
	if s.PublicNet.IPv4 != nil {
		public = append(public, s.PublicNet.IPv4.IP)
	}
	if s.PublicNet.IPv6 != nil {
		public = append(public, firstOf64(s.PublicNet.IPv6.IP))
	}
	in.PublicIps = core.PlainIPs(public...)
	var private []string
	for _, n := range s.PrivateNet {
		private = append(private, n.IP)
	}
	in.PrivateIps = core.PlainIPs(private...)
	spec := wire.CloudSpec{}
	if t := s.ServerType; t != nil {
		spec.Size = t.Name
		spec.Cpu = t.Cores
		spec.MemoryMb = int64(t.Memory*1024 + 0.5)
		spec.DiskGb = int64(t.Disk)
		switch t.Architecture {
		case "x86":
			spec.Arch = "x86_64"
		case "arm":
			spec.Arch = "arm64"
		default:
			spec.Arch = t.Architecture
		}
	}
	if s.Image != nil {
		spec.OS = s.Image.Description
	}
	if spec != (wire.CloudSpec{}) {
		in.Spec = &spec
	}
	return in
}

// --- actions ---

func (a *adapter) Action(ctx context.Context, act core.Action) (wire.CloudActionResult, error) {
	ctx, cancel := context.WithTimeout(ctx, writeBudget)
	defer cancel()
	var path string
	var body any
	switch act.Kind {
	case wire.CloudActionKindStart:
		path = "poweron"
	case wire.CloudActionKindStop:
		path = "shutdown"
	case wire.CloudActionKindReboot:
		path = "reboot"
	case wire.CloudActionKindHardReset:
		path = "reset"
	case wire.CloudActionKindForceStop:
		path = "poweroff"
	case wire.CloudActionKindSnapshotCreate:
		path = "create_image"
		b := map[string]string{"type": "snapshot"}
		if act.Name != "" {
			b["description"] = act.Name
		}
		body = b
	default:
		return wire.CloudActionResult{}, core.Unsupported("%s", core.T(act.Locale, "Hetzner servers do not offer this action.", "Hetzner 服务器不支持这个操作。"))
	}
	token, ok := a.token(act.Ref.Account)
	id, okID := serverID(act.Ref.ID)
	if !ok || !okID {
		return wire.CloudActionResult{}, noServer(ctx)
	}
	if body == nil {
		body = struct{}{}
	}
	var resp struct {
		Action *action `json:"action"`
		Image  *image  `json:"image"`
	}
	if err := a.call(ctx, token, http.MethodPost, "/servers/"+strconv.FormatInt(id, 10)+"/actions/"+path, nil, body, &resp); err != nil {
		return wire.CloudActionResult{}, err
	}
	if resp.Action != nil && resp.Action.Status == "error" {
		e := apiError{Code: "action_failed", Message: core.T(act.Locale, "the action failed", "操作失败")}
		if resp.Action.Error != nil {
			e = *resp.Action.Error
		}
		return wire.CloudActionResult{}, vendorError(http.StatusUnprocessableEntity, e, token)
	}
	res := wire.CloudActionResult{Status: wire.CloudActionStatusAccepted}
	if act.Kind == wire.CloudActionKindSnapshotCreate && resp.Image != nil && resp.Image.ID > 0 {
		snap := snapshot(*resp.Image)
		if snap.Name == "" {
			snap.Name = act.Name
		}
		res.Snapshot = &snap
	}
	return res, nil
}

// --- snapshots ---

func snapshot(im image) wire.CloudSnapshot {
	s := wire.CloudSnapshot{ID: wire.CloudSnapshotID(strconv.FormatInt(im.ID, 10)), Name: im.Description}
	switch {
	case im.Deleted != nil && *im.Deleted != "":
		s.State = wire.CloudSnapshotStateDeleting
	case im.Status == "available":
		s.State = wire.CloudSnapshotStateReady
	case im.Status == "unavailable":
		s.State = wire.CloudSnapshotStateFailed
	default:
		s.State = wire.CloudSnapshotStateCreating
	}
	if t, err := time.Parse(time.RFC3339, im.Created); err == nil {
		s.CreatedAt = t.UTC()
	}
	if im.ImageSize != nil && *im.ImageSize > 0 {
		s.SizeGb = *im.ImageSize
	}
	return s
}

func (a *adapter) Snapshots(ctx context.Context, ref wire.CloudInstanceRef) ([]wire.CloudSnapshot, error) {
	ctx, cancel := context.WithTimeout(ctx, readBudget)
	defer cancel()
	s, err := a.server(ctx, ref)
	if err != nil {
		return nil, err
	}
	token, _ := a.token(ref.Account)
	var out []wire.CloudSnapshot
	for page, n := 1, 0; n < maxPages && len(out) < core.MaxSnapshots; n++ {
		var resp struct {
			Images []image `json:"images"`
			pagination
		}
		q := url.Values{"type": {"snapshot"}, "sort": {"created:desc"}, "page": {strconv.Itoa(page)}, "per_page": {strconv.Itoa(perPage)}}
		if err := a.call(ctx, token, http.MethodGet, "/images", q, nil, &resp); err != nil {
			if ctx.Err() != nil && len(out) > 0 { // out of time: the newest ones found so far
				return out, nil
			}
			return nil, err
		}
		for _, im := range resp.Images {
			if im.ID > 0 && im.Type == "snapshot" && im.CreatedFrom != nil && im.CreatedFrom.ID == s.ID {
				out = append(out, snapshot(im))
			}
		}
		next := resp.Meta.Pagination.NextPage
		if next == nil || *next <= page || len(resp.Images) == 0 {
			break
		}
		page = *next
	}
	return out, nil
}
