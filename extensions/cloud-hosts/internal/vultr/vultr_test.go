package vultr

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona-extensions/extensions/cloud-hosts/internal/core"
	"github.com/nwverse/persona-extensions/extensions/cloud-hosts/internal/coretest"
	"github.com/nwverse/persona/sdk/go/wire"
)

const (
	keyAlpha = "TESTKEYALPHAEXAMPLE0000000000001"
	keyBeta  = "TESTKEYBETAEXAMPLE00000000000002"
	keyBad   = "TESTKEYREVOKEDEXAMPLE00000000003"
	idA      = "cb676a46-66fd-4dfb-b839-443f2e6c0b60"
	idB      = "0a1b2c3d-0000-4000-8000-00000000000b"
)

var secrets = []string{keyAlpha, keyBeta, keyBad}

var fixedNow = time.Date(2026, 11, 27, 9, 30, 0, 0, time.UTC)

type canned struct {
	status int
	body   string
}

// fake is the Vultr API: accounts by key and their instances.
type fake struct {
	t         *testing.T
	mu        sync.Mutex
	instances map[string]map[string]map[string]any
	canned    map[string]canned
	calls     []string
	bodies    map[string]map[string]any
	pageSize  int // instances per list page; 3 when 0, so that a cursor is followed
}

func newFake(t *testing.T) (*fake, *httptest.Server) {
	f := &fake{t: t, instances: map[string]map[string]map[string]any{keyAlpha: {}, keyBeta: {}},
		canned: map[string]canned{}, bodies: map[string]map[string]any{}}
	srv := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(srv.Close)
	return f, srv
}

func vm(id, label, status, power, server, ip string) map[string]any {
	return map[string]any{
		"id": id, "os": "Ubuntu 24.04 LTS x64", "ram": 1024, "disk": 25, "main_ip": ip, "vcpu_count": 1,
		"region": "ewr", "plan": "vc2-1c-1gb", "date_created": "2026-10-01T08:00:00+00:00", "status": status,
		"allowed_bandwidth": 1000, "netmask_v4": "255.255.254.0", "gateway_v4": "203.0.113.1",
		"power_status": power, "server_status": server, "v6_network": "2001:db8:20::", "v6_main_ip": "2001:db8:20::5",
		"v6_network_size": 64, "label": label, "internal_ip": "10.1.96.3",
		"kvm":      "https://my.vultr.com/subs/vps/novnc/api.php?data=SYNTHETICCONSOLETOKEN",
		"hostname": label + ".example.com", "tag": "", "tags": []any{}, "os_id": 2284, "app_id": 0, "image_id": "",
		"firewall_group_id": "", "features": []any{"ipv6"},
	}
}

func (f *fake) add(key string, v map[string]any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.instances[key][v["id"].(string)] = v
}

func (f *fake) callList() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func (f *fake) count(call string) int {
	n := 0
	for _, c := range f.callList() {
		if c == call {
			n++
		}
	}
	return n
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func verr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]any{"error": msg, "status": status})
}

func (f *fake) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := r.Method + " " + r.URL.Path
	f.calls = append(f.calls, key)
	if c, ok := f.canned[key]; ok {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(c.status)
		_, _ = io.WriteString(w, c.body)
		return
	}
	auth := r.Header.Get("Authorization")
	insts, ok := f.instances[strings.TrimPrefix(auth, "Bearer ")]
	if !ok || !strings.HasPrefix(auth, "Bearer ") {
		verr(w, 401, "Invalid API token.")
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/v2")
	parts := strings.Split(strings.Trim(path, "/"), "/")
	switch {
	case r.Method == http.MethodGet && path == "/instances":
		if r.URL.Query().Get("per_page") != "500" {
			f.t.Errorf("per_page %q", r.URL.Query().Get("per_page"))
		}
		ids := make([]string, 0, len(insts))
		for id := range insts {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		// The fake pages by 3 so that a cursor is followed; the cursor is the index to start at.
		from, _ := strconv.Atoi(r.URL.Query().Get("cursor"))
		per := f.pageSize
		if per == 0 {
			per = 3
		}
		to := min(from+per, len(ids))
		list := []any{}
		for _, id := range ids[from:to] {
			list = append(list, insts[id])
		}
		next := ""
		if to < len(ids) {
			next = strconv.Itoa(to)
		}
		writeJSON(w, 200, map[string]any{"instances": list, "meta": map[string]any{"total": len(ids), "links": map[string]any{"next": next, "prev": ""}}})
	case r.Method == http.MethodGet && len(parts) == 2 && parts[0] == "instances":
		v, ok := insts[parts[1]]
		if !ok {
			verr(w, 404, "Not found")
			return
		}
		writeJSON(w, 200, map[string]any{"instance": v})
	case r.Method == http.MethodPost && len(parts) == 3 && parts[0] == "instances":
		v, ok := insts[parts[1]]
		if !ok {
			verr(w, 404, "Not found")
			return
		}
		switch parts[2] {
		case "start":
			v["power_status"] = "running"
			v["main_ip"] = "203.0.113.99"
		case "halt":
			v["power_status"] = "stopped"
		case "reboot":
		default:
			verr(w, 404, "Not found")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	case r.Method == http.MethodPost && path == "/snapshots":
		if r.Header.Get("Content-Type") != "application/json" {
			f.t.Errorf("Content-Type %q", r.Header.Get("Content-Type"))
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.bodies[key] = body
		if _, ok := insts[fmt.Sprint(body["instance_id"])]; !ok {
			verr(w, 400, "Invalid instance-id.")
			return
		}
		writeJSON(w, 201, map[string]any{"snapshot": map[string]any{"id": "5359435d-28b6-4ef4-99b8-36c9b1d7d6c2",
			"date_created": "2026-11-27T09:30:00+00:00", "description": body["description"], "size": 0, "compressed_size": 0,
			"status": "pending", "os_id": 2284, "app_id": 0}})
	default:
		verr(w, 404, "Not found")
	}
}

func newAdapter(t *testing.T, srv *httptest.Server, keys string) core.Adapter {
	t.Helper()
	a, err := New(Config{Tokens: keys, BaseURL: srv.URL + "/v2", HTTP: srv.Client(), Now: func() time.Time { return fixedNow }})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func asJSON(t *testing.T, v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

var refA = wire.CloudInstanceRef{Account: "default", ID: idA}

func TestNewNeedsAKey(t *testing.T) {
	if _, err := New(Config{Tokens: "\n\n"}); coretest.Code(err) != wire.CloudErrorCodeNotSignedIn {
		t.Fatalf("%v", err)
	}
	_, err := New(Config{Tokens: "main=bad key-value"})
	if coretest.Code(err) != wire.CloudErrorCodeNotSignedIn || !strings.Contains(err.Error(), "main") {
		t.Fatalf("%v", err)
	}
	coretest.NoLeak(t, err.Error(), "key-value")
	a, err := New(Config{Tokens: "a=" + keyAlpha + "\nb=" + keyBeta})
	if err != nil {
		t.Fatal(err)
	}
	if got := a.(core.SecretHolder).SecretValues(); len(got) != 2 {
		t.Fatalf("%v", got)
	}
}

func TestInstancesAcrossAccounts(t *testing.T) {
	f, srv := newFake(t)
	for i := 0; i < 7; i++ {
		f.add(keyAlpha, vm(fmt.Sprintf("00000000-0000-4000-8000-%012d", i), fmt.Sprintf("alpha-%d", i), "active", "running", "ok", "203.0.113.10"))
	}
	f.add(keyBeta, vm(idB, "beta-web", "active", "stopped", "ok", "198.51.100.7"))
	a := newAdapter(t, srv, "alpha="+keyAlpha+"\nbeta="+keyBeta+"\ngamma="+keyBad)
	list, problems, err := a.Instances(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 8 || len(problems) != 1 || problems[0] != "gamma" {
		t.Fatalf("%d %v", len(list), problems)
	}
	if f.count("GET /v2/instances") != 5 { // alpha by cursor in three pages, beta, gamma
		t.Fatalf("%v", f.callList())
	}
	p := coretest.Provider(a, nil, secrets...)
	out, err := p.Instances(context.Background(), wire.CloudInstancesParams{Refresh: true})
	if err != nil {
		t.Fatal(err)
	}
	raw := coretest.Valid(t, "cloud.instances", out)
	coretest.NoLeak(t, string(raw), secrets...)
	coretest.NoLeak(t, string(raw), "SYNTHETICCONSOLETOKEN")
	if !out.Partial || !strings.Contains(out.Detail, "gamma") {
		t.Fatalf("%v %q", out.Partial, out.Detail)
	}
}

func TestInstancesAllAccountsFail(t *testing.T) {
	_, srv := newFake(t)
	p := coretest.Provider(newAdapter(t, srv, keyBad), nil, secrets...)
	_, err := p.Instances(context.Background(), wire.CloudInstancesParams{Refresh: true})
	if coretest.Code(err) != wire.CloudErrorCodeNotSignedIn {
		t.Fatalf("%v", err)
	}
}

func TestInstanceFields(t *testing.T) {
	f, srv := newFake(t)
	f.add(keyAlpha, vm(idA, "web-1", "active", "running", "ok", "203.0.113.10"))
	p := coretest.Provider(newAdapter(t, srv, keyAlpha), nil, secrets...)
	in, err := p.Instance(context.Background(), wire.CloudInstanceParams{Instance: refA})
	if err != nil {
		t.Fatal(err)
	}
	raw := coretest.Valid(t, "cloud.instance", in)
	coretest.NoLeak(t, string(raw), "SYNTHETICCONSOLETOKEN", "novnc")
	want := wire.CloudInstance{
		Account: "default", ID: idA, DisplayName: "web-1", State: wire.CloudInstanceStateRunning, Region: "ewr",
		PublicIps:  []string{"203.0.113.10", "2001:db8:20::5"},
		PrivateIps: []string{"10.1.96.3"},
		Spec:       &wire.CloudSpec{Size: "vc2-1c-1gb", Cpu: 1, MemoryMb: 1024, DiskGb: 25, OS: "Ubuntu 24.04 LTS x64"},
		ConsoleURL: "https://my.vultr.com/subs/?id=" + idA,
		Actions: []wire.CloudActionKind{wire.CloudActionKindHardReset, wire.CloudActionKindForceStop, wire.CloudActionKindSyncIp,
			wire.CloudActionKindSnapshotCreate, wire.CloudActionKindConsole},
		ObservedAt: fixedNow,
	}
	if asJSON(t, in) != asJSON(t, want) {
		t.Fatalf("got  %s\nwant %s", asJSON(t, in), asJSON(t, want))
	}
	res, err := p.Action(context.Background(), wire.CloudActionParams{Instance: refA, Action: wire.CloudActionKindConsole, RequestID: "c"})
	if err != nil {
		t.Fatal(err)
	}
	coretest.Valid(t, "cloud.action", res)
	if !strings.HasPrefix(res.URL, "https://my.vultr.com/") || strings.Contains(res.URL, "@") || strings.Contains(res.URL, keyAlpha) {
		t.Fatalf("%q", res.URL)
	}

	// No address: main_ip 0.0.0.0 and an empty IPv6 are left out.
	v := vm(idB, "", "active", "running", "ok", "0.0.0.0")
	v["v6_main_ip"], v["internal_ip"] = "", ""
	f.add(keyAlpha, v)
	in, err = p.Instance(context.Background(), wire.CloudInstanceParams{Instance: wire.CloudInstanceRef{Account: "default", ID: idB}})
	if err != nil || len(in.PublicIps) != 0 || len(in.PrivateIps) != 0 || in.DisplayName != ".example.com" {
		t.Fatalf("%+v %v", in, err)
	}
}

func TestStateMapping(t *testing.T) {
	cases := []struct {
		status, power, server string
		state                 wire.CloudInstanceState
		detail                bool
		offers, lacks         []wire.CloudActionKind
	}{
		{"pending", "running", "none", wire.CloudInstanceStateStarting, true, nil, []wire.CloudActionKind{wire.CloudActionKindStart, wire.CloudActionKindForceStop}},
		{"active", "running", "installingbooting", wire.CloudInstanceStateStarting, true, nil, []wire.CloudActionKind{wire.CloudActionKindForceStop}},
		{"active", "running", "ok", wire.CloudInstanceStateRunning, false, []wire.CloudActionKind{wire.CloudActionKindHardReset, wire.CloudActionKindForceStop, wire.CloudActionKindSnapshotCreate}, []wire.CloudActionKind{wire.CloudActionKindStart, wire.CloudActionKindStop, wire.CloudActionKindReboot, wire.CloudActionKindSnapshotList}},
		{"active", "stopped", "ok", wire.CloudInstanceStateStopped, false, []wire.CloudActionKind{wire.CloudActionKindStart}, []wire.CloudActionKind{wire.CloudActionKindForceStop, wire.CloudActionKindStop}},
		{"suspended", "stopped", "ok", wire.CloudInstanceStateStopped, true, nil, []wire.CloudActionKind{wire.CloudActionKindStart, wire.CloudActionKindSnapshotCreate}},
		{"resizing", "running", "ok", wire.CloudInstanceStateUnknown, true, nil, []wire.CloudActionKind{wire.CloudActionKindForceStop}},
		{"active", "running", "locked", wire.CloudInstanceStateRunning, true, nil, []wire.CloudActionKind{wire.CloudActionKindForceStop, wire.CloudActionKindHardReset}},
		{"active", "", "ok", wire.CloudInstanceStateUnknown, true, nil, []wire.CloudActionKind{wire.CloudActionKindStart}},
	}
	f, srv := newFake(t)
	for i, c := range cases {
		f.add(keyAlpha, vm(fmt.Sprintf("00000000-0000-4000-8000-%012d", i), "x", c.status, c.power, c.server, "203.0.113.20"))
	}
	p := coretest.Provider(newAdapter(t, srv, keyAlpha), nil, secrets...)
	for i, c := range cases {
		in, err := p.Instance(context.Background(), wire.CloudInstanceParams{Instance: wire.CloudInstanceRef{Account: "default", ID: wire.CloudInstanceID(fmt.Sprintf("00000000-0000-4000-8000-%012d", i))}})
		if err != nil {
			t.Fatal(err)
		}
		coretest.Valid(t, "cloud.instance", in)
		name := c.status + "/" + c.power + "/" + c.server
		if in.State != c.state || (in.StateDetail != "") != c.detail {
			t.Errorf("%s: %s %q", name, in.State, in.StateDetail)
		}
		if !core.Offers(in.Actions, wire.CloudActionKindSyncIp) || !core.Offers(in.Actions, wire.CloudActionKindConsole) {
			t.Errorf("%s: %v", name, in.Actions)
		}
		for _, k := range c.offers {
			if !core.Offers(in.Actions, k) {
				t.Errorf("%s: lacks %s", name, k)
			}
		}
		for _, k := range c.lacks {
			if core.Offers(in.Actions, k) {
				t.Errorf("%s: offers %s", name, k)
			}
		}
	}
}

func TestActions(t *testing.T) {
	cases := []struct {
		kind wire.CloudActionKind
		verb string
	}{
		{wire.CloudActionKindStart, "start"},
		{wire.CloudActionKindForceStop, "halt"},
		{wire.CloudActionKindHardReset, "reboot"},
	}
	f, srv := newFake(t)
	f.add(keyAlpha, vm(idA, "web-1", "active", "running", "ok", "203.0.113.10"))
	p := coretest.Provider(newAdapter(t, srv, keyAlpha), nil, secrets...)
	for _, c := range cases {
		params := wire.CloudActionParams{Instance: refA, Action: c.kind, RequestID: wire.CloudRequestID("req-" + c.verb)}
		for range 2 {
			res, err := p.Action(context.Background(), params)
			if err != nil {
				t.Fatalf("%s: %v", c.kind, err)
			}
			coretest.Valid(t, "cloud.action", res)
			if res.Status != wire.CloudActionStatusAccepted {
				t.Fatalf("%s: %s", c.kind, res.Status)
			}
		}
		if n := f.count("POST /v2/instances/" + idA + "/" + c.verb); n != 1 {
			t.Fatalf("%s: %d: %v", c.kind, n, f.callList())
		}
	}
	res, err := p.Action(context.Background(), wire.CloudActionParams{Instance: refA, Action: wire.CloudActionKindSnapshotCreate, Name: "pre-upgrade", RequestID: "snap"})
	if err != nil {
		t.Fatal(err)
	}
	coretest.Valid(t, "cloud.action", res)
	if res.Status != wire.CloudActionStatusAccepted || res.Snapshot == nil || res.Snapshot.ID != "5359435d-28b6-4ef4-99b8-36c9b1d7d6c2" ||
		res.Snapshot.State != wire.CloudSnapshotStateCreating || res.Snapshot.Name != "pre-upgrade" {
		t.Fatalf("%+v", res)
	}
	f.mu.Lock()
	body := f.bodies["POST /v2/snapshots"]
	f.mu.Unlock()
	if body["instance_id"] != idA || body["description"] != "pre-upgrade" {
		t.Fatalf("%v", body)
	}
}

func TestUnsupported(t *testing.T) {
	f, srv := newFake(t)
	a := newAdapter(t, srv, keyAlpha)
	for _, k := range []wire.CloudActionKind{wire.CloudActionKindStop, wire.CloudActionKindReboot, wire.CloudActionKindCustom, wire.CloudActionKindConsole, wire.CloudActionKindSyncIp} {
		if _, err := a.Action(context.Background(), core.Action{Ref: refA, Kind: k}); coretest.Code(err) != wire.CloudErrorCodeUnsupported {
			t.Fatalf("%s: %v", k, err)
		}
	}
	p := coretest.Provider(a, nil, secrets...)
	if _, err := p.Snapshots(context.Background(), wire.CloudSnapshotsParams{Instance: refA}); coretest.Code(err) != wire.CloudErrorCodeUnsupported {
		t.Fatalf("%v", err)
	}
	if _, err := p.Action(context.Background(), wire.CloudActionParams{Instance: refA, Action: wire.CloudActionKindStop, RequestID: "x"}); coretest.Code(err) != wire.CloudErrorCodeUnsupported {
		t.Fatalf("%v", err)
	}
	if len(f.callList()) != 0 {
		t.Fatalf("%v", f.callList())
	}
}

func TestVendorErrors(t *testing.T) {
	cases := []struct {
		status    int
		msg       string
		want      wire.CloudErrorCode
		transient bool
	}{
		{401, "Invalid API token.", wire.CloudErrorCodeNotSignedIn, false},
		{403, "Unauthorized IP address", wire.CloudErrorCodeNotSignedIn, false},
		{404, "Not found", wire.CloudErrorCodeNotFound, false},
		{400, "Invalid instance-id.", wire.CloudErrorCodeNotFound, false},
		{400, "Unable to start server: server is already running", wire.CloudErrorCodeInvalidState, false},
		{412, "Server is currently locked", wire.CloudErrorCodeInvalidState, false},
		{429, "Rate limit reached - please try your request again later.", wire.CloudErrorCodeRateLimited, true},
		{502, "Bad gateway", wire.CloudErrorCodeFailed, false},
		{503, "This plan is not available in the selected location.", wire.CloudErrorCodeCapacity, false},
		{503, "Insufficient capacity in this region", wire.CloudErrorCodeCapacity, false},
		{503, "Service temporarily down", wire.CloudErrorCodeFailed, false},
		{400, "Something else went wrong", wire.CloudErrorCodeFailed, false},
		{500, "", wire.CloudErrorCodeFailed, false},
	}
	for i, c := range cases {
		f, srv := newFake(t)
		f.add(keyAlpha, vm(idA, "web-1", "active", "stopped", "ok", "203.0.113.10"))
		f.canned["POST /v2/instances/"+idA+"/start"] = canned{c.status, fmt.Sprintf(`{"error":"%s (key %s)","status":%d}`, c.msg, keyAlpha, c.status)}
		p := coretest.Provider(newAdapter(t, srv, keyAlpha), nil, secrets...)
		_, err := p.Action(context.Background(), wire.CloudActionParams{Instance: refA, Action: wire.CloudActionKindStart, RequestID: wire.CloudRequestID(fmt.Sprintf("e-%d", i))})
		if got := coretest.Code(err); got != c.want {
			t.Errorf("%d %q: %s (%v)", c.status, c.msg, got, err)
			continue
		}
		coretest.NoLeak(t, err.Error(), secrets...)
		_, aerr := newAdapter(t, srv, keyAlpha).Action(context.Background(), core.Action{Ref: refA, Kind: wire.CloudActionKindStart})
		coretest.NoLeak(t, aerr.Error(), secrets...)
		// Only a rate limit is certain not to have been carried out.
		if got := core.AsError(aerr).Transient; got != c.transient {
			t.Errorf("%d %q: transient %v", c.status, c.msg, got)
		}
	}
}

func TestLongVendorErrorIsNotClipped(t *testing.T) {
	f, srv := newFake(t)
	f.add(keyAlpha, vm(idA, "web-1", "active", "stopped", "ok", "203.0.113.10"))
	long := strings.Repeat("x", 400) + " end"
	f.canned["POST /v2/instances/"+idA+"/start"] = canned{400, fmt.Sprintf(`{"error":%q,"status":400}`, long)}
	_, err := newAdapter(t, srv, keyAlpha).Action(context.Background(), core.Action{Ref: refA, Kind: wire.CloudActionKindStart})
	if !strings.HasSuffix(err.Error(), " end") {
		t.Fatalf("clipped: %q", err.Error())
	}
}

func TestChineseWords(t *testing.T) {
	f, srv := newFake(t)
	f.add(keyAlpha, vm(idA, "web-1", "suspended", "stopped", "ok", "203.0.113.10"))
	p := coretest.Provider(newAdapter(t, srv, keyAlpha), nil, secrets...)
	in, err := p.Instance(context.Background(), wire.CloudInstanceParams{Instance: refA, Locale: "zh-CN"})
	if err != nil {
		t.Fatal(err)
	}
	if want := core.T("zh-CN", "Vultr has suspended the instance.", "Vultr 已暂停这台实例。"); in.StateDetail != want || want == "Vultr has suspended the instance." {
		t.Fatalf("detail %q", in.StateDetail)
	}
	_, err = p.Snapshots(context.Background(), wire.CloudSnapshotsParams{Instance: refA, Locale: "zh-CN"})
	if want := core.T("zh-CN", "Vultr does not list snapshots by instance.", "Vultr 无法按实例列出快照。"); err == nil || err.Error() != want {
		t.Fatalf("%v", err)
	}
}

func TestNotFound(t *testing.T) {
	f, srv := newFake(t)
	f.add(keyAlpha, vm(idA, "web-1", "active", "running", "ok", "203.0.113.10"))
	p := coretest.Provider(newAdapter(t, srv, keyAlpha), nil, secrets...)
	refs := []wire.CloudInstanceRef{{Account: "default", ID: idB}, {Account: "other", ID: idA}, {Account: "default", ID: "../account"},
		{Account: "default", ID: "cb67/6a46%2F 0b60"}}
	for _, ref := range refs {
		if _, err := p.Instance(context.Background(), wire.CloudInstanceParams{Instance: ref}); coretest.Code(err) != wire.CloudErrorCodeNotFound {
			t.Fatalf("%v: %v", ref, err)
		}
		if _, err := p.Action(context.Background(), wire.CloudActionParams{Instance: ref, Action: wire.CloudActionKindSnapshotCreate, RequestID: wire.CloudRequestID("nf" + string(ref.Account) + string(ref.ID))}); coretest.Code(err) != wire.CloudErrorCodeNotFound {
			t.Fatalf("snapshot %v: %v", ref, err)
		}
	}
	// An id that cannot be a Vultr UUID is answered without a request: only idB was asked about.
	if got := f.count("GET /v2/instances/" + idB); got != 1 || len(f.callList()) != 2 {
		t.Fatalf("%v", f.callList())
	}
}

func TestStartReadsTheNewAddress(t *testing.T) {
	f, srv := newFake(t)
	f.add(keyAlpha, vm(idA, "web-1", "active", "stopped", "ok", "203.0.113.10"))
	p := coretest.Provider(newAdapter(t, srv, keyAlpha), nil, secrets...)
	before, err := p.Instance(context.Background(), wire.CloudInstanceParams{Instance: refA})
	if err != nil || before.PublicIps[0] != "203.0.113.10" || before.State != wire.CloudInstanceStateStopped {
		t.Fatalf("%+v %v", before, err)
	}
	if _, err := p.Action(context.Background(), wire.CloudActionParams{Instance: refA, Action: wire.CloudActionKindStart, RequestID: "s"}); err != nil {
		t.Fatal(err)
	}
	after, err := p.Instance(context.Background(), wire.CloudInstanceParams{Instance: refA})
	if err != nil || after.PublicIps[0] != "203.0.113.99" || after.State != wire.CloudInstanceStateRunning {
		t.Fatalf("%+v %v", after, err)
	}
	res, err := p.Action(context.Background(), wire.CloudActionParams{Instance: refA, Action: wire.CloudActionKindSyncIp, RequestID: "sync"})
	if err != nil {
		t.Fatal(err)
	}
	coretest.Valid(t, "cloud.action", res)
	if res.Instance == nil || res.Instance.PublicIps[0] != "203.0.113.99" {
		t.Fatalf("%+v", res.Instance)
	}
}

func TestInstancesCutAt500(t *testing.T) {
	f, srv := newFake(t)
	f.pageSize = 500 // as Vultr does for per_page=500
	for i := 1; i <= 520; i++ {
		f.add(keyAlpha, vm(fmt.Sprintf("00000000-0000-4000-8000-%012d", i), fmt.Sprintf("v-%03d", i), "active", "running", "ok", "203.0.113.10"))
	}
	a := newAdapter(t, srv, keyAlpha)
	list, _, err := a.Instances(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != core.MaxInstances || f.count("GET /v2/instances") != 1 {
		t.Fatalf("got %d after %d calls", len(list), f.count("GET /v2/instances"))
	}
	out, err := coretest.Provider(a, nil, secrets...).Instances(context.Background(), wire.CloudInstancesParams{Refresh: true})
	if err != nil {
		t.Fatal(err)
	}
	coretest.Valid(t, "cloud.instances", out)
	if len(out.Instances) != core.MaxInstances || !out.Partial || out.Detail == "" {
		t.Fatalf("n %d partial %v detail %q", len(out.Instances), out.Partial, out.Detail)
	}
}
