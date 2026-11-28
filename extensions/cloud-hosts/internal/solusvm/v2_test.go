package solusvm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona-extensions/extensions/cloud-hosts/internal/core"
	"github.com/nwverse/persona-extensions/extensions/cloud-hosts/internal/coretest"
	"github.com/nwverse/persona/sdk/go/wire"
)

const (
	v2TokenMain = "test-token-0001-solusvm2-main"
	v2TokenAlt  = "test-token-0002-solusvm2-alt"
	v2TokenDead = "test-token-0003-solusvm2-dead"
)

var v2Secrets = []string{v2TokenMain, v2TokenAlt, v2TokenDead}

type v2MockServer struct {
	id      int
	name    string
	status  string
	ipv4    []string
	ipv6    string
	susp    bool
	nextIP  string // the first IPv4 after the next start
	failure *v2Failure
}

type v2Failure struct {
	status int
	body   string
}

type v2Mock struct {
	t         *testing.T
	mu        sync.Mutex
	servers   map[string][]*v2MockServer // by token
	calls     []string                   // "METHOD path body"
	snapshots map[int][]string           // JSON snapshot objects by server id
	snapFail  *v2Failure
	perPage   int
}

func (m *v2Mock) count(prefix string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, c := range m.calls {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

func (s *v2MockServer) json() string {
	v4 := []string{}
	for i, ip := range s.ipv4 {
		v4 = append(v4, fmt.Sprintf(`{"id": %d, "ip": %q, "gateway": "203.0.113.1", "netmask": "255.255.255.0", "is_primary": %t}`, i+1, ip, i == 0))
	}
	v6 := "[]"
	if s.ipv6 != "" { // one object, as SolusVM 2 sends it
		v6 = fmt.Sprintf(`{"id": 3, "range": "2001:db8:5::/64", "primary_ip": %q, "gateway": "fe80::1"}`, s.ipv6)
	}
	return fmt.Sprintf(`{"id": %d, "name": %q, "uuid": "00000000-0000-4000-8000-%012d", "status": %q, "is_suspended": %t,
		"virtualization_type": "kvm", "ip_addresses": {"ipv4": [%s], "ipv6": %s},
		"specifications": {"vcpu": 2, "ram": 2147483648, "disk": 40},
		"plan": {"id": 4, "name": "KVM-2G"}, "location": {"id": 1, "name": "Example City"},
		"created_at": "2026-01-02T03:04:05+00:00"}`,
		s.id, s.name, s.id, s.status, s.susp, strings.Join(v4, ","), v6)
}

func (m *v2Mock) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	t := m.t
	w.Header().Set("Content-Type", "application/json")
	body, _ := io.ReadAll(r.Body)
	if r.Header.Get("Accept") != "application/json" {
		t.Errorf("accept %q", r.Header.Get("Accept"))
	}
	if len(body) > 0 && r.Header.Get("Content-Type") != "application/json" {
		t.Errorf("content type %q", r.Header.Get("Content-Type"))
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, strings.TrimSpace(r.Method+" "+r.URL.Path+" "+string(body)))
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	list, known := m.servers[token]
	if !ok || !known {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprintf(w, `{"message": "Unauthenticated. Token %s is not valid."}`, token)
		return
	}
	path, ok := strings.CutPrefix(r.URL.Path, "/api/v1/")
	if !ok {
		http.NotFound(w, r)
		return
	}
	parts := strings.Split(path, "/")
	if parts[0] != "servers" {
		http.NotFound(w, r)
		return
	}
	if len(parts) == 1 && r.Method == http.MethodGet {
		page := 1
		fmt.Sscan(r.URL.Query().Get("page"), &page)
		per := m.perPage
		if per == 0 {
			per = 2
		}
		last := (len(list) + per - 1) / per
		if last == 0 {
			last = 1
		}
		var items []string
		for i := (page - 1) * per; i < page*per && i < len(list); i++ {
			items = append(items, list[i].json())
		}
		next := "null"
		if page < last {
			next = fmt.Sprintf(`"https://panel.example.com/api/v1/servers?page=%d"`, page+1)
		}
		fmt.Fprintf(w, `{"data": [%s], "links": {"first": "https://panel.example.com/api/v1/servers?page=1", "next": %s},
			"meta": {"current_page": %d, "last_page": %d, "per_page": %d, "total": %d}}`,
			strings.Join(items, ","), next, page, last, per, len(list))
		return
	}
	var srv *v2MockServer
	for _, s := range list {
		if len(parts) > 1 && fmt.Sprint(s.id) == parts[1] {
			srv = s
		}
	}
	if srv == nil {
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, `{"message": "No query results for model [App\\Server]."}`)
		return
	}
	if srv.failure != nil && len(parts) == 3 && parts[2] != "snapshots" {
		w.WriteHeader(srv.failure.status)
		fmt.Fprint(w, srv.failure.body)
		return
	}
	switch {
	case len(parts) == 2 && r.Method == http.MethodGet:
		fmt.Fprintf(w, `{"data": %s}`, srv.json())
	case len(parts) == 3 && r.Method == http.MethodPost && parts[2] != "snapshots":
		switch parts[2] {
		case "start":
			srv.status = "starting"
			if srv.nextIP != "" {
				srv.ipv4[0] = srv.nextIP
			}
		case "stop":
			srv.status = "stopping"
		case "restart":
			srv.status = "restarting"
		default:
			http.NotFound(w, r)
			return
		}
		fmt.Fprintf(w, `{"data": {"id": 901, "action": "vm-%s", "status": "pending", "progress": 0, "compute_resource_vm_id": %d}}`, parts[2], srv.id)
	case len(parts) == 3 && parts[2] == "snapshots" && r.Method == http.MethodGet:
		fmt.Fprintf(w, `{"data": [%s], "links": {"next": null}, "meta": {"current_page": 1, "last_page": 1}}`, strings.Join(m.snapshots[srv.id], ","))
	case len(parts) == 3 && parts[2] == "snapshots" && r.Method == http.MethodPost:
		if m.snapFail != nil {
			w.WriteHeader(m.snapFail.status)
			fmt.Fprint(w, m.snapFail.body)
			return
		}
		var req struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(body, &req); err != nil || req.Name == "" {
			t.Errorf("snapshot body %q", body)
		}
		fmt.Fprintf(w, `{"data": {"id": 55, "name": %q, "status": "processing", "size": 0, "created_at": "2026-11-27T08:30:00+00:00"}}`, req.Name)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
		fmt.Fprint(w, `{"message": "The method is not supported for this route."}`)
	}
}

func newV2Mock(t *testing.T) (*v2Mock, *httptest.Server) {
	m := &v2Mock{t: t, servers: map[string][]*v2MockServer{
		v2TokenMain: {
			{id: 101, name: "alpha.example.com", status: "started", ipv4: []string{"203.0.113.21"}, ipv6: "2001:db8:5::2"},
			{id: 102, name: "beta.example.com", status: "stopped", ipv4: []string{"198.51.100.22"}},
			{id: 103, name: "gamma.example.com", status: "started", ipv4: []string{"10.10.0.3", "192.0.2.33"}, susp: true},
		},
		v2TokenAlt: {
			{id: 201, name: "delta.example.org", status: "restarting", ipv4: []string{"192.0.2.44"}},
		},
	}, snapshots: map[int][]string{
		101: {
			`{"id": 7, "name": "before-upgrade", "status": "available", "size": 3.5, "created_at": "2026-11-01T10:00:00+00:00"}`,
			`{"id": 8, "name": "nightly", "status": "processing", "size": 0, "created_at": "2026-11-20 02:00:00"}`,
			`{"id": 9, "name": "broken", "status": "failed", "created_at": "2026-10-01T00:00:00Z"}`,
		},
	}}
	srv := httptest.NewUnstartedServer(m)
	srv.Config.ErrorLog = log.New(io.Discard, "", 0)
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return m, srv
}

func newV2(t *testing.T, srv *httptest.Server, token string) core.Adapter {
	t.Helper()
	a, err := NewV2(V2Config{BaseURL: srv.URL, Token: token, HTTP: srv.Client(), Now: func() time.Time { return fixedNow }})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func v2All() string {
	return "main=" + v2TokenMain + "\nalt=" + v2TokenAlt + "\ndead=" + v2TokenDead
}

func TestV2Config(t *testing.T) {
	for _, bad := range []string{"", " \n# none\n"} {
		_, err := NewV2(V2Config{BaseURL: "https://panel.example.com", Token: bad})
		if coretest.Code(err) != wire.CloudErrorCodeNotSignedIn {
			t.Errorf("%q: %v", bad, err)
		}
	}
	if _, err := NewV2(V2Config{BaseURL: "not a url", Token: v2TokenMain}); err == nil {
		t.Fatal("a bad base was accepted")
	}
	a, err := NewV2(V2Config{BaseURL: "https://panel.example.com", Token: v2All()})
	if err != nil {
		t.Fatal(err)
	}
	if got := a.(core.SecretHolder).SecretValues(); strings.Join(got, ",") != strings.Join(v2Secrets, ",") {
		t.Fatalf("secrets %v", got)
	}
}

func TestV2Instances(t *testing.T) {
	m, srv := newV2Mock(t)
	p := coretest.Provider(newV2(t, srv, v2All()), nil, v2Secrets...)
	list, err := p.Instances(context.Background(), wire.CloudInstancesParams{Locale: "en", Refresh: true})
	if err != nil {
		t.Fatal(err)
	}
	raw := coretest.Valid(t, "cloud.instances", list)
	coretest.NoLeak(t, string(raw), v2Secrets...)
	if !list.Partial || !strings.Contains(list.Detail, "dead") || strings.Contains(list.Detail, "main") || len(list.Instances) != 4 {
		t.Fatalf("list %s", raw)
	}
	if m.count("GET /api/v1/servers") < 3 { // two pages for main, one for alt, one for dead
		t.Errorf("calls %v", m.calls)
	}
	by := map[string]wire.CloudInstance{}
	for _, in := range list.Instances {
		by[string(in.Account)+"/"+string(in.ID)] = in
	}
	alpha := by["main/101"]
	if alpha.State != wire.CloudInstanceStateRunning || alpha.DisplayName != "alpha.example.com" || alpha.Region != "Example City" ||
		strings.Join(alpha.PublicIps, ",") != "203.0.113.21,2001:db8:5::2" {
		t.Errorf("alpha %+v", alpha)
	}
	if alpha.Spec == nil || *alpha.Spec != (wire.CloudSpec{Size: "KVM-2G", Cpu: 2, MemoryMb: 2048, DiskGb: 40}) {
		t.Errorf("alpha spec %+v", alpha.Spec)
	}
	if alpha.ConsoleURL != srv.URL+"/servers/101" {
		t.Errorf("alpha console %q", alpha.ConsoleURL)
	}
	if got := fmt.Sprint(alpha.Actions); got != "[stop reboot force_stop sync_ip snapshot_list snapshot_create console]" {
		t.Errorf("alpha actions %s", got)
	}
	if beta := by["main/102"]; beta.State != wire.CloudInstanceStateStopped || fmt.Sprint(beta.Actions) != "[start sync_ip snapshot_list snapshot_create console]" {
		t.Errorf("beta %+v", beta)
	}
	gamma := by["main/103"]
	if gamma.State != wire.CloudInstanceStateStopped || gamma.StateDetail == "" || fmt.Sprint(gamma.Actions) != "[sync_ip snapshot_list console]" ||
		strings.Join(gamma.PrivateIps, ",") != "10.10.0.3" || strings.Join(gamma.PublicIps, ",") != "192.0.2.33" {
		t.Errorf("gamma %+v", gamma)
	}
	if delta := by["alt/201"]; delta.State != wire.CloudInstanceStateRebooting {
		t.Errorf("delta %+v", delta)
	}

	// Only failing accounts: the error itself.
	_, err = coretest.Provider(newV2(t, srv, v2TokenDead), nil, v2Secrets...).Instances(context.Background(), wire.CloudInstancesParams{Refresh: true})
	if coretest.Code(err) != wire.CloudErrorCodeNotSignedIn {
		t.Fatalf("dead only: %v", err)
	}
	coretest.NoLeak(t, err.Error(), v2Secrets...)
}

func TestV2Paging(t *testing.T) {
	m, srv := newV2Mock(t)
	var many []*v2MockServer
	for i := range 23 {
		many = append(many, &v2MockServer{id: 1000 + i, name: fmt.Sprint("s", i), status: "started", ipv4: []string{fmt.Sprintf("203.0.113.%d", 100+i)}})
	}
	m.servers[v2TokenMain] = many
	m.perPage = 5
	list, _, err := newV2(t, srv, v2TokenMain).Instances(context.Background())
	if err != nil || len(list) != 23 {
		t.Fatalf("%d %v", len(list), err)
	}
	if n := m.count("GET /api/v1/servers"); n != 5 {
		t.Fatalf("pages read %d", n)
	}
}

func TestV2StateMapping(t *testing.T) {
	a := &v2Adapter{console: "https://panel.example.com", now: func() time.Time { return fixedNow }}
	acct := core.NamedToken{Account: "default"}
	for _, tc := range []struct {
		status  string
		susp    bool
		state   wire.CloudInstanceState
		actions string
	}{
		{"started", false, wire.CloudInstanceStateRunning, "[stop reboot force_stop sync_ip snapshot_list snapshot_create console]"},
		{"stopped", false, wire.CloudInstanceStateStopped, "[start sync_ip snapshot_list snapshot_create console]"},
		{"starting", false, wire.CloudInstanceStateStarting, "[force_stop sync_ip snapshot_list console]"},
		{"stopping", false, wire.CloudInstanceStateStopping, "[force_stop sync_ip snapshot_list console]"},
		{"restarting", false, wire.CloudInstanceStateRebooting, "[force_stop sync_ip snapshot_list console]"},
		{"started", true, wire.CloudInstanceStateStopped, "[sync_ip snapshot_list console]"},
		{"deleting", false, wire.CloudInstanceStateTerminated, "[sync_ip]"},
		{"not exists", false, wire.CloudInstanceStateTerminated, "[sync_ip]"},
		{"paused", false, wire.CloudInstanceStateStopped, "[sync_ip snapshot_list console]"},
		{"processing", false, wire.CloudInstanceStateStarting, "[sync_ip snapshot_list console]"},
		{"not created", false, wire.CloudInstanceStateUnknown, "[sync_ip snapshot_list console]"},
		{"unavailable", false, wire.CloudInstanceStateUnknown, "[sync_ip snapshot_list console]"},
		{"", false, wire.CloudInstanceStateUnknown, "[sync_ip snapshot_list console]"},
	} {
		in := a.instance("en", acct, v2Server{ID: "5", Status: tc.status, IsSuspended: tc.susp})
		if in.State != tc.state || fmt.Sprint(in.Actions) != tc.actions {
			t.Errorf("%q/%v: %s %v", tc.status, tc.susp, in.State, in.Actions)
		}
		if (tc.status == "paused" || tc.status == "processing") && in.StateDetail == "" {
			t.Errorf("%q: no detail", tc.status)
		}
	}
	// A task running for it (is_processing): the state stays, the power actions wait.
	for status, want := range map[string]wire.CloudInstanceState{"started": wire.CloudInstanceStateRunning, "stopped": wire.CloudInstanceStateStopped} {
		in := a.instance("en", acct, v2Server{ID: "5", Status: status, IsProcessing: "true"})
		if in.State != want || in.StateDetail == "" || fmt.Sprint(in.Actions) != "[sync_ip snapshot_list console]" {
			t.Errorf("processing %s: %s %v %q", status, in.State, in.Actions, in.StateDetail)
		}
	}
	// The primary IPv4 comes first whatever the panel's order; IPv6 as one object or as a list.
	for raw, want := range map[string]string{
		`{"ipv4": [{"ip": "198.51.100.40", "is_primary": false}, {"ip": "203.0.113.41", "is_primary": true}], "ipv6": {"primary_ip": "2001:db8:1::2", "range": "2001:db8:1::/64"}}`: "203.0.113.41,198.51.100.40,2001:db8:1::2",
		`{"ipv4": [{"ip": "10.0.0.8", "is_primary": 0}, {"ip": "192.0.2.8", "is_primary": 1}], "ipv6": [{"primary_ip": "2001:db8:2::2"}]}`:                                          "192.0.2.8,2001:db8:2::2|10.0.0.8",
		`{"ipv4": [{"ip": "192.0.2.9"}], "ipv6": null}`:                              "192.0.2.9",
		`{"ipv4": [], "ipv6": {"primary_ip": null, "range": "2001:db8:3::/64"}}`:     "",
		`{"ipv4": [{"ip": "192.0.2.10", "is_primary": true}], "ipv6": "unexpected"}`: "192.0.2.10",
	} {
		var srv v2Server
		if err := json.Unmarshal([]byte(`{"id": 7, "status": "started", "ip_addresses": `+raw+`}`), &srv); err != nil {
			t.Fatalf("%s: %v", raw, err)
		}
		in := a.instance("en", acct, srv)
		got := strings.Join(in.PublicIps, ",")
		if len(in.PrivateIps) > 0 {
			got += "|" + strings.Join(in.PrivateIps, ",")
		}
		if got != want {
			t.Errorf("%s: %s, want %s", raw, got, want)
		}
	}
	// Numbers JSON cannot carry are no numbers: the answer still encodes.
	var odd v2Server
	if err := json.Unmarshal([]byte(`{"id": 8, "status": "started", "specifications": {"vcpu": "NaN", "ram": "Inf", "disk": "-Inf"}}`), &odd); err != nil {
		t.Fatal(err)
	}
	if in := a.instance("en", acct, odd); in.Spec != nil {
		t.Errorf("non-finite spec %+v", in.Spec)
	}
	var snap v2Snapshot
	if err := json.Unmarshal([]byte(`{"id": 1, "name": "x", "status": "available", "size": "NaN"}`), &snap); err != nil {
		t.Fatal(err)
	}
	if s := snap.snapshot(); s.SizeGb != 0 {
		t.Errorf("NaN size %v", s.SizeGb)
	} else if _, err := json.Marshal(s); err != nil {
		t.Errorf("encode: %v", err)
	}
	// IPv6 given as a range only: its address is taken when it is a plain one.
	var s v2Server
	if err := json.Unmarshal([]byte(`{"id": "6", "status": "started", "ip_addresses": {"ipv4": [], "ipv6": [{"range": "2001:db8:9::/64"}, {"range": "2001:db8:a::1"}]}}`), &s); err != nil {
		t.Fatal(err)
	}
	if in := a.instance("en", acct, s); strings.Join(in.PublicIps, ",") != "2001:db8:a::1" {
		t.Errorf("v6 range %v", in.PublicIps)
	}
}

func TestV2Actions(t *testing.T) {
	m, srv := newV2Mock(t)
	p := coretest.Provider(newV2(t, srv, v2All()), nil, v2Secrets...)
	ctx := context.Background()
	alpha := wire.CloudInstanceRef{Account: "main", ID: "101"}
	beta := wire.CloudInstanceRef{Account: "main", ID: "102"}
	for i, tc := range []struct {
		ref  wire.CloudInstanceRef
		kind wire.CloudActionKind
		call string
	}{
		{alpha, wire.CloudActionKindReboot, "POST /api/v1/servers/101/restart"},
		{alpha, wire.CloudActionKindStop, "POST /api/v1/servers/101/stop"},
		{alpha, wire.CloudActionKindForceStop, `POST /api/v1/servers/101/stop {"force":true}`},
		{beta, wire.CloudActionKindStart, "POST /api/v1/servers/102/start"},
	} {
		res, err := p.Action(ctx, wire.CloudActionParams{Instance: tc.ref, Action: tc.kind, RequestID: wire.CloudRequestID(fmt.Sprint("r", i))})
		if err != nil {
			t.Fatalf("%s: %v", tc.kind, err)
		}
		raw := coretest.Valid(t, "cloud.action", res)
		coretest.NoLeak(t, string(raw), v2Secrets...)
		m.mu.Lock()
		last := m.calls[len(m.calls)-1]
		m.mu.Unlock()
		if res.Status != wire.CloudActionStatusAccepted || last != tc.call {
			t.Fatalf("%s: %s, call %q", tc.kind, raw, last)
		}
	}
	// Idempotency through the provider.
	for range 3 {
		if _, err := p.Action(ctx, wire.CloudActionParams{Instance: beta, Action: wire.CloudActionKindStart, RequestID: "same"}); err != nil {
			t.Fatal(err)
		}
	}
	if n := m.count("POST /api/v1/servers/102/start"); n != 2 {
		t.Fatalf("start calls %d, want 2", n)
	}
	// Snapshots.
	res, err := p.Action(ctx, wire.CloudActionParams{Instance: alpha, Action: wire.CloudActionKindSnapshotCreate, Name: "pre-deploy", RequestID: "snap-1"})
	if err != nil {
		t.Fatal(err)
	}
	coretest.Valid(t, "cloud.action", res)
	if res.Status != wire.CloudActionStatusAccepted || res.Snapshot == nil || res.Snapshot.ID != "55" || res.Snapshot.Name != "pre-deploy" ||
		res.Snapshot.State != wire.CloudSnapshotStateCreating || m.count(`POST /api/v1/servers/101/snapshots {"name":"pre-deploy"}`) != 1 {
		t.Fatalf("snapshot_create %+v %v", res, m.calls)
	}
	if _, err := p.Action(ctx, wire.CloudActionParams{Instance: alpha, Action: wire.CloudActionKindSnapshotCreate, RequestID: "snap-2"}); err != nil {
		t.Fatal(err)
	}
	if m.count(`POST /api/v1/servers/101/snapshots {"name":"persona-20261127-083000"}`) != 1 {
		t.Fatalf("default snapshot name: %v", m.calls)
	}
	snaps, err := p.Snapshots(ctx, wire.CloudSnapshotsParams{Instance: alpha})
	if err != nil {
		t.Fatal(err)
	}
	raw := coretest.Valid(t, "cloud.snapshots", snaps)
	if len(snaps.Snapshots) != 3 || snaps.Snapshots[0].ID != "8" || snaps.Snapshots[0].State != wire.CloudSnapshotStateCreating ||
		snaps.Snapshots[1].ID != "7" || snaps.Snapshots[1].State != wire.CloudSnapshotStateReady || snaps.Snapshots[1].SizeGb != 3.5 ||
		snaps.Snapshots[2].State != wire.CloudSnapshotStateFailed {
		t.Fatalf("snapshots %s", raw)
	}
	for _, kind := range []wire.CloudActionKind{wire.CloudActionKindHardReset} {
		if _, err := p.Action(ctx, wire.CloudActionParams{Instance: alpha, Action: kind, RequestID: "u1"}); coretest.Code(err) != wire.CloudErrorCodeUnsupported {
			t.Errorf("%s: %v", kind, err)
		}
	}
	if _, err := p.Action(ctx, wire.CloudActionParams{Instance: alpha, Action: wire.CloudActionKindCustom, Custom: "renew", RequestID: "u2"}); coretest.Code(err) != wire.CloudErrorCodeUnsupported {
		t.Errorf("custom: %v", err)
	}
	// Console: the server's page at the panel.
	res, err = p.Action(ctx, wire.CloudActionParams{Instance: alpha, Action: wire.CloudActionKindConsole, RequestID: "c1"})
	if err != nil {
		t.Fatal(err)
	}
	raw = coretest.Valid(t, "cloud.action", res)
	coretest.NoLeak(t, string(raw), v2Secrets...)
	u, _ := url.Parse(res.URL)
	if u.Scheme != "https" || u.User != nil || u.RawQuery != "" || u.Path != "/servers/101" {
		t.Fatalf("console %s", raw)
	}
}

func TestV2IPAfterStart(t *testing.T) {
	m, srv := newV2Mock(t)
	m.servers[v2TokenMain][1].nextIP = "198.51.100.200"
	p := coretest.Provider(newV2(t, srv, v2All()), nil, v2Secrets...)
	ctx := context.Background()
	ref := wire.CloudInstanceRef{Account: "main", ID: "102"}
	if in, err := p.Instance(ctx, wire.CloudInstanceParams{Instance: ref}); err != nil || in.PublicIps[0] != "198.51.100.22" {
		t.Fatalf("before %+v %v", in, err)
	}
	if _, err := p.Action(ctx, wire.CloudActionParams{Instance: ref, Action: wire.CloudActionKindStart, RequestID: "go"}); err != nil {
		t.Fatal(err)
	}
	in, err := p.Instance(ctx, wire.CloudInstanceParams{Instance: ref})
	if err != nil {
		t.Fatal(err)
	}
	coretest.Valid(t, "cloud.instance", in)
	if in.State != wire.CloudInstanceStateStarting || in.PublicIps[0] != "198.51.100.200" {
		t.Fatalf("after %+v", in)
	}
	res, err := p.Action(ctx, wire.CloudActionParams{Instance: ref, Action: wire.CloudActionKindSyncIp, RequestID: "sync"})
	if err != nil || res.Instance == nil || res.Instance.PublicIps[0] != "198.51.100.200" {
		t.Fatalf("sync_ip %+v %v", res, err)
	}
}

func TestV2Errors(t *testing.T) {
	m, srv := newV2Mock(t)
	p := coretest.Provider(newV2(t, srv, v2All()), nil, v2Secrets...)
	ctx := context.Background()
	check := func(name string, err error, want wire.CloudErrorCode) {
		t.Helper()
		if coretest.Code(err) != want {
			t.Errorf("%s: %v, want %s", name, err, want)
		}
		if err != nil {
			coretest.NoLeak(t, err.Error(), v2Secrets...)
		}
	}
	_, err := p.Instance(ctx, wire.CloudInstanceParams{Instance: wire.CloudInstanceRef{Account: "main", ID: "999"}})
	check("missing server", err, wire.CloudErrorCodeNotFound)
	_, err = p.Instance(ctx, wire.CloudInstanceParams{Instance: wire.CloudInstanceRef{Account: "main", ID: "../users"}})
	check("not an id", err, wire.CloudErrorCodeNotFound)
	_, err = p.Instance(ctx, wire.CloudInstanceParams{Instance: wire.CloudInstanceRef{Account: "nobody", ID: "101"}})
	check("unknown account", err, wire.CloudErrorCodeNotFound)
	_, err = p.Instance(ctx, wire.CloudInstanceParams{Instance: wire.CloudInstanceRef{Account: "dead", ID: "101"}})
	check("bad token echoed", err, wire.CloudErrorCodeNotSignedIn)

	alpha := m.servers[v2TokenMain][0]
	for i, tc := range []struct {
		status int
		body   string
		want   wire.CloudErrorCode
	}{
		{422, `{"message": "Server is already started."}`, wire.CloudErrorCodeInvalidState},
		{422, `{"message": "The given data was invalid.", "errors": {"server": ["The server is in status processing."]}}`, wire.CloudErrorCodeInvalidState},
		{409, `{"message": "Another task is running for this server."}`, wire.CloudErrorCodeInvalidState},
		{429, `{"message": "Too Many Attempts."}`, wire.CloudErrorCodeRateLimited},
		{403, `{"message": "This action is unauthorized."}`, wire.CloudErrorCodeNotSignedIn},
		{422, `{"message": "Not enough resources on the compute resource."}`, wire.CloudErrorCodeCapacity},
		{422, `{"message": "Operation forbidden by the administrator."}`, wire.CloudErrorCodeRefused},
		{500, `{"message": "Server Error with ` + v2TokenMain + `"}`, wire.CloudErrorCodeFailed},
		{502, `<html>bad gateway</html>`, wire.CloudErrorCodeFailed},
	} {
		m.mu.Lock()
		alpha.failure = &v2Failure{tc.status, tc.body}
		m.mu.Unlock()
		_, err := p.Action(ctx, wire.CloudActionParams{Instance: wire.CloudInstanceRef{Account: "main", ID: "101"},
			Action: wire.CloudActionKindStop, RequestID: wire.CloudRequestID(fmt.Sprint("e", i))})
		check(tc.body, err, tc.want)
	}
	m.mu.Lock()
	alpha.failure = nil
	m.snapFail = &v2Failure{422, `{"message": "Snapshot limit reached for this server."}`}
	m.mu.Unlock()
	_, err = p.Action(ctx, wire.CloudActionParams{Instance: wire.CloudInstanceRef{Account: "main", ID: "101"},
		Action: wire.CloudActionKindSnapshotCreate, RequestID: "snap-full"})
	check("snapshot limit", err, wire.CloudErrorCodeCapacity)
	// Field validation is refused, not a state, even in words like "already" or "is not".
	for i, body := range []string{
		`{"message": "The given data was invalid.", "errors": {"name": ["The name has already been taken."]}}`,
		`{"message": "The given data was invalid.", "errors": {"plan": ["The selected plan is not valid."]}}`,
	} {
		m.mu.Lock()
		m.snapFail = &v2Failure{422, body}
		m.mu.Unlock()
		_, err = p.Action(ctx, wire.CloudActionParams{Instance: wire.CloudInstanceRef{Account: "main", ID: "101"},
			Action: wire.CloudActionKindSnapshotCreate, Name: "dup", RequestID: wire.CloudRequestID(fmt.Sprint("snap-v", i))})
		check(body, err, wire.CloudErrorCodeRefused)
	}
}
