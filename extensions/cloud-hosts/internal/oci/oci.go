// Package oci is the cloud-hosts adapter for Oracle Cloud Infrastructure Compute (Core Services API 20160918),
// Always Free shapes included. Requests are signed in pure Go (sign.go); the Monitoring API (20180401) backs the
// read-only "keepalive" custom action that tells whether an Always Free instance looks idle enough to be reclaimed.
package oci

import (
	"bytes"
	"context"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/netip"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona-extensions/extensions/cloud-hosts/internal/core"
	"github.com/nwverse/persona/sdk/go/wire"
)

// CustomKeepalive is the read custom action that probes an Always Free instance's activity (keepalive.go).
const CustomKeepalive wire.CloudCustomActionID = "keepalive"

// Config is the adapter's setup.
type Config struct {
	// ConfigFile is the text of an OCI config file: [DEFAULT] and other profiles with user, fingerprint, tenancy and
	// region. Other profiles inherit what they leave out from [DEFAULT], as the OCI CLI does. key_file is ignored; a
	// key_content (or key) entry may hold the profile's own PEM key on one line with \n escapes.
	ConfigFile string
	// PrivateKey is the PEM RSA private key (PKCS#8 or PKCS#1, unencrypted) of every profile without a key of its own.
	PrivateKey string
	// Compartments are the compartment OCIDs to list, for every profile; none lists each profile's tenancy (its root
	// compartment). With several profiles, a compartment one profile cannot see (OCI answers 404) is taken to belong
	// to another profile's tenancy; it is reported only when no profile could read it.
	Compartments []string
	// DataDir is where keep-alive probe results are kept (oci-keepalive.json, 0600); "" keeps them in memory only.
	DataDir string
	// HTTP is the client (nil: core.NewHTTPClient with its defaults).
	HTTP *http.Client
	// Now is the clock (nil: time.Now).
	Now func() time.Time
	// Endpoint is the base address of a service ("iaas" or "telemetry") in a region. Nil is production:
	// https://iaas.<region>.oraclecloud.com and https://telemetry.<region>.oraclecloud.com. Tests and overrides only.
	Endpoint func(service, region string) string
}

const (
	vendor        = "Oracle Cloud"
	coreAPI       = "/20160918"
	monitoringAPI = "/20180401"
	pageLimit     = "100"
	parallel      = 8 // requests in flight at once, per kind (lists, addresses)

	listBudget     = 18 * time.Second // under cloud.instances' 20 s
	instanceBudget = 14 * time.Second // under cloud.instance's 15 s
	actionBudget   = 28 * time.Second // under cloud.action's 30 s
)

// profile is one usable profile of the config file: an account.
type profile struct {
	account string
	tenancy string
	region  string
	signer  *signer
}

type adapter struct {
	profiles     []*profile
	broken       []string // accounts of profiles that cannot be used, reported as problems
	compartments []string
	client       *http.Client
	now          func() time.Time
	endpoint     func(service, region string) string
	secrets      []string
	probes       *probeStore
}

// New reads the config file and the key. It answers not_signed_in when no profile is usable.
func New(c Config) (core.Adapter, error) {
	ini := core.ParseINI(c.ConfigFile)
	if len(ini) == 0 {
		return nil, core.NotSignedIn("The OCI config file is missing or has no profile.")
	}
	a := &adapter{compartments: core.CleanList(c.Compartments), client: c.HTTP, now: c.Now, endpoint: c.Endpoint,
		probes: newProbeStore(c.DataDir)}
	if a.client == nil {
		a.client = core.NewHTTPClient(core.HTTPOptions{})
	}
	if a.now == nil {
		a.now = time.Now
	}
	if a.endpoint == nil {
		a.endpoint = func(service, region string) string { return "https://" + service + "." + region + ".oraclecloud.com" }
	}
	// The credential values only, not the config file whole: its other lines (region=us-ashburn-1) are not secret,
	// and registering them would blank them out of console links.
	a.secrets = append(a.secrets, c.PrivateKey)
	for _, sec := range ini {
		for _, k := range []string{"pass_phrase"} {
			a.secrets = append(a.secrets, sec[k])
		}
	}

	var shared *rsa.PrivateKey
	sharedErr := "no private key was given"
	if strings.TrimSpace(c.PrivateKey) != "" {
		k, err := parseKey(c.PrivateKey)
		if err != nil {
			sharedErr = "the private key " + err.Error()
		} else {
			shared, sharedErr = k, ""
		}
	}

	names := make([]string, 0, len(ini))
	for n := range ini {
		names = append(names, n)
	}
	sort.Strings(names)
	defaults := ini["default"]
	var firstProblem string
	for _, name := range names {
		sec := ini[name]
		get := func(k string) string {
			if v := unquote(sec[k]); v != "" {
				return v
			}
			if name != "default" {
				return unquote(defaults[k])
			}
			return ""
		}
		problem := ""
		var missing []string
		for _, k := range []string{"user", "fingerprint", "tenancy", "region"} {
			if get(k) == "" {
				missing = append(missing, k)
			}
		}
		keyText := get("key_content")
		if keyText == "" {
			keyText = get("key")
		}
		keyText = strings.ReplaceAll(keyText, `\n`, "\n")
		if keyText != "" {
			a.secrets = append(a.secrets, sec["key_content"], sec["key"], keyText)
		}
		key := shared
		switch {
		case !core.ValidAccountName(name):
			problem = "its name cannot name an account (1-40 letters, digits, dots, dashes, underscores)"
		case len(missing) > 0:
			problem = "it has no " + strings.Join(missing, ", ")
		case !validRegion(get("region")):
			problem = "its region is not an OCI region name"
		case keyText != "":
			k, err := parseKey(keyText)
			if err != nil {
				problem = "its key " + err.Error()
			}
			key = k
		case shared == nil:
			problem = sharedErr
		}
		user, fp, tenancy := get("user"), get("fingerprint"), get("tenancy")
		a.secrets = append(a.secrets, user, fp, tenancy)
		if problem != "" {
			if firstProblem == "" {
				firstProblem = "OCI profile " + name + ": " + problem + "."
			}
			// A [DEFAULT] without the fields of an account of its own is only the other profiles' defaults.
			if name != "default" || len(missing) == 0 {
				a.broken = append(a.broken, name)
			}
			continue
		}
		s := newSigner(tenancy, user, fp, key)
		a.secrets = append(a.secrets, s.keyID)
		a.profiles = append(a.profiles, &profile{account: name, tenancy: tenancy, region: get("region"), signer: s})
	}
	if len(a.profiles) == 0 {
		return nil, core.NotSignedIn("%s", firstProblem)
	}
	return a, nil
}

func unquote(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 && (s[0] == '"' && s[len(s)-1] == '"' || s[0] == '\'' && s[len(s)-1] == '\'') {
		s = s[1 : len(s)-1]
	}
	return strings.TrimSpace(s)
}

func validRegion(s string) bool {
	if s == "" || len(s) > 40 {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
			return false
		}
	}
	return true
}

// validOCID reports whether s looks like an OCID. An id that does not (a slash, a percent sign, a space) is never one
// OCI knows, so it is answered not_found without a request; ids that pass are still path-escaped where they go into a
// path, and they go into monitoring queries as they are.
func validOCID(s string) bool {
	if !strings.HasPrefix(s, "ocid1.") || len(s) > 255 {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

// SecretValues are every credential the adapter holds: the keys, and the OCIDs and fingerprints of the key ids.
func (a *adapter) SecretValues() []string { return core.CleanList(a.secrets) }

func (a *adapter) profile(account wire.CloudAccountID) *profile {
	for _, p := range a.profiles {
		if p.account == string(account) {
			return p
		}
	}
	return nil
}

// call sends one signed request and decodes a 2xx answer into out (when not nil). A non-2xx answer is the matching
// *core.Error. It returns the response headers for paging.
func (a *adapter) call(ctx context.Context, p *profile, service, method, path string, q url.Values, body []byte, hdr http.Header, out any) (http.Header, error) {
	u, err := core.JoinURL(a.endpoint(service, p.region), path, q)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), bytes.NewReader(body))
	if err != nil {
		return nil, core.Failed("%s", core.T(core.Locale(ctx), "The request to Oracle Cloud could not be made.", "无法发出发往 Oracle Cloud 的请求。"))
	}
	req.Header.Set("Accept", "application/json")
	for k, v := range hdr {
		req.Header[k] = v
	}
	if err := p.signer.sign(req, body, a.now()); err != nil {
		return nil, core.Failed("%s", core.T(core.Locale(ctx), "The request to Oracle Cloud could not be signed.", "无法为发往 Oracle Cloud 的请求签名。"))
	}
	resp, err := core.Do(ctx, a.client, req)
	if err != nil {
		return nil, err
	}
	if !resp.OK() {
		return resp.Header, apiError(core.Locale(ctx), resp.Status, resp.Body)
	}
	if out != nil {
		if err := resp.JSON(out); err != nil {
			return resp.Header, err
		}
	}
	return resp.Header, nil
}

// apiError maps OCI's {"code","message"} error body and status to the hub's codes. The vendor's words are not
// clipped here: the Provider redacts before it clips. Only TooManyRequests (429) is transient: it is the one answer
// that proves OCI did not act on the request.
func apiError(locale string, status int, body []byte) *core.Error {
	var e struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	_ = json.Unmarshal(body, &e)
	msg := core.OneLine(e.Message, 0)
	if msg == "" {
		msg = core.OneLine(e.Code, 0)
	}
	text := core.T(locale, vendor+" answered an error (HTTP "+strconv.Itoa(status)+").", vendor+" 返回了错误（HTTP "+strconv.Itoa(status)+"）。")
	if msg != "" {
		text = vendor + ": " + msg
	}
	lower := strings.ToLower(e.Message)
	switch {
	case e.Code == "NotAuthenticated" || e.Code == "SignUpRequired" || status == http.StatusUnauthorized:
		return core.NotSignedIn("%s", text)
	case e.Code == "NotAuthorizedOrNotFound" || status == http.StatusNotFound:
		return core.NotFound("%s", text)
	case e.Code == "TooManyRequests" || status == http.StatusTooManyRequests:
		return core.RateLimited("%s", text)
	case e.Code == "LimitExceeded" || e.Code == "QuotaExceeded" || e.Code == "OutOfCapacity" ||
		strings.Contains(lower, "out of host capacity") || strings.Contains(lower, "out of capacity"):
		return core.Capacity("%s", text)
	case e.Code == "IncorrectState" || e.Code == "InvalidatedRetryToken" || status == http.StatusConflict:
		return core.InvalidState("%s", text)
	}
	out := core.StatusError(vendor, status, msg)
	out.Message = text
	return out
}

// ociInstance is the part of OCI's Instance the adapter reads.
type ociInstance struct {
	ID                 string `json:"id"`
	DisplayName        string `json:"displayName"`
	LifecycleState     string `json:"lifecycleState"`
	CompartmentID      string `json:"compartmentId"`
	AvailabilityDomain string `json:"availabilityDomain"`
	Region             string `json:"region"`
	Shape              string `json:"shape"`
	ShapeConfig        *struct {
		Ocpus                     float64 `json:"ocpus"`
		Vcpus                     float64 `json:"vcpus"`
		MemoryInGBs               float64 `json:"memoryInGBs"`
		NetworkingBandwidthInGbps float64 `json:"networkingBandwidthInGbps"`
		ProcessorDescription      string  `json:"processorDescription"`
	} `json:"shapeConfig"`
	PreemptibleInstanceConfig json.RawMessage `json:"preemptibleInstanceConfig"`
}

func (in *ociInstance) preemptible() bool {
	raw := bytes.TrimSpace(in.PreemptibleInstanceConfig)
	return len(raw) > 0 && string(raw) != "null"
}

func (in *ociInstance) terminatedState() bool {
	return in.LifecycleState == "TERMINATING" || in.LifecycleState == "TERMINATED"
}

// alwaysFreeShape reports a shape Oracle's Always Free tier offers, and so one its idle reclamation applies to.
func alwaysFreeShape(shape string) bool {
	return shape == "VM.Standard.E2.1.Micro" || shape == "VM.Standard.A1.Flex"
}

// addrs are an instance's addresses, the primary VNIC's first.
type addrs struct {
	public, private []string
}

// toWire maps an OCI instance. ad are its addresses (nil when not read).
func (a *adapter) toWire(locale string, p *profile, in ociInstance, ad *addrs, now time.Time) wire.CloudInstance {
	out := wire.CloudInstance{
		Account:     wire.CloudAccountID(p.account),
		ID:          wire.CloudInstanceID(in.ID),
		DisplayName: in.DisplayName,
		Region:      p.region,
		Zone:        in.AvailabilityDomain,
		ConsoleURL:  "https://cloud.oracle.com/compute/instances/" + url.PathEscape(in.ID) + "?region=" + url.QueryEscape(p.region),
		ObservedAt:  now,
	}
	if out.DisplayName == "" {
		out.DisplayName = in.ID
	}
	if ad != nil {
		out.PublicIps, out.PrivateIps = ad.public, ad.private
	}
	spot := in.preemptible()
	switch in.LifecycleState {
	case "PROVISIONING", "STARTING":
		out.State = wire.CloudInstanceStateStarting
	case "RUNNING":
		out.State = wire.CloudInstanceStateRunning
		out.Actions = []wire.CloudActionKind{wire.CloudActionKindStop, wire.CloudActionKindReboot,
			wire.CloudActionKindHardReset, wire.CloudActionKindForceStop}
	case "STOPPING":
		out.State = wire.CloudInstanceStateStopping
	case "STOPPED":
		out.State = wire.CloudInstanceStateStopped
		out.Actions = []wire.CloudActionKind{wire.CloudActionKindStart}
	case "TERMINATING", "TERMINATED":
		out.State = wire.CloudInstanceStateTerminated
		if spot {
			// Deleted or being deleted is terminated, preemptible or not: preempted is reserved for an instance the
			// vendor stopped that can be started again, and OCI preemption always terminates. The API does not say
			// whether a person or a preemption ended it, so the detail only says it was most likely preemption.
			out.StateDetail = core.T(locale,
				"Oracle Cloud ended this preemptible instance, most likely to take its capacity back. It cannot be started again.",
				"Oracle Cloud 终止了这台抢占式实例，多半是为了收回容量。它无法再启动。")
		}
	case "MOVING":
		out.State = wire.CloudInstanceStateUnknown
		out.StateDetail = core.T(locale, "Oracle Cloud is moving this instance (MOVING).", "Oracle Cloud 正在迁移这台实例（MOVING）。")
	case "CREATING_IMAGE":
		out.State = wire.CloudInstanceStateUnknown
		out.StateDetail = core.T(locale, "Oracle Cloud is making an image of this instance (CREATING_IMAGE).", "Oracle Cloud 正在为这台实例制作镜像（CREATING_IMAGE）。")
	default:
		out.State = wire.CloudInstanceStateUnknown
		st := core.OneLine(in.LifecycleState, 0)
		out.StateDetail = core.T(locale, "Oracle Cloud reports the state "+st+".", "Oracle Cloud 报告的状态是 "+st+"。")
	}
	if spot {
		// Oracle: "Preemptible instances cannot be started, stopped, or rebooted." Only reading them is offered.
		out.Actions = nil
	}
	out.Actions = append(out.Actions, wire.CloudActionKindSyncIp, wire.CloudActionKindConsole)

	if in.Shape != "" || in.ShapeConfig != nil {
		s := wire.CloudSpec{Size: in.Shape, Arch: "amd64"}
		if strings.Contains(in.Shape, ".A1.") || strings.Contains(in.Shape, ".A2.") {
			s.Arch = "arm64"
		}
		if sc := in.ShapeConfig; sc != nil {
			s.Cpu = sc.Vcpus
			if s.Cpu == 0 {
				s.Cpu = sc.Ocpus
			}
			s.MemoryMb = int64(sc.MemoryInGBs * 1024)
			if strings.Contains(strings.ToLower(sc.ProcessorDescription), "ampere") {
				s.Arch = "arm64"
			}
		}
		out.Spec = &s
	}
	var lc wire.CloudLifecycle
	lc.Spot = spot
	if alwaysFreeShape(in.Shape) && !in.terminatedState() {
		lc.KeepaliveAt = a.keepaliveAt(in.ID, now)
		out.CustomActions = []wire.CloudCustomActionID{CustomKeepalive}
	}
	if lc != (wire.CloudLifecycle{}) {
		out.Lifecycle = &lc
	}
	return out
}

// getInstance is GET /instances/{id}.
func (a *adapter) getInstance(ctx context.Context, p *profile, id string) (ociInstance, error) {
	var in ociInstance
	if _, err := a.call(ctx, p, "iaas", http.MethodGet, coreAPI+"/instances/"+url.PathEscape(id), nil, nil, nil, &in); err != nil {
		return ociInstance{}, err
	}
	if in.ID != id {
		return ociInstance{}, noSuchInstance(ctx)
	}
	return in, nil
}

// addresses reads the instance's attached VNICs: the primary one first, IPv4 before IPv6.
func (a *adapter) addresses(ctx context.Context, p *profile, in ociInstance) (*addrs, error) {
	var atts []struct {
		VnicID         string `json:"vnicId"`
		LifecycleState string `json:"lifecycleState"`
		NicIndex       int    `json:"nicIndex"`
	}
	q := url.Values{"compartmentId": {in.CompartmentID}, "instanceId": {in.ID}, "limit": {pageLimit}}
	if _, err := a.call(ctx, p, "iaas", http.MethodGet, coreAPI+"/vnicAttachments", q, nil, nil, &atts); err != nil {
		return nil, addressError(ctx, err)
	}
	type vnic struct {
		IsPrimary     bool     `json:"isPrimary"`
		PublicIP      string   `json:"publicIp"`
		PrivateIP     string   `json:"privateIp"`
		IPv6Addresses []string `json:"ipv6Addresses"`
		nic           int
	}
	var vnics []vnic
	for _, at := range atts {
		if at.LifecycleState != "ATTACHED" || !validOCID(at.VnicID) {
			continue
		}
		var v vnic
		if _, err := a.call(ctx, p, "iaas", http.MethodGet, coreAPI+"/vnics/"+url.PathEscape(at.VnicID), nil, nil, nil, &v); err != nil {
			if core.AsError(err).Code == wire.CloudErrorCodeNotFound {
				// a VNIC detached since the attachment list, or one in a compartment this user cannot read: the
				// instance still exists, so its other addresses are what can be shown
				continue
			}
			return nil, addressError(ctx, err)
		}
		v.nic = at.NicIndex
		vnics = append(vnics, v)
	}
	sort.SliceStable(vnics, func(i, j int) bool {
		if vnics[i].IsPrimary != vnics[j].IsPrimary {
			return vnics[i].IsPrimary
		}
		return vnics[i].nic < vnics[j].nic
	})
	var public, private, v6pub, v6priv []string
	for _, v := range vnics {
		public = append(public, v.PublicIP)
		private = append(private, v.PrivateIP)
		for _, s := range v.IPv6Addresses {
			if ip, err := netip.ParseAddr(core.PlainIP(s)); err == nil && ip.IsPrivate() {
				v6priv = append(v6priv, s)
			} else {
				v6pub = append(v6pub, s)
			}
		}
	}
	return &addrs{public: core.PlainIPs(append(public, v6pub...)...), private: core.PlainIPs(append(private, v6priv...)...)}, nil
}

// addressError keeps a failure to read the addresses from looking like the instance itself is gone: OCI answers
// 404 NotAuthorizedOrNotFound for a VNIC or attachment the user may not read, and only GetInstance's 404 means the
// instance does not exist.
func addressError(ctx context.Context, err error) error {
	if e := core.AsError(err); e.Code == wire.CloudErrorCodeNotFound {
		return core.Failed("%s", core.T(core.Locale(ctx),
			"Oracle Cloud did not let this account read the instance's addresses (HTTP 404).",
			"Oracle Cloud 不允许这个账号读取这台实例的地址（HTTP 404）。"))
	}
	return err
}

// noSuchInstance is not_found in the caller's language.
func noSuchInstance(ctx context.Context) *core.Error {
	return core.NotFound("%s", core.T(core.Locale(ctx), "Oracle Cloud has no such instance.", "Oracle Cloud 上没有这台实例。"))
}

// list pages through one compartment's instances, at most core.MaxInstances.
func (a *adapter) list(ctx context.Context, p *profile, compartment string) ([]ociInstance, error) {
	var out []ociInstance
	page := ""
	for len(out) < core.MaxInstances {
		q := url.Values{"compartmentId": {compartment}, "limit": {pageLimit}}
		if page != "" {
			q.Set("page", page)
		}
		var batch []ociInstance
		h, err := a.call(ctx, p, "iaas", http.MethodGet, coreAPI+"/instances", q, nil, nil, &batch)
		if err != nil {
			return nil, err
		}
		out = append(out, batch...)
		page = h.Get("opc-next-page")
		if page == "" || len(batch) == 0 {
			break
		}
	}
	if len(out) > core.MaxInstances {
		out = out[:core.MaxInstances]
	}
	return out, nil
}

// Instances lists every compartment of every profile at once (at most 8 lists in flight), then reads the addresses
// of the instances that are not terminated (at most 8 at a time). An instance whose addresses could not be read is
// still listed, without them.
func (a *adapter) Instances(ctx context.Context) ([]wire.CloudInstance, []string, error) {
	ctx, cancel := context.WithTimeout(ctx, listBudget)
	defer cancel()

	type job struct {
		p          *profile
		comp       string
		configured bool
		list       []ociInstance
		err        error
	}
	var jobs []*job
	for _, p := range a.profiles {
		if len(a.compartments) == 0 {
			jobs = append(jobs, &job{p: p, comp: p.tenancy})
			continue
		}
		for _, c := range a.compartments {
			jobs = append(jobs, &job{p: p, comp: c, configured: true})
		}
	}
	sem := make(chan struct{}, parallel)
	var wg sync.WaitGroup
	for _, j := range jobs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				j.err = ctx.Err()
				return
			}
			defer func() { <-sem }()
			j.list, j.err = a.list(ctx, j.p, j.comp)
		}()
	}
	wg.Wait()

	problems := map[string]bool{}
	for _, b := range a.broken {
		problems[b] = true
	}
	read := map[string]bool{}
	missedBy := map[string][]string{}
	var firstErr error
	failed := 0
	for _, j := range jobs {
		if j.err == nil {
			read[j.comp] = true
			continue
		}
		failed++
		if firstErr == nil {
			firstErr = j.err
		}
		if e := core.AsError(j.err); j.configured && len(a.profiles) > 1 && e.Code == wire.CloudErrorCodeNotFound {
			missedBy[j.comp] = append(missedBy[j.comp], j.p.account)
			continue
		}
		problems[j.p.account] = true
	}
	for comp, accounts := range missedBy {
		if !read[comp] {
			for _, acc := range accounts {
				problems[acc] = true
			}
		}
	}
	if failed == len(jobs) && len(jobs) > 0 {
		return nil, nil, firstErr
	}

	type item struct {
		p  *profile
		in ociInstance
		ad *addrs
	}
	// Profiles of one tenancy (two users, or a compartment listed for several profiles) see the same instances; an
	// instance OCID is unique across OCI, so each one is listed once, under the first profile that saw it.
	var items []*item
	seen := map[string]bool{}
	for _, j := range jobs {
		for _, in := range j.list {
			if validOCID(in.ID) && !seen[in.ID] {
				seen[in.ID] = true
				items = append(items, &item{p: j.p, in: in})
			}
		}
	}
	if len(items) > core.MaxInstances {
		items = items[:core.MaxInstances]
	}
	asem := make(chan struct{}, parallel)
	for _, it := range items {
		if it.in.terminatedState() {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case asem <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-asem }()
			if ad, err := a.addresses(ctx, it.p, it.in); err == nil {
				it.ad = ad
			}
		}()
	}
	wg.Wait()

	now, loc := a.now(), core.Locale(ctx)
	out := make([]wire.CloudInstance, 0, len(items))
	for _, it := range items {
		w := a.toWire(loc, it.p, it.in, it.ad, now)
		if it.ad == nil && !it.in.terminatedState() && w.StateDetail == "" {
			w.StateDetail = core.T(loc, "Its addresses could not be read just now.", "暂时没能读到它的地址。")
		}
		out = append(out, w)
	}
	var plist []string
	for n := range problems {
		plist = append(plist, n)
	}
	sort.Strings(plist)
	return out, plist, nil
}

// Instance reads the instance and its addresses from OCI now.
func (a *adapter) Instance(ctx context.Context, ref wire.CloudInstanceRef) (wire.CloudInstance, error) {
	ctx, cancel := context.WithTimeout(ctx, instanceBudget)
	defer cancel()
	p := a.profile(ref.Account)
	if p == nil || !validOCID(string(ref.ID)) {
		return wire.CloudInstance{}, noSuchInstance(ctx)
	}
	in, err := a.getInstance(ctx, p, string(ref.ID))
	if err != nil {
		return wire.CloudInstance{}, err
	}
	var ad *addrs
	if !in.terminatedState() {
		if ad, err = a.addresses(ctx, p, in); err != nil {
			return wire.CloudInstance{}, err
		}
	}
	return a.toWire(core.Locale(ctx), p, in, ad, a.now()), nil
}

// powerActions are the standard kinds and OCI's InstanceAction for each.
var powerActions = map[wire.CloudActionKind]string{
	wire.CloudActionKindStart:     "START",
	wire.CloudActionKindStop:      "SOFTSTOP",
	wire.CloudActionKindReboot:    "SOFTRESET",
	wire.CloudActionKindHardReset: "RESET",
	wire.CloudActionKindForceStop: "STOP",
}

// retryToken is the opc-retry-token of a request: the same for every retry of one request_id (OCI accepts up to 64
// characters).
func retryToken(act core.Action) string {
	sum := sha256.Sum256([]byte(string(act.Ref.Account) + "\x00" + string(act.Ref.ID) + "\x00" + string(act.Kind) + "\x00" + string(act.RequestID)))
	return "persona-" + hex.EncodeToString(sum[:24])
}

// Action is the power actions (POST /instances/{id}?action=…) and the keepalive probe. It never waits for the
// instance to get there: OCI took the request, so it answers accepted.
func (a *adapter) Action(ctx context.Context, act core.Action) (wire.CloudActionResult, error) {
	ctx, cancel := context.WithTimeout(ctx, actionBudget)
	defer cancel()
	p := a.profile(act.Ref.Account)
	if act.Locale == "" {
		act.Locale = core.Locale(ctx)
	}
	ctx = core.WithLocale(ctx, act.Locale)
	if p == nil || !validOCID(string(act.Ref.ID)) {
		return wire.CloudActionResult{}, noSuchInstance(ctx)
	}
	id := string(act.Ref.ID)
	if act.Kind == wire.CloudActionKindCustom {
		if act.Custom != CustomKeepalive {
			return wire.CloudActionResult{}, core.Unsupported("%s", core.T(act.Locale, "This action is not supported.", "不支持这个操作。"))
		}
		return a.keepalive(ctx, p, id, act.Locale)
	}
	op, ok := powerActions[act.Kind]
	if !ok {
		return wire.CloudActionResult{}, core.Unsupported("%s", core.T(act.Locale, "Oracle Cloud instances do not support this action here.", "这里不支持对 Oracle Cloud 实例执行这个操作。"))
	}
	hdr := http.Header{"Opc-Retry-Token": {retryToken(act)}}
	var in ociInstance
	if _, err := a.call(ctx, p, "iaas", http.MethodPost, coreAPI+"/instances/"+url.PathEscape(id), url.Values{"action": {op}}, []byte{}, hdr, &in); err != nil {
		return wire.CloudActionResult{}, err
	}
	res := wire.CloudActionResult{Status: wire.CloudActionStatusAccepted}
	if in.ID == id {
		if ad, err := a.addresses(ctx, p, in); err == nil {
			w := a.toWire(act.Locale, p, in, ad, a.now())
			res.Instance = &w
		}
	}
	return res, nil
}

// Snapshots: OCI's boot volume backups are not offered here.
func (a *adapter) Snapshots(ctx context.Context, _ wire.CloudInstanceRef) ([]wire.CloudSnapshot, error) {
	return nil, core.Unsupported("%s", core.T(core.Locale(ctx), "Snapshots of Oracle Cloud instances are not supported.", "不支持 Oracle Cloud 实例的快照。"))
}
