package digitalocean

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
	tokenAlpha = "test-token-alpha-0001"
	tokenBeta  = "test-token-beta-0002"
	tokenBad   = "test-token-revoked-0003"
)

var secrets = []string{tokenAlpha, tokenBeta, tokenBad}

var fixedNow = time.Date(2026, 11, 27, 9, 30, 0, 0, time.UTC)

type canned struct {
	status int
	body   string
}

// fake is the DigitalOcean API: teams by token, their droplets and snapshots.
type fake struct {
	t         *testing.T
	mu        sync.Mutex
	droplets  map[string]map[int64]map[string]any
	snapshots map[int64][]map[string]any
	canned    map[string]canned
	calls     []string
	bodies    []map[string]any
	baseURL   string
}

func newFake(t *testing.T) (*fake, *httptest.Server) {
	f := &fake{t: t, droplets: map[string]map[int64]map[string]any{tokenAlpha: {}, tokenBeta: {}},
		snapshots: map[int64][]map[string]any{}, canned: map[string]canned{}}
	srv := httptest.NewServer(http.HandlerFunc(f.serve))
	f.baseURL = srv.URL
	t.Cleanup(srv.Close)
	return f, srv
}

func droplet1(id int64, name, status, ipv4 string) map[string]any {
	return map[string]any{
		"id": id, "name": name, "memory": 2048, "vcpus": 1, "disk": 50, "locked": false, "status": status,
		"created_at": "2026-10-01T08:00:00Z", "features": []any{"private_networking"},
		"image":     map[string]any{"id": 1, "name": "24.04 (LTS) x64", "distribution": "Ubuntu", "slug": "ubuntu-24-04-x64", "public": true},
		"size_slug": "s-1vcpu-2gb",
		"networks": map[string]any{
			"v4": []any{
				map[string]any{"ip_address": "10.110.0.5", "netmask": "255.255.240.0", "gateway": "", "type": "private"},
				map[string]any{"ip_address": ipv4, "netmask": "255.255.240.0", "gateway": "203.0.113.1", "type": "public"},
			},
			"v6": []any{map[string]any{"ip_address": "2001:db8:10::51", "netmask": 64, "gateway": "2001:db8:10::1", "type": "public"}},
		},
		"region": map[string]any{"name": "Frankfurt 1", "slug": "fra1", "available": true},
		"tags":   []any{},
	}
}

func (f *fake) add(token string, d map[string]any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.droplets[token][d["id"].(int64)] = d
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

func derr(w http.ResponseWriter, status int, id, msg string) {
	writeJSON(w, status, map[string]any{"id": id, "message": msg, "request_id": "00000000-0000-4000-8000-000000000000"})
}

func pageOf(q map[string][]string, total int) (from, to int, more bool) {
	p, _ := strconv.Atoi(firstOr(q["page"], "1"))
	per, _ := strconv.Atoi(firstOr(q["per_page"], "20"))
	from, to = min((p-1)*per, total), min(p*per, total)
	return from, to, to < total
}

func firstOr(v []string, d string) string {
	if len(v) == 0 {
		return d
	}
	return v[0]
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
	droplets, ok := f.droplets[strings.TrimPrefix(auth, "Bearer ")]
	if !ok || !strings.HasPrefix(auth, "Bearer ") {
		derr(w, 401, "unauthorized", "Unable to authenticate you")
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/v2")
	parts := strings.Split(strings.Trim(path, "/"), "/")
	switch {
	case r.Method == http.MethodGet && path == "/droplets":
		if r.URL.Query().Get("per_page") != "200" {
			f.t.Errorf("per_page %q", r.URL.Query().Get("per_page"))
		}
		ids := make([]int64, 0, len(droplets))
		for id := range droplets {
			ids = append(ids, id)
		}
		sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
		from, to, more := pageOf(r.URL.Query(), len(ids))
		list := []any{}
		for _, id := range ids[from:to] {
			list = append(list, droplets[id])
		}
		pages := map[string]any{}
		if more {
			p, _ := strconv.Atoi(r.URL.Query().Get("page"))
			pages["next"] = fmt.Sprintf("%s/v2/droplets?page=%d&per_page=200", f.baseURL, p+1)
			pages["last"] = pages["next"]
		}
		writeJSON(w, 200, map[string]any{"droplets": list, "links": map[string]any{"pages": pages}, "meta": map[string]any{"total": len(ids)}})
	case r.Method == http.MethodGet && len(parts) == 2 && parts[0] == "droplets":
		id, _ := strconv.ParseInt(parts[1], 10, 64)
		d, ok := droplets[id]
		if !ok {
			derr(w, 404, "not_found", "The resource you were accessing could not be found.")
			return
		}
		writeJSON(w, 200, map[string]any{"droplet": d})
	case r.Method == http.MethodPost && len(parts) == 3 && parts[0] == "droplets" && parts[2] == "actions":
		if r.Header.Get("Content-Type") != "application/json" {
			f.t.Errorf("Content-Type %q", r.Header.Get("Content-Type"))
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.bodies = append(f.bodies, body)
		id, _ := strconv.ParseInt(parts[1], 10, 64)
		d, ok := droplets[id]
		if !ok {
			derr(w, 404, "not_found", "The resource you were accessing could not be found.")
			return
		}
		switch body["type"] {
		case "power_on":
			d["status"] = "active"
			d["networks"].(map[string]any)["v4"] = []any{map[string]any{"ip_address": "203.0.113.99", "type": "public"}}
		case "power_off", "shutdown":
			d["status"] = "off"
		case "reboot", "power_cycle", "snapshot":
		default:
			derr(w, 422, "unprocessable_entity", "unknown action type")
			return
		}
		writeJSON(w, 201, map[string]any{"action": map[string]any{"id": 36804636, "status": "in-progress", "type": body["type"],
			"started_at": "2026-11-27T09:30:00Z", "completed_at": nil, "resource_id": id, "resource_type": "droplet", "region_slug": "fra1"}})
	case r.Method == http.MethodGet && len(parts) == 3 && parts[0] == "droplets" && parts[2] == "snapshots":
		id, _ := strconv.ParseInt(parts[1], 10, 64)
		if _, ok := droplets[id]; !ok {
			derr(w, 404, "not_found", "The resource you were accessing could not be found.")
			return
		}
		all := f.snapshots[id]
		from, to, more := pageOf(r.URL.Query(), len(all))
		list := []any{}
		for _, s := range all[from:to] {
			list = append(list, s)
		}
		pages := map[string]any{}
		if more {
			pages["next"] = f.baseURL + "/v2/droplets/" + parts[1] + "/snapshots?page=2"
		}
		writeJSON(w, 200, map[string]any{"snapshots": list, "links": map[string]any{"pages": pages}, "meta": map[string]any{"total": len(all)}})
	default:
		derr(w, 404, "not_found", "no route")
	}
}

func newAdapter(t *testing.T, srv *httptest.Server, tokens string) core.Adapter {
	t.Helper()
	a, err := New(Config{Tokens: tokens, BaseURL: srv.URL + "/v2", HTTP: srv.Client(), Now: func() time.Time { return fixedNow }})
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

var ref42 = wire.CloudInstanceRef{Account: "default", ID: "42"}

func TestNewNeedsAToken(t *testing.T) {
	if _, err := New(Config{}); coretest.Code(err) != wire.CloudErrorCodeNotSignedIn {
		t.Fatalf("%v", err)
	}
	_, err := New(Config{Tokens: "team=bad\ttoken-value"})
	if coretest.Code(err) != wire.CloudErrorCodeNotSignedIn || !strings.Contains(err.Error(), "team") {
		t.Fatalf("%v", err)
	}
	coretest.NoLeak(t, err.Error(), "token-value")
	a, err := New(Config{Tokens: tokenAlpha})
	if err != nil {
		t.Fatal(err)
	}
	if got := a.(core.SecretHolder).SecretValues(); len(got) != 1 || got[0] != tokenAlpha {
		t.Fatalf("%v", got)
	}
}

func TestInstancesAcrossAccounts(t *testing.T) {
	f, srv := newFake(t)
	for i := int64(1); i <= 250; i++ {
		f.add(tokenAlpha, droplet1(i, fmt.Sprintf("alpha-%03d", i), "active", "203.0.113.10"))
	}
	f.add(tokenBeta, droplet1(4242, "beta-web", "off", "198.51.100.7"))
	a := newAdapter(t, srv, "alpha="+tokenAlpha+"\nbeta="+tokenBeta+"\ngamma="+tokenBad)
	list, problems, err := a.Instances(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 251 || len(problems) != 1 || problems[0] != "gamma" {
		t.Fatalf("got %d, problems %v", len(list), problems)
	}
	if f.count("GET /v2/droplets") != 4 {
		t.Fatalf("calls %v", f.callList())
	}
	p := coretest.Provider(a, nil, secrets...)
	out, err := p.Instances(context.Background(), wire.CloudInstancesParams{Refresh: true, Locale: "en"})
	if err != nil {
		t.Fatal(err)
	}
	raw := coretest.Valid(t, "cloud.instances", out)
	coretest.NoLeak(t, string(raw), secrets...)
	if !out.Partial || !strings.Contains(out.Detail, "gamma") {
		t.Fatalf("partial %v %q", out.Partial, out.Detail)
	}
}

func TestInstancesAllAccountsFail(t *testing.T) {
	_, srv := newFake(t)
	p := coretest.Provider(newAdapter(t, srv, tokenBad), nil, secrets...)
	_, err := p.Instances(context.Background(), wire.CloudInstancesParams{Refresh: true})
	if coretest.Code(err) != wire.CloudErrorCodeNotSignedIn {
		t.Fatalf("%v", err)
	}
}

func TestInstanceFields(t *testing.T) {
	f, srv := newFake(t)
	f.add(tokenAlpha, droplet1(42, "web-1", "active", "203.0.113.10"))
	p := coretest.Provider(newAdapter(t, srv, tokenAlpha), nil, secrets...)
	in, err := p.Instance(context.Background(), wire.CloudInstanceParams{Instance: ref42})
	if err != nil {
		t.Fatal(err)
	}
	coretest.Valid(t, "cloud.instance", in)
	want := wire.CloudInstance{
		Account: "default", ID: "42", DisplayName: "web-1", State: wire.CloudInstanceStateRunning, Region: "fra1",
		PublicIps:  []string{"203.0.113.10", "2001:db8:10::51"},
		PrivateIps: []string{"10.110.0.5"},
		Spec:       &wire.CloudSpec{Size: "s-1vcpu-2gb", Cpu: 1, MemoryMb: 2048, DiskGb: 50, OS: "Ubuntu 24.04 (LTS) x64"},
		ConsoleURL: "https://cloud.digitalocean.com/droplets/42",
		Actions: []wire.CloudActionKind{wire.CloudActionKindStop, wire.CloudActionKindReboot, wire.CloudActionKindHardReset,
			wire.CloudActionKindForceStop, wire.CloudActionKindSyncIp, wire.CloudActionKindSnapshotList,
			wire.CloudActionKindSnapshotCreate, wire.CloudActionKindConsole},
		ObservedAt: fixedNow,
	}
	if asJSON(t, in) != asJSON(t, want) {
		t.Fatalf("got  %s\nwant %s", asJSON(t, in), asJSON(t, want))
	}
	res, err := p.Action(context.Background(), wire.CloudActionParams{Instance: ref42, Action: wire.CloudActionKindConsole, RequestID: "c"})
	if err != nil {
		t.Fatal(err)
	}
	coretest.Valid(t, "cloud.action", res)
	if res.URL != "https://cloud.digitalocean.com/droplets/42" || strings.Contains(res.URL, "@") || strings.Contains(res.URL, "?") {
		t.Fatalf("%q", res.URL)
	}
}

func TestStateMapping(t *testing.T) {
	cases := []struct {
		status string
		locked bool
		state  wire.CloudInstanceState
		detail bool
		offers []wire.CloudActionKind
		lacks  []wire.CloudActionKind
	}{
		{"new", false, wire.CloudInstanceStateStarting, false, []wire.CloudActionKind{wire.CloudActionKindConsole}, []wire.CloudActionKind{wire.CloudActionKindStart, wire.CloudActionKindStop}},
		{"active", false, wire.CloudInstanceStateRunning, false, []wire.CloudActionKind{wire.CloudActionKindStop, wire.CloudActionKindReboot, wire.CloudActionKindHardReset, wire.CloudActionKindForceStop}, []wire.CloudActionKind{wire.CloudActionKindStart}},
		{"off", false, wire.CloudInstanceStateStopped, false, []wire.CloudActionKind{wire.CloudActionKindStart, wire.CloudActionKindSnapshotCreate}, []wire.CloudActionKind{wire.CloudActionKindStop}},
		{"archive", false, wire.CloudInstanceStateTerminated, true, nil, []wire.CloudActionKind{wire.CloudActionKindStart, wire.CloudActionKindConsole}},
		{"active", true, wire.CloudInstanceStateRunning, true, []wire.CloudActionKind{wire.CloudActionKindSnapshotList}, []wire.CloudActionKind{wire.CloudActionKindStop, wire.CloudActionKindReboot, wire.CloudActionKindForceStop, wire.CloudActionKindSnapshotCreate}},
		{"off", true, wire.CloudInstanceStateStopped, true, nil, []wire.CloudActionKind{wire.CloudActionKindStart}},
		{"mystery", false, wire.CloudInstanceStateUnknown, true, nil, []wire.CloudActionKind{wire.CloudActionKindStart}},
	}
	f, srv := newFake(t)
	for i, c := range cases {
		d := droplet1(int64(100+i), "d", c.status, "203.0.113.20")
		d["locked"] = c.locked
		f.add(tokenAlpha, d)
	}
	p := coretest.Provider(newAdapter(t, srv, tokenAlpha), nil, secrets...)
	for i, c := range cases {
		in, err := p.Instance(context.Background(), wire.CloudInstanceParams{Instance: wire.CloudInstanceRef{Account: "default", ID: wire.CloudInstanceID(strconv.Itoa(100 + i))}})
		if err != nil {
			t.Fatal(err)
		}
		coretest.Valid(t, "cloud.instance", in)
		if in.State != c.state || (in.StateDetail != "") != c.detail {
			t.Errorf("%s/%v: %s %q", c.status, c.locked, in.State, in.StateDetail)
		}
		if !core.Offers(in.Actions, wire.CloudActionKindSyncIp) {
			t.Errorf("%s: no sync_ip", c.status)
		}
		for _, k := range c.offers {
			if !core.Offers(in.Actions, k) {
				t.Errorf("%s/%v: lacks %s: %v", c.status, c.locked, k, in.Actions)
			}
		}
		for _, k := range c.lacks {
			if core.Offers(in.Actions, k) {
				t.Errorf("%s/%v: offers %s: %v", c.status, c.locked, k, in.Actions)
			}
		}
	}
}

func TestActions(t *testing.T) {
	cases := []struct {
		kind wire.CloudActionKind
		typ  string
	}{
		{wire.CloudActionKindStart, "power_on"},
		{wire.CloudActionKindStop, "shutdown"},
		{wire.CloudActionKindReboot, "reboot"},
		{wire.CloudActionKindHardReset, "power_cycle"},
		{wire.CloudActionKindForceStop, "power_off"},
		{wire.CloudActionKindSnapshotCreate, "snapshot"},
	}
	f, srv := newFake(t)
	f.add(tokenAlpha, droplet1(42, "web-1", "active", "203.0.113.10"))
	p := coretest.Provider(newAdapter(t, srv, tokenAlpha), nil, secrets...)
	for i, c := range cases {
		params := wire.CloudActionParams{Instance: ref42, Action: c.kind, RequestID: wire.CloudRequestID("req-" + c.typ)}
		if c.kind == wire.CloudActionKindSnapshotCreate {
			params.Name = "nightly"
		}
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
		f.mu.Lock()
		n, body := len(f.bodies), f.bodies[len(f.bodies)-1]
		f.mu.Unlock()
		if n != i+1 || body["type"] != c.typ {
			t.Fatalf("%s: %d calls, last %v", c.kind, n, body)
		}
		if c.kind == wire.CloudActionKindSnapshotCreate && body["name"] != "nightly" {
			t.Fatalf("snapshot body %v", body)
		}
	}
	if f.count("POST /v2/droplets/42/actions") != len(cases) {
		t.Fatalf("calls %v", f.callList())
	}
}

func TestUnsupported(t *testing.T) {
	f, srv := newFake(t)
	a := newAdapter(t, srv, tokenAlpha)
	for _, k := range []wire.CloudActionKind{wire.CloudActionKindCustom, wire.CloudActionKindConsole, wire.CloudActionKindSyncIp, "teleport"} {
		if _, err := a.Action(context.Background(), core.Action{Ref: ref42, Kind: k}); coretest.Code(err) != wire.CloudErrorCodeUnsupported {
			t.Fatalf("%s: %v", k, err)
		}
	}
	p := coretest.Provider(a, nil, secrets...)
	if _, err := p.Action(context.Background(), wire.CloudActionParams{Instance: ref42, Action: wire.CloudActionKindCustom, Custom: "renew", RequestID: "x"}); coretest.Code(err) != wire.CloudErrorCodeUnsupported {
		t.Fatalf("%v", err)
	}
	if len(f.callList()) != 0 {
		t.Fatalf("%v", f.callList())
	}
}

func TestVendorErrors(t *testing.T) {
	cases := []struct {
		status    int
		id        string
		msg       string
		want      wire.CloudErrorCode
		transient bool
	}{
		{401, "unauthorized", "Unable to authenticate you", wire.CloudErrorCodeNotSignedIn, false},
		{403, "forbidden", "You do not have access for the attempted action.", wire.CloudErrorCodeNotSignedIn, false},
		{404, "not_found", "The resource you were accessing could not be found.", wire.CloudErrorCodeNotFound, false},
		{422, "unprocessable_entity", "Droplet is already powered on.", wire.CloudErrorCodeInvalidState, false},
		{422, "unprocessable_entity", "Droplet must be powered off.", wire.CloudErrorCodeInvalidState, false},
		{422, "unprocessable_entity", "Droplet already has a pending event.", wire.CloudErrorCodeInvalidState, false},
		{422, "unprocessable_entity", "request payload validation failed", wire.CloudErrorCodeFailed, false},
		{422, "unprocessable_entity", "You have reached the snapshot limit for your account.", wire.CloudErrorCodeCapacity, false},
		{409, "conflict", "Droplet already has a pending event.", wire.CloudErrorCodeInvalidState, false},
		{429, "too_many_requests", "API Rate limit exceeded.", wire.CloudErrorCodeRateLimited, true},
		{429, "", "", wire.CloudErrorCodeRateLimited, true},
		{502, "", "", wire.CloudErrorCodeFailed, false},
		{500, "server_error", "Server was unable to give you a response.", wire.CloudErrorCodeFailed, false},
		{400, "bad_request", "invalid body", wire.CloudErrorCodeFailed, false},
		{503, "", "", wire.CloudErrorCodeFailed, false},
	}
	for i, c := range cases {
		f, srv := newFake(t)
		f.add(tokenAlpha, droplet1(42, "web-1", "off", "203.0.113.10"))
		body := fmt.Sprintf(`{"id":%q,"message":"%s (token %s)"}`, c.id, c.msg, tokenAlpha)
		f.canned["POST /v2/droplets/42/actions"] = canned{c.status, body}
		p := coretest.Provider(newAdapter(t, srv, tokenAlpha), nil, secrets...)
		_, err := p.Action(context.Background(), wire.CloudActionParams{Instance: ref42, Action: wire.CloudActionKindStart, RequestID: wire.CloudRequestID(fmt.Sprintf("e-%d", i))})
		if got := coretest.Code(err); got != c.want {
			t.Errorf("%d %s: %s (%v)", c.status, c.id, got, err)
			continue
		}
		coretest.NoLeak(t, err.Error(), secrets...)
		_, aerr := newAdapter(t, srv, tokenAlpha).Action(context.Background(), core.Action{Ref: ref42, Kind: wire.CloudActionKindStart})
		coretest.NoLeak(t, aerr.Error(), secrets...)
		// Only a rate limit is certain not to have been carried out.
		if got := core.AsError(aerr).Transient; got != c.transient {
			t.Errorf("%d %s: transient %v", c.status, c.id, got)
		}
	}
}

func TestLongVendorErrorIsNotClipped(t *testing.T) {
	f, srv := newFake(t)
	f.add(tokenAlpha, droplet1(42, "web-1", "off", "203.0.113.10"))
	long := strings.Repeat("x", 400) + " end"
	f.canned["POST /v2/droplets/42/actions"] = canned{400, fmt.Sprintf(`{"id":"bad_request","message":%q}`, long)}
	_, err := newAdapter(t, srv, tokenAlpha).Action(context.Background(), core.Action{Ref: ref42, Kind: wire.CloudActionKindStart})
	if !strings.HasSuffix(err.Error(), " end") {
		t.Fatalf("clipped: %q", err.Error())
	}
}

func TestChineseWords(t *testing.T) {
	f, srv := newFake(t)
	f.add(tokenAlpha, droplet1(42, "web-1", "archive", "203.0.113.10"))
	p := coretest.Provider(newAdapter(t, srv, tokenAlpha), nil, secrets...)
	in, err := p.Instance(context.Background(), wire.CloudInstanceParams{Instance: ref42, Locale: "zh-CN"})
	if err != nil {
		t.Fatal(err)
	}
	if want := core.T("zh-CN", "The droplet is archived.", "这台云主机已归档。"); in.StateDetail != want || want == "The droplet is archived." {
		t.Fatalf("detail %q", in.StateDetail)
	}
	en, err := p.Instance(context.Background(), wire.CloudInstanceParams{Instance: ref42, Locale: "en"})
	if err != nil || en.StateDetail != "The droplet is archived." {
		t.Fatalf("%q %v", en.StateDetail, err)
	}
}

func TestNotFound(t *testing.T) {
	f, srv := newFake(t)
	f.add(tokenAlpha, droplet1(42, "web-1", "active", "203.0.113.10"))
	p := coretest.Provider(newAdapter(t, srv, tokenAlpha), nil, secrets...)
	refs := []wire.CloudInstanceRef{{Account: "default", ID: "43"}, {Account: "other", ID: "42"}, {Account: "default", ID: "abc"},
		{Account: "default", ID: "4/2%2F 1"}, {Account: "default", ID: "42/../43"}}
	for _, ref := range refs {
		if _, err := p.Instance(context.Background(), wire.CloudInstanceParams{Instance: ref}); coretest.Code(err) != wire.CloudErrorCodeNotFound {
			t.Fatalf("%v: %v", ref, err)
		}
		if _, err := p.Snapshots(context.Background(), wire.CloudSnapshotsParams{Instance: ref}); coretest.Code(err) != wire.CloudErrorCodeNotFound {
			t.Fatalf("snapshots %v: %v", ref, err)
		}
		if _, err := p.Action(context.Background(), wire.CloudActionParams{Instance: ref, Action: wire.CloudActionKindStart, RequestID: wire.CloudRequestID("nf-" + string(ref.Account) + string(ref.ID))}); coretest.Code(err) != wire.CloudErrorCodeNotFound {
			t.Fatalf("action %v: %v", ref, err)
		}
	}
	// An id that cannot be a droplet's is answered without a request: only droplet 43 was asked about.
	for _, c := range f.callList() {
		if !strings.HasPrefix(c, "GET /v2/droplets/43") && c != "POST /v2/droplets/43/actions" {
			t.Fatalf("unexpected request %q: %v", c, f.callList())
		}
	}
}

func TestStartReadsTheNewAddress(t *testing.T) {
	f, srv := newFake(t)
	f.add(tokenAlpha, droplet1(42, "web-1", "off", "203.0.113.10"))
	p := coretest.Provider(newAdapter(t, srv, tokenAlpha), nil, secrets...)
	before, err := p.Instance(context.Background(), wire.CloudInstanceParams{Instance: ref42})
	if err != nil || before.PublicIps[0] != "203.0.113.10" {
		t.Fatalf("%+v %v", before, err)
	}
	if _, err := p.Action(context.Background(), wire.CloudActionParams{Instance: ref42, Action: wire.CloudActionKindStart, RequestID: "s"}); err != nil {
		t.Fatal(err)
	}
	res, err := p.Action(context.Background(), wire.CloudActionParams{Instance: ref42, Action: wire.CloudActionKindSyncIp, RequestID: "sync"})
	if err != nil {
		t.Fatal(err)
	}
	coretest.Valid(t, "cloud.action", res)
	if res.Instance == nil || res.Instance.PublicIps[0] != "203.0.113.99" || res.Instance.State != wire.CloudInstanceStateRunning {
		t.Fatalf("%+v", res.Instance)
	}
}

func TestSnapshots(t *testing.T) {
	f, srv := newFake(t)
	f.add(tokenAlpha, droplet1(42, "web-1", "active", "203.0.113.10"))
	for i := 0; i < 130; i++ {
		f.snapshots[42] = append(f.snapshots[42], map[string]any{"id": 6372321 + i, "name": fmt.Sprintf("web-1-%d", i),
			"regions": []any{"fra1"}, "created_at": time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(i) * time.Hour).Format(time.RFC3339),
			"resource_id": "42", "resource_type": "droplet", "min_disk_size": 50, "size_gigabytes": 2.36})
	}
	p := coretest.Provider(newAdapter(t, srv, tokenAlpha), nil, secrets...)
	out, err := p.Snapshots(context.Background(), wire.CloudSnapshotsParams{Instance: ref42})
	if err != nil {
		t.Fatal(err)
	}
	coretest.Valid(t, "cloud.snapshots", out)
	if len(out.Snapshots) != 130 || out.Snapshots[0].Name != "web-1-129" || out.Snapshots[0].ID != "6372450" ||
		out.Snapshots[0].State != wire.CloudSnapshotStateReady || out.Snapshots[0].SizeGb != 2.36 {
		t.Fatalf("%d %+v", len(out.Snapshots), out.Snapshots[0])
	}
	if f.count("GET /v2/droplets/42/snapshots") != 2 {
		t.Fatalf("%v", f.callList())
	}
}

func TestInstancesCutAt500(t *testing.T) {
	f, srv := newFake(t)
	for i := 1; i <= 520; i++ {
		f.add(tokenAlpha, droplet1(int64(i), fmt.Sprintf("d-%03d", i), "active", "203.0.113.10"))
	}
	a := newAdapter(t, srv, tokenAlpha)
	list, _, err := a.Instances(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != core.MaxInstances || f.count("GET /v2/droplets") != 3 {
		t.Fatalf("got %d after %d calls", len(list), f.count("GET /v2/droplets"))
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
