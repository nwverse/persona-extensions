package pterodactyl

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona-extensions/extensions/cloud-hosts/internal/core"
	"github.com/nwverse/persona-extensions/extensions/cloud-hosts/internal/coretest"
	"github.com/nwverse/persona/sdk/go/wire"
)

// Synthetic keys only.
const (
	keyAlpha  = "ptlc_testEXAMPLEalpha0000000000000000000001"
	keyBeta   = "ptlc_testEXAMPLEbeta00000000000000000000002"
	keyBroken = "ptlc_testEXAMPLEbroken000000000000000000003"
)

var fixedNow = time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)

// mockServer is one server on the fake panel.
type mockServer struct {
	id, name, node string
	status         string // "" (null), installing, suspended, ...
	suspended      bool
	transferring   bool
	state          string // current_state, "" makes /resources fail with 502
	statsStatus    int    // when set, /resources answers this status
	ip, alias      string
	backupsLimit   int
	cpu, mem, disk int
	backups        []map[string]any
	pelican        bool     // a Pelican panel: a failed reinstallation also counts as not installed
	perms          []string // the key's user_permissions on the server (nil: "*", the owner)
	// onStart changes the server when it gets the start signal (a new address, say).
	onStart func(*mockServer)
}

type fail struct {
	status int
	body   string
}

// panel is a fake Pterodactyl panel: it checks every request's method, path, query and headers.
type panel struct {
	t       *testing.T
	mu      sync.Mutex
	keys    map[string]string // key -> account
	servers map[string][]*mockServer
	perPage int
	// fails answers a request ("METHOD path", or "account" for every request of that key) with an error.
	fails map[string]fail
	calls []string // "account METHOD path body"
}

func newPanel(t *testing.T) *panel {
	return &panel{t: t, keys: map[string]string{keyAlpha: "alpha", keyBeta: "beta", keyBroken: "broken"},
		servers: map[string][]*mockServer{}, perPage: 100, fails: map[string]fail{}}
}

func (p *panel) count(prefix string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	n := 0
	for _, c := range p.calls {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

func (p *panel) find(account, id string) *mockServer {
	for _, s := range p.servers[account] {
		if s.id == id {
			return s
		}
	}
	return nil
}

func writeErr(w http.ResponseWriter, status int, code, detail string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"errors": []map[string]any{{"code": code, "status": strconv.Itoa(status), "detail": detail}}})
}

// installing is the panel's is_installing: !isInstalled(), which is also true for a failed installation (Pterodactyl
// and Pelican) and, on Pelican, for a failed reinstallation.
func (s *mockServer) installing() bool {
	return s.status == "installing" || s.status == "install_failed" || (s.pelican && s.status == "reinstall_failed")
}

func (s *mockServer) attributes() map[string]any {
	var status any
	if s.status != "" {
		status = s.status
	}
	return map[string]any{
		"server_owner": true, "identifier": s.id, "internal_id": 7, "uuid": s.id + "-0000-4000-8000-000000000000",
		"name": s.name, "node": s.node, "is_node_under_maintenance": false,
		"sftp_details": map[string]any{"ip": "sftp.panel.example.com", "port": 2022},
		"description":  "", "limits": map[string]any{"memory": s.mem, "swap": 0, "disk": s.disk, "io": 500, "cpu": s.cpu, "threads": nil, "oom_disabled": true},
		"invocation": "java -jar server.jar", "docker_image": "registry.example.com/java:21",
		"feature_limits": map[string]any{"databases": 0, "allocations": 1, "backups": s.backupsLimit},
		"status":         status, "is_suspended": s.suspended, "is_installing": s.installing(), "is_transferring": s.transferring,
		"relationships": map[string]any{"allocations": map[string]any{"object": "list", "data": []any{
			map[string]any{"object": "allocation", "attributes": map[string]any{"id": 1, "ip": "198.51.100.200", "ip_alias": nil, "port": 25566, "notes": nil, "is_default": false}},
			map[string]any{"object": "allocation", "attributes": map[string]any{"id": 2, "ip": s.ip, "ip_alias": nilIfEmpty(s.alias), "port": 25565, "notes": nil, "is_default": true}},
		}}},
	}
}

func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func pageMeta(total, page, per int) map[string]any {
	pages := (total + per - 1) / per
	if pages == 0 {
		pages = 1
	}
	return map[string]any{"pagination": map[string]any{"total": total, "count": 0, "per_page": per, "current_page": page, "total_pages": pages, "links": map[string]any{}}}
}

func (p *panel) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	t := p.t
	if got := r.Header.Get("Accept"); got != "Application/vnd.pterodactyl.v1+json" {
		t.Errorf("%s %s: Accept %q", r.Method, r.URL.Path, got)
	}
	tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	account := p.keys[tok]
	if !ok || account == "" {
		writeErr(w, 401, "AuthenticationException", "Unauthenticated.")
		return
	}
	body, _ := io.ReadAll(r.Body)
	if r.Method == http.MethodPost && r.Header.Get("Content-Type") != "application/json" {
		t.Errorf("POST %s: Content-Type %q", r.URL.Path, r.Header.Get("Content-Type"))
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls = append(p.calls, strings.TrimSpace(account+" "+r.Method+" "+r.URL.Path+" "+string(body)))
	if f, ok := p.fails[account]; ok {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(f.status)
		_, _ = io.WriteString(w, f.body)
		return
	}
	if f, ok := p.fails[r.Method+" "+r.URL.Path]; ok {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(f.status)
		_, _ = io.WriteString(w, f.body)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	path := r.URL.Path
	if path == "/api/client" {
		if r.Method != http.MethodGet {
			t.Errorf("%s /api/client", r.Method)
		}
		if r.URL.Query().Get("per_page") != "100" {
			t.Errorf("list per_page %q", r.URL.Query().Get("per_page"))
		}
		page, err := strconv.Atoi(r.URL.Query().Get("page"))
		if err != nil || page < 1 {
			t.Errorf("list page %q", r.URL.Query().Get("page"))
			page = 1
		}
		all := p.servers[account]
		var data []any
		for i := (page - 1) * p.perPage; i < len(all) && i < page*p.perPage; i++ {
			data = append(data, map[string]any{"object": "server", "attributes": all[i].attributes()})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": data, "meta": pageMeta(len(all), page, p.perPage)})
		return
	}
	rest, ok := strings.CutPrefix(path, "/api/client/servers/")
	if !ok {
		writeErr(w, 404, "NotFoundHttpException", "The requested resource could not be found on the server.")
		return
	}
	id, sub, _ := strings.Cut(rest, "/")
	s := p.find(account, id)
	if s == nil {
		writeErr(w, 404, "NotFoundHttpException", "The requested resource could not be found on the server.")
		return
	}
	conflict := ""
	switch {
	case s.suspended:
		conflict = "This server is currently suspended and the functionality requested is unavailable."
	case s.installing():
		conflict = "This server has not yet completed its installation process, please try again later."
	case s.transferring:
		conflict = "This server is currently being transferred to a new machine, please try again later."
	}
	perms := s.perms
	if perms == nil {
		perms = []string{"*"}
	}
	switch {
	case sub == "" && r.Method == http.MethodGet:
		_ = json.NewEncoder(w).Encode(map[string]any{"object": "server", "attributes": s.attributes(), "meta": map[string]any{"is_server_owner": s.perms == nil, "user_permissions": perms}})
	case sub == "resources" && r.Method == http.MethodGet:
		if conflict != "" && !(s.suspended) {
			writeErr(w, 409, "ConflictHttpException", conflict)
			return
		}
		if s.statsStatus != 0 {
			writeErr(w, s.statsStatus, "HttpException", "An error was encountered while processing this request.")
			return
		}
		if s.state == "" {
			writeErr(w, 502, "DaemonConnectionException", "")
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"object": "stats", "attributes": map[string]any{
			"current_state": s.state, "is_suspended": s.suspended,
			"resources": map[string]any{"memory_bytes": 588701696, "cpu_absolute": 0.5, "disk_bytes": 190554380, "network_rx_bytes": 694, "network_tx_bytes": 337, "uptime": 0}}})
	case sub == "power" && r.Method == http.MethodPost:
		if conflict != "" {
			writeErr(w, 409, "ConflictHttpException", conflict)
			return
		}
		var req struct {
			Signal string `json:"signal"`
		}
		if err := json.Unmarshal(body, &req); err != nil || !slices.Contains([]string{"start", "stop", "restart", "kill"}, req.Signal) {
			t.Errorf("power body %q", body)
			writeErr(w, 422, "ValidationException", "The selected signal is invalid.")
			return
		}
		if req.Signal == "start" {
			s.state = "starting"
			if s.onStart != nil {
				s.onStart(s)
			}
		}
		w.WriteHeader(http.StatusNoContent)
	case sub == "backups" && r.Method == http.MethodGet:
		if r.URL.Query().Get("per_page") != "50" {
			t.Errorf("backups per_page %q", r.URL.Query().Get("per_page"))
		}
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		if page < 1 {
			t.Errorf("backups page %q", r.URL.Query().Get("page"))
			page = 1
		}
		per := 2
		var data []any
		for i := (page - 1) * per; i < len(s.backups) && i < page*per; i++ {
			data = append(data, map[string]any{"object": "backup", "attributes": s.backups[i]})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": data, "meta": pageMeta(len(s.backups), page, per)})
	case sub == "backups" && r.Method == http.MethodPost:
		if len(s.backups) >= s.backupsLimit {
			writeErr(w, 400, "TooManyBackupsException", fmt.Sprintf("Cannot create a new backup, this server has reached its limit of %d backups.", s.backupsLimit))
			return
		}
		var req map[string]any
		_ = json.Unmarshal(body, &req)
		name, _ := req["name"].(string)
		if name == "" {
			name = "Backup at 2026-11-01 12:00:00"
		}
		b := map[string]any{"uuid": fmt.Sprintf("b%07d-0000-4000-8000-000000000000", len(s.backups)+1), "is_successful": false, "is_locked": false,
			"name": name, "ignored_files": []string{}, "checksum": nil, "bytes": 0, "created_at": "2026-11-01T12:00:00+00:00", "completed_at": nil}
		s.backups = append(s.backups, b)
		_ = json.NewEncoder(w).Encode(map[string]any{"object": "backup", "attributes": b})
	default:
		t.Errorf("unexpected %s %s", r.Method, path)
		writeErr(w, 405, "MethodNotAllowedHttpException", "")
	}
}

// start serves p over TLS and makes an adapter for keys.
func start(t *testing.T, p *panel, apiKey string) (core.Adapter, *httptest.Server) {
	t.Helper()
	srv := httptest.NewUnstartedServer(p)
	srv.Config.ErrorLog = log.New(io.Discard, "", 0)
	srv.StartTLS()
	t.Cleanup(srv.Close)
	a, err := New(Config{BaseURL: srv.URL, APIKey: apiKey, HTTP: srv.Client(), Now: func() time.Time { return fixedNow }})
	if err != nil {
		t.Fatal(err)
	}
	return a, srv
}

var allKeys = []string{keyAlpha, keyBeta, keyBroken}

func provider(a core.Adapter) *core.Provider { return coretest.Provider(a, nil, allKeys...) }

func ref(account, id string) wire.CloudInstanceRef {
	return wire.CloudInstanceRef{Account: wire.CloudAccountID(account), ID: wire.CloudInstanceID(id)}
}

func asJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func TestNewRejectsUnusableSetup(t *testing.T) {
	cases := map[string]Config{
		"no address":      {APIKey: keyAlpha},
		"not http":        {BaseURL: "ftp://panel.example.com", APIKey: keyAlpha},
		"credentials":     {BaseURL: "https://user:pw@panel.example.com", APIKey: keyAlpha},
		"no key":          {BaseURL: "https://panel.example.com", APIKey: "  \n# comment\n"},
		"application key": {BaseURL: "https://panel.example.com", APIKey: "ptla_testEXAMPLEapplication00000000000"},
		"pelican app key": {BaseURL: "https://panel.example.com", APIKey: "papp_testEXAMPLEapplication00000000000"},
		"bad characters":  {BaseURL: "https://panel.example.com", APIKey: "one=ptlc_test EXAMPLE\ntwo=" + keyBeta},
	}
	for name, c := range cases {
		_, err := New(c)
		if coretest.Code(err) != wire.CloudErrorCodeNotSignedIn {
			t.Errorf("%s: %v (%s)", name, err, coretest.Code(err))
		}
		coretest.NoLeak(t, errText(err), keyAlpha, keyBeta, "ptla_testEXAMPLEapplication00000000000", "papp_testEXAMPLEapplication00000000000", "ptlc_test EXAMPLE")
		if strings.Contains(name, "key") && !strings.Contains(errText(err), "pacc_") {
			t.Errorf("%s: the message does not name Pelican's account keys: %v", name, err)
		}
	}
	// A Pelican account key is a client key.
	if _, err := New(Config{BaseURL: "https://panel.example.com", APIKey: "pacc_testEXAMPLEaccount00000000000000"}); err != nil {
		t.Fatalf("pacc_ key: %v", err)
	}
	a, err := New(Config{BaseURL: "https://panel.example.com/api/client/", APIKey: keyAlpha})
	if err != nil {
		t.Fatal(err)
	}
	ad := a.(*adapter)
	if ad.base != "https://panel.example.com" || ad.console != "https://panel.example.com" || ad.accounts[0].Account != "default" {
		t.Fatalf("base %q console %q accounts %v", ad.base, ad.console, ad.accounts[0].Account)
	}
	if !slices.Equal(ad.SecretValues(), []string{keyAlpha}) {
		t.Fatal("secret values")
	}
	if strings.Contains(fmt.Sprint(a), keyAlpha) {
		t.Fatal("String leaks the key")
	}
}

func TestInstancesAcrossAccounts(t *testing.T) {
	p := newPanel(t)
	p.perPage = 2 // three servers on two pages
	p.servers["alpha"] = []*mockServer{
		{id: "a1b2c3d4", name: "Survival", node: "node-eu-1", state: "running", ip: "0.0.0.0", alias: "203.0.113.10", backupsLimit: 3, cpu: 200, mem: 4096, disk: 10240},
		{id: "a1b2c3d5", name: "Creative", node: "node-eu-1", state: "offline", ip: "10.0.0.5", alias: "panel-node.example.com", cpu: 0, mem: 0, disk: 0},
		{id: "a1b2c3d6", name: "Modded", node: "node-eu-2", suspended: true, state: "offline", ip: "198.51.100.7"},
	}
	p.servers["beta"] = []*mockServer{{id: "b0000001", name: "Proxy", node: "node-us-1", state: "starting", ip: "2001:db8::10"}}
	p.fails["broken"] = fail{401, `{"errors":[{"code":"AuthenticationException","status":"401","detail":"Unauthenticated: ` + keyBroken + `"}]}`}
	a, srv := start(t, p, "alpha="+keyAlpha+"\nbeta="+keyBeta+"\nbroken="+keyBroken)

	list, problems, err := a.Instances(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(problems, []string{"broken"}) {
		t.Fatalf("problems %v", problems)
	}
	if len(list) != 4 {
		t.Fatalf("%d instances: %s", len(list), asJSON(list))
	}
	if p.count("alpha GET /api/client") != 2+3-1 { // two list pages, two resources calls (the suspended one is not asked)
		t.Fatalf("calls %v", p.calls)
	}
	byID := map[wire.CloudInstanceID]wire.CloudInstance{}
	for _, in := range list {
		byID[in.ID] = in
		if !in.ObservedAt.Equal(fixedNow) {
			t.Errorf("%s observed %v", in.ID, in.ObservedAt)
		}
	}
	s := byID["a1b2c3d4"]
	if s.Account != "alpha" || s.DisplayName != "Survival" || s.Region != "node-eu-1" || s.State != wire.CloudInstanceStateRunning ||
		!slices.Equal(s.PublicIps, []string{"203.0.113.10"}) || len(s.PrivateIps) != 0 ||
		*s.Spec != (wire.CloudSpec{Cpu: 2, MemoryMb: 4096, DiskGb: 10}) || s.ConsoleURL != srv.URL+"/server/a1b2c3d4" ||
		!slices.Equal(s.Actions, []wire.CloudActionKind{"stop", "reboot", "force_stop", "sync_ip", "snapshot_list", "snapshot_create", "console"}) {
		t.Errorf("running: %s", asJSON(s))
	}
	c := byID["a1b2c3d5"]
	if c.State != wire.CloudInstanceStateStopped || c.Spec != nil || len(c.PublicIps) != 0 || !slices.Equal(c.PrivateIps, []string{"10.0.0.5"}) ||
		!slices.Equal(c.Actions, []wire.CloudActionKind{"start", "sync_ip", "console"}) {
		t.Errorf("offline: %s", asJSON(c))
	}
	m := byID["a1b2c3d6"]
	if m.State != wire.CloudInstanceStateStopped || !strings.Contains(m.StateDetail, "suspended") ||
		!slices.Equal(m.Actions, []wire.CloudActionKind{"sync_ip", "console"}) {
		t.Errorf("suspended: %s", asJSON(m))
	}
	b := byID["b0000001"]
	if b.Account != "beta" || b.State != wire.CloudInstanceStateStarting || !slices.Equal(b.PublicIps, []string{"2001:db8::10"}) {
		t.Errorf("beta: %s", asJSON(b))
	}

	// Through the provider: partial, the broken account named, schema-valid, no key anywhere.
	out, err := provider(a).Instances(context.Background(), wire.CloudInstancesParams{Contribution: "test", Locale: "en", Refresh: true})
	if err != nil {
		t.Fatal(err)
	}
	raw := coretest.Valid(t, "cloud.instances", out)
	coretest.NoLeak(t, string(raw), allKeys...)
	if !out.Partial || !strings.Contains(out.Detail, "broken") || len(out.Instances) != 4 {
		t.Fatalf("list %s", raw)
	}
}

func TestInstancesAllAccountsFail(t *testing.T) {
	p := newPanel(t)
	p.fails["alpha"] = fail{401, `{"errors":[{"code":"AuthenticationException","status":"401","detail":"Unauthenticated."}]}`}
	a, _ := start(t, p, keyAlpha)
	_, err := provider(a).Instances(context.Background(), wire.CloudInstancesParams{Refresh: true})
	if coretest.Code(err) != wire.CloudErrorCodeNotSignedIn {
		t.Fatalf("%v", err)
	}
}

func TestStateMapping(t *testing.T) {
	cases := []struct {
		srv     mockServer
		state   wire.CloudInstanceState
		detail  string
		actions []wire.CloudActionKind
	}{
		{mockServer{state: "running"}, "running", "", []wire.CloudActionKind{"stop", "reboot", "force_stop", "sync_ip", "console"}},
		{mockServer{state: "starting"}, "starting", "", []wire.CloudActionKind{"stop", "reboot", "force_stop", "sync_ip", "console"}},
		{mockServer{state: "stopping"}, "stopping", "", []wire.CloudActionKind{"force_stop", "sync_ip", "console"}},
		{mockServer{state: "offline", backupsLimit: 2}, "stopped", "", []wire.CloudActionKind{"start", "sync_ip", "snapshot_list", "snapshot_create", "console"}},
		{mockServer{state: "offline", suspended: true, status: "suspended", backupsLimit: 2}, "stopped", "suspended", []wire.CloudActionKind{"sync_ip", "console"}},
		{mockServer{status: "installing"}, "unknown", "installed", []wire.CloudActionKind{"sync_ip", "console"}},
		{mockServer{state: "stopped"}, "stopped", "", []wire.CloudActionKind{"start", "sync_ip", "console"}},
		{mockServer{state: "exited"}, "stopped", "", []wire.CloudActionKind{"start", "sync_ip", "console"}},
		{mockServer{status: "install_failed", state: "offline"}, "unknown", "installation failed", []wire.CloudActionKind{"sync_ip", "console"}},
		{mockServer{status: "reinstall_failed", pelican: true, state: "offline"}, "unknown", "reinstallation failed", []wire.CloudActionKind{"sync_ip", "console"}},
		{mockServer{status: "restoring_backup", state: "offline"}, "unknown", "restored", []wire.CloudActionKind{"sync_ip", "console"}},
		{mockServer{transferring: true, state: "running"}, "unknown", "moved", []wire.CloudActionKind{"sync_ip", "console"}},
		{mockServer{status: "reinstall_failed", state: "offline"}, "stopped", "reinstallation failed", []wire.CloudActionKind{"start", "sync_ip", "console"}},
		// The daemon is down: the state is unknown and the panel decides on a power action.
		{mockServer{state: ""}, "unknown", "could not say", []wire.CloudActionKind{"start", "stop", "reboot", "force_stop", "sync_ip", "console"}},
		{mockServer{state: "weird"}, "unknown", "does not know", []wire.CloudActionKind{"start", "stop", "reboot", "force_stop", "sync_ip", "console"}},
		{mockServer{state: "running", statsStatus: 409}, "unknown", "does not allow", []wire.CloudActionKind{"sync_ip", "console"}},
	}
	for i, c := range cases {
		p := newPanel(t)
		s := c.srv
		s.id, s.name, s.ip = "c0ffee01", "Case", "192.0.2.50"
		p.servers["alpha"] = []*mockServer{&s}
		a, _ := start(t, p, keyAlpha)
		prov := provider(a)
		in, err := prov.Instance(context.Background(), wire.CloudInstanceParams{Instance: ref("default", "c0ffee01")})
		if err != nil {
			t.Fatalf("case %d: %v", i, err)
		}
		coretest.Valid(t, "cloud.instance", in)
		if in.State != c.state || !strings.Contains(in.StateDetail, c.detail) || !slices.Equal(in.Actions, c.actions) {
			t.Errorf("case %d: %s", i, asJSON(in))
		}
		list, err := prov.Instances(context.Background(), wire.CloudInstancesParams{Refresh: true})
		if err != nil || len(list.Instances) != 1 {
			t.Fatalf("case %d list: %v %s", i, err, asJSON(list))
		}
		if got := list.Instances[0]; got.State != c.state || !slices.Equal(got.Actions, c.actions) {
			t.Errorf("case %d list: %s", i, asJSON(got))
		}
	}
}

func TestPowerActions(t *testing.T) {
	p := newPanel(t)
	p.servers["alpha"] = []*mockServer{{id: "a1b2c3d4", name: "Survival", state: "running", ip: "203.0.113.10"}}
	a, _ := start(t, p, keyAlpha)
	prov := provider(a)
	for kind, signal := range map[wire.CloudActionKind]string{"start": "start", "stop": "stop", "reboot": "restart", "force_stop": "kill"} {
		params := wire.CloudActionParams{Instance: ref("default", "a1b2c3d4"), Action: kind, RequestID: wire.CloudRequestID("req-" + string(kind))}
		want := `alpha POST /api/client/servers/a1b2c3d4/power {"signal":"` + signal + `"}`
		before := p.count(want)
		for range 2 { // the same request_id twice: the panel hears it once
			res, err := prov.Action(context.Background(), params)
			if err != nil {
				t.Fatalf("%s: %v", kind, err)
			}
			coretest.Valid(t, "cloud.action", res)
			if res.Status != wire.CloudActionStatusAccepted {
				t.Fatalf("%s: %s", kind, asJSON(res))
			}
		}
		if p.count(want)-before != 1 {
			t.Fatalf("%s: %v", kind, p.calls)
		}
	}
}

func TestUnsupportedActions(t *testing.T) {
	p := newPanel(t)
	p.servers["alpha"] = []*mockServer{{id: "a1b2c3d4", state: "running"}}
	a, _ := start(t, p, keyAlpha)
	for _, kind := range []wire.CloudActionKind{"hard_reset", "custom", "snapshot_list"} {
		_, err := a.Action(context.Background(), core.Action{Ref: ref("default", "a1b2c3d4"), Kind: kind, Custom: "renew", RequestID: "r"})
		if coretest.Code(err) != wire.CloudErrorCodeUnsupported {
			t.Errorf("%s: %v", kind, err)
		}
	}
	prov := provider(a)
	for _, params := range []wire.CloudActionParams{
		{Instance: ref("default", "a1b2c3d4"), Action: "hard_reset", RequestID: "r1"},
		{Instance: ref("default", "a1b2c3d4"), Action: "custom", Custom: "renew", RequestID: "r2"},
	} {
		if _, err := prov.Action(context.Background(), params); coretest.Code(err) != wire.CloudErrorCodeUnsupported {
			t.Errorf("%s: %v", params.Action, err)
		}
	}
	if p.count("alpha POST") != 0 {
		t.Fatalf("calls %v", p.calls)
	}
}

func TestVendorErrors(t *testing.T) {
	echo := func(status int, code, detail string) fail {
		return fail{status, fmt.Sprintf(`{"errors":[{"code":%q,"status":"%d","detail":%q}]}`, code, status, detail)}
	}
	cases := []struct {
		f         fail
		code      wire.CloudErrorCode
		transient bool
	}{
		{echo(401, "AuthenticationException", "Unauthenticated. Bearer "+keyAlpha), wire.CloudErrorCodeNotSignedIn, false},
		{echo(403, "AccessDeniedHttpException", "This action is unauthorized for key "+keyAlpha), wire.CloudErrorCodeNotSignedIn, false},
		{echo(404, "NotFoundHttpException", "The requested resource could not be found on the server."), wire.CloudErrorCodeNotFound, false},
		{echo(409, "ConflictHttpException", "Another power action is currently being processed for this server, please try again later."), wire.CloudErrorCodeInvalidState, false},
		{echo(429, "TooManyRequestsHttpException", "Too Many Attempts. ("+keyAlpha+")"), wire.CloudErrorCodeRateLimited, true},
		{echo(400, "TooManyBackupsException", "Cannot create a new backup, this server has reached its limit of 2 backups."), wire.CloudErrorCodeRefused, false},
		{echo(422, "ValidationException", "The backup limit was reached."), wire.CloudErrorCodeRefused, false},
		{echo(422, "ValidationException", "The name may not be greater than 191 characters."), wire.CloudErrorCodeFailed, false},
		// After a 5xx or a gateway's answer the panel may still have acted: none of them is transient.
		{echo(502, "DaemonConnectionException", ""), wire.CloudErrorCodeFailed, false},
		{fail{504, `<html>Gateway Timeout</html>`}, wire.CloudErrorCodeFailed, false},
		{echo(500, "HttpException", "Server Error "+keyAlpha), wire.CloudErrorCodeFailed, false},
		{fail{503, ``}, wire.CloudErrorCodeFailed, false},
	}
	for i, c := range cases {
		p := newPanel(t)
		p.servers["alpha"] = []*mockServer{{id: "a1b2c3d4", state: "running", backupsLimit: 2}}
		p.fails["POST /api/client/servers/a1b2c3d4/power"] = c.f
		p.fails["POST /api/client/servers/a1b2c3d4/backups"] = c.f
		a, _ := start(t, p, keyAlpha)
		_, err := a.Action(context.Background(), core.Action{Ref: ref("default", "a1b2c3d4"), Kind: "start", RequestID: "r"})
		ce := core.AsError(err)
		if c.code == wire.CloudErrorCodeRefused {
			_, err = a.Action(context.Background(), core.Action{Ref: ref("default", "a1b2c3d4"), Kind: "snapshot_create", RequestID: "r"})
			ce = core.AsError(err)
		}
		if ce == nil || ce.Code != c.code || ce.Transient != c.transient {
			t.Errorf("case %d (%d): %+v", i, c.f.status, ce)
			continue
		}
		_, perr := provider(a).Action(context.Background(), wire.CloudActionParams{Instance: ref("default", "a1b2c3d4"),
			Action: "snapshot_create", RequestID: wire.CloudRequestID(fmt.Sprint("req-", i))})
		coretest.NoLeak(t, errText(perr), allKeys...)
	}
}

// A key the panel echoes back anywhere in a long detail does not leave even in part: the adapter does not clip, the
// Provider redacts first and clips after.
func TestEchoedKeyInALongDetail(t *testing.T) {
	for _, at := range []int{170, 290, 400} {
		p := newPanel(t)
		p.servers["alpha"] = []*mockServer{{id: "a1b2c3d4", state: "running"}}
		p.fails["POST /api/client/servers/a1b2c3d4/power"] = fail{500, fmt.Sprintf(`{"errors":[{"code":"HttpException","status":"500","detail":%q}]}`, strings.Repeat("x", at)+keyAlpha+" tail")}
		a, _ := start(t, p, keyAlpha)
		_, err := a.Action(context.Background(), core.Action{Ref: ref("default", "a1b2c3d4"), Kind: "start", RequestID: "r"})
		if !strings.HasSuffix(errText(err), "[redacted] tail") {
			t.Fatalf("at %d: the adapter clipped or kept the key: %q", at, errText(err))
		}
		_, perr := provider(a).Action(context.Background(), wire.CloudActionParams{Instance: ref("default", "a1b2c3d4"), Action: "start", RequestID: "r2"})
		for _, text := range []string{errText(err), errText(perr)} {
			if text == "" || strings.Contains(text, keyAlpha[:8]) {
				t.Fatalf("at %d: key or its prefix in %q", at, text)
			}
			coretest.NoLeak(t, text, allKeys...)
		}
	}
}

// An id that could not stay one path segment ("/", "%", " ") is not_found without any request to the panel.
func TestIDsThatCannotBeAPathSegment(t *testing.T) {
	p := newPanel(t)
	p.servers["alpha"] = []*mockServer{{id: "a1b2c3d4", state: "running"}}
	a, _ := start(t, p, keyAlpha)
	prov := provider(a)
	for i, id := range []string{"a1b2/c3d4", "a1b2%2Fc3d4", "a1b2 c3d4", "a/b%c d", "a1b2c3d4/power"} {
		r := ref("default", id)
		if _, err := prov.Instance(context.Background(), wire.CloudInstanceParams{Instance: r}); coretest.Code(err) != wire.CloudErrorCodeNotFound {
			t.Errorf("instance %q: %v", id, err)
		}
		if _, err := prov.Snapshots(context.Background(), wire.CloudSnapshotsParams{Instance: r}); coretest.Code(err) != wire.CloudErrorCodeNotFound {
			t.Errorf("snapshots %q: %v", id, err)
		}
		for _, kind := range []wire.CloudActionKind{"start", "snapshot_create"} {
			if _, err := prov.Action(context.Background(), wire.CloudActionParams{Instance: r, Action: kind, RequestID: wire.CloudRequestID("x-" + string(kind) + "-" + strconv.Itoa(i))}); coretest.Code(err) != wire.CloudErrorCodeNotFound {
				t.Errorf("%s %q: %v", kind, id, err)
			}
		}
	}
	if n := p.count("alpha"); n != 0 {
		t.Fatalf("the panel was asked: %v", p.calls)
	}
}

// The adapter's own words follow the caller's language; the panel's are passed on as they are.
func TestChineseWords(t *testing.T) {
	p := newPanel(t)
	p.servers["alpha"] = []*mockServer{{id: "a1b2c3d4", state: "offline", suspended: true, status: "suspended"}, {id: "a1b2c3d5", state: ""}}
	p.fails["POST /api/client/servers/a1b2c3d5/power"] = fail{502, `{"errors":[{"code":"DaemonConnectionException","status":"502","detail":""}]}`}
	a, _ := start(t, p, keyAlpha)
	prov := provider(a)
	in, err := prov.Instance(context.Background(), wire.CloudInstanceParams{Instance: ref("default", "a1b2c3d4"), Locale: "zh-CN"})
	if err != nil || in.StateDetail != "这台服务器已被面板管理员暂停。" {
		t.Fatalf("%v %q", err, in.StateDetail)
	}
	list, err := prov.Instances(context.Background(), wire.CloudInstancesParams{Refresh: true, Locale: "zh-CN"})
	if err != nil || len(list.Instances) != 2 {
		t.Fatalf("%v %s", err, asJSON(list))
	}
	for _, in := range list.Instances {
		if in.ID == "a1b2c3d5" && in.StateDetail != "面板无法确认这台服务器是否在运行。" {
			t.Fatalf("list detail %q", in.StateDetail)
		}
	}
	_, err = prov.Action(context.Background(), wire.CloudActionParams{Instance: ref("default", "a1b2c3d5"), Action: "start", RequestID: "zh-1", Locale: "zh-CN"})
	if !strings.Contains(errText(err), "面板连不上") {
		t.Fatalf("zh error %v", err)
	}
	if _, err = prov.Instance(context.Background(), wire.CloudInstanceParams{Instance: ref("default", "a1b2c3d4")}); err != nil {
		t.Fatal(err)
	}
	en, err := prov.Instance(context.Background(), wire.CloudInstanceParams{Instance: ref("default", "a1b2c3d4"), Locale: "en"})
	if err != nil || en.StateDetail != "The server is suspended by the panel's administrator." {
		t.Fatalf("%v %q", err, en.StateDetail)
	}
	// The panel's own words stay as the panel wrote them.
	p.fails["POST /api/client/servers/a1b2c3d4/power"] = fail{409, `{"errors":[{"code":"ConflictHttpException","status":"409","detail":"This server is currently suspended."}]}`}
	_, err = prov.Action(context.Background(), wire.CloudActionParams{Instance: ref("default", "a1b2c3d4"), Action: "start", RequestID: "zh-2", Locale: "zh-CN"})
	if !strings.Contains(errText(err), "This server is currently suspended.") {
		t.Fatalf("vendor words %v", err)
	}
}

// A subuser is offered only what its permissions on the server allow: the panel answers the rest with 403.
func TestSubuserPermissions(t *testing.T) {
	cases := []struct {
		perms   []string
		state   string
		actions []wire.CloudActionKind
	}{
		{[]string{"control.console", "websocket.connect"}, "offline", []wire.CloudActionKind{"sync_ip", "console"}},
		{[]string{"control.start", "backup.read"}, "offline", []wire.CloudActionKind{"start", "sync_ip", "snapshot_list", "console"}},
		{[]string{"control.stop"}, "running", []wire.CloudActionKind{"stop", "force_stop", "sync_ip", "console"}},
		{[]string{"control.restart", "backup.create"}, "running", []wire.CloudActionKind{"reboot", "sync_ip", "snapshot_create", "console"}},
		{[]string{"control.*", "backup.*"}, "running", []wire.CloudActionKind{"stop", "reboot", "force_stop", "sync_ip", "snapshot_list", "snapshot_create", "console"}},
		{[]string{}, "running", []wire.CloudActionKind{"sync_ip", "console"}},
	}
	for i, c := range cases {
		p := newPanel(t)
		p.servers["alpha"] = []*mockServer{{id: "a1b2c3d4", state: c.state, backupsLimit: 3, perms: c.perms}}
		a, _ := start(t, p, keyAlpha)
		in, err := provider(a).Instance(context.Background(), wire.CloudInstanceParams{Instance: ref("default", "a1b2c3d4")})
		if err != nil {
			t.Fatalf("case %d: %v", i, err)
		}
		coretest.Valid(t, "cloud.instance", in)
		if !slices.Equal(in.Actions, c.actions) {
			t.Errorf("case %d: %v", i, in.Actions)
		}
	}
}

// A large account: at most statsPerAccount resources reads per listing, and more than 500 servers make a partial list.
func TestLargeAccount(t *testing.T) {
	p := newPanel(t)
	for i := range core.MaxInstances + 20 {
		p.servers["alpha"] = append(p.servers["alpha"], &mockServer{id: fmt.Sprintf("s%07d", i), name: "Bulk", state: "running", ip: "203.0.113.1"})
	}
	a, _ := start(t, p, keyAlpha)
	out, err := provider(a).Instances(context.Background(), wire.CloudInstancesParams{Refresh: true})
	if err != nil {
		t.Fatal(err)
	}
	coretest.Valid(t, "cloud.instances", out)
	if len(out.Instances) != core.MaxInstances || !out.Partial || !strings.Contains(out.Detail, "500") {
		t.Fatalf("%d instances, partial %v, detail %q", len(out.Instances), out.Partial, out.Detail)
	}
	if n := p.count("alpha GET /api/client/servers/"); n != statsPerAccount {
		t.Fatalf("%d resources calls", n)
	}
	if pages := p.count("alpha GET /api/client") - p.count("alpha GET /api/client/"); pages != 5 { // 500 servers read
		t.Fatalf("list pages: %d", pages)
	}
	running, notAsked := 0, 0
	for _, in := range out.Instances {
		switch {
		case in.State == wire.CloudInstanceStateRunning:
			running++
		case in.State == wire.CloudInstanceStateUnknown && strings.Contains(in.StateDetail, "not asked"):
			notAsked++
		}
	}
	if running != statsPerAccount || notAsked != core.MaxInstances-statsPerAccount {
		t.Fatalf("running %d, not asked %d", running, notAsked)
	}
}

// The panel's rate limit stops the listing's resources reads at once.
func TestListingStopsAtRateLimit(t *testing.T) {
	p := newPanel(t)
	for i := range 40 {
		p.servers["alpha"] = append(p.servers["alpha"], &mockServer{id: fmt.Sprintf("s%07d", i), state: "running", statsStatus: 429})
	}
	a, _ := start(t, p, keyAlpha)
	list, problems, err := a.Instances(context.Background())
	if err != nil || len(problems) != 0 || len(list) != 40 {
		t.Fatalf("%v %v %d", err, problems, len(list))
	}
	if n := p.count("alpha GET /api/client/servers/"); n < 1 || n > statsWorkers {
		t.Fatalf("%d resources calls after a rate-limit answer", n)
	}
	skipped := 0
	for _, in := range list {
		if in.State != wire.CloudInstanceStateUnknown {
			t.Fatalf("%s: %s", in.ID, in.State)
		}
		if strings.Contains(in.StateDetail, "rate limit") {
			skipped++
		}
	}
	if skipped < 40-statsWorkers {
		t.Fatalf("%d skipped", skipped)
	}
}

func TestInstanceNotFound(t *testing.T) {
	p := newPanel(t)
	p.servers["alpha"] = []*mockServer{{id: "a1b2c3d4", state: "running"}}
	a, _ := start(t, p, keyAlpha)
	prov := provider(a)
	for _, r := range []wire.CloudInstanceRef{ref("default", "ffffffff"), ref("nobody", "a1b2c3d4"), ref("default", "../../admin")} {
		_, err := prov.Instance(context.Background(), wire.CloudInstanceParams{Instance: r})
		if coretest.Code(err) != wire.CloudErrorCodeNotFound {
			t.Errorf("%v: %v", r, err)
		}
		_, err = prov.Snapshots(context.Background(), wire.CloudSnapshotsParams{Instance: r})
		if coretest.Code(err) != wire.CloudErrorCodeNotFound {
			t.Errorf("snapshots %v: %v", r, err)
		}
	}
	if p.count("alpha GET /api/client/servers/..") != 0 {
		t.Fatalf("a path outside the server was asked: %v", p.calls)
	}
}

func TestNewAddressAfterStart(t *testing.T) {
	p := newPanel(t)
	p.servers["alpha"] = []*mockServer{{id: "a1b2c3d4", name: "Survival", state: "offline", ip: "0.0.0.0", alias: "203.0.113.10",
		onStart: func(s *mockServer) { s.alias = "203.0.113.99"; s.state = "running" }}}
	a, _ := start(t, p, keyAlpha)
	prov := provider(a)
	r := ref("default", "a1b2c3d4")
	in, err := prov.Instance(context.Background(), wire.CloudInstanceParams{Instance: r})
	if err != nil || !slices.Equal(in.PublicIps, []string{"203.0.113.10"}) || in.State != "stopped" {
		t.Fatalf("%v %s", err, asJSON(in))
	}
	if _, err := prov.Action(context.Background(), wire.CloudActionParams{Instance: r, Action: "start", RequestID: "start-1"}); err != nil {
		t.Fatal(err)
	}
	in, err = prov.Instance(context.Background(), wire.CloudInstanceParams{Instance: r})
	if err != nil || !slices.Equal(in.PublicIps, []string{"203.0.113.99"}) || in.State != "running" {
		t.Fatalf("%v %s", err, asJSON(in))
	}
	res, err := prov.Action(context.Background(), wire.CloudActionParams{Instance: r, Action: "sync_ip", RequestID: "sync-1"})
	if err != nil || res.Instance == nil || !slices.Equal(res.Instance.PublicIps, []string{"203.0.113.99"}) {
		t.Fatalf("%v %s", err, asJSON(res))
	}
	coretest.Valid(t, "cloud.action", res)
}

func TestAddresses(t *testing.T) {
	cases := []struct {
		ip, alias       string
		public, private []string
	}{
		{"203.0.113.5", "", []string{"203.0.113.5"}, nil},
		{"0.0.0.0", "198.51.100.9", []string{"198.51.100.9"}, nil},
		{"192.168.1.20", "203.0.113.7", []string{"203.0.113.7"}, []string{"192.168.1.20"}},
		{"172.16.4.4", "node.example.com", nil, []string{"172.16.4.4"}},
		{"0.0.0.0", "", nil, nil},
		{"::", "2001:db8::5", []string{"2001:db8::5"}, nil},
		{cgnat.Addr().Next().String(), "", nil, []string{cgnat.Addr().Next().String()}}, // shared address space, built here
	}
	for _, c := range cases {
		var s server
		raw, _ := json.Marshal((&mockServer{id: "x", ip: c.ip, alias: c.alias}).attributes())
		if err := json.Unmarshal(raw, &s); err != nil {
			t.Fatal(err)
		}
		pub, priv := addresses(s)
		if !slices.Equal(pub, c.public) || !slices.Equal(priv, c.private) {
			t.Errorf("%s/%s: %v %v", c.ip, c.alias, pub, priv)
		}
	}
}

func TestConsoleLink(t *testing.T) {
	p := newPanel(t)
	p.servers["alpha"] = []*mockServer{{id: "a1b2c3d4", state: "running"}}
	a, srv := start(t, p, keyAlpha)
	res, err := provider(a).Action(context.Background(), wire.CloudActionParams{Instance: ref("default", "a1b2c3d4"), Action: "console", RequestID: "c1"})
	if err != nil {
		t.Fatal(err)
	}
	raw := coretest.Valid(t, "cloud.action", res)
	coretest.NoLeak(t, string(raw), allKeys...)
	if res.URL != srv.URL+"/server/a1b2c3d4" || !strings.HasPrefix(res.URL, "https://") || strings.Contains(res.URL, "?") || strings.Contains(res.URL, "@") {
		t.Fatalf("console %s", raw)
	}

	// A plain http panel has no console link.
	plain := httptest.NewServer(p)
	t.Cleanup(plain.Close)
	b, err := New(Config{BaseURL: plain.URL, APIKey: keyAlpha, HTTP: plain.Client()})
	if err != nil {
		t.Fatal(err)
	}
	in, err := provider(b).Instance(context.Background(), wire.CloudInstanceParams{Instance: ref("default", "a1b2c3d4")})
	if err != nil || in.ConsoleURL != "" || slices.Contains(in.Actions, wire.CloudActionKindConsole) {
		t.Fatalf("%v %s", err, asJSON(in))
	}
	if _, err := provider(b).Action(context.Background(), wire.CloudActionParams{Instance: ref("default", "a1b2c3d4"), Action: "console", RequestID: "c2"}); coretest.Code(err) != wire.CloudErrorCodeUnsupported {
		t.Fatalf("console on http: %v", err)
	}
}

func TestSnapshots(t *testing.T) {
	p := newPanel(t)
	p.servers["alpha"] = []*mockServer{{id: "a1b2c3d4", state: "running", backupsLimit: 5, backups: []map[string]any{
		{"uuid": "b0000001-0000-4000-8000-000000000000", "is_successful": true, "is_locked": false, "name": "Before update", "ignored_files": []string{}, "checksum": "sha1:0000", "bytes": 2147483648, "created_at": "2026-10-01T10:00:00+00:00", "completed_at": "2026-10-01T10:05:00+00:00"},
		{"uuid": "b0000002-0000-4000-8000-000000000000", "is_successful": false, "is_locked": false, "name": "Broken", "ignored_files": []string{}, "checksum": nil, "bytes": 0, "created_at": "2026-10-02T10:00:00+00:00", "completed_at": "2026-10-02T10:01:00+00:00"},
		{"uuid": "b0000003-0000-4000-8000-000000000000", "is_successful": false, "is_locked": false, "name": "Running now", "ignored_files": []string{}, "checksum": nil, "bytes": 0, "created_at": "2026-10-03T10:00:00+00:00", "completed_at": nil},
	}}}
	a, _ := start(t, p, keyAlpha)
	prov := provider(a)
	r := ref("default", "a1b2c3d4")
	list, err := prov.Snapshots(context.Background(), wire.CloudSnapshotsParams{Instance: r})
	if err != nil {
		t.Fatal(err)
	}
	coretest.Valid(t, "cloud.snapshots", list)
	if p.count("alpha GET /api/client/servers/a1b2c3d4/backups") != 2 { // two pages
		t.Fatalf("calls %v", p.calls)
	}
	if len(list.Snapshots) != 3 || list.Snapshots[0].State != "creating" || list.Snapshots[1].State != "failed" ||
		list.Snapshots[2].State != "ready" || list.Snapshots[2].SizeGb != 2 || list.Snapshots[2].Name != "Before update" ||
		!list.Snapshots[2].CreatedAt.Equal(time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)) {
		t.Fatalf("snapshots %s", asJSON(list))
	}

	res, err := prov.Action(context.Background(), wire.CloudActionParams{Instance: r, Action: "snapshot_create", Name: "Nightly", RequestID: "snap-1"})
	if err != nil {
		t.Fatal(err)
	}
	coretest.Valid(t, "cloud.action", res)
	if res.Status != "accepted" || res.Snapshot == nil || res.Snapshot.Name != "Nightly" || res.Snapshot.State != "creating" ||
		p.count(`alpha POST /api/client/servers/a1b2c3d4/backups {"name":"Nightly"}`) != 1 {
		t.Fatalf("%s %v", asJSON(res), p.calls)
	}
	// Unnamed: the panel names it.
	if _, err := prov.Action(context.Background(), wire.CloudActionParams{Instance: r, Action: "snapshot_create", RequestID: "snap-2"}); err != nil ||
		p.count(`alpha POST /api/client/servers/a1b2c3d4/backups {}`) != 1 {
		t.Fatalf("%v %v", err, p.calls)
	}
	// The limit of 5 is reached now: the panel refuses.
	_, err = prov.Action(context.Background(), wire.CloudActionParams{Instance: r, Action: "snapshot_create", RequestID: "snap-3"})
	if coretest.Code(err) != wire.CloudErrorCodeRefused || !strings.Contains(err.Error(), "limit") {
		t.Fatalf("limit: %v", err)
	}
}
