package solusvm

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
	v1KeyWeb  = "TESTKEY-0001-AAAAA"
	v1HashWeb = "testhash0000000000000000000000000000001"
	v1KeyNat  = "TESTKEY-0002-BBBBB"
	v1HashNat = "testhash0000000000000000000000000000002"
	v1KeyBad  = "TESTKEY-0003-CCCCC"
	v1HashBad = "testhash0000000000000000000000000000003"
)

var v1Secrets = []string{v1KeyWeb, v1HashWeb, v1KeyNat, v1HashNat, v1KeyBad, v1HashBad}

type v1MockVPS struct {
	hash, vmstat, hostname, mainIP, ips string
	nextIP                              string // the main address after the next boot
	noVmstat                            bool   // info leaves vmstat out (older panels): status has to be asked
	actionError                         string // a power command answers status=error with this
}

type v1Mock struct {
	t     *testing.T
	mu    sync.Mutex
	vps   map[string]*v1MockVPS // by key
	calls []string              // "key action"
	raw   func(w http.ResponseWriter, action string) bool
}

func (m *v1Mock) count(action string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, c := range m.calls {
		if strings.HasSuffix(c, " "+action) {
			n++
		}
	}
	return n
}

func (m *v1Mock) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	t := m.t
	if r.Method != http.MethodPost || r.URL.Path != "/api/client/command.php" {
		t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		http.NotFound(w, r)
		return
	}
	if r.URL.RawQuery != "" {
		t.Errorf("the client API got a query string: %q", r.URL.RawQuery)
	}
	if ct := r.Header.Get("Content-Type"); ct != "application/x-www-form-urlencoded" {
		t.Errorf("content type %q", ct)
	}
	if err := r.ParseForm(); err != nil {
		t.Fatal(err)
	}
	key, hash, action := r.PostForm.Get("key"), r.PostForm.Get("hash"), r.PostForm.Get("action")
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, key+" "+action)
	if m.raw != nil && m.raw(w, action) {
		return
	}
	// Real panels wrap every answer in an XML declaration and a <ctrl> element.
	out := &strings.Builder{}
	defer func() {
		fmt.Fprintf(w, "<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<ctrl>%s</ctrl>\n", out.String())
	}()
	v := m.vps[key]
	if v == nil || v.hash != hash {
		fmt.Fprint(out, "<status>error</status><statusmsg>Invalid key/hash</statusmsg>")
		return
	}
	switch action {
	case "info":
		for _, f := range []string{"status", "ipaddr", "hdd", "mem", "bw"} {
			if r.PostForm.Get(f) != "true" {
				t.Errorf("info without %s=true", f)
			}
		}
		fmt.Fprint(out, "<status>success</status><statusmsg></statusmsg>")
		if v.noVmstat {
			fmt.Fprint(out, "<vmstat></vmstat>") // what real panels answer when status=true is left out
		} else {
			fmt.Fprintf(out, "<vmstat>%s</vmstat>", v.vmstat)
		}
		fmt.Fprintf(out, "<hostname>%s</hostname><ipaddress>%s</ipaddress><ipaddr>%s</ipaddr>"+
			"<hdd>21474836480,5368709120,16106127360,25</hdd><mem>1073741824,268435456,805306368,25</mem>"+
			"<bw>1099511627776,1073741824,1098437885952,0</bw><type>kvm</type>", v.hostname, v.mainIP, v.ips)
	case "status":
		fmt.Fprintf(out, "<status>success</status><statusmsg>%s</statusmsg><vmstat>%s</vmstat><hostname>%s</hostname><ipaddress>%s</ipaddress>",
			v.vmstat, v.vmstat, v.hostname, v.mainIP)
	case "boot", "shutdown", "reboot":
		if v.actionError != "" {
			fmt.Fprintf(out, "<status>error</status><statusmsg>%s</statusmsg>", v.actionError)
			return
		}
		switch action {
		case "boot":
			v.vmstat = "online"
			if v.nextIP != "" {
				old := v.mainIP
				v.mainIP = v.nextIP
				v.ips = strings.Replace(v.ips, old, v.nextIP, 1)
			}
			fmt.Fprint(out, "<status>success</status><statusmsg>booted</statusmsg>")
		case "shutdown":
			v.vmstat = "offline"
			fmt.Fprint(out, "<status>success</status><statusmsg>shutdown</statusmsg>")
		default:
			fmt.Fprint(out, "<status>success</status><statusmsg>rebooted</statusmsg>")
		}
	default:
		fmt.Fprint(out, "<status>error</status><statusmsg>Invalid action</statusmsg>")
	}
}

func newV1Mock(t *testing.T) (*v1Mock, *httptest.Server) {
	m := &v1Mock{t: t, vps: map[string]*v1MockVPS{
		v1KeyWeb: {hash: v1HashWeb, vmstat: "online", hostname: "web.example.com", mainIP: "203.0.113.10",
			ips: "203.0.113.10,2001:db8::10"},
		v1KeyNat: {hash: v1HashNat, vmstat: "offline", hostname: "nat.example.org", mainIP: "10.0.0.5",
			ips: "10.0.0.5, 198.51.100.7"},
	}}
	srv := httptest.NewUnstartedServer(m)
	srv.Config.ErrorLog = log.New(io.Discard, "", 0) // the wrong-pin test's refused handshake
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return m, srv
}

func v1Creds(lines ...string) string { return strings.Join(lines, "\n") }

func newV1(t *testing.T, srv *httptest.Server, creds string) core.Adapter {
	t.Helper()
	a, err := NewV1(V1Config{BaseURL: srv.URL, Credentials: creds, HTTP: srv.Client(), Now: func() time.Time { return fixedNow }})
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

func TestV1Credentials(t *testing.T) {
	servers, err := parseV1Credentials(v1KeyWeb + ":" + v1HashWeb)
	if err != nil || len(servers) != 1 || servers[0].label != "vps" || servers[0].key != v1KeyWeb || servers[0].hash != v1HashWeb {
		t.Fatalf("single line: %+v %v", servers, err)
	}
	servers, err = parseV1Credentials("# my servers\n\nweb = " + v1KeyWeb + ":" + v1HashWeb + "\r\nnat=" + v1KeyNat + ":" + v1HashNat + "\n")
	if err != nil || len(servers) != 2 || servers[0].label != "web" || servers[1].label != "nat" || servers[1].hash != v1HashNat {
		t.Fatalf("labelled lines: %+v %v", servers, err)
	}
	for _, bad := range []string{"", "  \n# nothing\n", v1KeyWeb, "web=" + v1KeyWeb + ":",
		"web=" + v1KeyWeb + ":" + v1HashWeb + "\n" + v1KeyNat + ":" + v1HashNat,
		"web=" + v1KeyWeb + ":" + v1HashWeb + "\nweb=" + v1KeyNat + ":" + v1HashNat} {
		_, err := NewV1(V1Config{BaseURL: "https://panel.example.com", Credentials: bad})
		if coretest.Code(err) != wire.CloudErrorCodeNotSignedIn {
			t.Errorf("%q: %v", bad, err)
		}
		if err != nil {
			coretest.NoLeak(t, err.Error(), v1Secrets...)
		}
	}
	if _, err := NewV1(V1Config{BaseURL: "ftp://panel.example.com", Credentials: v1KeyWeb + ":" + v1HashWeb}); err == nil {
		t.Fatal("an ftp base was accepted")
	}
	a, _ := NewV1(V1Config{BaseURL: "https://panel.example.com", Credentials: "web=" + v1KeyWeb + ":" + v1HashWeb})
	got := a.(core.SecretHolder).SecretValues()
	if len(got) != 2 || got[0] != v1KeyWeb || got[1] != v1HashWeb {
		t.Fatalf("secrets %v", got)
	}
}

func TestV1Instances(t *testing.T) {
	m, srv := newV1Mock(t)
	a := newV1(t, srv, v1Creds("web="+v1KeyWeb+":"+v1HashWeb, "nat="+v1KeyNat+":"+v1HashNat, "broken="+v1KeyBad+":"+v1HashBad))
	p := coretest.Provider(a, nil, v1Secrets...)
	list, err := p.Instances(context.Background(), wire.CloudInstancesParams{Locale: "en", Refresh: true})
	if err != nil {
		t.Fatal(err)
	}
	raw := coretest.Valid(t, "cloud.instances", list)
	coretest.NoLeak(t, string(raw), v1Secrets...)
	if !list.Partial || !strings.Contains(list.Detail, "broken") || len(list.Instances) != 2 {
		t.Fatalf("list: %s", raw)
	}
	byID := map[wire.CloudInstanceID]wire.CloudInstance{}
	for _, in := range list.Instances {
		byID[in.ID] = in
		if in.Account != V1Account {
			t.Errorf("account %q", in.Account)
		}
	}
	web, nat := byID["web"], byID["nat"]
	if web.State != wire.CloudInstanceStateRunning || web.DisplayName != "web.example.com" ||
		strings.Join(web.PublicIps, ",") != "203.0.113.10,2001:db8::10" || len(web.PrivateIps) != 0 {
		t.Errorf("web: %+v", web)
	}
	if web.Spec == nil || web.Spec.MemoryMb != 1024 || web.Spec.DiskGb != 20 {
		t.Errorf("web spec: %+v", web.Spec)
	}
	if got := fmt.Sprint(web.Actions); got != "[stop reboot sync_ip console]" {
		t.Errorf("web actions %s", got)
	}
	if web.ConsoleURL != srv.URL || !strings.HasPrefix(web.ConsoleURL, "https://") || !web.ObservedAt.Equal(fixedNow) {
		t.Errorf("web console %q observed %v", web.ConsoleURL, web.ObservedAt)
	}
	if nat.State != wire.CloudInstanceStateStopped || strings.Join(nat.PrivateIps, ",") != "10.0.0.5" ||
		strings.Join(nat.PublicIps, ",") != "198.51.100.7" {
		t.Errorf("nat: %+v", nat)
	}
	if got := fmt.Sprint(nat.Actions); got != "[start sync_ip console]" {
		t.Errorf("nat actions %s", got)
	}
	if m.count("info") != 3 || m.count("status") != 0 { // info with status=true carries vmstat: one call each
		t.Errorf("info calls %d, status calls %d", m.count("info"), m.count("status"))
	}

	// Every server failing is an error, not an empty list.
	bad := newV1(t, srv, v1KeyBad+":"+v1HashBad)
	_, err = coretest.Provider(bad, nil, v1Secrets...).Instances(context.Background(), wire.CloudInstancesParams{Refresh: true})
	if coretest.Code(err) != wire.CloudErrorCodeNotSignedIn {
		t.Fatalf("all failing: %v", err)
	}
}

func TestV1StateMapping(t *testing.T) {
	a := &v1Adapter{console: "https://panel.example.com", now: func() time.Time { return fixedNow }}
	s := v1Server{label: "x"}
	for _, tc := range []struct {
		vmstat  string
		state   wire.CloudInstanceState
		actions string
		detail  bool
	}{
		{"online", wire.CloudInstanceStateRunning, "[stop reboot sync_ip console]", false},
		{"Online", wire.CloudInstanceStateRunning, "[stop reboot sync_ip console]", false},
		{"offline", wire.CloudInstanceStateStopped, "[start sync_ip console]", false},
		{"disabled", wire.CloudInstanceStateStopped, "[sync_ip console]", true},
		{"suspended", wire.CloudInstanceStateStopped, "[sync_ip console]", true},
		{"rebuilding", wire.CloudInstanceStateUnknown, "[sync_ip console]", true},
		{"", wire.CloudInstanceStateUnknown, "[sync_ip console]", false},
	} {
		in := a.instance("en", s, map[string]string{"vmstat": tc.vmstat})
		if in.State != tc.state || fmt.Sprint(in.Actions) != tc.actions || (in.StateDetail != "") != tc.detail {
			t.Errorf("%q: %s %v %q", tc.vmstat, in.State, in.Actions, in.StateDetail)
		}
		if in.DisplayName != "x" {
			t.Errorf("display name %q", in.DisplayName)
		}
	}
	// A plain http panel has no console link, so no console action.
	a.console = ""
	if in := a.instance("en", s, map[string]string{"vmstat": "online"}); in.ConsoleURL != "" || core.Offers(in.Actions, wire.CloudActionKindConsole) {
		t.Errorf("http panel: %+v", in)
	}
}

func TestV1StatusFallback(t *testing.T) {
	m, srv := newV1Mock(t)
	m.vps[v1KeyWeb].noVmstat = true
	a := newV1(t, srv, "web="+v1KeyWeb+":"+v1HashWeb)
	in, err := coretest.Provider(a, nil, v1Secrets...).Instance(context.Background(),
		wire.CloudInstanceParams{Instance: wire.CloudInstanceRef{Account: V1Account, ID: "web"}})
	if err != nil {
		t.Fatal(err)
	}
	coretest.Valid(t, "cloud.instance", in)
	if in.State != wire.CloudInstanceStateRunning || m.count("status") != 1 {
		t.Fatalf("fallback: %+v, status calls %d", in, m.count("status"))
	}
}

func TestScanTags(t *testing.T) {
	// The real envelope: the leaf tags inside <ctrl> are read, the wrapper itself is not a value.
	env := scanTags("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<ctrl><status>success</status><statusmsg>online</statusmsg>" +
		"<vmstat>online</vmstat><hostname>h.example.com</hostname><ipaddress>203.0.113.5</ipaddress><vmstat>second</vmstat></ctrl>")
	if env["status"] != "success" || env["statusmsg"] != "online" || env["vmstat"] != "online" || env["ipaddress"] != "203.0.113.5" {
		t.Fatalf("envelope: %v", env)
	}
	if _, ok := env["ctrl"]; ok {
		t.Fatalf("wrapper stored: %v", env)
	}
	if e := scanTags(`<?xml version="1.0"?><ctrl><status>error</status><statusmsg>Invalid key/hash</statusmsg></ctrl>`); e["status"] != "error" || e["statusmsg"] != "Invalid key/hash" {
		t.Fatalf("error envelope: %v", e)
	}

	tags := scanTags("junk <Status>success</Status>\n<statusmsg> a &amp; b </statusmsg><x>unclosed <hostname>h.example.com</hostname><empty></empty><hostname>second</hostname> < bad>")
	if tags["status"] != "success" || tags["statusmsg"] != "a & b" || tags["hostname"] != "h.example.com" || tags["empty"] != "" {
		t.Fatalf("%v", tags)
	}
	if _, ok := tags["x"]; ok {
		t.Fatalf("unclosed tag read: %v", tags)
	}
}

func TestV1Actions(t *testing.T) {
	m, srv := newV1Mock(t)
	a := newV1(t, srv, v1Creds("web="+v1KeyWeb+":"+v1HashWeb, "nat="+v1KeyNat+":"+v1HashNat))
	p := coretest.Provider(a, nil, v1Secrets...)
	ctx := context.Background()
	web := wire.CloudInstanceRef{Account: V1Account, ID: "web"}
	nat := wire.CloudInstanceRef{Account: V1Account, ID: "nat"}
	for i, tc := range []struct {
		ref  wire.CloudInstanceRef
		kind wire.CloudActionKind
		cmd  string
	}{
		{web, wire.CloudActionKindReboot, "reboot"},
		{web, wire.CloudActionKindStop, "shutdown"},
		{nat, wire.CloudActionKindStart, "boot"},
	} {
		res, err := p.Action(ctx, wire.CloudActionParams{Instance: tc.ref, Action: tc.kind, RequestID: wire.CloudRequestID(fmt.Sprint("req-", i))})
		if err != nil {
			t.Fatalf("%s: %v", tc.kind, err)
		}
		raw := coretest.Valid(t, "cloud.action", res)
		coretest.NoLeak(t, string(raw), v1Secrets...)
		if res.Status != wire.CloudActionStatusAccepted || m.count(tc.cmd) != 1 {
			t.Fatalf("%s: %s, %d calls", tc.kind, raw, m.count(tc.cmd))
		}
	}
	// The same request id is answered from the record: the panel hears it once.
	for range 2 {
		if _, err := p.Action(ctx, wire.CloudActionParams{Instance: web, Action: wire.CloudActionKindReboot, RequestID: "req-again"}); err != nil {
			t.Fatal(err)
		}
	}
	if m.count("reboot") != 2 {
		t.Fatalf("reboot calls %d, want 2", m.count("reboot"))
	}
	for _, kind := range []wire.CloudActionKind{wire.CloudActionKindForceStop, wire.CloudActionKindHardReset, wire.CloudActionKindSnapshotCreate} {
		_, err := p.Action(ctx, wire.CloudActionParams{Instance: web, Action: kind, RequestID: wire.CloudRequestID("u-" + kind)})
		if coretest.Code(err) != wire.CloudErrorCodeUnsupported {
			t.Errorf("%s: %v", kind, err)
		}
	}
	if _, err := p.Action(ctx, wire.CloudActionParams{Instance: web, Action: wire.CloudActionKindCustom, Custom: "renew", RequestID: "c-1"}); coretest.Code(err) != wire.CloudErrorCodeUnsupported {
		t.Errorf("custom: %v", err)
	}
	if _, err := p.Snapshots(ctx, wire.CloudSnapshotsParams{Instance: web}); coretest.Code(err) != wire.CloudErrorCodeUnsupported {
		t.Errorf("snapshots: %v", err)
	}
	// Console is the panel's https base, without credentials.
	res, err := p.Action(ctx, wire.CloudActionParams{Instance: web, Action: wire.CloudActionKindConsole, RequestID: "con-1"})
	if err != nil {
		t.Fatal(err)
	}
	raw := coretest.Valid(t, "cloud.action", res)
	coretest.NoLeak(t, string(raw), v1Secrets...)
	u, _ := url.Parse(res.URL)
	if res.Status != wire.CloudActionStatusDone || u.Scheme != "https" || u.User != nil || u.RawQuery != "" || res.URL != srv.URL {
		t.Fatalf("console %s", raw)
	}
}

func TestV1IPAfterStart(t *testing.T) {
	m, srv := newV1Mock(t)
	m.vps[v1KeyNat].mainIP, m.vps[v1KeyNat].ips, m.vps[v1KeyNat].nextIP = "198.51.100.7", "198.51.100.7,10.0.0.5", "198.51.100.99"
	a := newV1(t, srv, "nat="+v1KeyNat+":"+v1HashNat)
	p := coretest.Provider(a, nil, v1Secrets...)
	ctx := context.Background()
	ref := wire.CloudInstanceRef{Account: V1Account, ID: "nat"}
	before, err := p.Instance(ctx, wire.CloudInstanceParams{Instance: ref})
	if err != nil || before.PublicIps[0] != "198.51.100.7" {
		t.Fatalf("before: %+v %v", before, err)
	}
	if _, err := p.Action(ctx, wire.CloudActionParams{Instance: ref, Action: wire.CloudActionKindStart, RequestID: "start-1"}); err != nil {
		t.Fatal(err)
	}
	after, err := p.Instance(ctx, wire.CloudInstanceParams{Instance: ref})
	if err != nil {
		t.Fatal(err)
	}
	coretest.Valid(t, "cloud.instance", after)
	if after.State != wire.CloudInstanceStateRunning || strings.Join(after.PublicIps, ",") != "198.51.100.99" || strings.Join(after.PrivateIps, ",") != "10.0.0.5" {
		t.Fatalf("after: %+v", after)
	}
	res, err := p.Action(ctx, wire.CloudActionParams{Instance: ref, Action: wire.CloudActionKindSyncIp, RequestID: "sync-1"})
	if err != nil {
		t.Fatal(err)
	}
	coretest.Valid(t, "cloud.action", res)
	if res.Instance == nil || res.Instance.PublicIps[0] != "198.51.100.99" {
		t.Fatalf("sync_ip %+v", res)
	}
}

func TestV1Errors(t *testing.T) {
	m, srv := newV1Mock(t)
	ctx := context.Background()
	a := newV1(t, srv, v1Creds("web="+v1KeyWeb+":"+v1HashWeb, "broken="+v1KeyBad+":"+v1HashBad))
	p := coretest.Provider(a, nil, v1Secrets...)
	web := wire.CloudInstanceRef{Account: V1Account, ID: "web"}
	act := func(id string) error {
		_, err := p.Action(ctx, wire.CloudActionParams{Instance: web, Action: wire.CloudActionKindReboot, RequestID: wire.CloudRequestID(id)})
		return err
	}
	check := func(name string, err error, want wire.CloudErrorCode) {
		t.Helper()
		if coretest.Code(err) != want {
			t.Errorf("%s: %v, want %s", name, err, want)
		}
		if err != nil {
			coretest.NoLeak(t, err.Error(), v1Secrets...)
		}
	}
	_, err := p.Instance(ctx, wire.CloudInstanceParams{Instance: wire.CloudInstanceRef{Account: V1Account, ID: "broken"}})
	check("bad hash", err, wire.CloudErrorCodeNotSignedIn)
	_, err = p.Instance(ctx, wire.CloudInstanceParams{Instance: wire.CloudInstanceRef{Account: V1Account, ID: "missing"}})
	check("unknown label", err, wire.CloudErrorCodeNotFound)
	_, err = p.Instance(ctx, wire.CloudInstanceParams{Instance: wire.CloudInstanceRef{Account: "other", ID: "web"}})
	check("other account", err, wire.CloudErrorCodeNotFound)

	for i, tc := range []struct {
		msg  string
		want wire.CloudErrorCode
	}{
		{"Virtual server is already online", wire.CloudErrorCodeInvalidState},
		{"Virtual server is suspended", wire.CloudErrorCodeInvalidState},
		{"Too many requests, slow down", wire.CloudErrorCodeRateLimited},
		{"Virtual server not found", wire.CloudErrorCodeNotFound},
		{"Invalid key/hash " + v1KeyWeb + " " + v1HashWeb, wire.CloudErrorCodeNotSignedIn},
		{"Reboot is not allowed by your host", wire.CloudErrorCodeRefused},
	} {
		m.mu.Lock()
		m.vps[v1KeyWeb].actionError = tc.msg
		m.mu.Unlock()
		check(tc.msg, act(fmt.Sprint("err-", i)), tc.want)
	}

	// HTTP failures and unreadable answers.
	for _, tc := range []struct {
		status int
		body   string
		want   wire.CloudErrorCode
	}{
		{http.StatusForbidden, "Forbidden", wire.CloudErrorCodeNotSignedIn},
		{http.StatusTooManyRequests, "", wire.CloudErrorCodeRateLimited},
		{http.StatusOK, "<html>login " + v1KeyWeb + "</html>", wire.CloudErrorCodeFailed},
		{http.StatusInternalServerError, "<status>error</status><statusmsg>key " + v1KeyWeb + " is not valid</statusmsg>", wire.CloudErrorCodeFailed},
	} {
		m.mu.Lock()
		m.raw = func(w http.ResponseWriter, _ string) bool {
			w.WriteHeader(tc.status)
			fmt.Fprint(w, tc.body)
			return true
		}
		m.mu.Unlock()
		_, err := p.Instance(ctx, wire.CloudInstanceParams{Instance: web})
		check(fmt.Sprint(tc.status, tc.body), err, tc.want)
	}
}

func TestV1PinnedCertificate(t *testing.T) {
	_, srv := newV1Mock(t)
	sum := sha256.Sum256(srv.Certificate().Raw)
	pin := hex.EncodeToString(sum[:])
	a, err := NewV1(V1Config{BaseURL: srv.URL, Credentials: "web=" + v1KeyWeb + ":" + v1HashWeb, PinSHA256: pin})
	if err != nil {
		t.Fatal(err)
	}
	ref := wire.CloudInstanceRef{Account: V1Account, ID: "web"}
	if in, err := a.Instance(context.Background(), ref); err != nil || in.State != wire.CloudInstanceStateRunning {
		t.Fatalf("pinned: %+v %v", in, err)
	}
	wrong := strings.Repeat("ab", 32)
	a, _ = NewV1(V1Config{BaseURL: srv.URL, Credentials: "web=" + v1KeyWeb + ":" + v1HashWeb, PinSHA256: wrong})
	_, err = a.Instance(context.Background(), ref)
	if coretest.Code(err) != wire.CloudErrorCodeFailed {
		t.Fatalf("wrong pin: %v", err)
	}
	coretest.NoLeak(t, err.Error(), v1Secrets...)
	coretest.NoLeak(t, asJSON(t, err), v1Secrets...)
}

// TestPinValidation: a certificate pin that is set but is not a SHA-256 fingerprint is a configuration error, not
// silently ignored; the usual spellings of a real one are accepted, and a client of one's own skips the check.
func TestV1PinValidation(t *testing.T) {
	hexPin := strings.Repeat("ab", 32)
	colons := strings.TrimSuffix(strings.Repeat("AB:", 32), ":")
	for _, good := range []string{"", "  ", hexPin, colons, "SHA256 Fingerprint=" + colons, "sha256:" + hexPin, "SHA-256 " + colons, " " + hexPin + " "} {
		if _, err := NewV1(V1Config{BaseURL: "https://panel.example.com", Credentials: v1KeyWeb + ":" + v1HashWeb, PinSHA256: good}); err != nil {
			t.Errorf("%q: %v", good, err)
		}
	}
	for _, bad := range []string{"abcd", hexPin + "ab", strings.Repeat("zz", 32), "md5:" + hexPin, strings.Repeat("ab", 20)} {
		_, err := NewV1(V1Config{BaseURL: "https://panel.example.com", Credentials: v1KeyWeb + ":" + v1HashWeb, PinSHA256: bad})
		if coretest.Code(err) != wire.CloudErrorCodeFailed || !strings.Contains(err.Error(), "not a SHA-256 fingerprint") {
			t.Errorf("%q: %v", bad, err)
		}
	}
	if _, err := NewV1(V1Config{BaseURL: "https://panel.example.com", Credentials: v1KeyWeb + ":" + v1HashWeb, PinSHA256: "abcd", HTTP: http.DefaultClient}); err != nil {
		t.Errorf("a pin with a client of one's own: %v", err)
	}
}
