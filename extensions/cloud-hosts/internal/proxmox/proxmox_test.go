package proxmox

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona-extensions/extensions/cloud-hosts/internal/core"
	"github.com/nwverse/persona-extensions/extensions/cloud-hosts/internal/coretest"
	"github.com/nwverse/persona/sdk/go/wire"
)

// Synthetic credentials only.
const (
	prodID     = "persona@pve!cloudhosts"
	prodSecret = "00000000-test-0000-0000-000000000001"
	labID      = "lab@pve!cloudhosts"
	labSecret  = "00000000-test-0000-0000-000000000002"
)

var secrets = []string{prodSecret, labSecret, prodID + "=" + prodSecret, labID + "=" + labSecret}

var fixedNow = time.Date(2026, 11, 27, 10, 0, 0, 0, time.UTC)

type mockGuest struct {
	node, typ string
	vmid      int
	name      string
	status    string
	qmp       string
	lock      string
	template  bool
	// agent is the network-get-interfaces result (qemu) or the interfaces list (lxc); nil answers "agent not running".
	agent     any
	startIPs  any // the addresses the guest has after its next start
	snapshots []map[string]any
}

// key is where the mock serves the guest: its node, type and vmid.
func (g *mockGuest) key() string { return g.node + "/" + g.typ + "/" + strconv.Itoa(g.vmid) }

// id is the guest's instance id: its type and vmid.
func (g *mockGuest) id() wire.CloudInstanceID {
	return wire.CloudInstanceID(g.typ + "/" + strconv.Itoa(g.vmid))
}

type mock struct {
	t      *testing.T
	mu     sync.Mutex
	tokens map[string]bool // Authorization header values that are accepted
	guests map[string]*mockGuest
	calls  []string
	forms  map[string]url.Values
	// fail, when set, may answer a request itself (error injection).
	fail func(w http.ResponseWriter, r *http.Request) bool
}

func newMock(t *testing.T, guests ...*mockGuest) *mock {
	m := &mock{t: t, tokens: map[string]bool{"PVEAPIToken=" + prodID + "=" + prodSecret: true},
		guests: map[string]*mockGuest{}, forms: map[string]url.Values{}}
	for _, g := range guests {
		m.guests[g.key()] = g
	}
	return m
}

// total is the number of requests the mock served.
func (m *mock) total() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.calls)
}

// move migrates g to node, as a live migration or an HA relocation does. The caller holds m.mu or no request runs.
func (m *mock) move(g *mockGuest, node string) {
	delete(m.guests, g.key())
	g.node = node
	m.guests[g.key()] = g
}

func (m *mock) count(prefix string) int {
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

// writeRaw answers with a status line of its own reason phrase, as Proxmox VE does ("500 VM 100 not running").
func writeRaw(w http.ResponseWriter, code int, reason, body string) {
	conn, buf, err := w.(http.Hijacker).Hijack()
	if err != nil {
		panic(err)
	}
	defer conn.Close()
	fmt.Fprintf(buf, "HTTP/1.1 %d %s\r\nContent-Type: application/json;charset=UTF-8\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s",
		code, reason, len(body), body)
	buf.Flush()
}

func writeData(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json;charset=UTF-8")
	json.NewEncoder(w).Encode(map[string]any{"data": v})
}

func (m *mock) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	defer m.mu.Unlock()
	call := r.Method + " " + r.URL.Path
	if r.URL.RawQuery != "" {
		call += "?" + r.URL.RawQuery
	}
	m.calls = append(m.calls, call)
	if r.Header.Get("Accept") != "application/json" || r.Header.Get("Cookie") != "" || r.Header.Get("CSRFPreventionToken") != "" {
		m.t.Errorf("%s: unexpected headers %v", call, r.Header)
	}
	if !m.tokens[r.Header.Get("Authorization")] {
		// Echo the credential back, as a careless proxy might: it must never leave the adapter.
		writeRaw(w, 401, "authentication failure", `{"data":null,"message":"bad token `+r.Header.Get("Authorization")+`"}`)
		return
	}
	if m.fail != nil && m.fail(w, r) {
		return
	}
	p := strings.TrimPrefix(r.URL.Path, "/api2/json/")
	if p == "cluster/resources" {
		if r.Method != http.MethodGet || r.URL.Query().Get("type") != "vm" {
			m.t.Errorf("bad list request %s", call)
		}
		var out []map[string]any
		for _, g := range m.guests {
			e := map[string]any{"id": g.typ + "/" + strconv.Itoa(g.vmid), "vmid": g.vmid, "name": g.name, "node": g.node,
				"type": g.typ, "status": g.status, "maxcpu": 2, "maxmem": 4 << 30, "maxdisk": 32 << 30}
			if g.template {
				e["template"] = 1
			}
			if g.lock != "" {
				e["lock"] = g.lock
			}
			out = append(out, e)
		}
		writeData(w, out)
		return
	}
	parts := strings.Split(p, "/") // nodes/{node}/{type}/{vmid}/...
	if len(parts) < 5 || parts[0] != "nodes" {
		writeRaw(w, 501, "Method 'GET /"+p+"' not implemented", `{"data":null}`)
		return
	}
	g := m.guests[parts[1]+"/"+parts[2]+"/"+parts[3]]
	if g == nil {
		conf := "qemu-server"
		if parts[2] == "lxc" {
			conf = "lxc"
		}
		writeRaw(w, 500, "Configuration file 'nodes/"+parts[1]+"/"+conf+"/"+parts[3]+".conf' does not exist", `{"data":null}`)
		return
	}
	rest := strings.Join(parts[4:], "/")
	switch {
	case r.Method == http.MethodGet && rest == "status/current":
		cur := map[string]any{"status": g.status, "name": g.name, "cpus": 2, "maxmem": 4 << 30, "maxdisk": 32 << 30, "vmid": g.vmid}
		if g.qmp != "" {
			cur["qmpstatus"] = g.qmp
		}
		if g.lock != "" {
			cur["lock"] = g.lock
		}
		if g.template {
			cur["template"] = 1
		}
		writeData(w, cur)
	case r.Method == http.MethodGet && rest == "agent/network-get-interfaces" && g.typ == "qemu":
		if g.status != "running" || (g.qmp != "" && g.qmp != "running") {
			m.t.Errorf("addresses asked of a guest that is %s/%s", g.status, g.qmp)
		}
		if g.agent == nil {
			writeRaw(w, 500, "QEMU guest agent is not running", `{"data":null}`)
			return
		}
		writeData(w, map[string]any{"result": g.agent})
	case r.Method == http.MethodGet && rest == "interfaces" && g.typ == "lxc":
		writeData(w, g.agent)
	case r.Method == http.MethodPost && strings.HasPrefix(rest, "status/"):
		if r.Header.Get("Content-Type") != "application/x-www-form-urlencoded" {
			m.t.Errorf("%s: content type %q", call, r.Header.Get("Content-Type"))
		}
		r.ParseForm()
		m.forms[call] = r.PostForm
		switch op := strings.TrimPrefix(rest, "status/"); op {
		case "start":
			if g.status == "running" {
				writeRaw(w, 500, "VM "+parts[3]+" already running", `{"data":null}`)
				return
			}
			g.status, g.agent = "running", g.startIPs
		case "shutdown", "stop":
			if g.status != "running" {
				writeRaw(w, 500, "VM "+parts[3]+" not running", `{"data":null}`)
				return
			}
			g.status = "stopped"
		case "reboot", "reset":
		default:
			writeRaw(w, 501, "Method not implemented", `{"data":null}`)
			return
		}
		writeData(w, "UPID:"+parts[1]+":0000ABCD:00001234:6740A000:qm"+strings.TrimPrefix(rest, "status/")+":"+parts[3]+":"+prodID+":")
	case rest == "snapshot" && r.Method == http.MethodGet:
		writeData(w, g.snapshots)
	case rest == "snapshot" && r.Method == http.MethodPost:
		r.ParseForm()
		m.forms[call] = r.PostForm
		writeData(w, "UPID:"+parts[1]+":0000ABCD:00001234:6740A000:qmsnapshot:"+parts[3]+":"+prodID+":")
	default:
		writeRaw(w, 501, "Method '"+r.Method+" /"+p+"' not implemented", `{"data":null}`)
	}
}

// start serves m over TLS and is an adapter that trusts the server's certificate by its pin.
func start(t *testing.T, m *mock, token string) (*httptest.Server, core.Adapter) {
	t.Helper()
	srv := quietTLS(m)
	t.Cleanup(srv.Close)
	sum := sha256.Sum256(srv.Certificate().Raw)
	a, err := New(Config{BaseURL: srv.URL, Token: token, PinSHA256: hex.EncodeToString(sum[:]),
		Now: func() time.Time { return fixedNow }})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return srv, a
}

// quietTLS is an httptest TLS server that does not log the handshakes a pinned client refuses.
func quietTLS(h http.Handler) *httptest.Server {
	srv := httptest.NewUnstartedServer(h)
	srv.Config.ErrorLog = log.New(io.Discard, "", 0)
	srv.StartTLS()
	return srv
}

func prodToken() string { return prodID + "=" + prodSecret }

func twoAccounts() string {
	return "# two tokens\nprod=" + prodID + "=" + prodSecret + "\n\nlab=" + labID + "=" + labSecret + "\n"
}

func agentIPs(ips ...string) []map[string]any {
	var addrs []map[string]any
	for _, ip := range ips {
		addrs = append(addrs, map[string]any{"ip-address": ip, "ip-address-type": "ipv4", "prefix": 24})
	}
	return []map[string]any{
		{"name": "lo", "ip-addresses": []map[string]any{{"ip-address": "127.0.0.1", "prefix": 8}, {"ip-address": "::1", "prefix": 128}}},
		{"name": "docker0", "ip-addresses": []map[string]any{{"ip-address": "172.16.0.1", "prefix": 16}}},
		{"name": "eth0", "hardware-address": "02:00:00:00:00:01", "ip-addresses": append(addrs,
			map[string]any{"ip-address": "fe80::1", "ip-address-type": "ipv6", "prefix": 64})},
	}
}

func allSecrets(t *testing.T, a core.Adapter) []string {
	t.Helper()
	return append(append([]string{}, secrets...), a.(core.SecretHolder).SecretValues()...)
}

func mustJSON(v any) string { b, _ := json.Marshal(v); return string(b) }

func TestParseTokens(t *testing.T) {
	accs, err := parseTokens(prodToken())
	if err != nil || len(accs) != 1 || accs[0].name != "default" || accs[0].tokenID != prodID || accs[0].secret != prodSecret {
		t.Fatalf("single token: %+v %v", accs, err)
	}
	accs, err = parseTokens("PVEAPIToken=" + prodToken())
	if err != nil || len(accs) != 1 || accs[0].name != "default" || accs[0].tokenID != prodID {
		t.Fatalf("header form: %+v %v", accs, err)
	}
	accs, err = parseTokens("prod=" + prodToken())
	if err != nil || len(accs) != 1 || accs[0].name != "prod" || accs[0].secret != prodSecret {
		t.Fatalf("one named token: %+v %v", accs, err)
	}
	accs, err = parseTokens(twoAccounts())
	if err != nil || len(accs) != 2 || accs[0].name != "prod" || accs[1].name != "lab" || accs[1].tokenID != labID {
		t.Fatalf("two tokens: %+v %v", accs, err)
	}
	for _, bad := range []string{"", "  \n# nothing\n", "no-bang=" + prodSecret, "persona@pve!cloudhosts", "pve!tok=" + prodSecret,
		"persona@pve!=" + prodSecret, prodToken() + "\n" + labID + "=" + labSecret, "a=" + prodToken() + "\na=" + labID + "=" + labSecret} {
		_, err := parseTokens(bad)
		if coretest.Code(err) != wire.CloudErrorCodeNotSignedIn {
			t.Errorf("%q: got %v, want not_signed_in", bad, err)
		}
		if err != nil {
			coretest.NoLeak(t, err.Error(), secrets...)
		}
	}
	if _, err := New(Config{BaseURL: "https://pve.example.com:8006"}); coretest.Code(err) != wire.CloudErrorCodeNotSignedIn {
		t.Errorf("missing token: %v", err)
	}
}

func TestNewConfig(t *testing.T) {
	for _, base := range []string{"", "http://pve.example.com:8006", "https://user:pw@pve.example.com", "pve.example.com", "https://pve.example.com/?a=b"} {
		if _, err := New(Config{BaseURL: base, Token: prodToken()}); err == nil {
			t.Errorf("%q: accepted", base)
		}
	}
	if _, err := New(Config{BaseURL: "https://pve.example.com:8006", Token: prodToken(), PinSHA256: "abc"}); err == nil {
		t.Error("a short pin was accepted")
	}
	a, err := New(Config{BaseURL: "https://pve.example.com:8006/", Token: prodToken(),
		PinSHA256: strings.Repeat("AB:", 31) + "AB"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	vals := a.(core.SecretHolder).SecretValues()
	for _, want := range []string{prodSecret, prodID + "=" + prodSecret, "PVEAPIToken=" + prodID + "=" + prodSecret, prodID} {
		found := false
		for _, v := range vals {
			found = found || v == want
		}
		if !found {
			t.Errorf("SecretValues lacks %q", want)
		}
	}
}

func TestWrongPin(t *testing.T) {
	m := newMock(t)
	srv := quietTLS(m)
	defer srv.Close()
	a, err := New(Config{BaseURL: srv.URL, Token: prodToken(), PinSHA256: strings.Repeat("00", 32)})
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = a.Instances(context.Background())
	if coretest.Code(err) != wire.CloudErrorCodeFailed || !strings.Contains(err.Error(), "certificate") {
		t.Fatalf("got %v", err)
	}
	if n := m.total(); n != 0 {
		t.Fatalf("%d requests reached a server with another certificate", n)
	}
}

func TestInstancesAcrossAccounts(t *testing.T) {
	m := newMock(t,
		&mockGuest{node: "pve1", typ: "qemu", vmid: 100, name: "web", status: "running",
			agent: agentIPs("203.0.113.10", "10.0.0.5")},
		&mockGuest{node: "pve1", typ: "qemu", vmid: 101, name: "db", status: "running"}, // no agent
		&mockGuest{node: "pve2", typ: "lxc", vmid: 200, name: "cache", status: "running", agent: []map[string]any{
			{"name": "lo", "inet": "127.0.0.1/8", "inet6": "::1/128"},
			{"name": "eth0", "inet": "192.168.1.20/24", "inet6": "2001:db8::20/64 fe80::20/64"},
		}},
		&mockGuest{node: "pve2", typ: "lxc", vmid: 201, name: "off", status: "stopped"},
		&mockGuest{node: "pve2", typ: "qemu", vmid: 9000, name: "tmpl", status: "stopped", template: true},
		&mockGuest{node: "pve3", typ: "qemu", vmid: 300, name: "lost", status: "unknown"},
		&mockGuest{node: "pve1", typ: "qemu", vmid: 102, name: "backing-up", status: "running", lock: "backup"},
		&mockGuest{node: "pve1", typ: "qemu", vmid: 103, name: "paused", status: "running", qmp: "paused",
			agent: agentIPs("203.0.113.30")}, // its agent is not asked while QEMU is paused
	)
	_, a := start(t, m, twoAccounts()) // lab's token is refused by the mock
	list, problems, err := a.Instances(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(problems) != 1 || problems[0] != "lab" {
		t.Fatalf("problems %v", problems)
	}
	got := map[wire.CloudInstanceID]wire.CloudInstance{}
	for _, in := range list {
		if in.Account != "prod" {
			t.Errorf("instance of account %q", in.Account)
		}
		got[in.ID] = in
	}
	if len(got) != 7 {
		t.Fatalf("got %d instances: %s", len(got), mustJSON(list))
	}
	if _, ok := got["qemu/9000"]; ok {
		t.Error("a template was listed")
	}
	web := got["qemu/100"]
	if web.State != wire.CloudInstanceStateRunning || web.DisplayName != "web" || web.Zone != "pve1" ||
		strings.Join(web.PublicIps, ",") != "203.0.113.10" || strings.Join(web.PrivateIps, ",") != "10.0.0.5" ||
		web.Spec == nil || web.Spec.Cpu != 2 || web.Spec.MemoryMb != 4096 || web.Spec.DiskGb != 32 || !web.ObservedAt.Equal(fixedNow) {
		t.Errorf("web: %s", mustJSON(web))
	}
	if db := got["qemu/101"]; len(db.PublicIps)+len(db.PrivateIps) != 0 || db.State != wire.CloudInstanceStateRunning {
		t.Errorf("db: %s", mustJSON(db))
	}
	cache := got["lxc/200"]
	if strings.Join(cache.PublicIps, ",") != "2001:db8::20" || strings.Join(cache.PrivateIps, ",") != "192.168.1.20" || cache.Zone != "pve2" {
		t.Errorf("cache: %s", mustJSON(cache))
	}
	if off := got["lxc/201"]; off.State != wire.CloudInstanceStateStopped || !core.Offers(off.Actions, wire.CloudActionKindStart) {
		t.Errorf("off: %s", mustJSON(off))
	}
	if lost := got["qemu/300"]; lost.State != wire.CloudInstanceStateUnknown || lost.StateDetail == "" {
		t.Errorf("lost: %s", mustJSON(lost))
	}
	if paused := got["qemu/103"]; paused.State != wire.CloudInstanceStateStopped || paused.StateDetail == "" ||
		len(paused.PublicIps) != 0 || core.Offers(paused.Actions, wire.CloudActionKindStop) {
		t.Errorf("paused in the list (as Instance shows it): %s", mustJSON(paused))
	}
	if b := got["qemu/102"]; b.State != wire.CloudInstanceStateRunning || !strings.Contains(b.StateDetail, "backup") ||
		core.Offers(b.Actions, wire.CloudActionKindStop) || core.Offers(b.Actions, wire.CloudActionKindSnapshotCreate) {
		t.Errorf("locked: %s", mustJSON(b))
	}
	// One list call per account; QEMU's own status only of the running QEMU guests; addresses only of the running
	// guests.
	if n := m.count("GET /api2/json/cluster/resources"); n != 2 {
		t.Errorf("%d list calls", n)
	}
	for _, id := range []string{"100", "101", "102", "103"} {
		if n := m.count("GET /api2/json/nodes/pve1/qemu/" + id + "/status/current"); n != 1 {
			t.Errorf("qemu %s: %d status reads", id, n)
		}
	}
	if n := m.count("GET /api2/json/nodes/pve2/lxc/200/status") + m.count("GET /api2/json/nodes/pve2/lxc/201/status") +
		m.count("GET /api2/json/nodes/pve3/"); n != 0 {
		t.Errorf("the list asked a status of a container or a stopped or unknown guest")
	}
	if n := m.count("GET /api2/json/nodes/pve2/lxc/201/interfaces"); n != 0 {
		t.Errorf("the addresses of a stopped guest were asked")
	}

	// Through the provider: partial, naming the account only, and no credential anywhere.
	p := coretest.Provider(a, nil, secrets...)
	ans, err := p.Instances(context.Background(), wire.CloudInstancesParams{Locale: "en", Refresh: true})
	if err != nil {
		t.Fatal(err)
	}
	raw := coretest.Valid(t, "cloud.instances", ans)
	coretest.NoLeak(t, string(raw), allSecrets(t, a)...)
	if !ans.Partial || !strings.Contains(ans.Detail, "lab") {
		t.Errorf("partial %v detail %q", ans.Partial, ans.Detail)
	}
}

func TestInstancesAllAccountsFail(t *testing.T) {
	m := newMock(t)
	m.tokens = map[string]bool{}
	_, a := start(t, m, twoAccounts())
	_, _, err := a.Instances(context.Background())
	if coretest.Code(err) != wire.CloudErrorCodeNotSignedIn {
		t.Fatalf("got %v", err)
	}
	p := coretest.Provider(a, nil, secrets...)
	_, err = p.Instances(context.Background(), wire.CloudInstancesParams{Refresh: true})
	if coretest.Code(err) != wire.CloudErrorCodeNotSignedIn {
		t.Fatalf("provider: %v", err)
	}
	coretest.NoLeak(t, err.Error(), allSecrets(t, a)...)
}

func TestStateMapping(t *testing.T) {
	type want struct {
		state   wire.CloudInstanceState
		detail  bool
		actions string
	}
	reads := "sync_ip,snapshot_list,console"
	cases := []struct {
		g    mockGuest
		want want
	}{
		{mockGuest{typ: "qemu", status: "running", qmp: "running"}, want{wire.CloudInstanceStateRunning, false,
			"stop,reboot,hard_reset,force_stop,sync_ip,snapshot_list,snapshot_create,console"}},
		{mockGuest{typ: "lxc", status: "running"}, want{wire.CloudInstanceStateRunning, false,
			"stop,reboot,force_stop,sync_ip,snapshot_list,snapshot_create,console"}},
		{mockGuest{typ: "qemu", status: "stopped", qmp: "stopped"}, want{wire.CloudInstanceStateStopped, false,
			"start,sync_ip,snapshot_list,snapshot_create,console"}},
		// Hibernated (suspended to disk): stopped with the lock "suspended", and a start resumes it.
		{mockGuest{typ: "qemu", status: "stopped", qmp: "stopped", lock: "suspended"}, want{wire.CloudInstanceStateStopped, true,
			"start,sync_ip,snapshot_list,console"}},
		{mockGuest{typ: "qemu", status: "running", lock: "suspending"}, want{wire.CloudInstanceStateRunning, true, reads}},
		{mockGuest{typ: "lxc", status: "stopped"}, want{wire.CloudInstanceStateStopped, false,
			"start,sync_ip,snapshot_list,snapshot_create,console"}},
		{mockGuest{typ: "qemu", status: "running", qmp: "paused"}, want{wire.CloudInstanceStateStopped, true, reads}},
		{mockGuest{typ: "qemu", status: "running", qmp: "suspended"}, want{wire.CloudInstanceStateStopped, true, reads}},
		{mockGuest{typ: "qemu", status: "running", qmp: "prelaunch"}, want{wire.CloudInstanceStateStopped, true, reads}},
		{mockGuest{typ: "qemu", status: "running", qmp: "shutdown"}, want{wire.CloudInstanceStateStopping, false, reads}},
		{mockGuest{typ: "qemu", status: "running", qmp: "inmigrate"}, want{wire.CloudInstanceStateRunning, true, reads}},
		{mockGuest{typ: "qemu", status: "running", qmp: "guest-panicked"}, want{wire.CloudInstanceStateRunning, true,
			"hard_reset,force_stop,sync_ip,snapshot_list,console"}},
		{mockGuest{typ: "qemu", status: "running", lock: "migrate"}, want{wire.CloudInstanceStateRunning, true, reads}},
		{mockGuest{typ: "qemu", status: "stopped", lock: "backup"}, want{wire.CloudInstanceStateStopped, true, reads}},
		{mockGuest{typ: "lxc", status: "stopped", lock: "snapshot"}, want{wire.CloudInstanceStateStopped, true, reads}},
		{mockGuest{typ: "qemu", status: "stopped", lock: "rollback"}, want{wire.CloudInstanceStateStopped, true, reads}},
		{mockGuest{typ: "qemu", status: "unknown"}, want{wire.CloudInstanceStateUnknown, true, "sync_ip,console"}},
		{mockGuest{typ: "qemu", status: "weird"}, want{wire.CloudInstanceStateUnknown, true, "sync_ip,console"}},
	}
	for i, c := range cases {
		g := c.g
		g.node, g.vmid, g.name = "pve1", 100+i, "guest"
		m := newMock(t, &g)
		_, a := start(t, m, prodToken())
		p := coretest.Provider(a, nil, secrets...)
		in, err := p.Instance(context.Background(), wire.CloudInstanceParams{Instance: wire.CloudInstanceRef{Account: "default", ID: g.id()}})
		if err != nil {
			t.Fatalf("%d: %v", i, err)
		}
		coretest.Valid(t, "cloud.instance", in)
		var acts []string
		for _, k := range in.Actions {
			acts = append(acts, string(k))
		}
		if in.State != c.want.state || (in.StateDetail != "") != c.want.detail || strings.Join(acts, ",") != c.want.actions {
			t.Errorf("%d %+v: got %s %q %v", i, c.g, in.State, in.StateDetail, acts)
		}
	}
}

func TestInstanceNotFound(t *testing.T) {
	m := newMock(t, &mockGuest{node: "pve1", typ: "qemu", vmid: 9000, status: "stopped", template: true},
		&mockGuest{node: "pve1", typ: "qemu", vmid: 100, status: "running"})
	_, a := start(t, m, prodToken())
	for _, r := range []wire.CloudInstanceRef{
		{Account: "default", ID: "qemu/999"},      // the cluster has no such guest
		{Account: "default", ID: "qemu/9000"},     // a template
		{Account: "default", ID: "lxc/100"},       // vmid 100 is a QEMU VM, not a container
		{Account: "other", ID: "qemu/100"},        // no such account
		{Account: "default", ID: "vm/100"},        // not qemu or lxc
		{Account: "default", ID: "qemu/01"},       // not a vmid
		{Account: "default", ID: "qemu/100/x"},    // too many parts
		{Account: "default", ID: "pve1/qemu/100"}, // a node is not part of the id
		{Account: "default", ID: "../100"},        // not a type
		{Account: "default", ID: "qemu/1%2F..2"},  // not a vmid
	} {
		_, err := a.Instance(context.Background(), r)
		if coretest.Code(err) != wire.CloudErrorCodeNotFound {
			t.Errorf("%v: got %v", r, err)
		}
	}
}

func TestActions(t *testing.T) {
	cases := []struct {
		typ, status string
		kind        wire.CloudActionKind
		path        string
	}{
		{"qemu", "stopped", wire.CloudActionKindStart, "/status/start"},
		{"lxc", "stopped", wire.CloudActionKindStart, "/status/start"},
		{"qemu", "running", wire.CloudActionKindStop, "/status/shutdown"},
		{"lxc", "running", wire.CloudActionKindStop, "/status/shutdown"},
		{"qemu", "running", wire.CloudActionKindReboot, "/status/reboot"},
		{"lxc", "running", wire.CloudActionKindReboot, "/status/reboot"},
		{"qemu", "running", wire.CloudActionKindHardReset, "/status/reset"},
		{"qemu", "running", wire.CloudActionKindForceStop, "/status/stop"},
		{"lxc", "running", wire.CloudActionKindForceStop, "/status/stop"},
	}
	for i, c := range cases {
		g := &mockGuest{node: "pve1", typ: c.typ, vmid: 100, status: c.status, agent: agentIPs("203.0.113.10")}
		m := newMock(t, g)
		_, a := start(t, m, prodToken())
		p := coretest.Provider(a, nil, secrets...)
		params := wire.CloudActionParams{Instance: wire.CloudInstanceRef{Account: "default", ID: g.id()},
			Action: c.kind, RequestID: wire.CloudRequestID(fmt.Sprintf("req-%d", i))}
		res, err := p.Action(context.Background(), params)
		if err != nil {
			t.Fatalf("%v: %v", c, err)
		}
		raw := coretest.Valid(t, "cloud.action", res)
		coretest.NoLeak(t, string(raw), allSecrets(t, a)...)
		if res.Status != wire.CloudActionStatusAccepted {
			t.Errorf("%v: status %s", c, res.Status)
		}
		want := "POST /api2/json/nodes/pve1/" + c.typ + "/100" + c.path
		if m.count(want) != 1 || m.total() != 2 { // the cluster's list (where the guest is), then the action
			t.Errorf("%v: %d calls, want one %s", c, m.total(), want)
		}
		// The same request id again is answered from the record: the node hears of it once.
		if _, err := p.Action(context.Background(), params); err != nil {
			t.Fatalf("%v again: %v", c, err)
		}
		if m.count(want) != 1 {
			t.Errorf("%v: a repeated request id reached the node again", c)
		}
	}
}

func TestUnsupportedActions(t *testing.T) {
	g := &mockGuest{node: "pve1", typ: "lxc", vmid: 200, status: "running"}
	m := newMock(t, g)
	_, a := start(t, m, prodToken())
	ref := wire.CloudInstanceRef{Account: "default", ID: "lxc/200"}
	for _, k := range []wire.CloudActionKind{wire.CloudActionKindHardReset, wire.CloudActionKindCustom, wire.CloudActionKindConsole,
		wire.CloudActionKindSyncIp, wire.CloudActionKindSnapshotList, "fly"} {
		_, err := a.Action(context.Background(), core.Action{Ref: ref, Kind: k, RequestID: "r"})
		if coretest.Code(err) != wire.CloudErrorCodeUnsupported {
			t.Errorf("%s: got %v", k, err)
		}
	}
	p := coretest.Provider(a, nil, secrets...)
	_, err := p.Action(context.Background(), wire.CloudActionParams{Instance: ref, Action: wire.CloudActionKindCustom, Custom: "renew", RequestID: "r2"})
	if coretest.Code(err) != wire.CloudErrorCodeUnsupported {
		t.Errorf("custom through the provider: %v", err)
	}
	if n := m.total(); n != 0 {
		t.Errorf("%d requests of unsupported actions reached the node", n)
	}
}

func TestErrorMapping(t *testing.T) {
	echo := `{"data":null,"message":"token PVEAPIToken=` + prodID + `=` + prodSecret + ` rejected"}`
	cases := []struct {
		status    int
		reason    string
		body      string
		code      wire.CloudErrorCode
		transient bool
	}{
		{401, "authentication failure", echo, wire.CloudErrorCodeNotSignedIn, false},
		{401, "no tokenid", `{"data":null}`, wire.CloudErrorCodeNotSignedIn, false},
		{403, "Permission check failed (/vms/100, VM.PowerMgmt)", echo, wire.CloudErrorCodeNotSignedIn, false},
		{500, "Configuration file 'nodes/pve1/qemu-server/100.conf' does not exist", `{"data":null}`, wire.CloudErrorCodeNotFound, false},
		{500, "no such VM ('100')", `{"data":null}`, wire.CloudErrorCodeNotFound, false},
		{500, "Server Error", `{"data":null,"message":"hostname lookup 'pve9' failed - no such node"}`, wire.CloudErrorCodeNotFound, false},
		{500, "VM 100 not running", `{"data":null}`, wire.CloudErrorCodeInvalidState, false},
		{500, "CT 100 already running", `{"data":null}`, wire.CloudErrorCodeInvalidState, false},
		{500, "VM is locked (backup)", `{"data":null}`, wire.CloudErrorCodeInvalidState, false},
		{500, "CT is locked (snapshot)", echo, wire.CloudErrorCodeInvalidState, false},
		{500, "start failed: not enough memory on node", `{"data":null}`, wire.CloudErrorCodeCapacity, false},
		{429, "Too Many Requests", `{"data":null}`, wire.CloudErrorCodeRateLimited, true},
		{400, "Parameter verification failed.", `{"data":null,"errors":{"snapname":"invalid format - invalid configuration ID 'x y'"}}`, wire.CloudErrorCodeFailed, false},
		// Transient only when the request certainly was not carried out: a node that could not reach the guest's node
		// (595, 596) or a bare 500 is no proof of that.
		{501, "Method 'POST /nodes/pve1/qemu/100/status/fly' not implemented", `{"data":null}`, wire.CloudErrorCodeFailed, false},
		{595, "Errors during connection establishment, proxy handshake failed", `{"data":null}`, wire.CloudErrorCodeFailed, false},
		{596, "Connection timed out", echo, wire.CloudErrorCodeFailed, false},
		{500, "Internal Server Error", echo, wire.CloudErrorCodeFailed, false},
		{500, "Internal Server Error", `{"data":null}`, wire.CloudErrorCodeFailed, false},
		{503, "Service Unavailable", `{"data":null}`, wire.CloudErrorCodeFailed, false},
		// A missing dependency of a guest that is there is not the guest being gone.
		{500, "storage 'local-lvm' does not exist", `{"data":null}`, wire.CloudErrorCodeFailed, false},
		{500, "volume 'local-lvm:vm-100-disk-0' does not exist", `{"data":null}`, wire.CloudErrorCodeFailed, false},
		{500, "start failed: open /dev/kvm: No such file or directory", `{"data":null}`, wire.CloudErrorCodeFailed, false},
		// Proxmox VE's own refusals come as 500 with its words: deterministic, so not transient.
		{500, "VM is paused - cannot shutdown", `{"data":null}`, wire.CloudErrorCodeInvalidState, false},
		{500, "snapshot name 'nightly' already used", `{"data":null}`, wire.CloudErrorCodeRefused, false},
		{500, "unable to use snapshot name 'current' (reserved name)", `{"data":null}`, wire.CloudErrorCodeRefused, false},
		{500, "command 'qm' failed: exit code 1", `{"data":null}`, wire.CloudErrorCodeFailed, false},
		{302, "Found", ``, wire.CloudErrorCodeFailed, false},
	}
	for _, c := range cases {
		m := newMock(t, &mockGuest{node: "pve1", typ: "qemu", vmid: 100, status: "running"})
		m.fail = func(w http.ResponseWriter, r *http.Request) bool {
			if !strings.HasPrefix(r.URL.Path, "/api2/json/nodes/") {
				return false // the cluster's list answers: the node's own call fails
			}
			if c.status == 302 {
				w.Header().Set("Location", "https://elsewhere.example.com/steal")
				w.WriteHeader(302)
				return true
			}
			writeRaw(w, c.status, c.reason, c.body)
			return true
		}
		_, a := start(t, m, prodToken())
		ref := wire.CloudInstanceRef{Account: "default", ID: "qemu/100"}
		_, err := a.Action(context.Background(), core.Action{Ref: ref, Kind: wire.CloudActionKindStop, RequestID: "r"})
		e := core.AsError(err)
		if e == nil || e.Code != c.code || e.Transient != c.transient {
			t.Errorf("%d %q: got %#v, want %s transient=%v", c.status, c.reason, e, c.code, c.transient)
			continue
		}
		if c.reason != "" && c.status != 302 && c.reason != http.StatusText(c.status) && !strings.Contains(e.Message, strings.SplitN(c.reason, " (", 2)[0]) {
			t.Errorf("%d: the vendor's words are missing from %q", c.status, e.Message)
		}
		if strings.Contains(c.body, "snapname") && !strings.Contains(e.Message, "snapname") {
			t.Errorf("the body's errors are missing from %q", e.Message)
		}
		// Through the provider the message is free of credentials, even when the node echoes them.
		p := coretest.Provider(a, nil, secrets...)
		_, perr := p.Action(context.Background(), wire.CloudActionParams{Instance: ref, Action: wire.CloudActionKindStop, RequestID: "p"})
		if coretest.Code(perr) != c.code {
			t.Errorf("%d via provider: %v", c.status, perr)
		}
		coretest.NoLeak(t, perr.Error(), allSecrets(t, a)...)
		coretest.NoLeak(t, err.Error(), prodSecret)
		_, ierr := p.Instance(context.Background(), wire.CloudInstanceParams{Instance: ref})
		if coretest.Code(ierr) != c.code {
			t.Errorf("%d instance via provider: %v", c.status, ierr)
		}
		coretest.NoLeak(t, ierr.Error(), allSecrets(t, a)...)
	}
}

func TestStartReadsNewAddress(t *testing.T) {
	g := &mockGuest{node: "pve1", typ: "qemu", vmid: 100, name: "web", status: "stopped", startIPs: agentIPs("203.0.113.20", "10.0.0.5")}
	m := newMock(t, g)
	_, a := start(t, m, prodToken())
	p := coretest.Provider(a, nil, secrets...)
	ref := wire.CloudInstanceRef{Account: "default", ID: "qemu/100"}
	ctx := context.Background()

	before, err := p.Instance(ctx, wire.CloudInstanceParams{Instance: ref})
	if err != nil || before.State != wire.CloudInstanceStateStopped || len(before.PublicIps) != 0 {
		t.Fatalf("before: %s %v", mustJSON(before), err)
	}
	if res, err := p.Action(ctx, wire.CloudActionParams{Instance: ref, Action: wire.CloudActionKindStart, RequestID: "start-1"}); err != nil || res.Status != wire.CloudActionStatusAccepted {
		t.Fatalf("start: %v %v", res, err)
	}
	after, err := p.Instance(ctx, wire.CloudInstanceParams{Instance: ref})
	if err != nil || after.State != wire.CloudInstanceStateRunning || strings.Join(after.PublicIps, ",") != "203.0.113.20" {
		t.Fatalf("after: %s %v", mustJSON(after), err)
	}
	// The guest gets another address: sync_ip reads it from the node, not from what was read before.
	m.mu.Lock()
	g.agent = agentIPs("198.51.100.7", "10.0.0.5")
	m.mu.Unlock()
	res, err := p.Action(ctx, wire.CloudActionParams{Instance: ref, Action: wire.CloudActionKindSyncIp, RequestID: "sync-1"})
	if err != nil || res.Instance == nil || strings.Join(res.Instance.PublicIps, ",") != "198.51.100.7" {
		t.Fatalf("sync_ip: %s %v", mustJSON(res), err)
	}
	coretest.Valid(t, "cloud.action", res)
	list, err := p.Instances(ctx, wire.CloudInstancesParams{Refresh: true})
	if err != nil || len(list.Instances) != 1 || strings.Join(list.Instances[0].PublicIps, ",") != "198.51.100.7" {
		t.Fatalf("list: %s %v", mustJSON(list), err)
	}
	if n := m.count("POST /api2/json/nodes/pve1/qemu/100/status/start"); n != 1 {
		t.Errorf("%d start calls", n)
	}
	if m.forms["POST /api2/json/nodes/pve1/qemu/100/status/start"] == nil {
		t.Error("the start's form was not sent")
	}
}

func TestSnapshots(t *testing.T) {
	g := &mockGuest{node: "pve1", typ: "qemu", vmid: 100, status: "running", snapshots: []map[string]any{
		{"name": "before-upgrade", "snaptime": 1764000000, "description": "x", "vmstate": 0},
		{"name": "nightly", "snaptime": 1764200000, "parent": "before-upgrade"},
		{"name": "taking", "snaptime": 1764300000, "snapstate": "prepare"},
		{"name": "current", "parent": "taking", "running": 1, "description": "You are here!"},
	}}
	m := newMock(t, g)
	_, a := start(t, m, prodToken())
	p := coretest.Provider(a, nil, secrets...)
	ref := wire.CloudInstanceRef{Account: "default", ID: "qemu/100"}
	ctx := context.Background()

	list, err := p.Snapshots(ctx, wire.CloudSnapshotsParams{Instance: ref})
	if err != nil {
		t.Fatal(err)
	}
	coretest.Valid(t, "cloud.snapshots", list)
	var names []string
	for _, s := range list.Snapshots {
		names = append(names, string(s.ID)+":"+string(s.State))
	}
	if strings.Join(names, ",") != "taking:creating,nightly:ready,before-upgrade:ready" {
		t.Errorf("snapshots %v", names)
	}
	if !list.Snapshots[1].CreatedAt.Equal(time.Unix(1764200000, 0)) {
		t.Errorf("created at %v", list.Snapshots[1].CreatedAt)
	}

	for i, c := range []struct{ asked, want, desc string }{
		{"Before upgrade 2026!", "Before-upgrade-2026", "Before upgrade 2026!"},
		{"", "persona-20261127-100000", "Taken by Persona"},
		{"2026 nightly", "s-2026-nightly", "2026 nightly"},
		{"\u00e9\u00e8", "persona-20261127-100000", "\u00e9\u00e8"}, // nothing of it is allowed
		{"a", "persona-20261127-100000", "a"},                       // a name needs 2 characters
		{"current", "persona-20261127-100000", "current"},           // reserved
		{"Pending", "persona-20261127-100000", "Pending"},           // reserved, in any case
		{"ab", "ab", "ab"},
		{strings.Repeat("a", 60), strings.Repeat("a", 40), strings.Repeat("a", 60)},
	} {
		res, err := p.Action(ctx, wire.CloudActionParams{Instance: ref, Action: wire.CloudActionKindSnapshotCreate, Name: c.asked,
			RequestID: wire.CloudRequestID(fmt.Sprintf("snap-%d", i))})
		if err != nil {
			t.Fatalf("%q: %v", c.asked, err)
		}
		coretest.Valid(t, "cloud.action", res)
		if res.Status != wire.CloudActionStatusAccepted || res.Snapshot == nil || string(res.Snapshot.ID) != c.want ||
			res.Snapshot.State != wire.CloudSnapshotStateCreating {
			t.Errorf("%q: %s", c.asked, mustJSON(res))
		}
		m.mu.Lock()
		form := m.forms["POST /api2/json/nodes/pve1/qemu/100/snapshot"]
		m.mu.Unlock()
		if form.Get("snapname") != c.want || form.Get("description") != c.desc {
			t.Errorf("%q: form %v", c.asked, form)
		}
	}

	// A snapshot the node refuses (an LXC with a bind mount, say) is the node's words, mapped.
	m.fail = func(w http.ResponseWriter, r *http.Request) bool {
		if r.Method == http.MethodPost {
			writeRaw(w, 500, "VM is locked (snapshot)", `{"data":null}`)
			return true
		}
		return false
	}
	_, err = p.Action(ctx, wire.CloudActionParams{Instance: ref, Action: wire.CloudActionKindSnapshotCreate, RequestID: "snap-locked"})
	if coretest.Code(err) != wire.CloudErrorCodeInvalidState {
		t.Errorf("locked snapshot: %v", err)
	}
	if _, err := p.Snapshots(ctx, wire.CloudSnapshotsParams{Instance: wire.CloudInstanceRef{Account: "default", ID: "qemu/555"}}); coretest.Code(err) != wire.CloudErrorCodeNotFound {
		t.Errorf("snapshots of a missing guest: %v", err)
	}
}

func TestConsole(t *testing.T) {
	m := newMock(t,
		&mockGuest{node: "pve1", typ: "qemu", vmid: 100, status: "running"},
		&mockGuest{node: "pve-2", typ: "lxc", vmid: 200, status: "stopped"})
	srv, a := start(t, m, prodToken())
	p := coretest.Provider(a, nil, secrets...)
	for id, want := range map[wire.CloudInstanceID]string{
		"qemu/100": srv.URL + "/?console=kvm&node=pve1&novnc=1&vmid=100",
		"lxc/200":  srv.URL + "/?console=lxc&node=pve-2&novnc=1&vmid=200",
	} {
		ref := wire.CloudInstanceRef{Account: "default", ID: id}
		res, err := p.Action(context.Background(), wire.CloudActionParams{Instance: ref, Action: wire.CloudActionKindConsole, RequestID: "c"})
		if err != nil {
			t.Fatal(err)
		}
		raw := coretest.Valid(t, "cloud.action", res)
		coretest.NoLeak(t, string(raw), allSecrets(t, a)...)
		if res.Status != wire.CloudActionStatusDone || res.URL != want {
			t.Errorf("%s: %q, want %q", id, res.URL, want)
		}
		u, err := url.Parse(res.URL)
		if err != nil || u.Scheme != "https" || u.User != nil || u.Query().Has("ticket") || u.Query().Has("token") {
			t.Errorf("%s: link %q", id, res.URL)
		}
	}
}

func TestMigratedGuest(t *testing.T) {
	g := &mockGuest{node: "pve1", typ: "qemu", vmid: 100, name: "web", status: "running", agent: agentIPs("203.0.113.10")}
	m := newMock(t, g)
	_, a := start(t, m, prodToken())
	p := coretest.Provider(a, nil, secrets...)
	ref := wire.CloudInstanceRef{Account: "default", ID: "qemu/100"}
	ctx := context.Background()

	in, err := p.Instance(ctx, wire.CloudInstanceParams{Instance: ref})
	if err != nil || in.Zone != "pve1" {
		t.Fatalf("before: %s %v", mustJSON(in), err)
	}
	// An HA relocation moves it: the same id still names it, now on pve2.
	m.mu.Lock()
	m.move(g, "pve2")
	m.mu.Unlock()
	in, err = p.Instance(ctx, wire.CloudInstanceParams{Instance: ref})
	if err != nil || in.ID != "qemu/100" || in.Zone != "pve2" || in.State != wire.CloudInstanceStateRunning ||
		strings.Join(in.PublicIps, ",") != "203.0.113.10" {
		t.Fatalf("after the move: %s %v", mustJSON(in), err)
	}
	coretest.Valid(t, "cloud.instance", in)
	if _, err := p.Action(ctx, wire.CloudActionParams{Instance: ref, Action: wire.CloudActionKindReboot, RequestID: "reboot-1"}); err != nil {
		t.Fatal(err)
	}
	if m.count("POST /api2/json/nodes/pve2/qemu/100/status/reboot") != 1 {
		t.Error("the reboot did not go to the guest's new node")
	}

	// It moves again between the lookup and the call: the node it left says its configuration is not there, the
	// guest is looked up once more and asked on its new node.
	moved := false
	m.fail = func(w http.ResponseWriter, r *http.Request) bool {
		if !moved && r.URL.Path == "/api2/json/nodes/pve2/qemu/100/status/current" {
			moved = true
			m.move(g, "pve3")
			writeRaw(w, 500, "Configuration file 'nodes/pve2/qemu-server/100.conf' does not exist", `{"data":null}`)
			return true
		}
		return false
	}
	in, err = p.Instance(ctx, wire.CloudInstanceParams{Instance: ref})
	if err != nil || in.Zone != "pve3" || in.ID != "qemu/100" {
		t.Fatalf("moved during the read: %s %v", mustJSON(in), err)
	}
	if n := m.count("GET /api2/json/nodes/pve3/qemu/100/status/current"); n != 1 {
		t.Errorf("%d reads on the new node", n)
	}
	snaps, err := p.Snapshots(ctx, wire.CloudSnapshotsParams{Instance: ref})
	if err != nil {
		t.Fatal(err)
	}
	coretest.Valid(t, "cloud.snapshots", snaps)
	if m.count("GET /api2/json/nodes/pve3/qemu/100/snapshot") != 1 {
		t.Error("the snapshots were not read on the guest's node")
	}
}

func TestAddressPrivilege(t *testing.T) {
	for _, c := range []struct {
		typ, path, want string
	}{
		{"qemu", "/agent/network-get-interfaces", "VM.GuestAgent.Audit"},
		{"lxc", "/interfaces", "VM.Audit"},
	} {
		g := &mockGuest{node: "pve1", typ: c.typ, vmid: 100, status: "running", agent: agentIPs("203.0.113.10")}
		m := newMock(t, g)
		m.fail = func(w http.ResponseWriter, r *http.Request) bool {
			if strings.HasSuffix(r.URL.Path, c.path) {
				writeRaw(w, 403, "Permission check failed (/vms/100, VM.GuestAgent.Audit)", `{"data":null}`)
				return true
			}
			return false
		}
		_, a := start(t, m, prodToken())
		p := coretest.Provider(a, nil, secrets...)
		ref := wire.CloudInstanceRef{Account: "default", ID: g.id()}
		res, err := p.Action(context.Background(), wire.CloudActionParams{Instance: ref, Action: wire.CloudActionKindSyncIp, RequestID: "s"})
		if err != nil || res.Instance == nil {
			t.Fatalf("%s: %v", c.typ, err)
		}
		coretest.Valid(t, "cloud.action", res)
		if res.Instance.State != wire.CloudInstanceStateRunning || len(res.Instance.PublicIps) != 0 ||
			!strings.Contains(res.Instance.StateDetail, c.want) {
			t.Errorf("%s: %s", c.typ, mustJSON(res.Instance))
		}
		list, err := p.Instances(context.Background(), wire.CloudInstancesParams{Refresh: true})
		if err != nil || len(list.Instances) != 1 || !strings.Contains(list.Instances[0].StateDetail, c.want) {
			t.Errorf("%s list: %s %v", c.typ, mustJSON(list), err)
		}
	}
	// A guest without an agent has no addresses and nothing to say about it.
	g := &mockGuest{node: "pve1", typ: "qemu", vmid: 100, status: "running"}
	_, a := start(t, newMock(t, g), prodToken())
	in, err := a.Instance(context.Background(), wire.CloudInstanceRef{Account: "default", ID: g.id()})
	if err != nil || in.StateDetail != "" || len(in.PublicIps) != 0 {
		t.Errorf("no agent: %s %v", mustJSON(in), err)
	}
}

func TestValidNode(t *testing.T) {
	for _, ok := range []string{"pve1", "pve-2", "PVE3", strings.Repeat("a", 63)} {
		if !validNode(ok) {
			t.Errorf("%q refused", ok)
		}
	}
	for _, bad := range []string{"", ".", "..", "-pve", "pve-", "pve.example.com", "pve_1", "a/b", "pve 1", strings.Repeat("a", 64)} {
		if validNode(bad) {
			t.Errorf("%q accepted", bad)
		}
	}
	// A cluster entry with a node name that is not one is left out of the list.
	m := newMock(t, &mockGuest{node: "..", typ: "qemu", vmid: 100, status: "stopped"},
		&mockGuest{node: "pve1", typ: "qemu", vmid: 101, status: "stopped"})
	_, a := start(t, m, prodToken())
	list, _, err := a.Instances(context.Background())
	if err != nil || len(list) != 1 || list[0].ID != "qemu/101" {
		t.Errorf("list: %s %v", mustJSON(list), err)
	}
	if _, err := a.Instance(context.Background(), wire.CloudInstanceRef{Account: "default", ID: "qemu/100"}); coretest.Code(err) != wire.CloudErrorCodeNotFound {
		t.Errorf("a guest on a bad node name: %v", err)
	}
}

func TestSharedAddressSpaceIsPrivate(t *testing.T) {
	// The shared address space (RFC 6598, carrier-grade NAT and overlay networks) is not reachable from outside.
	if ip := cgnat.Addr().Next(); !isPrivate(ip) {
		t.Errorf("%s classed public", ip)
	}
	for _, s := range []string{"10.0.0.5", "172.16.0.1", "192.168.1.20"} {
		if !isPrivate(netip.MustParseAddr(s)) {
			t.Errorf("%s classed public", s)
		}
	}
	for _, s := range []string{"203.0.113.10", "198.51.100.7", "2001:db8::20"} {
		if isPrivate(netip.MustParseAddr(s)) {
			t.Errorf("%s classed private", s)
		}
	}
}

func TestOddIDsAskNothing(t *testing.T) {
	// An instance id is "<qemu or lxc>/<vmid>": one of another shape is not_found before any request, so no part of it
	// ever reaches a path.
	m := newMock(t, &mockGuest{node: "pve1", typ: "qemu", vmid: 100, status: "running"})
	_, a := start(t, m, prodToken())
	p := coretest.Provider(a, nil, secrets...)
	ctx := context.Background()
	for i, id := range []wire.CloudInstanceID{"qemu/1 0%2F/x", "qemu/100 ", "qemu/10%30", "qemu/a b", "lxc/../100", "qemu//100"} {
		ref := wire.CloudInstanceRef{Account: "default", ID: id}
		if _, err := p.Instance(ctx, wire.CloudInstanceParams{Instance: ref}); coretest.Code(err) != wire.CloudErrorCodeNotFound {
			t.Errorf("%q instance: %v", id, err)
		}
		if _, err := p.Snapshots(ctx, wire.CloudSnapshotsParams{Instance: ref}); coretest.Code(err) != wire.CloudErrorCodeNotFound {
			t.Errorf("%q snapshots: %v", id, err)
		}
		if _, err := p.Action(ctx, wire.CloudActionParams{Instance: ref, Action: wire.CloudActionKindStop, RequestID: wire.CloudRequestID("odd-" + strconv.Itoa(i))}); coretest.Code(err) != wire.CloudErrorCodeNotFound {
			t.Errorf("%q action: %v", id, err)
		}
	}
	if n := m.total(); n != 0 {
		t.Errorf("%d requests for ids of another shape", n)
	}
}

func TestEscapedNodeInPath(t *testing.T) {
	// The node comes from the vendor's answer and is escaped into the path; validNode already keeps out anything that
	// would need escaping, so the path is the node as it is.
	r := ref{node: "pve-1", typ: "qemu", vmid: 100}
	if got := r.path("/status/current"); got != "nodes/pve-1/qemu/100/status/current" {
		t.Errorf("path %q", got)
	}
	u, err := core.JoinURL("https://pve.example.com:8006", "/api2/json/"+ref{node: "a b%/c", typ: "qemu", vmid: 1}.path(""), nil)
	if err != nil || u.EscapedPath() != "/api2/json/nodes/a%20b%25%2Fc/qemu/1" {
		t.Errorf("escaped path %v %v", u, err)
	}
}

func TestChineseDetails(t *testing.T) {
	m := newMock(t, &mockGuest{node: "pve1", typ: "qemu", vmid: 100, status: "running", qmp: "paused"},
		&mockGuest{node: "pve1", typ: "qemu", vmid: 101, status: "running", lock: "backup"})
	_, a := start(t, m, prodToken())
	p := coretest.Provider(a, nil, secrets...)
	ctx := context.Background()
	in, err := p.Instance(ctx, wire.CloudInstanceParams{Locale: "zh-CN", Instance: wire.CloudInstanceRef{Account: "default", ID: "qemu/100"}})
	want := core.T("zh-CN", "", "已在 Proxmox VE 中暂停（QEMU 状态：paused），请在 Proxmox VE 中恢复。")
	if err != nil || in.StateDetail != want {
		t.Errorf("paused, zh: %q %v", in.StateDetail, err)
	}
	en, err := p.Instance(ctx, wire.CloudInstanceParams{Locale: "en", Instance: wire.CloudInstanceRef{Account: "default", ID: "qemu/100"}})
	if err != nil || en.StateDetail != "Paused in Proxmox VE (QEMU reports paused): resume it there." {
		t.Errorf("paused, en: %q %v", en.StateDetail, err)
	}
	list, err := p.Instances(ctx, wire.CloudInstancesParams{Locale: "zh-CN", Refresh: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, in := range list.Instances {
		if in.ID == "qemu/101" && !strings.Contains(in.StateDetail, core.T("zh-CN", "", "已被 Proxmox VE 锁定（backup）。")) {
			t.Errorf("locked, zh: %q", in.StateDetail)
		}
	}
	_, err = p.Instance(ctx, wire.CloudInstanceParams{Locale: "zh-CN", Instance: wire.CloudInstanceRef{Account: "default", ID: "qemu/555"}})
	if coretest.Code(err) != wire.CloudErrorCodeNotFound || err.Error() != core.T("zh-CN", "", "Proxmox VE 中没有这台虚拟机或容器。") {
		t.Errorf("not found, zh: %v", err)
	}
}

func TestLongErrorNotClipped(t *testing.T) {
	// The adapter does not clip the vendor's words: the Provider redacts first, then clips, so an echoed credential
	// near the cut is still removed whole.
	long := strings.Repeat("x", 250) + " PVEAPIToken=" + prodID + "=" + prodSecret
	m := newMock(t, &mockGuest{node: "pve1", typ: "qemu", vmid: 100, status: "running"})
	m.fail = func(w http.ResponseWriter, r *http.Request) bool {
		if r.Method == http.MethodPost {
			writeRaw(w, 500, "start failed", `{"data":null,"message":"`+long+`"}`)
			return true
		}
		return false
	}
	_, a := start(t, m, prodToken())
	ref := wire.CloudInstanceRef{Account: "default", ID: "qemu/100"}
	_, err := a.Action(context.Background(), core.Action{Ref: ref, Kind: wire.CloudActionKindStop, RequestID: "r"})
	if e := core.AsError(err); e == nil || len(e.Message) < 250 || strings.HasSuffix(e.Message, "…") {
		t.Errorf("clipped: %v", err)
	}
	p := coretest.Provider(a, nil, secrets...)
	_, perr := p.Action(context.Background(), wire.CloudActionParams{Instance: ref, Action: wire.CloudActionKindStop, RequestID: "p"})
	coretest.NoLeak(t, perr.Error(), allSecrets(t, a)...)
	for n := 6; n < len(prodSecret); n++ {
		if strings.Contains(perr.Error(), prodSecret[:n]) {
			t.Fatalf("a part of the secret survived: %q", perr.Error())
		}
	}
}
