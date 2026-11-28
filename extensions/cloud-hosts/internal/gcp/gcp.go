// Package gcp is the cloud-hosts adapter for Google Cloud Compute Engine. It signs in with service-account keys
// (an RS256 JWT assertion exchanged for an access token, all in the standard library), lists the VMs of every
// project the keys reach, powers them on and off, resets them, and lists and takes boot-disk snapshots.
//
// An account is a project id; an instance id is "<zone>/<name>".
package gcp

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona-extensions/extensions/cloud-hosts/internal/core"
	"github.com/nwverse/persona/sdk/go/wire"
)

// Production endpoints.
const (
	DefaultComputeBase = "https://compute.googleapis.com/compute/v1"
	DefaultTokenURL    = "https://oauth2.googleapis.com/token"
	computeScope       = "https://www.googleapis.com/auth/compute"
	vendor             = "Google Cloud"
)

// Bounds of the work behind one call, under the hub's own (20 s, 15 s, 30 s).
const (
	listBudget     = 18 * time.Second
	instanceBudget = 14 * time.Second
	actionBudget   = 28 * time.Second
	parallel       = 8
	maxPages       = 20
	listCap        = core.MaxInstances // the Provider says a list of this many may be incomplete
	snapshotPages  = 3
)

// Config sets the adapter up.
type Config struct {
	// ServiceAccounts is the secret: one service-account JSON key, a JSON array of keys, or keys one after another.
	// Each key's project_id is an account.
	ServiceAccounts string
	// Projects are more projects to read with the first key.
	Projects []string
	// HTTP is the client (core.NewHTTPClient when nil).
	HTTP *http.Client
	// Now is the clock (time.Now when nil).
	Now func() time.Time
	// ComputeBase is the Compute Engine API root (DefaultComputeBase when empty). Tests and overrides only.
	ComputeBase string
	// TokenURL is the OAuth2 token endpoint (DefaultTokenURL when empty). Tests and overrides only: a key file's own
	// token_uri is never used.
	TokenURL string
}

type account struct {
	project string
	key     *serviceKey
}

type adapter struct {
	http     *http.Client
	now      func() time.Time
	base     string
	tokenURL string
	keys     []*serviceKey
	accounts []*account
	byID     map[string]*account
	redact   *core.Redactor
	onJoin   func() // test hook: a caller joins a token refresh another caller runs
}

var projectRe = regexp.MustCompile(`^(?:[a-z0-9][a-z0-9.-]{0,62}:)?[a-z][a-z0-9-]{0,61}[a-z0-9]$`)

// New is the adapter for cfg; a not_signed_in error when the keys are missing or unusable.
func New(cfg Config) (core.Adapter, error) {
	keys, err := parseKeys(cfg.ServiceAccounts)
	if err != nil {
		return nil, err
	}
	a := &adapter{http: cfg.HTTP, now: cfg.Now, base: strings.TrimSpace(cfg.ComputeBase),
		tokenURL: strings.TrimSpace(cfg.TokenURL), keys: keys, byID: map[string]*account{}, redact: &core.Redactor{}}
	if a.http == nil {
		a.http = core.NewHTTPClient(core.HTTPOptions{})
	}
	if a.now == nil {
		a.now = time.Now
	}
	if a.base == "" {
		a.base = DefaultComputeBase
	}
	if a.tokenURL == "" {
		a.tokenURL = DefaultTokenURL
	}
	if u, err := url.Parse(a.tokenURL); err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil {
		return nil, core.Failed("the configured token endpoint is not a usable URL")
	}
	if _, err := core.JoinURL(a.base, "", nil); err != nil {
		return nil, err
	}
	a.redact.Add(a.SecretValues()...)
	add := func(project string, k *serviceKey) error {
		if !projectRe.MatchString(project) {
			return core.NotSignedIn("a project named in the Google Cloud settings is not a valid project id")
		}
		if a.byID[project] == nil {
			acc := &account{project: project, key: k}
			a.byID[project] = acc
			a.accounts = append(a.accounts, acc)
		}
		return nil
	}
	for _, k := range keys {
		if k.project != "" {
			if err := add(k.project, k); err != nil {
				return nil, err
			}
		}
	}
	for _, p := range core.CleanList(cfg.Projects) {
		if err := add(p, keys[0]); err != nil {
			return nil, err
		}
	}
	if len(a.accounts) == 0 {
		return nil, core.NotSignedIn("the service-account key names no project_id and no project is configured")
	}
	return a, nil
}

// SecretValues are the credentials the adapter holds: each key's private key PEM and its key id. The client email,
// project ids and endpoints are not credentials (Google's messages name them, and the console link carries the
// project); access tokens are removed by scrub while they are cached and by shape.
func (a *adapter) SecretValues() []string {
	var out []string
	for _, k := range a.keys {
		out = append(out, k.pemText, k.keyID)
	}
	return out
}

// Compute Engine's JSON.

// gInt is an int64 Compute Engine writes as a string (or a number).
type gInt int64

func (n *gInt) UnmarshalJSON(b []byte) error {
	var v json.Number
	if err := json.Unmarshal(bytes.Trim(b, `"`), &v); err != nil {
		*n = 0
		return nil
	}
	i, _ := v.Int64()
	*n = gInt(i)
	return nil
}

type gInstance struct {
	Name               string `json:"name"`
	Zone               string `json:"zone"`
	MachineType        string `json:"machineType"`
	Status             string `json:"status"`
	StatusMessage      string `json:"statusMessage"`
	SelfLink           string `json:"selfLink"`
	CPUPlatform        string `json:"cpuPlatform"`
	LastStartTimestamp string `json:"lastStartTimestamp"`
	NetworkInterfaces  []struct {
		NetworkIP     string `json:"networkIP"`
		IPv6Address   string `json:"ipv6Address"`
		AccessConfigs []struct {
			NatIP string `json:"natIP"`
		} `json:"accessConfigs"`
		IPv6AccessConfigs []struct {
			ExternalIPv6 string `json:"externalIpv6"`
		} `json:"ipv6AccessConfigs"`
	} `json:"networkInterfaces"`
	Disks []struct {
		Boot         bool     `json:"boot"`
		Source       string   `json:"source"`
		DiskSizeGb   gInt     `json:"diskSizeGb"`
		Architecture string   `json:"architecture"`
		Licenses     []string `json:"licenses"`
	} `json:"disks"`
	Scheduling struct {
		Preemptible       bool   `json:"preemptible"`
		ProvisioningModel string `json:"provisioningModel"`
	} `json:"scheduling"`
}

func (g *gInstance) spot() bool {
	return g.Scheduling.Preemptible || strings.EqualFold(g.Scheduling.ProvisioningModel, "SPOT")
}

// bootDisk is the boot disk's source link ("" when there is none).
func (g *gInstance) bootDisk() (source string, sizeGb int64, arch string, licenses []string) {
	for _, d := range g.Disks {
		if d.Boot {
			return d.Source, int64(d.DiskSizeGb), d.Architecture, d.Licenses
		}
	}
	return "", 0, "", nil
}

type gOpError struct {
	Errors []struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"errors"`
}

type gOperation struct {
	Name                string    `json:"name"`
	Status              string    `json:"status"`
	OperationType       string    `json:"operationType"`
	TargetLink          string    `json:"targetLink"`
	InsertTime          string    `json:"insertTime"`
	StartTime           string    `json:"startTime"`
	EndTime             string    `json:"endTime"`
	CreationTimestamp   string    `json:"creationTimestamp"`
	HTTPErrorStatusCode int       `json:"httpErrorStatusCode"`
	Error               *gOpError `json:"error"`
}

type gSnapshot struct {
	ID                string `json:"id"`
	Name              string `json:"name"`
	Status            string `json:"status"`
	SourceDisk        string `json:"sourceDisk"`
	CreationTimestamp string `json:"creationTimestamp"`
	DiskSizeGb        gInt   `json:"diskSizeGb"`
	StorageBytes      gInt   `json:"storageBytes"`
}

// call is one Compute Engine request of acc; out receives the JSON answer.
func (a *adapter) call(ctx context.Context, acc *account, method, path string, q url.Values, body any, out any) error {
	tok, err := a.token(ctx, acc.key)
	if err != nil {
		return err
	}
	u, err := core.JoinURL(a.base, path, q) // path is escaped: see seg
	if err != nil {
		return err
	}
	var rd *bytes.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return core.Failed("%s", core.T(core.Locale(ctx), "The request could not be written.", "无法生成请求。"))
		}
		rd = bytes.NewReader(raw)
	} else if method == http.MethodPost {
		rd = bytes.NewReader(nil)
	}
	var req *http.Request
	if rd != nil {
		req, err = http.NewRequestWithContext(ctx, method, u.String(), rd)
	} else {
		req, err = http.NewRequestWithContext(ctx, method, u.String(), nil)
	}
	if err != nil {
		return core.Failed("%s", core.T(core.Locale(ctx), "The request could not be written.", "无法生成请求。"))
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := core.Do(ctx, a.http, req)
	if err != nil {
		return err
	}
	if !resp.OK() {
		// The error is built while the token is still cached, so an echo of it is redacted, then the token goes.
		aerr := a.apiError(core.Locale(ctx), resp)
		if resp.Status == http.StatusUnauthorized {
			acc.key.drop(tok)
		}
		return aerr
	}
	return resp.JSON(out)
}

// seg is one path segment in its escaped form, for core.JoinURL. Every project, zone, name and disk put into a path
// goes through it exactly once (they are validated before, so it changes nothing of a real one).
func seg(s string) string { return url.PathEscape(s) }

func instancePath(project, zone, name string) string {
	return "projects/" + seg(project) + "/zones/" + seg(zone) + "/instances/" + seg(name)
}

var (
	nameRe = regexp.MustCompile(`^[a-z]([-a-z0-9]{0,61}[a-z0-9])?$`)
	zoneRe = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)
)

// resolve is ref's account, zone and name, or not_found without asking Google: an id that is not "<zone>/<name>" of
// Compute Engine's own shapes names no VM.
func (a *adapter) resolve(loc string, ref wire.CloudInstanceRef) (*account, string, string, error) {
	acc := a.byID[string(ref.Account)]
	if acc == nil {
		return nil, "", "", core.NotFound("%s", core.T(loc, "Google Cloud: this project is not set up.", "Google Cloud：没有设置这个项目。"))
	}
	zone, name, ok := strings.Cut(string(ref.ID), "/")
	if !ok || !zoneRe.MatchString(zone) || !nameRe.MatchString(name) {
		return nil, "", "", core.NotFound("%s", core.T(loc, "Google Cloud: no such VM.", "Google Cloud：没有这台虚拟机。"))
	}
	return acc, zone, name, nil
}

func lastSegment(s string) string {
	s = strings.TrimRight(s, "/")
	if i := strings.LastIndexByte(s, '/'); i >= 0 {
		return s[i+1:]
	}
	return s
}

// Instances lists the VMs of every project, the projects at most eight at a time.
func (a *adapter) Instances(ctx context.Context) ([]wire.CloudInstance, []string, error) {
	ctx, cancel := context.WithTimeout(ctx, listBudget)
	defer cancel()

	type result struct {
		list    []gInstance
		err     error
		partial bool
	}
	results := make([]result, len(a.accounts))
	var (
		mu    sync.Mutex
		total int
		wg    sync.WaitGroup
	)
	sem := make(chan struct{}, parallel)
	for i, acc := range a.accounts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				results[i].err = core.AsError(ctx.Err())
				return
			}
			defer func() { <-sem }()
			full := func() bool {
				mu.Lock()
				defer mu.Unlock()
				return total >= listCap
			}
			q := url.Values{"maxResults": {"500"}, "returnPartialSuccess": {"true"}}
			for page := 0; page < maxPages && !full(); page++ {
				var ans struct {
					Items map[string]struct {
						Instances []gInstance `json:"instances"`
					} `json:"items"`
					NextPageToken string   `json:"nextPageToken"`
					Unreachables  []string `json:"unreachables"`
				}
				if err := a.call(ctx, acc, http.MethodGet, "projects/"+seg(acc.project)+"/aggregated/instances", q, nil, &ans); err != nil {
					results[i].err = err
					return
				}
				scopes := make([]string, 0, len(ans.Items))
				for k := range ans.Items {
					scopes = append(scopes, k)
				}
				sort.Strings(scopes)
				n := 0
				for _, k := range scopes {
					results[i].list = append(results[i].list, ans.Items[k].Instances...)
					n += len(ans.Items[k].Instances)
				}
				mu.Lock()
				total += n
				mu.Unlock()
				if len(ans.Unreachables) > 0 {
					results[i].partial = true
				}
				if ans.NextPageToken == "" {
					break
				}
				q.Set("pageToken", ans.NextPageToken)
			}
		}()
	}
	wg.Wait()

	// The preemption look-ups, only for spot VMs that are off, also at most eight at a time.
	type job struct{ acc, idx int }
	var jobs []job
	pre := make([][]bool, len(results))
	for i, r := range results {
		pre[i] = make([]bool, len(r.list))
		for j := range r.list {
			if needsPreemptionCheck(&r.list[j]) {
				jobs = append(jobs, job{i, j})
			}
		}
	}
	for _, jb := range jobs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				g := &results[jb.acc].list[jb.idx]
				pre[jb.acc][jb.idx] = preemptedByMessage(g)
				return
			}
			defer func() { <-sem }()
			pre[jb.acc][jb.idx] = a.preempted(ctx, a.accounts[jb.acc], &results[jb.acc].list[jb.idx])
		}()
	}
	wg.Wait()

	var out []wire.CloudInstance
	var problems []string
	var firstErr error
	failed := 0
	for i, r := range results {
		if r.err != nil {
			failed++
			if firstErr == nil {
				firstErr = r.err
			}
			problems = append(problems, a.accounts[i].project)
			continue
		}
		if r.partial {
			problems = append(problems, a.accounts[i].project)
		}
		for j := range r.list {
			out = append(out, a.toInstance(core.Locale(ctx), a.accounts[i].project, &r.list[j], pre[i][j]))
		}
	}
	if failed == len(a.accounts) && firstErr != nil {
		return nil, nil, firstErr
	}
	return out, problems, nil
}

// Instance reads one VM now.
func (a *adapter) Instance(ctx context.Context, ref wire.CloudInstanceRef) (wire.CloudInstance, error) {
	ctx, cancel := context.WithTimeout(ctx, instanceBudget)
	defer cancel()
	acc, zone, name, err := a.resolve(core.Locale(ctx), ref)
	if err != nil {
		return wire.CloudInstance{}, err
	}
	g, err := a.get(ctx, acc, zone, name)
	if err != nil {
		return wire.CloudInstance{}, err
	}
	pre := false
	if needsPreemptionCheck(&g) {
		pre = a.preempted(ctx, acc, &g)
	}
	return a.toInstance(core.Locale(ctx), acc.project, &g, pre), nil
}

func (a *adapter) get(ctx context.Context, acc *account, zone, name string) (gInstance, error) {
	var g gInstance
	if err := a.call(ctx, acc, http.MethodGet, instancePath(acc.project, zone, name), nil, nil, &g); err != nil {
		return gInstance{}, err
	}
	if g.Name == "" {
		return gInstance{}, core.Failed("%s", core.T(core.Locale(ctx), "Google Cloud answered without the VM.", "Google Cloud 的回答里没有这台虚拟机。"))
	}
	return g, nil
}

// Action asks for start (resume for a suspended VM), stop, hard_reset or snapshot_create; anything else is
// unsupported.
func (a *adapter) Action(ctx context.Context, act core.Action) (wire.CloudActionResult, error) {
	ctx, cancel := context.WithTimeout(ctx, actionBudget)
	defer cancel()
	var verb string
	switch act.Kind {
	case wire.CloudActionKindStart:
		verb = "start"
	case wire.CloudActionKindStop:
		verb = "stop"
	case wire.CloudActionKindHardReset:
		verb = "reset"
	case wire.CloudActionKindSnapshotCreate:
	default:
		return wire.CloudActionResult{}, core.Unsupported("%s", core.T(act.Locale,
			"Google Cloud does not offer this action for its VMs.", "Google Cloud 的虚拟机不支持这个操作。"))
	}
	acc, zone, name, err := a.resolve(act.Locale, act.Ref)
	if err != nil {
		return wire.CloudActionResult{}, err
	}
	if act.Kind == wire.CloudActionKindSnapshotCreate {
		return a.createSnapshot(ctx, acc, zone, name, act)
	}
	if verb == "start" {
		// Compute Engine wakes a suspended VM only with resume; start is for one stopped with stop (or preempted).
		g, err := a.get(ctx, acc, zone, name)
		if err != nil {
			return wire.CloudActionResult{}, err
		}
		if g.Status == "SUSPENDED" {
			verb = "resume"
		}
	}
	var op gOperation
	q := url.Values{"requestId": {requestUUID(act)}}
	if err := a.call(ctx, acc, http.MethodPost, instancePath(acc.project, zone, name)+"/"+verb, q, nil, &op); err != nil {
		return wire.CloudActionResult{}, err
	}
	if err := a.opError(act.Locale, &op); err != nil {
		return wire.CloudActionResult{}, err
	}
	status := wire.CloudActionStatusAccepted
	if op.Status == "DONE" {
		status = wire.CloudActionStatusDone
	}
	return wire.CloudActionResult{Status: status}, nil
}
