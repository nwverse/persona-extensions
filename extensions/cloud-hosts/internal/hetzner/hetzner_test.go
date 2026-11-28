package hetzner

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

// canned is an answer the fake gives instead of its own for one "METHOD /path".
type canned struct {
	status int
	body   string
}

// fake is a Hetzner Cloud API: projects by token, their servers and images.
type fake struct {
	t       *testing.T
	mu      sync.Mutex
	servers map[string]map[int64]map[string]any // token -> id -> server
	images  map[string][]map[string]any         // token -> images
	canned  map[string]canned
	calls   []string
	bodies  map[string]map[string]any
}

func newFake(t *testing.T) (*fake, *httptest.Server) {
	f := &fake{t: t, servers: map[string]map[int64]map[string]any{tokenAlpha: {}, tokenBeta: {}},
		images: map[string][]map[string]any{}, canned: map[string]canned{}, bodies: map[string]map[string]any{}}
	srv := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(srv.Close)
	return f, srv
}

func server1(id int64, name, status, ipv4 string) map[string]any {
	return map[string]any{
		"id": id, "name": name, "status": status, "created": "2026-10-01T08:00:00+00:00",
		"public_net": map[string]any{
			"ipv4": map[string]any{"ip": ipv4, "blocked": false, "dns_ptr": "static.example.com"},
			"ipv6": map[string]any{"ip": "2001:db8:1:2::/64", "blocked": false},
		},
		"private_net": []any{map[string]any{"network": 7, "ip": "10.0.0.2", "alias_ips": []any{}}},
		"server_type": map[string]any{"id": 22, "name": "cx22", "cores": 2, "memory": 4.0, "disk": 40, "architecture": "x86"},
		"location":    map[string]any{"id": 1, "name": "fsn1", "city": "Falkenstein", "country": "DE", "network_zone": "eu-central"},
		"image":       map[string]any{"id": 1, "type": "system", "description": "Ubuntu 24.04"},
		"locked":      false,
	}
}

func (f *fake) add(token string, s map[string]any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.servers[token][s["id"].(int64)] = s
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

func herr(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]any{"error": map[string]any{"code": code, "message": msg, "details": map[string]any{}}})
}

func page(q map[string][]string, total int) (from, to int, next any, pageNo int) {
	pageNo, _ = strconv.Atoi(firstOr(q["page"], "1"))
	per, _ := strconv.Atoi(firstOr(q["per_page"], "25"))
	from = (pageNo - 1) * per
	to = from + per
	if from > total {
		from = total
	}
	if to > total {
		to = total
	}
	next = nil
	if to < total {
		next = pageNo + 1
	}
	return
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
	if r.Header.Get("Accept") != "application/json" {
		f.t.Errorf("%s: Accept %q", key, r.Header.Get("Accept"))
	}
	if c, ok := f.canned[key]; ok {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(c.status)
		_, _ = io.WriteString(w, c.body)
		return
	}
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	servers, ok := f.servers[token]
	if !ok || !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
		herr(w, http.StatusUnauthorized, "unauthorized", "unable to authenticate")
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/v1")
	parts := strings.Split(strings.Trim(path, "/"), "/")
	switch {
	case r.Method == http.MethodGet && path == "/servers":
		ids := make([]int64, 0, len(servers))
		for id := range servers {
			ids = append(ids, id)
		}
		sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
		if r.URL.Query().Get("per_page") != "50" {
			f.t.Errorf("per_page %q", r.URL.Query().Get("per_page"))
		}
		from, to, next, p := page(r.URL.Query(), len(ids))
		list := []any{}
		for _, id := range ids[from:to] {
			list = append(list, servers[id])
		}
		writeJSON(w, 200, map[string]any{"servers": list, "meta": map[string]any{"pagination": map[string]any{
			"page": p, "per_page": 50, "previous_page": nil, "next_page": next, "last_page": 2, "total_entries": len(ids)}}})
	case r.Method == http.MethodGet && len(parts) == 2 && parts[0] == "servers":
		id, _ := strconv.ParseInt(parts[1], 10, 64)
		s, ok := servers[id]
		if !ok {
			herr(w, 404, "not_found", "server with ID "+parts[1]+" not found")
			return
		}
		writeJSON(w, 200, map[string]any{"server": s})
	case r.Method == http.MethodPost && len(parts) == 4 && parts[0] == "servers" && parts[2] == "actions":
		if r.Header.Get("Content-Type") != "application/json" {
			f.t.Errorf("%s: Content-Type %q", key, r.Header.Get("Content-Type"))
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.bodies[key] = body
		id, _ := strconv.ParseInt(parts[1], 10, 64)
		s, ok := servers[id]
		if !ok {
			herr(w, 404, "not_found", "server not found")
			return
		}
		switch parts[3] {
		case "poweron":
			s["status"] = "running"
			s["public_net"].(map[string]any)["ipv4"] = map[string]any{"ip": "203.0.113.99"}
		case "poweroff", "shutdown":
			s["status"] = "off"
		case "create_image":
			s["locked"] = true // as Hetzner does while the image is taken
			writeJSON(w, 201, map[string]any{
				"image":  map[string]any{"id": 9001, "type": "snapshot", "status": "creating", "description": body["description"], "created": "2026-11-27T09:30:00+00:00", "created_from": map[string]any{"id": id, "name": s["name"]}},
				"action": map[string]any{"id": 77, "command": "create_image", "status": "running", "progress": 0, "error": nil}})
			return
		case "reboot", "reset":
		default:
			herr(w, 404, "not_found", "no such action")
			return
		}
		writeJSON(w, 201, map[string]any{"action": map[string]any{"id": 13, "command": parts[3], "status": "running", "progress": 0, "error": nil}})
	case r.Method == http.MethodGet && path == "/images":
		if r.URL.Query().Get("type") != "snapshot" || r.URL.Query().Get("sort") != "created:desc" {
			f.t.Errorf("images query %q", r.URL.RawQuery)
		}
		all := f.images[token]
		from, to, next, p := page(r.URL.Query(), len(all))
		list := []any{}
		for _, im := range all[from:to] {
			list = append(list, im)
		}
		writeJSON(w, 200, map[string]any{"images": list, "meta": map[string]any{"pagination": map[string]any{"page": p, "next_page": next}}})
	default:
		herr(w, 404, "not_found", "no route")
	}
}

func newAdapter(t *testing.T, srv *httptest.Server, tokens string) core.Adapter {
	t.Helper()
	a, err := New(Config{Tokens: tokens, BaseURL: srv.URL + "/v1", HTTP: srv.Client(), Now: func() time.Time { return fixedNow }})
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

func TestNewNeedsAToken(t *testing.T) {
	for _, tokens := range []string{"", "  \n# only a comment\n"} {
		_, err := New(Config{Tokens: tokens})
		if coretest.Code(err) != wire.CloudErrorCodeNotSignedIn {
			t.Fatalf("%q: %v", tokens, err)
		}
	}
	_, err := New(Config{Tokens: "alpha=test token with spaces"})
	if coretest.Code(err) != wire.CloudErrorCodeNotSignedIn || !strings.Contains(err.Error(), "alpha") {
		t.Fatalf("bad token: %v", err)
	}
	coretest.NoLeak(t, err.Error(), "test token with spaces")
	if _, err := New(Config{Tokens: tokenAlpha, BaseURL: "ftp://example.com"}); coretest.Code(err) != wire.CloudErrorCodeFailed {
		t.Fatalf("bad base: %v", err)
	}
	a, err := New(Config{Tokens: "a=" + tokenAlpha + "\nb=" + tokenBeta})
	if err != nil {
		t.Fatal(err)
	}
	got := a.(core.SecretHolder).SecretValues()
	if len(got) != 2 || got[0] != tokenAlpha || got[1] != tokenBeta {
		t.Fatalf("secrets %v", got)
	}
}

func TestInstancesAcrossAccounts(t *testing.T) {
	f, srv := newFake(t)
	for i := int64(1); i <= 55; i++ {
		f.add(tokenAlpha, server1(i, fmt.Sprintf("alpha-%02d", i), "running", "203.0.113.10"))
	}
	f.add(tokenBeta, server1(4242, "beta-web", "off", "198.51.100.7"))
	a := newAdapter(t, srv, "alpha="+tokenAlpha+"\nbeta="+tokenBeta+"\ngamma="+tokenBad)

	list, problems, err := a.Instances(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 56 || len(problems) != 1 || problems[0] != "gamma" {
		t.Fatalf("got %d instances, problems %v", len(list), problems)
	}
	if f.count("GET /v1/servers") != 4 { // two pages of alpha, one of beta, one of gamma
		t.Fatalf("calls %v", f.callList())
	}

	p := coretest.Provider(a, nil, secrets...)
	out, err := p.Instances(context.Background(), wire.CloudInstancesParams{Contribution: "hetzner", Locale: "en", Refresh: true})
	if err != nil {
		t.Fatal(err)
	}
	raw := coretest.Valid(t, "cloud.instances", out)
	coretest.NoLeak(t, string(raw), secrets...)
	if !out.Partial || !strings.Contains(out.Detail, "gamma") || len(out.Instances) != 56 {
		t.Fatalf("partial %v detail %q n %d", out.Partial, out.Detail, len(out.Instances))
	}
	var beta *wire.CloudInstance
	for i := range out.Instances {
		if out.Instances[i].Account == "beta" {
			beta = &out.Instances[i]
		}
	}
	if beta == nil || beta.ID != "4242" || beta.State != wire.CloudInstanceStateStopped || !core.Offers(beta.Actions, wire.CloudActionKindStart) {
		t.Fatalf("beta %+v", beta)
	}
}

func TestInstancesAllAccountsFail(t *testing.T) {
	_, srv := newFake(t)
	a := newAdapter(t, srv, tokenBad)
	_, _, err := a.Instances(context.Background())
	if coretest.Code(err) != wire.CloudErrorCodeNotSignedIn {
		t.Fatalf("%v", err)
	}
	p := coretest.Provider(a, nil, secrets...)
	_, err = p.Instances(context.Background(), wire.CloudInstancesParams{Refresh: true})
	if coretest.Code(err) != wire.CloudErrorCodeNotSignedIn {
		t.Fatalf("%v", err)
	}
	coretest.NoLeak(t, err.Error(), secrets...)
}

func TestInstanceFields(t *testing.T) {
	f, srv := newFake(t)
	f.add(tokenAlpha, server1(42, "web-1", "running", "203.0.113.10"))
	p := coretest.Provider(newAdapter(t, srv, tokenAlpha), nil, secrets...)
	in, err := p.Instance(context.Background(), wire.CloudInstanceParams{Instance: wire.CloudInstanceRef{Account: "default", ID: "42"}})
	if err != nil {
		t.Fatal(err)
	}
	coretest.Valid(t, "cloud.instance", in)
	if f.count("GET /v1/servers/42") != 1 {
		t.Fatalf("calls %v", f.callList())
	}
	want := wire.CloudInstance{
		Account: "default", ID: "42", DisplayName: "web-1", State: wire.CloudInstanceStateRunning,
		Region:     "fsn1",
		PublicIps:  []string{"203.0.113.10", "2001:db8:1:2::1"},
		PrivateIps: []string{"10.0.0.2"},
		Spec:       &wire.CloudSpec{Size: "cx22", Cpu: 2, MemoryMb: 4096, DiskGb: 40, Arch: "x86_64", OS: "Ubuntu 24.04"},
		ConsoleURL: "https://console.hetzner.cloud/",
		Actions: []wire.CloudActionKind{wire.CloudActionKindStop, wire.CloudActionKindReboot, wire.CloudActionKindHardReset,
			wire.CloudActionKindForceStop, wire.CloudActionKindSyncIp, wire.CloudActionKindSnapshotList,
			wire.CloudActionKindSnapshotCreate, wire.CloudActionKindConsole},
		ObservedAt: fixedNow,
	}
	if asJSON(t, in) != asJSON(t, want) {
		t.Fatalf("got  %s\nwant %s", asJSON(t, in), asJSON(t, want))
	}

	// The console link opens the console, https and without credentials.
	res, err := p.Action(context.Background(), wire.CloudActionParams{Instance: wire.CloudInstanceRef{Account: in.Account, ID: in.ID}, Action: wire.CloudActionKindConsole, RequestID: "r-console"})
	if err != nil {
		t.Fatal(err)
	}
	coretest.Valid(t, "cloud.action", res)
	if res.URL != ConsoleURL || !strings.HasPrefix(res.URL, "https://") || strings.Contains(res.URL, "@") || strings.Contains(res.URL, "?") {
		t.Fatalf("console %q", res.URL)
	}
}

func TestIPv6Network(t *testing.T) {
	for in, want := range map[string]string{
		"2001:db8::/64":     "2001:db8::1",
		"2001:db8:5:6::/64": "2001:db8:5:6::1",
		"2001:db8::7":       "2001:db8::7",
		"not-an-address/64": "",
		"192.0.2.0/24":      "",
		"":                  "",
	} {
		if got := firstOf64(in); got != want {
			t.Errorf("%q: got %q want %q", in, got, want)
		}
	}
	// A server without addresses of either family lists none.
	s := server{ID: 1, Status: "running"}
	if in := (&adapter{now: time.Now}).instance("default", s, ""); len(in.PublicIps) != 0 {
		t.Fatalf("%v", in.PublicIps)
	}
}

func TestStateMapping(t *testing.T) {
	cases := []struct {
		status string
		state  wire.CloudInstanceState
		detail bool
		offers []wire.CloudActionKind
		lacks  []wire.CloudActionKind
	}{
		{"initializing", wire.CloudInstanceStateStarting, false, []wire.CloudActionKind{wire.CloudActionKindSyncIp}, []wire.CloudActionKind{wire.CloudActionKindStart, wire.CloudActionKindStop}},
		{"starting", wire.CloudInstanceStateStarting, false, []wire.CloudActionKind{wire.CloudActionKindConsole}, []wire.CloudActionKind{wire.CloudActionKindStart}},
		{"running", wire.CloudInstanceStateRunning, false, []wire.CloudActionKind{wire.CloudActionKindStop, wire.CloudActionKindReboot, wire.CloudActionKindHardReset, wire.CloudActionKindForceStop}, []wire.CloudActionKind{wire.CloudActionKindStart}},
		{"stopping", wire.CloudInstanceStateStopping, false, []wire.CloudActionKind{wire.CloudActionKindForceStop}, []wire.CloudActionKind{wire.CloudActionKindStart, wire.CloudActionKindStop}},
		{"off", wire.CloudInstanceStateStopped, false, []wire.CloudActionKind{wire.CloudActionKindStart, wire.CloudActionKindSnapshotCreate}, []wire.CloudActionKind{wire.CloudActionKindStop, wire.CloudActionKindReboot}},
		{"deleting", wire.CloudInstanceStateTerminated, true, []wire.CloudActionKind{wire.CloudActionKindSyncIp}, []wire.CloudActionKind{wire.CloudActionKindStart, wire.CloudActionKindConsole}},
		{"migrating", wire.CloudInstanceStateUnknown, true, []wire.CloudActionKind{wire.CloudActionKindSnapshotList}, []wire.CloudActionKind{wire.CloudActionKindStart}},
		{"rebuilding", wire.CloudInstanceStateUnknown, true, nil, []wire.CloudActionKind{wire.CloudActionKindStop}},
		{"unknown", wire.CloudInstanceStateUnknown, true, nil, []wire.CloudActionKind{wire.CloudActionKindStart}},
		{"brand_new_status", wire.CloudInstanceStateUnknown, true, nil, nil},
	}
	f, srv := newFake(t)
	for i, c := range cases {
		f.add(tokenAlpha, server1(int64(100+i), "s-"+c.status, c.status, "203.0.113.20"))
	}
	p := coretest.Provider(newAdapter(t, srv, tokenAlpha), nil, secrets...)
	for i, c := range cases {
		in, err := p.Instance(context.Background(), wire.CloudInstanceParams{Instance: wire.CloudInstanceRef{Account: "default", ID: wire.CloudInstanceID(strconv.Itoa(100 + i))}})
		if err != nil {
			t.Fatal(err)
		}
		coretest.Valid(t, "cloud.instance", in)
		if in.State != c.state || (in.StateDetail != "") != c.detail {
			t.Errorf("%s: state %s detail %q", c.status, in.State, in.StateDetail)
		}
		if !core.Offers(in.Actions, wire.CloudActionKindSyncIp) {
			t.Errorf("%s: no sync_ip", c.status)
		}
		for _, k := range c.offers {
			if !core.Offers(in.Actions, k) {
				t.Errorf("%s: does not offer %s: %v", c.status, k, in.Actions)
			}
		}
		for _, k := range c.lacks {
			if core.Offers(in.Actions, k) {
				t.Errorf("%s: offers %s: %v", c.status, k, in.Actions)
			}
		}
	}
}

func TestActions(t *testing.T) {
	cases := []struct {
		kind wire.CloudActionKind
		path string
	}{
		{wire.CloudActionKindStart, "poweron"},
		{wire.CloudActionKindStop, "shutdown"},
		{wire.CloudActionKindReboot, "reboot"},
		{wire.CloudActionKindHardReset, "reset"},
		{wire.CloudActionKindForceStop, "poweroff"},
	}
	f, srv := newFake(t)
	f.add(tokenAlpha, server1(42, "web-1", "running", "203.0.113.10"))
	p := coretest.Provider(newAdapter(t, srv, tokenAlpha), nil, secrets...)
	ref := wire.CloudInstanceRef{Account: "default", ID: "42"}
	for _, c := range cases {
		params := wire.CloudActionParams{Instance: ref, Action: c.kind, RequestID: wire.CloudRequestID("req-" + string(c.kind))}
		for range 2 { // the same request_id twice: the vendor hears it once
			res, err := p.Action(context.Background(), params)
			if err != nil {
				t.Fatalf("%s: %v", c.kind, err)
			}
			raw := coretest.Valid(t, "cloud.action", res)
			coretest.NoLeak(t, string(raw), secrets...)
			if res.Status != wire.CloudActionStatusAccepted {
				t.Fatalf("%s: %s", c.kind, res.Status)
			}
		}
		if n := f.count("POST /v1/servers/42/actions/" + c.path); n != 1 {
			t.Fatalf("%s: %d calls: %v", c.kind, n, f.callList())
		}
	}

	res, err := p.Action(context.Background(), wire.CloudActionParams{Instance: ref, Action: wire.CloudActionKindSnapshotCreate, Name: "before upgrade", RequestID: "req-snap"})
	if err != nil {
		t.Fatal(err)
	}
	coretest.Valid(t, "cloud.action", res)
	if res.Status != wire.CloudActionStatusAccepted || res.Snapshot == nil || res.Snapshot.ID != "9001" ||
		res.Snapshot.State != wire.CloudSnapshotStateCreating || res.Snapshot.Name != "before upgrade" {
		t.Fatalf("snapshot %+v", res)
	}
	f.mu.Lock()
	body := f.bodies["POST /v1/servers/42/actions/create_image"]
	f.mu.Unlock()
	if body["type"] != "snapshot" || body["description"] != "before upgrade" {
		t.Fatalf("create_image body %v", body)
	}

	// While the image is taken the server is locked: it offers only what reads, with a detail.
	in, err := p.Instance(context.Background(), wire.CloudInstanceParams{Instance: ref})
	if err != nil {
		t.Fatal(err)
	}
	coretest.Valid(t, "cloud.instance", in)
	want := []wire.CloudActionKind{wire.CloudActionKindSyncIp, wire.CloudActionKindSnapshotList, wire.CloudActionKindConsole}
	if in.State != wire.CloudInstanceStateStopped || in.StateDetail == "" || asJSON(t, in.Actions) != asJSON(t, want) {
		t.Fatalf("locked: %s %q %v", in.State, in.StateDetail, in.Actions)
	}
}

func TestInstancesCutAt500(t *testing.T) {
	f, srv := newFake(t)
	for i := int64(1); i <= 520; i++ {
		f.add(tokenAlpha, server1(i, fmt.Sprintf("s-%03d", i), "running", "203.0.113.10"))
	}
	a := newAdapter(t, srv, tokenAlpha)
	list, _, err := a.Instances(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != core.MaxInstances || f.count("GET /v1/servers") != 10 {
		t.Fatalf("got %d after %d calls", len(list), f.count("GET /v1/servers"))
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

func TestUnsupported(t *testing.T) {
	f, srv := newFake(t)
	f.add(tokenAlpha, server1(42, "web-1", "running", "203.0.113.10"))
	a := newAdapter(t, srv, tokenAlpha)
	ref := wire.CloudInstanceRef{Account: "default", ID: "42"}
	for _, k := range []wire.CloudActionKind{wire.CloudActionKindCustom, wire.CloudActionKindConsole, wire.CloudActionKindSyncIp, "teleport"} {
		_, err := a.Action(context.Background(), core.Action{Ref: ref, Kind: k, RequestID: "x"})
		if coretest.Code(err) != wire.CloudErrorCodeUnsupported {
			t.Fatalf("%s: %v", k, err)
		}
	}
	p := coretest.Provider(a, nil, secrets...)
	_, err := p.Action(context.Background(), wire.CloudActionParams{Instance: ref, Action: wire.CloudActionKindCustom, Custom: "renew", RequestID: "y"})
	if coretest.Code(err) != wire.CloudErrorCodeUnsupported {
		t.Fatalf("custom: %v", err)
	}
	if n := len(f.callList()); n != 0 {
		t.Fatalf("the vendor was asked: %v", f.callList())
	}
}

func TestVendorErrors(t *testing.T) {
	cases := []struct {
		status    int
		code      string
		want      wire.CloudErrorCode
		transient bool
	}{
		{401, "unauthorized", wire.CloudErrorCodeNotSignedIn, false},
		{403, "forbidden", wire.CloudErrorCodeNotSignedIn, false},
		{403, "maintenance", wire.CloudErrorCodeFailed, false},
		{403, "some_future_code", wire.CloudErrorCodeFailed, false},
		{403, "", wire.CloudErrorCodeNotSignedIn, false},
		{404, "not_found", wire.CloudErrorCodeNotFound, false},
		{409, "conflict", wire.CloudErrorCodeInvalidState, false},
		{423, "locked", wire.CloudErrorCodeInvalidState, false},
		{423, "protected", wire.CloudErrorCodeRefused, false},
		{422, "unsupported_error", wire.CloudErrorCodeUnsupported, false},
		{422, "service_error", wire.CloudErrorCodeFailed, false},
		{409, "uniqueness_error", wire.CloudErrorCodeFailed, false},
		{410, "deprecated_api_endpoint", wire.CloudErrorCodeFailed, false},
		{405, "method_not_allowed", wire.CloudErrorCodeFailed, false},
		{429, "rate_limit_exceeded", wire.CloudErrorCodeRateLimited, true},
		{429, "", wire.CloudErrorCodeRateLimited, true},
		{412, "resource_unavailable", wire.CloudErrorCodeCapacity, false},
		{403, "resource_limit_exceeded", wire.CloudErrorCodeCapacity, false},
		{400, "invalid_input", wire.CloudErrorCodeFailed, false},
		{500, "", wire.CloudErrorCodeFailed, false},
		{503, "unavailable", wire.CloudErrorCodeFailed, false},
		{504, "timeout", wire.CloudErrorCodeFailed, false},
		{502, "bad_gateway", wire.CloudErrorCodeFailed, false},
		{500, "server_error", wire.CloudErrorCodeFailed, false},
	}
	for i, c := range cases {
		f, srv := newFake(t)
		f.add(tokenAlpha, server1(42, "web-1", "off", "203.0.113.10"))
		body := fmt.Sprintf(`{"error":{"code":%q,"message":"refused for token %s (Bearer %s)","details":{}}}`, c.code, tokenAlpha, tokenAlpha)
		f.canned["POST /v1/servers/42/actions/poweron"] = canned{c.status, body}
		p := coretest.Provider(newAdapter(t, srv, tokenAlpha), nil, secrets...)
		_, err := p.Action(context.Background(), wire.CloudActionParams{Instance: wire.CloudInstanceRef{Account: "default", ID: "42"},
			Action: wire.CloudActionKindStart, RequestID: wire.CloudRequestID(fmt.Sprintf("e-%d", i))})
		if got := coretest.Code(err); got != c.want {
			t.Errorf("%d %s: got %s (%v)", c.status, c.code, got, err)
			continue
		}
		coretest.NoLeak(t, err.Error(), secrets...)
		// The adapter's own error is already without the token, and only a rate limit is transient.
		_, aerr := newAdapter(t, srv, tokenAlpha).Action(context.Background(), core.Action{Ref: wire.CloudInstanceRef{Account: "default", ID: "42"}, Kind: wire.CloudActionKindStart})
		coretest.NoLeak(t, aerr.Error(), secrets...)
		if got := core.AsError(aerr).Transient; got != c.transient {
			t.Errorf("%d %s: transient %v", c.status, c.code, got)
		}
	}
}

func TestNotFound(t *testing.T) {
	f, srv := newFake(t)
	f.add(tokenAlpha, server1(42, "web-1", "running", "203.0.113.10"))
	p := coretest.Provider(newAdapter(t, srv, tokenAlpha), nil, secrets...)
	for _, ref := range []wire.CloudInstanceRef{{Account: "default", ID: "43"}, {Account: "nobody", ID: "42"}, {Account: "default", ID: "42/../1"},
		{Account: "default", ID: "042"}, {Account: "default", ID: "4/2%2F 1"}} {
		_, err := p.Instance(context.Background(), wire.CloudInstanceParams{Instance: ref})
		if coretest.Code(err) != wire.CloudErrorCodeNotFound {
			t.Fatalf("%v: %v", ref, err)
		}
		_, err = p.Snapshots(context.Background(), wire.CloudSnapshotsParams{Instance: ref})
		if coretest.Code(err) != wire.CloudErrorCodeNotFound {
			t.Fatalf("snapshots %v: %v", ref, err)
		}
	}
	// Only the well-formed id was asked about: an id that cannot be a server's is answered without a request.
	if got := f.count("GET /v1/servers/43"); got != 2 || len(f.callList()) != 2 {
		t.Fatalf("calls %v", f.callList())
	}
	_, err := p.Action(context.Background(), wire.CloudActionParams{Instance: wire.CloudInstanceRef{Account: "default", ID: "4/2%2F 1"},
		Action: wire.CloudActionKindStart, RequestID: "odd-id"})
	if coretest.Code(err) != wire.CloudErrorCodeNotFound || len(f.callList()) != 2 {
		t.Fatalf("%v %v", err, f.callList())
	}
}

func TestLongVendorErrorIsNotClipped(t *testing.T) {
	f, srv := newFake(t)
	f.add(tokenAlpha, server1(42, "web-1", "off", "203.0.113.10"))
	long := strings.Repeat("x", 400) + " end"
	f.canned["POST /v1/servers/42/actions/poweron"] = canned{422, fmt.Sprintf(`{"error":{"code":"invalid_input","message":%q}}`, long)}
	_, err := newAdapter(t, srv, tokenAlpha).Action(context.Background(), core.Action{Ref: wire.CloudInstanceRef{Account: "default", ID: "42"}, Kind: wire.CloudActionKindStart})
	if !strings.HasSuffix(err.Error(), " end") {
		t.Fatalf("clipped: %q", err.Error())
	}
}

func TestChineseWords(t *testing.T) {
	f, srv := newFake(t)
	f.add(tokenAlpha, server1(42, "web-1", "deleting", "203.0.113.10"))
	p := coretest.Provider(newAdapter(t, srv, tokenAlpha), nil, secrets...)
	in, err := p.Instance(context.Background(), wire.CloudInstanceParams{Instance: wire.CloudInstanceRef{Account: "default", ID: "42"}, Locale: "zh-CN"})
	if err != nil {
		t.Fatal(err)
	}
	if want := core.T("zh-CN", "The server is being deleted.", "服务器正在删除。"); in.StateDetail != want || want == "The server is being deleted." {
		t.Fatalf("detail %q", in.StateDetail)
	}
	_, err = p.Instance(context.Background(), wire.CloudInstanceParams{Instance: wire.CloudInstanceRef{Account: "default", ID: "43"}, Locale: "zh-CN"})
	if coretest.Code(err) != wire.CloudErrorCodeNotFound {
		t.Fatalf("%v", err)
	}
	en, err := p.Instance(context.Background(), wire.CloudInstanceParams{Instance: wire.CloudInstanceRef{Account: "default", ID: "42"}, Locale: "en"})
	if err != nil || en.StateDetail != "The server is being deleted." {
		t.Fatalf("%q %v", en.StateDetail, err)
	}
}

func TestStartReadsTheNewAddress(t *testing.T) {
	f, srv := newFake(t)
	f.add(tokenAlpha, server1(42, "web-1", "off", "203.0.113.10"))
	p := coretest.Provider(newAdapter(t, srv, tokenAlpha), nil, secrets...)
	ref := wire.CloudInstanceRef{Account: "default", ID: "42"}
	before, err := p.Instance(context.Background(), wire.CloudInstanceParams{Instance: ref})
	if err != nil || before.PublicIps[0] != "203.0.113.10" || before.State != wire.CloudInstanceStateStopped {
		t.Fatalf("%+v %v", before, err)
	}
	if _, err := p.Action(context.Background(), wire.CloudActionParams{Instance: ref, Action: wire.CloudActionKindStart, RequestID: "start-1"}); err != nil {
		t.Fatal(err)
	}
	after, err := p.Instance(context.Background(), wire.CloudInstanceParams{Instance: ref})
	if err != nil || after.PublicIps[0] != "203.0.113.99" || after.State != wire.CloudInstanceStateRunning {
		t.Fatalf("%+v %v", after, err)
	}
	res, err := p.Action(context.Background(), wire.CloudActionParams{Instance: ref, Action: wire.CloudActionKindSyncIp, RequestID: "sync-1"})
	if err != nil {
		t.Fatal(err)
	}
	coretest.Valid(t, "cloud.action", res)
	if res.Instance == nil || res.Instance.PublicIps[0] != "203.0.113.99" {
		t.Fatalf("sync_ip %+v", res.Instance)
	}
}

func TestSnapshots(t *testing.T) {
	f, srv := newFake(t)
	f.add(tokenAlpha, server1(42, "web-1", "running", "203.0.113.10"))
	size := 1.25
	for i := 0; i < 60; i++ { // two pages: only every tenth image is of server 42
		from := int64(7)
		if i%10 == 0 {
			from = 42
		}
		im := map[string]any{"id": 500 + i, "type": "snapshot", "status": "available", "name": nil,
			"description": fmt.Sprintf("snap-%d", i), "image_size": size, "disk_size": 40,
			"created": fmt.Sprintf("2026-11-%02dT10:00:00+00:00", 1+i%25), "created_from": map[string]any{"id": from, "name": "x"}, "deleted": nil}
		if i == 50 {
			im["status"] = "creating"
			im["image_size"] = nil
		}
		f.images[tokenAlpha] = append(f.images[tokenAlpha], im)
	}
	p := coretest.Provider(newAdapter(t, srv, tokenAlpha), nil, secrets...)
	out, err := p.Snapshots(context.Background(), wire.CloudSnapshotsParams{Instance: wire.CloudInstanceRef{Account: "default", ID: "42"}})
	if err != nil {
		t.Fatal(err)
	}
	coretest.Valid(t, "cloud.snapshots", out)
	if len(out.Snapshots) != 6 {
		t.Fatalf("got %d: %+v", len(out.Snapshots), out.Snapshots)
	}
	for i := 1; i < len(out.Snapshots); i++ {
		if out.Snapshots[i].CreatedAt.After(out.Snapshots[i-1].CreatedAt) {
			t.Fatalf("not newest first: %+v", out.Snapshots)
		}
	}
	states := map[wire.CloudSnapshotState]int{}
	for _, s := range out.Snapshots {
		states[s.State]++
	}
	if states[wire.CloudSnapshotStateReady] != 5 || states[wire.CloudSnapshotStateCreating] != 1 {
		t.Fatalf("states %v", states)
	}
	if f.count("GET /v1/images") != 2 {
		t.Fatalf("calls %v", f.callList())
	}
}
