// Package digitalocean is the cloud-hosts adapter for DigitalOcean (API v2): droplets as instances, their power
// actions, and their snapshots.
//
// An account is one API token (a DigitalOcean team): the secret holds one token, or "name=token" lines for several
// teams. An instance id is the droplet's numeric id as a decimal string.
package digitalocean

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona-extensions/extensions/cloud-hosts/internal/core"
	"github.com/nwverse/persona/sdk/go/wire"
)

// DefaultBaseURL is the DigitalOcean API.
const DefaultBaseURL = "https://api.digitalocean.com/v2"

// consoleBase is the control panel's droplet page; the droplet id follows.
const consoleBase = "https://cloud.digitalocean.com/droplets/"

const (
	vendor       = "DigitalOcean"
	perPage      = 200
	snapPerPage  = 100
	maxSnapPages = 5
	parallel     = 8
	listBudget   = 18 * time.Second
	readBudget   = 14 * time.Second
	writeBudget  = 28 * time.Second
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
	if _, err := core.JoinURL(base, "/droplets", nil); err != nil {
		return nil, err
	}
	accounts := core.ParseNamedTokens(c.Tokens)
	if len(accounts) == 0 {
		return nil, core.NotSignedIn("No DigitalOcean API token is set up.")
	}
	a := &adapter{base: base, client: c.HTTP, now: c.Now, accounts: accounts, byName: map[string]string{}}
	for _, t := range accounts {
		if strings.ContainsFunc(t.Token, func(r rune) bool { return r <= ' ' || r == 0x7f }) {
			return nil, core.NotSignedIn("The DigitalOcean API token of account %s is not usable.", t.Account)
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

type network struct {
	IPAddress string `json:"ip_address"`
	Type      string `json:"type"`
}

type droplet struct {
	ID       int64   `json:"id"`
	Name     string  `json:"name"`
	Status   string  `json:"status"`
	Locked   bool    `json:"locked"`
	Memory   int64   `json:"memory"`
	VCPUs    float64 `json:"vcpus"`
	Disk     int64   `json:"disk"`
	SizeSlug string  `json:"size_slug"`
	Networks struct {
		V4 []network `json:"v4"`
		V6 []network `json:"v6"`
	} `json:"networks"`
	Region *struct {
		Slug string `json:"slug"`
	} `json:"region"`
	Image *struct {
		Distribution string `json:"distribution"`
		Name         string `json:"name"`
	} `json:"image"`
}

type links struct {
	Links struct {
		Pages struct {
			Next string `json:"next"`
		} `json:"pages"`
	} `json:"links"`
}

type snapshot struct {
	ID        json.RawMessage `json:"id"` // a number for a droplet's snapshot; a string tolerated
	Name      string          `json:"name"`
	CreatedAt string          `json:"created_at"`
	SizeGB    float64         `json:"size_gigabytes"`
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
			ID      string `json:"id"`
			Message string `json:"message"`
		}
		_ = json.Unmarshal(resp.Body, &e)
		return vendorError(resp.Status, e.ID, e.Message, token)
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

// vendorError is DigitalOcean's error id in the hub's vocabulary; the status decides when the id says nothing more.
// Only a rate limit is transient: after a 5xx the request may still have been carried out.
func vendorError(status int, id, message, token string) *core.Error {
	msg := scrub(message, token)
	text := vendor + ": " + msg
	if msg == "" {
		text = vendor + " answered " + strings.ReplaceAll(id, "_", " ")
	}
	switch strings.ToLower(strings.TrimSpace(id)) {
	case "unauthorized", "forbidden":
		return core.NotSignedIn("%s", text)
	case "not_found":
		return core.NotFound("%s", text)
	case "conflict":
		return core.InvalidState("%s", text)
	case "unprocessable_entity":
		// 422 is also DigitalOcean's answer to a request it could not validate: only words about the droplet's
		// state make it invalid_state, words about a limit make it capacity.
		return unprocessable(msg, text)
	case "too_many_requests":
		return core.RateLimited("%s", text)
	case "":
		return core.StatusError(vendor, status, msg)
	}
	if status >= 500 || status == http.StatusUnauthorized || status == http.StatusForbidden || status == http.StatusNotFound || status == http.StatusTooManyRequests {
		return core.StatusError(vendor, status, msg)
	}
	return core.Failed("%s", text)
}

// stateWords and limitWords tell DigitalOcean's 422 answers apart ("Droplet is already powered on.", "Droplet must
// be powered off", "Droplet already has a pending event.", "You have reached the snapshot limit", ...).
var (
	stateWords = []string{"already", "powered", "pending event", "must be", "locked", "in progress", "currently", "status"}
	limitWords = []string{"limit", "quota", "capacity", "not available", "unavailable"}
)

func unprocessable(msg, text string) *core.Error {
	low := strings.ToLower(msg)
	for _, w := range limitWords {
		if strings.Contains(low, w) {
			return core.Capacity("%s", text)
		}
	}
	for _, w := range stateWords {
		if strings.Contains(low, w) {
			return core.InvalidState("%s", text)
		}
	}
	return core.Failed("%s", text)
}

func dropletID(id wire.CloudInstanceID) (int64, bool) {
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
	for page := 1; len(out) < core.MaxInstances; page++ {
		var resp struct {
			Droplets []droplet `json:"droplets"`
			links
		}
		q := url.Values{"page": {strconv.Itoa(page)}, "per_page": {strconv.Itoa(perPage)}}
		if err := a.call(ctx, acct.Token, http.MethodGet, "/droplets", q, nil, &resp); err != nil {
			return nil, err
		}
		for _, d := range resp.Droplets {
			if d.ID > 0 && len(out) < core.MaxInstances {
				out = append(out, a.instance(acct.Account, d, loc))
			}
		}
		if resp.Links.Pages.Next == "" || len(resp.Droplets) == 0 {
			break
		}
	}
	return out, nil
}

func (a *adapter) Instance(ctx context.Context, ref wire.CloudInstanceRef) (wire.CloudInstance, error) {
	ctx, cancel := context.WithTimeout(ctx, readBudget)
	defer cancel()
	d, err := a.droplet(ctx, ref)
	if err != nil {
		return wire.CloudInstance{}, err
	}
	return a.instance(string(ref.Account), d, core.Locale(ctx)), nil
}

func (a *adapter) droplet(ctx context.Context, ref wire.CloudInstanceRef) (droplet, error) {
	token, ok := a.byName[string(ref.Account)]
	id, okID := dropletID(ref.ID)
	if !ok || !okID {
		return droplet{}, noDroplet(ctx)
	}
	var resp struct {
		Droplet *droplet `json:"droplet"`
	}
	if err := a.call(ctx, token, http.MethodGet, "/droplets/"+strconv.FormatInt(id, 10), nil, nil, &resp); err != nil {
		return droplet{}, err
	}
	if resp.Droplet == nil || resp.Droplet.ID != id {
		return droplet{}, noDroplet(ctx)
	}
	return *resp.Droplet, nil
}

// noDroplet is the not_found of a droplet that is not there (or an id that cannot be one).
func noDroplet(ctx context.Context) *core.Error {
	return core.NotFound("%s", core.T(core.Locale(ctx), "DigitalOcean: no such droplet.", "DigitalOcean：没有这台云主机。"))
}

// state is the hub's state for DigitalOcean's status, with a detail in loc's language where the status says more.
func state(status, loc string) (wire.CloudInstanceState, string) {
	switch status {
	case "new":
		return wire.CloudInstanceStateStarting, ""
	case "active":
		return wire.CloudInstanceStateRunning, ""
	case "off":
		return wire.CloudInstanceStateStopped, ""
	case "archive":
		return wire.CloudInstanceStateTerminated, core.T(loc, "The droplet is archived.", "这台云主机已归档。")
	}
	word := core.OneLine(status, 0) // DigitalOcean's own word, passed on as it is
	return wire.CloudInstanceStateUnknown, core.T(loc, "DigitalOcean reports the status "+word+".", "DigitalOcean 报告的状态为 "+word+"。")
}

// actions are what a droplet in st can be asked now; a locked one (an action in flight) offers no power actions.
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
	if st == wire.CloudInstanceStateTerminated {
		return []wire.CloudActionKind{sync}
	}
	if locked {
		return []wire.CloudActionKind{sync, list, cons}
	}
	switch st {
	case wire.CloudInstanceStateRunning:
		return []wire.CloudActionKind{stop, reboot, reset, off, sync, list, create, cons}
	case wire.CloudInstanceStateStopped:
		return []wire.CloudActionKind{start, sync, list, create, cons}
	}
	return []wire.CloudActionKind{sync, list, cons}
}

func (a *adapter) instance(account string, d droplet, loc string) wire.CloudInstance {
	st, detail := state(d.Status, loc)
	if d.Locked && st != wire.CloudInstanceStateTerminated {
		detail = strings.TrimSpace(detail + " " + core.T(loc,
			"A task is still running on the droplet; other actions are possible once it is done.",
			"云主机上还有任务在进行，完成后才能执行其他操作。"))
	}
	id := strconv.FormatInt(d.ID, 10)
	in := wire.CloudInstance{
		Account:     wire.CloudAccountID(account),
		ID:          wire.CloudInstanceID(id),
		DisplayName: d.Name,
		State:       st,
		StateDetail: detail,
		ConsoleURL:  consoleBase + id,
		Actions:     actions(st, d.Locked),
		ObservedAt:  a.now(),
	}
	if d.Region != nil {
		in.Region = d.Region.Slug
	}
	var public, private []string
	for _, n := range d.Networks.V4 {
		if n.Type == "public" {
			public = append(public, n.IPAddress)
		} else {
			private = append(private, n.IPAddress)
		}
	}
	for _, n := range d.Networks.V6 {
		if n.Type == "public" {
			public = append(public, n.IPAddress)
		} else {
			private = append(private, n.IPAddress)
		}
	}
	in.PublicIps = core.PlainIPs(public...)
	in.PrivateIps = core.PlainIPs(private...)
	spec := wire.CloudSpec{Size: d.SizeSlug, Cpu: d.VCPUs, MemoryMb: d.Memory, DiskGb: d.Disk}
	if d.Image != nil {
		spec.OS = strings.TrimSpace(d.Image.Distribution + " " + d.Image.Name)
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
	body := map[string]string{}
	switch act.Kind {
	case wire.CloudActionKindStart:
		body["type"] = "power_on"
	case wire.CloudActionKindStop:
		body["type"] = "shutdown"
	case wire.CloudActionKindReboot:
		body["type"] = "reboot"
	case wire.CloudActionKindHardReset:
		body["type"] = "power_cycle"
	case wire.CloudActionKindForceStop:
		body["type"] = "power_off"
	case wire.CloudActionKindSnapshotCreate:
		body["type"] = "snapshot"
		if act.Name != "" {
			body["name"] = act.Name
		}
	default:
		return wire.CloudActionResult{}, core.Unsupported("%s", core.T(act.Locale, "DigitalOcean droplets do not offer this action.", "DigitalOcean 云主机不支持这个操作。"))
	}
	token, ok := a.byName[string(act.Ref.Account)]
	id, okID := dropletID(act.Ref.ID)
	if !ok || !okID {
		return wire.CloudActionResult{}, noDroplet(ctx)
	}
	var resp struct {
		Action *struct {
			ID     int64  `json:"id"`
			Status string `json:"status"`
		} `json:"action"`
	}
	if err := a.call(ctx, token, http.MethodPost, "/droplets/"+strconv.FormatInt(id, 10)+"/actions", nil, body, &resp); err != nil {
		return wire.CloudActionResult{}, err
	}
	if resp.Action != nil && resp.Action.Status == "errored" {
		return wire.CloudActionResult{}, core.Failed("%s", core.T(act.Locale, "DigitalOcean: the action failed.", "DigitalOcean：操作失败。"))
	}
	res := wire.CloudActionResult{Status: wire.CloudActionStatusAccepted}
	if act.Kind == wire.CloudActionKindSnapshotCreate {
		// The answer is the action only: the snapshot's id is known once DigitalOcean has made it.
		res.Message = core.T(act.Locale, "The snapshot is being taken.", "正在创建快照。")
	}
	return res, nil
}

// --- snapshots ---

func (a *adapter) Snapshots(ctx context.Context, ref wire.CloudInstanceRef) ([]wire.CloudSnapshot, error) {
	ctx, cancel := context.WithTimeout(ctx, readBudget)
	defer cancel()
	token, ok := a.byName[string(ref.Account)]
	id, okID := dropletID(ref.ID)
	if !ok || !okID {
		return nil, noDroplet(ctx)
	}
	var out []wire.CloudSnapshot
	for page := 1; page <= maxSnapPages && len(out) < core.MaxSnapshots; page++ {
		var resp struct {
			Snapshots []snapshot `json:"snapshots"`
			links
		}
		q := url.Values{"page": {strconv.Itoa(page)}, "per_page": {strconv.Itoa(snapPerPage)}}
		if err := a.call(ctx, token, http.MethodGet, "/droplets/"+strconv.FormatInt(id, 10)+"/snapshots", q, nil, &resp); err != nil {
			return nil, err
		}
		for _, s := range resp.Snapshots {
			sid := strings.Trim(strings.TrimSpace(string(s.ID)), `"`)
			if sid == "" || sid == "null" {
				continue
			}
			snap := wire.CloudSnapshot{ID: wire.CloudSnapshotID(sid), Name: s.Name, State: wire.CloudSnapshotStateReady, SizeGb: s.SizeGB}
			if t, err := time.Parse(time.RFC3339, s.CreatedAt); err == nil {
				snap.CreatedAt = t.UTC()
			}
			out = append(out, snap)
		}
		if resp.Links.Pages.Next == "" || len(resp.Snapshots) == 0 {
			break
		}
	}
	return out, nil
}
