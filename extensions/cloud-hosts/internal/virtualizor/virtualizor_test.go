package virtualizor

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
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona-extensions/extensions/cloud-hosts/internal/core"
	"github.com/nwverse/persona-extensions/extensions/cloud-hosts/internal/coretest"
	"github.com/nwverse/persona/sdk/go/wire"
)

var fixedNow = time.Date(2026, 11, 27, 8, 30, 0, 0, time.UTC)

// Synthetic credentials of the mock panel.
const (
	keyMain  = "TESTAPIKEY0001MAINXXXXXXXXXXXXX"
	passMain = "testapipass0001mainxxxxxxxxxxxx"
	keyAlt   = "TESTAPIKEY0002ALTXXXXXXXXXXXXXX"
	passAlt  = "testapipass0002altxxxxxxxxxxxxx"
	keyDead  = "TESTAPIKEY0003DEADXXXXXXXXXXXXX"
	passDead = "testapipass0003deadxxxxxxxxxxxx"
)

var secrets = []string{keyMain, passMain, keyAlt, passAlt, keyDead, passDead}

func allCreds() string {
	return "main=" + keyMain + ":" + passMain + "\nalt=" + keyAlt + ":" + passAlt + "\ndead=" + keyDead + ":" + passDead
}

type mockVPS struct {
	id          int
	hostname    string
	status      int
	ips         []string
	suspended   string
	nwSuspended string
	nextIP      string // the first address after the next start
	actionError string // a power act answers {"error": [this]}
}

func (v *mockVPS) json() string {
	ips := []string{}
	for i, ip := range v.ips {
		ips = append(ips, fmt.Sprintf(`"%d": %q`, 10+i, ip))
	}
	nw := "null"
	if v.nwSuspended != "" {
		nw = fmt.Sprintf("%q", v.nwSuspended)
	}
	return fmt.Sprintf(`{"vpsid": "%d", "vps_name": "v%d", "uuid": "abcd%d", "serid": "0", "time": "1700000000",
		"edittime": "1700000000", "virt": "kvm", "uid": "0", "plid": "1", "hostname": %q, "osid": "100",
		"os_name": "ubuntu-24.04-x86_64", "iso": "", "space": "40", "inodes": "0", "ram": "2048", "burst": "0",
		"swap": "0", "cpu": "1000", "cores": "2", "cpupin": "-1", "cpu_percent": "0.00", "bandwidth": "1000",
		"network_speed": "0", "upload_speed": "0", "status": %d, "suspended": %q, "nw_suspended": %s,
		"ips": {%s}, "region": "Example Region"}`,
		v.id, v.id, v.id, v.hostname, v.status, v.suspended, nw, strings.Join(ips, ","))
}

type mock struct {
	t       *testing.T
	mu      sync.Mutex
	logins  map[string]string     // key -> pass
	vps     map[string][]*mockVPS // by key
	calls   []string              // "METHOD act svs"
	raw     func(w http.ResponseWriter, act string) bool
	noPages bool // the panel ignores page and reslen
	empty   bool // listvs answers "vs": []
	maxLen  int  // the panel caps reslen at this and answers its page object {start, len, maxNum}
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

func (m *mock) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	t := m.t
	w.Header().Set("Content-Type", "application/json")
	q := r.URL.Query()
	if r.URL.Path != "/index.php" || q.Get("api") != "json" {
		t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		http.NotFound(w, r)
		return
	}
	act, key, pass := q.Get("act"), q.Get("apikey"), q.Get("apipass")
	write := act == "start" || act == "stop" || act == "restart" || act == "poweroff"
	if write && (r.Method != http.MethodPost || q.Get("do") != "1") {
		t.Errorf("%s sent as %s with do=%q", act, r.Method, q.Get("do"))
	}
	if !write && r.Method != http.MethodGet {
		t.Errorf("%s sent as %s", act, r.Method)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, strings.TrimSpace(r.Method+" "+act+" "+q.Get("svs")))
	if m.raw != nil && m.raw(w, act) {
		return
	}
	if want, ok := m.logins[key]; !ok || want != pass {
		fmt.Fprint(w, `{"uid": -1, "act": "login", "title": "Login", "timenow": 1700000000}`)
		return
	}
	list := m.vps[key]
	switch act {
	case "listvs":
		if m.empty {
			fmt.Fprint(w, `{"title": "List VPS", "vs": [], "act": "listvs"}`)
			return
		}
		page, reslen := 1, len(list)
		if !m.noPages {
			fmt.Sscan(q.Get("page"), &page)
			fmt.Sscan(q.Get("reslen"), &reslen)
			if q.Get("page") == "" || q.Get("reslen") == "" {
				t.Errorf("listvs without paging: %v", q)
			}
		}
		if m.maxLen > 0 && reslen > m.maxLen {
			reslen = m.maxLen
		}
		var items []string
		for i := (page - 1) * reslen; i < page*reslen && i < len(list); i++ {
			items = append(items, fmt.Sprintf(`"%d": %s`, list[i].id, list[i].json()))
		}
		pageInfo := fmt.Sprint(page)
		if m.maxLen > 0 {
			pageInfo = fmt.Sprintf(`{"start": %d, "len": %d, "maxNum": %d}`, (page-1)*reslen, reslen, len(list))
		}
		fmt.Fprintf(w, `{"title": "List VPS", "uid": 7, "vs": {%s}, "act": "listvs", "page": %s}`, strings.Join(items, ","), pageInfo)
		return
	}
	var v *mockVPS
	for _, c := range list {
		if fmt.Sprint(c.id) == q.Get("svs") {
			v = c
		}
	}
	if v == nil {
		fmt.Fprint(w, `{"error": ["The VPS was not found"], "act": "`+act+`"}`)
		return
	}
	switch act {
	case "vpsmanage":
		fmt.Fprintf(w, `{"title": "VPS Management", "info": {"vpsid": "%d", "hostname": %q, "status": %d,
			"ip": %s, "flags": {"vnc": true}, "vps": %s}, "act": "vpsmanage"}`, v.id, v.hostname, v.status, mustJSON(v.ips), v.json())
	case "start", "stop", "restart", "poweroff":
		if v.actionError != "" {
			fmt.Fprintf(w, `{"error": {"%s": %q}, "act": %q}`, act, v.actionError, act)
			return
		}
		switch act {
		case "start":
			v.status = 1
			if v.nextIP != "" {
				v.ips[0] = v.nextIP
			}
			fmt.Fprint(w, `{"done": {"msg": "Start signal has been sent to the VPS"}, "act": "start"}`)
		case "stop", "poweroff":
			v.status = 0
			fmt.Fprintf(w, `{"done": true, "act": %q}`, act)
		default:
			fmt.Fprint(w, `{"done": "1", "act": "restart"}`)
		}
	default:
		fmt.Fprint(w, `{"error": ["Unknown act"]}`)
	}
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func newMock(t *testing.T) (*mock, *httptest.Server) {
	m := &mock{t: t, logins: map[string]string{keyMain: passMain, keyAlt: passAlt, keyDead: "a-different-pass-0000"},
		vps: map[string][]*mockVPS{
			keyMain: {
				{id: 101, hostname: "kvm1.example.com", status: 1, ips: []string{"203.0.113.50", "2001:db8:7::50"}, suspended: "0"},
				{id: 102, hostname: "nat1.example.com", status: 0, ips: []string{"172.16.5.2", "198.51.100.5"}, suspended: "0"},
				{id: 103, hostname: "late.example.com", status: 1, ips: []string{"192.0.2.77"}, suspended: "1"},
			},
			keyAlt: {
				{id: 201, hostname: "noisy.example.org", status: 1, ips: []string{"192.168.40.9"}, suspended: "0", nwSuspended: "1"},
			},
		}}
	srv := httptest.NewUnstartedServer(m)
	srv.Config.ErrorLog = log.New(io.Discard, "", 0) // the wrong-pin test's refused handshake
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return m, srv
}

func newAdapter(t *testing.T, srv *httptest.Server, creds string) core.Adapter {
	t.Helper()
	a, err := New(Config{BaseURL: srv.URL, Credentials: creds, HTTP: srv.Client(), Now: func() time.Time { return fixedNow }})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestConfig(t *testing.T) {
	a, err := New(Config{BaseURL: "https://panel.example.com:4083", Credentials: keyMain + ":" + passMain})
	if err != nil {
		t.Fatal(err)
	}
	ad := a.(*adapter)
	if len(ad.accounts) != 1 || ad.accounts[0].name != "default" || ad.accounts[0].key != keyMain || ad.accounts[0].pass != passMain {
		t.Fatalf("%+v", ad.accounts)
	}
	if ad.console != "https://panel.example.com:4083" {
		t.Fatalf("console %q", ad.console)
	}
	a, err = New(Config{BaseURL: "https://panel.example.com:4083", Credentials: allCreds()})
	if err != nil {
		t.Fatal(err)
	}
	if got := a.(core.SecretHolder).SecretValues(); strings.Join(got, ",") != strings.Join(secrets, ",") {
		t.Fatalf("secrets %v", got)
	}
	for _, bad := range []string{"", "# none", keyMain, "main=" + keyMain + ":", ":" + passMain} {
		_, err := New(Config{BaseURL: "https://panel.example.com:4083", Credentials: bad})
		if coretest.Code(err) != wire.CloudErrorCodeNotSignedIn {
			t.Errorf("%q: %v", bad, err)
		}
		if err != nil {
			coretest.NoLeak(t, err.Error(), secrets...)
		}
	}
	if _, err := New(Config{BaseURL: "panel", Credentials: keyMain + ":" + passMain}); err == nil {
		t.Fatal("a bad base was accepted")
	}
}

func TestInstances(t *testing.T) {
	m, srv := newMock(t)
	p := coretest.Provider(newAdapter(t, srv, allCreds()), nil, secrets...)
	list, err := p.Instances(context.Background(), wire.CloudInstancesParams{Locale: "en", Refresh: true})
	if err != nil {
		t.Fatal(err)
	}
	raw := coretest.Valid(t, "cloud.instances", list)
	coretest.NoLeak(t, string(raw), secrets...)
	if !list.Partial || !strings.Contains(list.Detail, "dead") || len(list.Instances) != 4 {
		t.Fatalf("list %s", raw)
	}
	if m.count("GET listvs") != 3 {
		t.Errorf("calls %v", m.calls)
	}
	by := map[string]wire.CloudInstance{}
	for _, in := range list.Instances {
		by[string(in.Account)+"/"+string(in.ID)] = in
	}
	kvm := by["main/101"]
	if kvm.State != wire.CloudInstanceStateRunning || kvm.DisplayName != "kvm1.example.com" || kvm.Region != "Example Region" ||
		strings.Join(kvm.PublicIps, ",") != "203.0.113.50,2001:db8:7::50" || len(kvm.PrivateIps) != 0 {
		t.Errorf("kvm %+v", kvm)
	}
	if kvm.Spec == nil || *kvm.Spec != (wire.CloudSpec{Cpu: 2, MemoryMb: 2048, DiskGb: 40, OS: "ubuntu-24.04-x86_64"}) {
		t.Errorf("kvm spec %+v", kvm.Spec)
	}
	if got := fmt.Sprint(kvm.Actions); got != "[stop reboot force_stop sync_ip console]" || kvm.ConsoleURL != srv.URL {
		t.Errorf("kvm actions %s console %q", got, kvm.ConsoleURL)
	}
	nat := by["main/102"]
	if nat.State != wire.CloudInstanceStateStopped || strings.Join(nat.PrivateIps, ",") != "172.16.5.2" ||
		strings.Join(nat.PublicIps, ",") != "198.51.100.5" || fmt.Sprint(nat.Actions) != "[start sync_ip console]" {
		t.Errorf("nat %+v", nat)
	}
	if late := by["main/103"]; late.StateDetail == "" || fmt.Sprint(late.Actions) != "[sync_ip console]" {
		t.Errorf("suspended %+v", late)
	}
	if noisy := by["alt/201"]; noisy.State != wire.CloudInstanceStateRunning || noisy.StateDetail == "" ||
		fmt.Sprint(noisy.Actions) != "[sync_ip console]" || strings.Join(noisy.PrivateIps, ",") != "192.168.40.9" {
		t.Errorf("network suspended %+v", noisy)
	}

	// Only a failing login: the error itself.
	_, err = coretest.Provider(newAdapter(t, srv, keyDead+":"+passDead), nil, secrets...).Instances(context.Background(), wire.CloudInstancesParams{Refresh: true})
	if coretest.Code(err) != wire.CloudErrorCodeNotSignedIn {
		t.Fatalf("dead only: %v", err)
	}
	coretest.NoLeak(t, err.Error(), secrets...)
}

func TestListPaging(t *testing.T) {
	m, srv := newMock(t)
	var many []*mockVPS
	for i := range 120 {
		many = append(many, &mockVPS{id: 1000 + i, hostname: fmt.Sprintf("s%d.example.com", i), status: i % 2,
			ips: []string{fmt.Sprintf("198.51.100.%d", i+1)}, suspended: "0"})
	}
	m.vps[keyMain] = many
	a := newAdapter(t, srv, keyMain+":"+passMain)
	list, _, err := a.Instances(context.Background())
	if err != nil || len(list) != 120 || m.count("GET listvs") != 3 {
		t.Fatalf("%d %v %d", len(list), err, m.count("GET listvs"))
	}
	if list[0].ID != "1000" || list[119].ID != "1119" {
		t.Fatalf("order %s %s", list[0].ID, list[119].ID)
	}
	// A panel that ignores paging answers everything every time: read once, kept once.
	m.mu.Lock()
	m.calls, m.noPages = nil, true
	m.mu.Unlock()
	list, _, err = a.Instances(context.Background())
	if err != nil || len(list) != 120 || m.count("GET listvs") != 2 {
		t.Fatalf("no paging: %d %v %d", len(list), err, m.count("GET listvs"))
	}
	// A panel that caps the page length below what was asked, and says so in its page object: every page is read.
	m.mu.Lock()
	m.calls, m.noPages, m.maxLen = nil, false, 20
	m.mu.Unlock()
	list, _, err = a.Instances(context.Background())
	if err != nil || len(list) != 120 || m.count("GET listvs") != 6 {
		t.Fatalf("capped pages: %d %v %d", len(list), err, m.count("GET listvs"))
	}
	// No servers: "vs": [].
	m.mu.Lock()
	m.empty = true
	m.mu.Unlock()
	list, problems, err := a.Instances(context.Background())
	if err != nil || len(list) != 0 || len(problems) != 0 {
		t.Fatalf("empty: %v %v %v", list, problems, err)
	}
}

func TestStateMapping(t *testing.T) {
	a := &adapter{console: "https://panel.example.com:4083", now: func() time.Time { return fixedNow }}
	acct := account{name: "default"}
	for _, tc := range []struct {
		raw     string
		state   wire.CloudInstanceState
		actions string
		detail  bool
	}{
		{`{"vpsid": "1", "status": 1}`, wire.CloudInstanceStateRunning, "[stop reboot force_stop sync_ip console]", false},
		{`{"vpsid": "1", "status": "1", "suspended": "0", "nw_suspended": null}`, wire.CloudInstanceStateRunning, "[stop reboot force_stop sync_ip console]", false},
		{`{"vpsid": "1", "status": 0}`, wire.CloudInstanceStateStopped, "[start sync_ip console]", false},
		{`{"vpsid": "1", "status": 0, "suspended": "1"}`, wire.CloudInstanceStateStopped, "[sync_ip console]", true},
		{`{"vpsid": "1", "status": 1, "nw_suspended": 1}`, wire.CloudInstanceStateRunning, "[sync_ip console]", true},
		{`{"vpsid": "1", "status": 2}`, wire.CloudInstanceStateStopped, "[sync_ip console]", true},
		{`{"vpsid": "1", "status": "2", "suspended": "0"}`, wire.CloudInstanceStateStopped, "[sync_ip console]", true},
		{`{"vpsid": "1", "status": "online"}`, wire.CloudInstanceStateRunning, "[stop reboot force_stop sync_ip console]", false},
		{`{"vpsid": "1", "status": "offline"}`, wire.CloudInstanceStateStopped, "[start sync_ip console]", false},
		{`{"vpsid": "1", "status": "suspended"}`, wire.CloudInstanceStateStopped, "[sync_ip console]", true},
		{`{"vpsid": "1", "status": 7}`, wire.CloudInstanceStateUnknown, "[sync_ip console]", true},
		{`{"vpsid": "1"}`, wire.CloudInstanceStateUnknown, "[sync_ip console]", false},
	} {
		var v vs
		if err := json.Unmarshal([]byte(tc.raw), &v); err != nil {
			t.Fatal(err)
		}
		in := a.instance("en", acct, v)
		if in.State != tc.state || fmt.Sprint(in.Actions) != tc.actions || (in.StateDetail != "") != tc.detail {
			t.Errorf("%s: %s %v %q", tc.raw, in.State, in.Actions, in.StateDetail)
		}
	}
	// Addresses as a list, and as a map read in id order.
	for raw, want := range map[string]string{
		`["10.0.0.9", "203.0.113.9"]`:                        "203.0.113.9|10.0.0.9",
		`{"12": "203.0.113.12", "2": "203.0.113.2"}`:         "203.0.113.2,203.0.113.12|",
		`{"a": "198.51.100.1", "1": "not-an-ip", "0": "::"}`: "198.51.100.1|",
	} {
		in := a.instance("en", acct, vs{VPSID: "1", IPs: json.RawMessage(raw)})
		if got := strings.Join(in.PublicIps, ",") + "|" + strings.Join(in.PrivateIps, ","); got != want {
			t.Errorf("%s: %s, want %s", raw, got, want)
		}
	}
	// A single "ip" (the documented listvs entry) is used when there is no "ips".
	for raw, want := range map[string]string{
		`{"vpsid": "1", "ip": "203.0.113.10"}`:                            "203.0.113.10|",
		`{"vpsid": "1", "ip": ["10.1.2.3", "198.51.100.3"]}`:              "198.51.100.3|10.1.2.3",
		`{"vpsid": "1", "ips": {"1": "192.0.2.1"}, "ip": "198.51.100.1"}`: "192.0.2.1|",
		`{"vpsid": "1", "ips": [], "ip": "2001:db8::5"}`:                  "2001:db8::5|",
	} {
		var v vs
		if err := json.Unmarshal([]byte(raw), &v); err != nil {
			t.Fatal(err)
		}
		in := a.instance("en", acct, v)
		if got := strings.Join(in.PublicIps, ",") + "|" + strings.Join(in.PrivateIps, ","); got != want {
			t.Errorf("%s: %s, want %s", raw, got, want)
		}
	}
	// Numbers JSON cannot carry are no numbers: the answer still encodes.
	var odd vs
	if err := json.Unmarshal([]byte(`{"vpsid": "1", "status": 1, "cores": "NaN", "ram": "+Inf", "space": "-Inf"}`), &odd); err != nil {
		t.Fatal(err)
	}
	if in := a.instance("en", acct, odd); in.Spec != nil {
		t.Errorf("non-finite spec %+v", in.Spec)
	} else if _, err := json.Marshal(in); err != nil {
		t.Errorf("encode: %v", err)
	}
	// A plain http panel has no console link.
	a.console = core.ConsoleBase("http://panel.example.com:4082")
	if in := a.instance("en", acct, vs{VPSID: "1", Status: "1"}); in.ConsoleURL != "" || core.Offers(in.Actions, wire.CloudActionKindConsole) {
		t.Errorf("http panel %+v", in)
	}
}

func TestActions(t *testing.T) {
	m, srv := newMock(t)
	p := coretest.Provider(newAdapter(t, srv, allCreds()), nil, secrets...)
	ctx := context.Background()
	kvm := wire.CloudInstanceRef{Account: "main", ID: "101"}
	nat := wire.CloudInstanceRef{Account: "main", ID: "102"}
	for i, tc := range []struct {
		ref  wire.CloudInstanceRef
		kind wire.CloudActionKind
		call string
	}{
		{kvm, wire.CloudActionKindReboot, "POST restart 101"},
		{kvm, wire.CloudActionKindStop, "POST stop 101"},
		{kvm, wire.CloudActionKindForceStop, "POST poweroff 101"},
		{nat, wire.CloudActionKindStart, "POST start 102"},
	} {
		res, err := p.Action(ctx, wire.CloudActionParams{Instance: tc.ref, Action: tc.kind, RequestID: wire.CloudRequestID(fmt.Sprint("r", i))})
		if err != nil {
			t.Fatalf("%s: %v", tc.kind, err)
		}
		raw := coretest.Valid(t, "cloud.action", res)
		coretest.NoLeak(t, string(raw), secrets...)
		m.mu.Lock()
		last := m.calls[len(m.calls)-1]
		m.mu.Unlock()
		if res.Status != wire.CloudActionStatusAccepted || last != tc.call {
			t.Fatalf("%s: %s, call %q", tc.kind, raw, last)
		}
	}
	for range 3 {
		if _, err := p.Action(ctx, wire.CloudActionParams{Instance: kvm, Action: wire.CloudActionKindReboot, RequestID: "same"}); err != nil {
			t.Fatal(err)
		}
	}
	if n := m.count("POST restart 101"); n != 2 {
		t.Fatalf("restart calls %d, want 2", n)
	}
	for _, kind := range []wire.CloudActionKind{wire.CloudActionKindHardReset, wire.CloudActionKindSnapshotCreate} {
		if _, err := p.Action(ctx, wire.CloudActionParams{Instance: kvm, Action: kind, RequestID: wire.CloudRequestID("u-" + kind)}); coretest.Code(err) != wire.CloudErrorCodeUnsupported {
			t.Errorf("%s: %v", kind, err)
		}
	}
	if _, err := p.Action(ctx, wire.CloudActionParams{Instance: kvm, Action: wire.CloudActionKindCustom, Custom: "renew", RequestID: "c"}); coretest.Code(err) != wire.CloudErrorCodeUnsupported {
		t.Errorf("custom: %v", err)
	}
	if _, err := p.Snapshots(ctx, wire.CloudSnapshotsParams{Instance: kvm}); coretest.Code(err) != wire.CloudErrorCodeUnsupported {
		t.Errorf("snapshots: %v", err)
	}
	res, err := p.Action(ctx, wire.CloudActionParams{Instance: kvm, Action: wire.CloudActionKindConsole, RequestID: "con"})
	if err != nil {
		t.Fatal(err)
	}
	raw := coretest.Valid(t, "cloud.action", res)
	coretest.NoLeak(t, string(raw), secrets...)
	u, _ := url.Parse(res.URL)
	if res.Status != wire.CloudActionStatusDone || u.Scheme != "https" || u.User != nil || u.RawQuery != "" || res.URL != srv.URL {
		t.Fatalf("console %s", raw)
	}
}

func TestIPAfterStart(t *testing.T) {
	m, srv := newMock(t)
	m.vps[keyMain][1].ips = []string{"198.51.100.5", "172.16.5.2"}
	m.vps[keyMain][1].nextIP = "198.51.100.205"
	p := coretest.Provider(newAdapter(t, srv, allCreds()), nil, secrets...)
	ctx := context.Background()
	ref := wire.CloudInstanceRef{Account: "main", ID: "102"}
	before, err := p.Instance(ctx, wire.CloudInstanceParams{Instance: ref})
	if err != nil {
		t.Fatal(err)
	}
	coretest.Valid(t, "cloud.instance", before)
	if before.State != wire.CloudInstanceStateStopped || before.PublicIps[0] != "198.51.100.5" || before.DisplayName != "nat1.example.com" {
		t.Fatalf("before %+v", before)
	}
	if _, err := p.Action(ctx, wire.CloudActionParams{Instance: ref, Action: wire.CloudActionKindStart, RequestID: "go"}); err != nil {
		t.Fatal(err)
	}
	after, err := p.Instance(ctx, wire.CloudInstanceParams{Instance: ref})
	if err != nil {
		t.Fatal(err)
	}
	raw := coretest.Valid(t, "cloud.instance", after)
	coretest.NoLeak(t, string(raw), secrets...)
	if after.State != wire.CloudInstanceStateRunning || strings.Join(after.PublicIps, ",") != "198.51.100.205" ||
		strings.Join(after.PrivateIps, ",") != "172.16.5.2" {
		t.Fatalf("after %s", raw)
	}
	res, err := p.Action(ctx, wire.CloudActionParams{Instance: ref, Action: wire.CloudActionKindSyncIp, RequestID: "sync"})
	if err != nil || res.Instance == nil || res.Instance.PublicIps[0] != "198.51.100.205" {
		t.Fatalf("sync_ip %+v %v", res, err)
	}
	if m.count("GET vpsmanage 102") != 3 {
		t.Fatalf("vpsmanage calls %v", m.calls)
	}
}

func TestErrors(t *testing.T) {
	m, srv := newMock(t)
	p := coretest.Provider(newAdapter(t, srv, allCreds()), nil, secrets...)
	ctx := context.Background()
	check := func(name string, err error, want wire.CloudErrorCode) {
		t.Helper()
		if coretest.Code(err) != want {
			t.Errorf("%s: %v, want %s", name, err, want)
		}
		if err != nil {
			coretest.NoLeak(t, err.Error(), secrets...)
			if strings.Contains(err.Error(), "apikey=") || strings.Contains(err.Error(), "index.php") {
				t.Errorf("%s: a request address in %q", name, err)
			}
		}
	}
	_, err := p.Instance(ctx, wire.CloudInstanceParams{Instance: wire.CloudInstanceRef{Account: "main", ID: "999"}})
	check("missing server", err, wire.CloudErrorCodeNotFound)
	_, err = p.Instance(ctx, wire.CloudInstanceParams{Instance: wire.CloudInstanceRef{Account: "main", ID: "1&act=x"}})
	check("not an id", err, wire.CloudErrorCodeNotFound)
	_, err = p.Instance(ctx, wire.CloudInstanceParams{Instance: wire.CloudInstanceRef{Account: "nobody", ID: "101"}})
	check("unknown account", err, wire.CloudErrorCodeNotFound)
	_, err = p.Instance(ctx, wire.CloudInstanceParams{Instance: wire.CloudInstanceRef{Account: "dead", ID: "101"}})
	check("login page", err, wire.CloudErrorCodeNotSignedIn)

	kvm := m.vps[keyMain][0]
	for i, tc := range []struct {
		msg  string
		want wire.CloudErrorCode
	}{
		{"The VPS is already running", wire.CloudErrorCodeInvalidState},
		{"This VPS is suspended. Please contact your host.", wire.CloudErrorCodeInvalidState},
		{"Too many requests. Please slow down", wire.CloudErrorCodeRateLimited},
		{"There are insufficient resources on the server", wire.CloudErrorCodeCapacity},
		{"Invalid API Key " + keyMain + " or API Pass " + passMain, wire.CloudErrorCodeNotSignedIn},
		{"The VPS does not exist", wire.CloudErrorCodeNotFound},
		{"Power actions are disabled by the administrator", wire.CloudErrorCodeRefused},
	} {
		m.mu.Lock()
		kvm.actionError = tc.msg
		m.mu.Unlock()
		_, err := p.Action(ctx, wire.CloudActionParams{Instance: wire.CloudInstanceRef{Account: "main", ID: "101"},
			Action: wire.CloudActionKindStop, RequestID: wire.CloudRequestID(fmt.Sprint("e", i))})
		check(tc.msg, err, tc.want)
	}

	for i, tc := range []struct {
		status int
		body   string
		want   wire.CloudErrorCode
	}{
		{http.StatusOK, `<html><title>Login</title><form>apikey ` + keyMain + `</form></html>`, wire.CloudErrorCodeNotSignedIn},
		{http.StatusForbidden, `Forbidden`, wire.CloudErrorCodeNotSignedIn},
		{http.StatusInternalServerError, `<html>Internal error</html>`, wire.CloudErrorCodeFailed},
		{http.StatusOK, `{"act": "start"}`, wire.CloudErrorCodeFailed},
		{http.StatusOK, `{"uid": -1, "act": "start", "title": "Start"}`, wire.CloudErrorCodeNotSignedIn},
		{http.StatusOK, `{"uid": "-1", "act": "start", "username": "` + keyMain + `"}`, wire.CloudErrorCodeNotSignedIn},
		{http.StatusTooManyRequests, `{"act": "start"}`, wire.CloudErrorCodeRateLimited},
		{http.StatusOK, `garbage ` + passMain, wire.CloudErrorCodeFailed},
	} {
		m.mu.Lock()
		m.raw = func(w http.ResponseWriter, _ string) bool {
			w.WriteHeader(tc.status)
			fmt.Fprint(w, tc.body)
			return true
		}
		m.mu.Unlock()
		_, err := p.Action(ctx, wire.CloudActionParams{Instance: wire.CloudInstanceRef{Account: "main", ID: "101"},
			Action: wire.CloudActionKindStart, RequestID: wire.CloudRequestID(fmt.Sprint("h", i))})
		check(fmt.Sprint(tc.status, " ", tc.body), err, tc.want)
	}

	// A panel that cannot be reached: the error names the host only, never the query with the key.
	srv.Close()
	_, err = p.Instance(ctx, wire.CloudInstanceParams{Instance: wire.CloudInstanceRef{Account: "main", ID: "101"}})
	check("closed", err, wire.CloudErrorCodeFailed)
	a := newAdapter(t, srv, allCreds())
	_, err = a.Instance(ctx, wire.CloudInstanceRef{Account: "main", ID: "101"})
	check("closed, adapter", err, wire.CloudErrorCodeFailed)
}

func TestPinnedCertificate(t *testing.T) {
	_, srv := newMock(t)
	sum := sha256.Sum256(srv.Certificate().Raw)
	a, err := New(Config{BaseURL: srv.URL, Credentials: "main=" + keyMain + ":" + passMain, PinSHA256: hex.EncodeToString(sum[:])})
	if err != nil {
		t.Fatal(err)
	}
	if in, err := a.Instance(context.Background(), wire.CloudInstanceRef{Account: "main", ID: "101"}); err != nil || in.State != wire.CloudInstanceStateRunning {
		t.Fatalf("pinned: %+v %v", in, err)
	}
	a, _ = New(Config{BaseURL: srv.URL, Credentials: "main=" + keyMain + ":" + passMain, PinSHA256: strings.Repeat("cd", 32)})
	_, err = a.Instance(context.Background(), wire.CloudInstanceRef{Account: "main", ID: "101"})
	if coretest.Code(err) != wire.CloudErrorCodeFailed {
		t.Fatalf("wrong pin: %v", err)
	}
	coretest.NoLeak(t, err.Error(), secrets...)
}

func TestNegativeUIDIsSignedOut(t *testing.T) {
	m, srv := newMock(t)
	m.raw = func(w http.ResponseWriter, act string) bool {
		// Not logged in, said only with uid < 0: the act asked is echoed, there is no "vs" and no "info".
		fmt.Fprintf(w, `{"uid": -1, "act": %q, "title": "Virtualizor", "timenow": 1700000000}`, act)
		return true
	}
	p := coretest.Provider(newAdapter(t, srv, keyMain+":"+passMain), nil, secrets...)
	ctx := context.Background()
	ref := wire.CloudInstanceRef{Account: "default", ID: "101"}
	if _, err := p.Instances(ctx, wire.CloudInstancesParams{Refresh: true}); coretest.Code(err) != wire.CloudErrorCodeNotSignedIn {
		t.Errorf("listvs: %v", err)
	}
	if _, err := p.Instance(ctx, wire.CloudInstanceParams{Instance: ref}); coretest.Code(err) != wire.CloudErrorCodeNotSignedIn {
		t.Errorf("vpsmanage: %v", err)
	}
	if _, err := p.Action(ctx, wire.CloudActionParams{Instance: ref, Action: wire.CloudActionKindStop, RequestID: "u"}); coretest.Code(err) != wire.CloudErrorCodeNotSignedIn {
		t.Errorf("stop: %v", err)
	}
}

func TestDocumentedShapes(t *testing.T) {
	m, srv := newMock(t)
	m.raw = func(w http.ResponseWriter, act string) bool {
		switch act {
		case "listvs":
			fmt.Fprint(w, `{"title": "Virtualizor", "vs": {"1": {"vpsid": "1", "vps_name": "v1001", "hostname": "web01.example.com",
				"ip": "203.0.113.10", "ram": "2048", "cores": "2", "space": "40", "status": "1"}},
				"timenow": 1758500000, "page": {"start": 0, "len": 50, "maxNum": 1}}`)
		case "vpsmanage":
			fmt.Fprint(w, `{"title": "Virtualizor", "vpsinfo": {"vpsid": "1", "hostname": "web01.example.com",
				"status": "online", "ip": "203.0.113.10"}, "timenow": 1758500000}`)
		default:
			return false
		}
		return true
	}
	p := coretest.Provider(newAdapter(t, srv, keyMain+":"+passMain), nil, secrets...)
	ctx := context.Background()
	list, err := p.Instances(ctx, wire.CloudInstancesParams{Refresh: true})
	if err != nil {
		t.Fatal(err)
	}
	coretest.Valid(t, "cloud.instances", list)
	if len(list.Instances) != 1 || list.Partial || strings.Join(list.Instances[0].PublicIps, ",") != "203.0.113.10" ||
		list.Instances[0].State != wire.CloudInstanceStateRunning {
		t.Fatalf("listvs %+v", list)
	}
	if m.count("GET listvs") != 1 {
		t.Errorf("maxNum 1 still read %d pages", m.count("GET listvs"))
	}
	in, err := p.Instance(ctx, wire.CloudInstanceParams{Instance: wire.CloudInstanceRef{Account: "default", ID: "1"}})
	if err != nil {
		t.Fatal(err)
	}
	coretest.Valid(t, "cloud.instance", in)
	if in.State != wire.CloudInstanceStateRunning || strings.Join(in.PublicIps, ",") != "203.0.113.10" || in.DisplayName != "web01.example.com" {
		t.Fatalf("vpsinfo %+v", in)
	}
}

func TestMorePages(t *testing.T) {
	for _, tc := range []struct {
		raw       string
		page, got int
		want      bool
	}{
		{`{"start": 0, "len": 50, "maxNum": 120}`, 1, 50, true},
		{`{"start": 100, "len": 50, "maxNum": 120}`, 3, 20, false},
		{`{"start": 0, "len": 20, "maxNum": 21}`, 1, 20, true},
		{`{"start": 0, "len": 20, "maxNum": 20}`, 1, 20, false},
		{`{"len": 20}`, 1, 20, true},
		{`{"len": 20}`, 2, 5, false},
		{`3`, 3, 50, true},
		{``, 1, 49, false},
	} {
		if got := morePages(json.RawMessage(tc.raw), tc.page, tc.got); got != tc.want {
			t.Errorf("%s page %d got %d: %v", tc.raw, tc.page, tc.got, got)
		}
	}
}

// TestPinValidation: a certificate pin that is set but is not a SHA-256 fingerprint is a configuration error, not
// silently ignored; the usual spellings of a real one are accepted, and a client of one's own skips the check.
func TestPinValidation(t *testing.T) {
	hexPin := strings.Repeat("ab", 32)
	colons := strings.TrimSuffix(strings.Repeat("AB:", 32), ":")
	for _, good := range []string{"", "  ", hexPin, colons, "SHA256 Fingerprint=" + colons, "sha256:" + hexPin, "SHA-256 " + colons, " " + hexPin + " "} {
		if _, err := New(Config{BaseURL: "https://panel.example.com", Credentials: keyMain + ":" + passMain, PinSHA256: good}); err != nil {
			t.Errorf("%q: %v", good, err)
		}
	}
	for _, bad := range []string{"abcd", hexPin + "ab", strings.Repeat("zz", 32), "md5:" + hexPin, strings.Repeat("ab", 20)} {
		_, err := New(Config{BaseURL: "https://panel.example.com", Credentials: keyMain + ":" + passMain, PinSHA256: bad})
		if coretest.Code(err) != wire.CloudErrorCodeFailed || !strings.Contains(err.Error(), "not a SHA-256 fingerprint") {
			t.Errorf("%q: %v", bad, err)
		}
	}
	if _, err := New(Config{BaseURL: "https://panel.example.com", Credentials: keyMain + ":" + passMain, PinSHA256: "abcd", HTTP: http.DefaultClient}); err != nil {
		t.Errorf("a pin with a client of one's own: %v", err)
	}
}
