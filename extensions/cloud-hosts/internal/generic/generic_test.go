package generic

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona-extensions/extensions/cloud-hosts/internal/core"
	"github.com/nwverse/persona-extensions/extensions/cloud-hosts/internal/coretest"
	"github.com/nwverse/persona/sdk/go/wire"
)

const (
	testToken     = "test-token-0001-webhook"
	queryToken    = "query-secret-0001"
	proxyPassword = "proxy-pass-0001"
)

var custom = map[wire.CloudCustomActionID]wire.CloudSafety{CustomRenew: wire.CloudSafetyConfirm, CustomProbe: wire.CloudSafetyRead}

var fixedNow = time.Date(2027, 1, 10, 12, 0, 0, 0, time.UTC)

func clock() func() time.Time { return func() time.Time { return fixedNow } }

// newTLSServer is httptest.NewTLSServer without the handshake errors of clients that do not trust it in the log.
func newTLSServer(t *testing.T, h http.Handler) *httptest.Server {
	t.Helper()
	srv := httptest.NewUnstartedServer(h)
	srv.Config.ErrorLog = log.New(io.Discard, "", 0)
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return srv
}

// tcpListener is a TCP probe target on 127.0.0.1 that accepts and closes connections. Its port is returned; close
// it to make the port refuse.
func tcpListener(t *testing.T) (net.Listener, string) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	t.Cleanup(func() { l.Close() })
	_, port, _ := net.SplitHostPort(l.Addr().String())
	return l, port
}

// closedPort is a port on 127.0.0.1 nothing listens on.
func closedPort(t *testing.T) string {
	l, port := tcpListener(t)
	l.Close()
	return port
}

// hook is one webhook call the mock received.
type hook struct {
	method, path, query string
	escapedPath         string
	header              http.Header
	body                map[string]string
}

// hookServer is a mock webhook endpoint: answer decides the status and body by path.
type hookServer struct {
	*httptest.Server
	mu    sync.Mutex
	calls []hook
}

func newHookServer(t *testing.T, answer func(path string) (int, string)) *hookServer {
	t.Helper()
	hs := &hookServer{}
	hs.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]string
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &body); err != nil {
				t.Errorf("webhook body is not JSON: %q", raw)
			}
		}
		hs.mu.Lock()
		hs.calls = append(hs.calls, hook{r.Method, r.URL.Path, r.URL.RawQuery, r.URL.EscapedPath(), r.Header.Clone(), body})
		hs.mu.Unlock()
		status, out := http.StatusOK, `{}`
		if answer != nil {
			status, out = answer(r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		io.WriteString(w, out)
	}))
	t.Cleanup(hs.Close)
	return hs
}

func (hs *hookServer) got() []hook {
	hs.mu.Lock()
	defer hs.mu.Unlock()
	return append([]hook(nil), hs.calls...)
}

func mustNew(t *testing.T, c Config) *adapter {
	t.Helper()
	if c.Now == nil {
		c.Now = clock()
	}
	ad, err := New(c)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return ad.(*adapter)
}

func ref(id string) wire.CloudInstanceRef {
	return wire.CloudInstanceRef{Account: Account, ID: wire.CloudInstanceID(id)}
}

func provider(ad core.Adapter) *core.Provider {
	return coretest.Provider(ad, custom)
}

func TestConfigErrors(t *testing.T) {
	cases := []struct {
		name, base string
		hosts      []string
		want       string // in the error
	}{
		{"no name", "", []string{"address=203.0.113.10"}, `host #1, key "name": is required`},
		{"bad name", "", []string{"name=Bad Name"}, `key "name"`},
		{"duplicate", "", []string{"name=a", "name=a"}, `host "a", key "name": is used by another host`},
		{"unknown key", "", []string{"name=a; colour=blue"}, `host "a", key "colour": is not a known key`},
		{"repeated key", "", []string{"name=a; region=x; region=y"}, `key "region": is given twice`},
		{"no equals", "", []string{"name=a; spot"}, `every entry must be key=value`},
		{"term days", "", []string{"name=a; term_days=zero"}, `key "term_days"`},
		{"keepalive", "", []string{"name=a; keepalive_days=-1"}, `key "keepalive_days"`},
		{"bool", "", []string{"name=a; spot=maybe"}, `key "spot": must be true or false`},
		{"date", "", []string{"name=a; expires=31/01/2027"}, `key "expires"`},
		{"relative without base", "", []string{"name=a; start=/hooks/a/start?token=" + queryToken}, `key "start": is a relative path but no webhook base`},
		{"http hook with https base", "https://hooks.example.com", []string{"name=a; stop=http://hooks.example.com/a?token=" + queryToken}, `key "stop": must be an https URL`},
		{"userinfo hook", "", []string{"name=a; reboot=https://user:" + queryToken + "@hooks.example.com/a"}, `key "reboot": may not carry a user name`},
		{"console http", "", []string{"name=a; console=http://panel.example.com/vps/1"}, `key "console": must be an https URL`},
		{"probe without address", "", []string{"name=a; probe=tcp:22"}, `key "probe": needs an address`},
		{"probe syntax", "", []string{"name=a; address=203.0.113.10; probe=udp:53"}, `key "probe"`},
		{"probe port", "", []string{"name=a; address=203.0.113.10; probe=tcp:99999"}, `key "probe": has no usable port`},
		{"bad address", "", []string{"name=a; address=not an address!"}, `key "address"`},
		{"private address", "", []string{"name=a; private_address=db.example.com"}, `key "private_address"`},
		{"method", "", []string{"name=a; method=PUT"}, `key "method": must be POST or GET`},
		{"cpu", "", []string{"name=a; cpu=lots"}, `key "cpu"`},
		{"base with query", "https://hooks.example.com/?key=" + queryToken, []string{"name=a"}, `may not carry a query`},
		{"base not url", "ftp://hooks.example.com", []string{"name=a"}, `webhook base address is not a usable`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := New(Config{Hosts: c.hosts, WebhookBase: c.base, WebhookToken: testToken})
			if err == nil {
				t.Fatalf("New accepted %q", c.hosts)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Fatalf("error %q does not say %q", err, c.want)
			}
			coretest.NoLeak(t, err.Error(), queryToken, testToken)
			if coretest.Code(err) != wire.CloudErrorCodeFailed {
				t.Fatalf("code %s", coretest.Code(err))
			}
		})
	}
	_, err := New(Config{Hosts: []string{"", "  "}})
	if coretest.Code(err) != wire.CloudErrorCodeNotSignedIn {
		t.Fatalf("no hosts: %v", err)
	}
	// The documented example is accepted.
	mustNew(t, Config{WebhookBase: "https://hooks.example.com", Hosts: []string{
		"name=free-vps; address=203.0.113.10; probe=tcp:22; start=/hooks/free-vps/start; stop=/hooks/free-vps/stop; " +
			"reboot=/hooks/free-vps/reboot; console=https://panel.example.com/vps/1; expires=2027-01-31; renews=2027-01-24; " +
			"term_days=30; renew=/hooks/free-vps/renew; region=Frankfurt; spot=false; auto_renew=false; keepalive_days=0",
	}})
	// http webhooks are allowed once the base is http.
	mustNew(t, Config{WebhookBase: "http://hooks.example.com", Hosts: []string{"name=a; start=http://other.example.com/x"}})
}

func TestStatesAndActions(t *testing.T) {
	_, up := tcpListener(t)
	down := closedPort(t)
	hooks := "start=/h/start; stop=/h/stop; reboot=/h/reboot; hard_reset=/h/reset; force_stop=/h/off; console=https://panel.example.com/vps/1"
	ad := mustNew(t, Config{WebhookBase: "https://hooks.example.com", Hosts: []string{
		"name=up; address=127.0.0.1; probe=tcp:" + up + "; " + hooks,
		"name=down; address=127.0.0.1; probe=tcp:" + down + "; " + hooks,
		"name=noprobe; address=203.0.113.10; " + hooks,
		"name=bare; title=Bare host; address=203.0.113.11",
		"name=partial; address=127.0.0.1; probe=tcp:" + up + "; start=/h/start; reboot=/h/reboot",
	}})
	list, problems, err := ad.Instances(context.Background())
	if err != nil || len(problems) != 0 || len(list) != 5 {
		t.Fatalf("Instances: %d %v %v", len(list), problems, err)
	}
	all := []wire.CloudActionKind{"start", "stop", "reboot", "hard_reset", "force_stop", "sync_ip", "console"}
	want := map[string]struct {
		state   wire.CloudInstanceState
		actions []wire.CloudActionKind
		custom  []wire.CloudCustomActionID
	}{
		"up":      {wire.CloudInstanceStateRunning, []wire.CloudActionKind{"stop", "reboot", "hard_reset", "force_stop", "sync_ip", "console"}, []wire.CloudCustomActionID{CustomProbe}},
		"down":    {wire.CloudInstanceStateStopped, []wire.CloudActionKind{"start", "sync_ip", "console"}, []wire.CloudCustomActionID{CustomProbe}},
		"noprobe": {wire.CloudInstanceStateUnknown, all, nil},
		"bare":    {wire.CloudInstanceStateUnknown, []wire.CloudActionKind{"sync_ip"}, nil},
		"partial": {wire.CloudInstanceStateRunning, []wire.CloudActionKind{"reboot", "sync_ip"}, []wire.CloudCustomActionID{CustomProbe}},
	}
	for _, in := range list {
		w := want[string(in.ID)]
		if in.Account != Account || in.State != w.state || !reflect.DeepEqual(in.Actions, w.actions) || !reflect.DeepEqual(in.CustomActions, w.custom) {
			t.Errorf("%s: state %s actions %v custom %v; want %s %v %v", in.ID, in.State, in.Actions, in.CustomActions, w.state, w.actions, w.custom)
		}
		if !in.ObservedAt.Equal(fixedNow) {
			t.Errorf("%s: observed_at %v", in.ID, in.ObservedAt)
		}
		if in.State != wire.CloudInstanceStateRunning && in.StateDetail == "" {
			t.Errorf("%s: no state detail", in.ID)
		}
	}
	if list[1].StateDetail != "It did not answer the probe: the connection was refused." {
		t.Errorf("down detail %q", list[1].StateDetail)
	}
	if list[3].DisplayName != "Bare host" || !reflect.DeepEqual(list[3].PublicIps, []string{"203.0.113.11"}) {
		t.Errorf("bare: %+v", list[3])
	}
	if list[0].ConsoleURL != "https://panel.example.com/vps/1" || len(list[0].PublicIps) != 0 || !reflect.DeepEqual(list[0].PrivateIps, []string{"127.0.0.1"}) {
		t.Errorf("up: %+v", list[0])
	}
}

func TestInstanceIsReadNow(t *testing.T) {
	l, port := tcpListener(t)
	ad := mustNew(t, Config{Hosts: []string{"name=vps; address=127.0.0.1; probe=tcp:" + port}})
	in, err := ad.Instance(context.Background(), ref("vps"))
	if err != nil || in.State != wire.CloudInstanceStateRunning {
		t.Fatalf("first read: %s %v", in.State, err)
	}
	l.Close()
	in, err = ad.Instance(context.Background(), ref("vps"))
	if err != nil || in.State != wire.CloudInstanceStateStopped {
		t.Fatalf("after the listener closed: %s %v (a cached answer?)", in.State, err)
	}
	if _, err := ad.Instance(context.Background(), ref("other")); coretest.Code(err) != wire.CloudErrorCodeNotFound {
		t.Fatalf("unknown host: %v", err)
	}
	if _, err := ad.Instance(context.Background(), wire.CloudInstanceRef{Account: "default", ID: "vps"}); coretest.Code(err) != wire.CloudErrorCodeNotFound {
		t.Fatalf("unknown account: %v", err)
	}
}

func TestProbeTimeoutAndHTTP(t *testing.T) {
	timeoutDial := func(ctx context.Context, network, addr string) (net.Conn, error) {
		return nil, &net.OpError{Op: "dial", Net: network, Err: context.DeadlineExceeded}
	}
	ad := mustNew(t, Config{Hosts: []string{"name=slow; address=203.0.113.10; probe=tcp:22"}, Dial: timeoutDial})
	in, _ := ad.Instance(context.Background(), ref("slow"))
	if in.State != wire.CloudInstanceStateStopped || in.StateDetail != "It did not answer the probe in time." {
		t.Fatalf("timeout: %s %q", in.State, in.StateDetail)
	}

	var status atomic.Int32
	status.Store(404)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" {
			t.Errorf("probe path %q", r.URL.Path)
		}
		w.WriteHeader(int(status.Load()))
	}))
	defer srv.Close()
	_, port, _ := net.SplitHostPort(srv.Listener.Addr().String())
	tls := newTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RawQuery != "key="+queryToken {
			t.Errorf("probe query %q", r.URL.RawQuery)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	ad = mustNew(t, Config{HTTP: tls.Client(), Hosts: []string{
		"name=web; address=127.0.0.1; probe=http:" + port + "/health",
		"name=tls; probe=" + tls.URL + "/ping?key=" + queryToken,
	}})
	if in, _ := ad.Instance(context.Background(), ref("web")); in.State != wire.CloudInstanceStateRunning {
		t.Fatalf("HTTP 404 counts as alive: %s %q", in.State, in.StateDetail)
	}
	// A 5xx means something answers, but not whether the host itself is up: unknown, with every power webhook.
	status.Store(503)
	if in, _ := ad.Instance(context.Background(), ref("web")); in.State != wire.CloudInstanceStateUnknown || !strings.Contains(in.StateDetail, "HTTP 503") {
		t.Fatalf("HTTP 503: %s %q", in.State, in.StateDetail)
	}
	if in, _ := ad.Instance(context.Background(), ref("tls")); in.State != wire.CloudInstanceStateRunning {
		t.Fatalf("https probe: %s %q", in.State, in.StateDetail)
	}
	if !contains(ad.SecretValues(), queryToken) {
		t.Fatalf("a probe URL's query is not a secret: %v", ad.SecretValues())
	}
}

// An https probe whose certificate does not verify (an https:<port> probe of a plain IP, a self-signed
// certificate) reached a host that is up: running, never "refused".
func TestProbeTLSFailureIsRunning(t *testing.T) {
	srv := newTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))
	_, port, _ := net.SplitHostPort(srv.Listener.Addr().String())
	ad := mustNew(t, Config{WebhookBase: "https://hooks.example.com", Hosts: []string{
		"name=selfsigned; address=127.0.0.1; probe=https:" + port + "/; start=/h/start; stop=/h/stop; reboot=/h/reboot",
	}})
	in, err := ad.Instance(context.Background(), ref("selfsigned"))
	if err != nil || in.State != wire.CloudInstanceStateRunning || !strings.Contains(in.StateDetail, "certificate was not accepted") {
		t.Fatalf("untrusted certificate: %s %q %v", in.State, in.StateDetail, err)
	}
	if !reflect.DeepEqual(in.Actions, []wire.CloudActionKind{"stop", "reboot", "sync_ip"}) {
		t.Fatalf("actions %v", in.Actions)
	}
	res, err := ad.Action(context.Background(), core.Action{Ref: ref("selfsigned"), Kind: wire.CloudActionKindCustom, Custom: CustomProbe, RequestID: "p", Locale: "en"})
	if err != nil || !strings.HasPrefix(res.Message, "Reachable: ") || !strings.Contains(res.Message, "certificate") {
		t.Fatalf("probe action: %+v %v", res, err)
	}
	// A port that accepts the connection but never speaks TLS is up too.
	_, tcpOnly := tcpListener(t)
	ad = mustNew(t, Config{Hosts: []string{"name=raw; address=127.0.0.1; probe=https:" + tcpOnly + "/"}})
	if in, _ := ad.Instance(context.Background(), ref("raw")); in.State != wire.CloudInstanceStateRunning {
		t.Fatalf("TLS on a closing port: %s %q", in.State, in.StateDetail)
	}
	// A port nothing listens on is still stopped.
	ad = mustNew(t, Config{Hosts: []string{"name=off; address=127.0.0.1; probe=https:" + closedPort(t) + "/"}})
	if in, _ := ad.Instance(context.Background(), ref("off")); in.State != wire.CloudInstanceStateStopped {
		t.Fatalf("closed https port: %s %q", in.State, in.StateDetail)
	}
}

// connectProxy is a fake HTTP proxy: CONNECT to an allowed target is tunnelled, others answer code.
func connectProxy(t *testing.T, allow func(target string) int, auth *atomic.Value) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect {
			t.Errorf("proxy got %s", r.Method)
			http.Error(w, "connect only", http.StatusMethodNotAllowed)
			return
		}
		auth.Store(r.Header.Get("Proxy-Authorization"))
		if code := allow(r.Host); code != http.StatusOK {
			http.Error(w, "blocked by persona sandbox egress", code)
			return
		}
		dst, err := net.Dial("tcp", r.Host)
		if err != nil {
			http.Error(w, "bad gateway", http.StatusBadGateway)
			return
		}
		c, buf, err := w.(http.Hijacker).Hijack()
		if err != nil {
			dst.Close()
			return
		}
		io.WriteString(c, "HTTP/1.1 200 Connection established\r\n\r\n")
		go func() { io.Copy(dst, buf); dst.Close() }()
		io.Copy(c, dst)
		c.Close()
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestProbeThroughProxy(t *testing.T) {
	_, up := tcpListener(t)
	down := closedPort(t)
	web := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer web.Close()
	_, webPort, _ := net.SplitHostPort(web.Listener.Addr().String())
	var auth atomic.Value
	px := connectProxy(t, func(target string) int {
		if strings.HasPrefix(target, "203.0.113.") {
			return http.StatusForbidden
		}
		return http.StatusOK
	}, &auth)
	pu, _ := url.Parse(px.URL)
	pu.User = url.UserPassword("persona", proxyPassword)
	dial := proxyDialer(func(*http.Request) (*url.URL, error) { return pu, nil })
	ad := mustNew(t, Config{Dial: dial, Hosts: []string{
		"name=up; address=127.0.0.1; probe=tcp:" + up,
		"name=down; address=127.0.0.1; probe=tcp:" + down,
		"name=blocked; address=203.0.113.10; probe=tcp:22",
		"name=web; address=127.0.0.1; probe=http:" + webPort + "/",
	}})
	list, _, err := ad.Instances(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	states := map[wire.CloudInstanceID]wire.CloudInstance{}
	for _, in := range list {
		states[in.ID] = in
	}
	if states["up"].State != wire.CloudInstanceStateRunning || states["web"].State != wire.CloudInstanceStateRunning {
		t.Fatalf("through the proxy: up %s web %s", states["up"].State, states["web"].State)
	}
	if states["down"].State != wire.CloudInstanceStateStopped {
		t.Fatalf("a refused tunnel: %s", states["down"].State)
	}
	b := states["blocked"]
	if b.State != wire.CloudInstanceStateUnknown || !strings.Contains(b.StateDetail, "allow that host") || !strings.Contains(b.StateDetail, "203.0.113.10") {
		t.Fatalf("a 403 from the proxy: %s %q", b.State, b.StateDetail)
	}
	wantAuth := "Basic " + base64.StdEncoding.EncodeToString([]byte("persona:"+proxyPassword))
	if got, _ := auth.Load().(string); got != wantAuth {
		t.Fatalf("Proxy-Authorization %q", got)
	}
	raw, _ := json.Marshal(list)
	coretest.NoLeak(t, string(raw), proxyPassword, pu.String())

	// The probe action through the proxy says the host is to be allowed, without the proxy's credentials.
	res, err := ad.Action(context.Background(), core.Action{Ref: ref("blocked"), Kind: wire.CloudActionKindCustom, Custom: CustomProbe, RequestID: "r1"})
	if err != nil || res.Status != wire.CloudActionStatusDone || !strings.Contains(res.Message, "allow that host") {
		t.Fatalf("probe action: %+v %v", res, err)
	}
	coretest.NoLeak(t, res.Message, proxyPassword)

	// No proxy: a direct dial.
	direct := proxyDialer(func(*http.Request) (*url.URL, error) { return nil, nil })
	ad = mustNew(t, Config{Dial: direct, Hosts: []string{"name=up; address=127.0.0.1; probe=tcp:" + up}})
	if in, _ := ad.Instance(context.Background(), ref("up")); in.State != wire.CloudInstanceStateRunning {
		t.Fatalf("direct: %s", in.State)
	}
	// An https probe through the tunnel: trusted, and with a certificate that does not verify.
	tlsWeb := newTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))
	_, tlsPort, _ := net.SplitHostPort(tlsWeb.Listener.Addr().String())
	for _, trusted := range []bool{true, false} {
		c := Config{Dial: dial, Hosts: []string{"name=tls; address=127.0.0.1; probe=https:" + tlsPort + "/"}}
		if trusted {
			c.HTTP = tlsWeb.Client()
		}
		in, _ := mustNew(t, c).Instance(context.Background(), ref("tls"))
		if in.State != wire.CloudInstanceStateRunning || trusted != (in.StateDetail == "") {
			t.Fatalf("https through the proxy (trusted %v): %s %q", trusted, in.State, in.StateDetail)
		}
	}

	// The proxy's own failures (407, 400, ...) say nothing of the host: unknown. 502/503 is the host refusing,
	// 504 the host not answering in time.
	for code, want := range map[int]wire.CloudInstanceState{
		http.StatusProxyAuthRequired:  wire.CloudInstanceStateUnknown,
		http.StatusBadRequest:         wire.CloudInstanceStateUnknown,
		http.StatusBadGateway:         wire.CloudInstanceStateStopped,
		http.StatusServiceUnavailable: wire.CloudInstanceStateStopped,
		http.StatusGatewayTimeout:     wire.CloudInstanceStateStopped,
	} {
		px := connectProxy(t, func(string) int { return code }, &auth)
		pu, _ := url.Parse(px.URL)
		ad := mustNew(t, Config{Dial: proxyDialer(func(*http.Request) (*url.URL, error) { return pu, nil }), Hosts: []string{"name=up; address=127.0.0.1; probe=tcp:" + up}})
		in, _ := ad.Instance(context.Background(), ref("up"))
		if in.State != want {
			t.Errorf("proxy %d: %s %q, want %s", code, in.State, in.StateDetail, want)
		}
		if code == http.StatusGatewayTimeout && in.StateDetail != "It did not answer the probe in time." {
			t.Errorf("proxy 504: %q", in.StateDetail)
		}
	}

	// An environment with HTTP_PROXY only: the https question names no proxy, the http one does.
	var asked []string
	var askedMu sync.Mutex
	onlyHTTP := proxyDialer(func(r *http.Request) (*url.URL, error) {
		askedMu.Lock()
		asked = append(asked, r.URL.Scheme)
		askedMu.Unlock()
		if r.URL.Scheme == "http" {
			return pu, nil
		}
		return nil, nil
	})
	auth.Store("")
	ad = mustNew(t, Config{Dial: onlyHTTP, Hosts: []string{"name=up; address=127.0.0.1; probe=tcp:" + up}})
	if in, _ := ad.Instance(context.Background(), ref("up")); in.State != wire.CloudInstanceStateRunning {
		t.Fatalf("HTTP_PROXY only: %s %q", in.State, in.StateDetail)
	}
	if got, _ := auth.Load().(string); got != wantAuth || !reflect.DeepEqual(asked, []string{"https", "http"}) {
		t.Fatalf("HTTP_PROXY only: the tunnel was not opened through the proxy (asked %v, auth %q)", asked, got)
	}

	// An unreachable proxy is not the host's fault.
	dead, _ := url.Parse("http://127.0.0.1:" + closedPort(t))
	ad = mustNew(t, Config{Dial: proxyDialer(func(*http.Request) (*url.URL, error) { return dead, nil }), Hosts: []string{"name=up; address=127.0.0.1; probe=tcp:" + up}})
	if in, _ := ad.Instance(context.Background(), ref("up")); in.State != wire.CloudInstanceStateUnknown {
		t.Fatalf("dead proxy: %s %q", in.State, in.StateDetail)
	}
}

func TestWebhookActions(t *testing.T) {
	hs := newHookServer(t, func(path string) (int, string) {
		switch path {
		case "/hooks/vps/stop":
			return http.StatusAccepted, `{"message":"Shutting down\nnow"}`
		case "/hooks/vps/reboot":
			return http.StatusOK, `{"status":"refused","message":"Maintenance window"}`
		}
		return http.StatusOK, `ok`
	})
	ad := mustNew(t, Config{WebhookBase: hs.URL + "/hooks", WebhookToken: testToken, Hosts: []string{
		"name=vps; start=/vps/start; stop=vps/stop; reboot=/vps/reboot; hard_reset=/vps/reset?token=" + queryToken + "; force_stop=" + hs.URL + "/abs/off",
		"name=get; method=get; start=/get/start",
		"name=none; address=203.0.113.10",
	}})
	do := func(id string, kind wire.CloudActionKind, rid string) (wire.CloudActionResult, error) {
		return ad.Action(context.Background(), core.Action{Ref: ref(id), Kind: kind, RequestID: wire.CloudRequestID(rid), Locale: "en"})
	}
	kinds := []struct {
		kind  wire.CloudActionKind
		path  string
		query string
	}{
		{"start", "/hooks/vps/start", ""},
		{"stop", "/hooks/vps/stop", ""},
		{"reboot", "/hooks/vps/reboot", ""},
		{"hard_reset", "/hooks/vps/reset", "token=" + queryToken},
		{"force_stop", "/abs/off", ""},
	}
	for i, k := range kinds {
		rid := "req-" + string(k.kind)
		res, err := do("vps", k.kind, rid)
		if err != nil {
			t.Fatalf("%s: %v", k.kind, err)
		}
		c := hs.got()[i]
		if c.method != http.MethodPost || c.path != k.path || c.query != k.query {
			t.Errorf("%s: called %s %s?%s", k.kind, c.method, c.path, c.query)
		}
		if c.header.Get("Authorization") != "Bearer "+testToken || c.header.Get("Idempotency-Key") != rid || c.header.Get("Content-Type") != "application/json" {
			t.Errorf("%s: headers %v", k.kind, c.header)
		}
		if !reflect.DeepEqual(c.body, map[string]string{"action": string(k.kind), "host": "vps", "request_id": rid}) {
			t.Errorf("%s: body %v", k.kind, c.body)
		}
		switch k.kind {
		case "stop":
			if res.Status != wire.CloudActionStatusAccepted || res.Message != "Shutting down now" {
				t.Errorf("stop: %+v", res)
			}
		case "reboot":
			if res.Status != wire.CloudActionStatusRefused || res.Message != "Maintenance window" {
				t.Errorf("reboot: %+v", res)
			}
		default:
			if res.Status != wire.CloudActionStatusAccepted || res.Message != "" {
				t.Errorf("%s: %+v", k.kind, res)
			}
		}
	}
	if _, err := do("get", "start", "req-get"); err != nil {
		t.Fatal(err)
	}
	c := hs.got()[len(kinds)]
	if c.method != http.MethodGet || c.path != "/hooks/get/start" || c.body != nil || c.header.Get("Content-Type") != "" || c.header.Get("Idempotency-Key") != "req-get" {
		t.Errorf("GET webhook: %+v", c)
	}
	// What has no webhook, and what custom hosts never do, is unsupported.
	for _, k := range []wire.CloudActionKind{"start", "stop", "snapshot_create"} {
		if _, err := do("none", k, "x"); coretest.Code(err) != wire.CloudErrorCodeUnsupported {
			t.Errorf("none %s: %v", k, err)
		}
	}
	if _, err := ad.Action(context.Background(), core.Action{Ref: ref("vps"), Kind: wire.CloudActionKindCustom, Custom: "other", RequestID: "x"}); coretest.Code(err) != wire.CloudErrorCodeUnsupported {
		t.Errorf("unknown custom: %v", err)
	}
	if _, err := ad.Snapshots(context.Background(), ref("vps")); coretest.Code(err) != wire.CloudErrorCodeUnsupported {
		t.Errorf("snapshots: %v", err)
	}
	if _, err := do("gone", "start", "x"); coretest.Code(err) != wire.CloudErrorCodeNotFound {
		t.Errorf("unknown host: %v", err)
	}
	secrets := ad.SecretValues()
	for _, s := range []string{testToken, queryToken, hs.URL + "/hooks/vps/reset?token=" + queryToken} {
		if !contains(secrets, s) {
			t.Errorf("SecretValues lacks %q", s)
		}
	}
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func TestWebhookErrors(t *testing.T) {
	var status atomic.Int32
	var body atomic.Value
	hs := newHookServer(t, func(string) (int, string) { return int(status.Load()), body.Load().(string) })
	ad := mustNew(t, Config{WebhookBase: hs.URL, WebhookToken: testToken, Hosts: []string{"name=vps; start=/start?sig=" + queryToken}})
	p := coretest.Provider(ad, custom)
	cases := []struct {
		status    int
		body      string
		code      wire.CloudErrorCode
		transient bool
	}{
		{401, `{"error":"bad token ` + testToken + `"}`, wire.CloudErrorCodeNotSignedIn, false},
		{403, `{"message":"forbidden for ` + hs.URL + `/start?sig=` + queryToken + `"}`, wire.CloudErrorCodeNotSignedIn, false},
		{404, `not here`, wire.CloudErrorCodeFailed, false},
		{409, `{"message":"already starting"}`, wire.CloudErrorCodeInvalidState, false},
		{429, `{}`, wire.CloudErrorCodeRateLimited, true},
		{503, `{"message":"no capacity left in this region"}`, wire.CloudErrorCodeCapacity, false},
		// A 5xx is no proof that the webhook did nothing: never transient.
		{503, `{"message":"down for maintenance"}`, wire.CloudErrorCodeFailed, false},
		{500, `{"message":"boom"}`, wire.CloudErrorCodeFailed, false},
		{502, `{"message":"bad gateway"}`, wire.CloudErrorCodeFailed, false},
		{400, `{"message":"bad request"}`, wire.CloudErrorCodeFailed, false},
	}
	for i, c := range cases {
		status.Store(int32(c.status))
		body.Store(c.body)
		// The adapter itself, for the Transient flag.
		_, aerr := ad.Action(context.Background(), core.Action{Ref: ref("vps"), Kind: "start", RequestID: wire.CloudRequestID("raw-" + string(rune('a'+i)))})
		if e := core.AsError(aerr); e == nil || e.Code != c.code || e.Transient != c.transient {
			t.Errorf("%d %s: adapter error %+v, want %s transient %v", c.status, c.body, e, c.code, c.transient)
		}
		_, err := p.Action(context.Background(), wire.CloudActionParams{Instance: ref("vps"), Action: "start", RequestID: wire.CloudRequestID("err-" + string(rune('a'+i)))})
		if coretest.Code(err) != c.code {
			t.Errorf("%d %s: code %s (%v), want %s", c.status, c.body, coretest.Code(err), err, c.code)
			continue
		}
		coretest.NoLeak(t, err.Error(), testToken, queryToken, hs.URL+"/start")
		if c.status == 404 && !strings.Contains(err.Error(), "does not exist") {
			t.Errorf("404: %q", err)
		}
	}
	if !strings.Contains(fmt.Sprint(core.AsError(func() error {
		status.Store(409)
		body.Store(`{"message":"already starting"}`)
		_, err := ad.Action(context.Background(), core.Action{Ref: ref("vps"), Kind: "start", RequestID: "detail"})
		return err
	}())), "already starting") {
		t.Errorf("a 409's own words are not passed on")
	}
	// The Provider keeps a 5xx as the request id's answer: the same id again does not call the webhook a second time.
	status.Store(500)
	body.Store(`{"message":"boom"}`)
	before := len(hs.got())
	for i := 0; i < 2; i++ {
		if _, err := p.Action(context.Background(), wire.CloudActionParams{Instance: ref("vps"), Action: "start", RequestID: "kept-5xx"}); coretest.Code(err) != wire.CloudErrorCodeFailed {
			t.Fatalf("5xx try %d: %v", i, err)
		}
	}
	if n := len(hs.got()) - before; n != 1 {
		t.Fatalf("a 5xx's request id called the webhook %d times", n)
	}
	// A 429 is transient: the same id may ask again.
	status.Store(429)
	body.Store(`{}`)
	before = len(hs.got())
	for i := 0; i < 2; i++ {
		p.Action(context.Background(), wire.CloudActionParams{Instance: ref("vps"), Action: "start", RequestID: "retry-429"})
	}
	if n := len(hs.got()) - before; n != 2 {
		t.Fatalf("a 429's request id called the webhook %d times", n)
	}
	// The webhook's words are passed on whole, and the adapter's own in the caller's language.
	status.Store(409)
	long := strings.Repeat("word ", 60) + "end"
	body.Store(`{"message":"` + long + `"}`)
	_, aerr := ad.Action(context.Background(), core.Action{Ref: ref("vps"), Kind: "start", RequestID: "long", Locale: "zh-CN"})
	if e := core.AsError(aerr); e == nil || !strings.HasSuffix(e.Message, long) || !strings.HasPrefix(e.Message, "start Webhook ") {
		t.Fatalf("409 in zh-CN: %v", aerr)
	}
	// A webhook that cannot be reached names its host only.
	hs.Close()
	_, err := p.Action(context.Background(), wire.CloudActionParams{Instance: ref("vps"), Action: "start", RequestID: "err-down"})
	if coretest.Code(err) != wire.CloudErrorCodeFailed {
		t.Fatalf("down: %v", err)
	}
	coretest.NoLeak(t, err.Error(), testToken, queryToken, "/start")
}

// Tokens in a webhook URL's path (a chat service's /services/<team>/<secret>, /hooks/<id>/start) and a short webhook
// token stay out of every message, also when the webhook echoes them. A token too short to be recognised is refused.
func TestWebhookPathAndShortSecrets(t *testing.T) {
	const pathSecret = "Xq7pathsecret0001"
	const shortToken = "tk012345"
	for _, tok := range []string{"tk01", "tk0123", "tk01234"} {
		_, err := New(Config{WebhookBase: "https://hooks.example.com", WebhookToken: tok, Hosts: []string{"name=vps; start=/s"}})
		if coretest.Code(err) != wire.CloudErrorCodeFailed || !strings.Contains(err.Error(), "at least 8 characters") {
			t.Fatalf("token %q: %v", tok, err)
		}
		coretest.NoLeak(t, err.Error(), tok)
	}
	var status atomic.Int32
	var body atomic.Value
	hs := newHookServer(t, func(string) (int, string) { return int(status.Load()), body.Load().(string) })
	ad := mustNew(t, Config{WebhookBase: hs.URL + "/services/T0001EXAMPLE", WebhookToken: shortToken, Hosts: []string{
		"name=vps; start=/" + pathSecret + "/start; stop=/hooks/ab12cd34ef/stop",
	}})
	p := coretest.Provider(ad, custom)
	for _, s := range []string{pathSecret, "ab12cd34ef", "T0001EXAMPLE"} {
		if !contains(ad.SecretValues(), s) {
			t.Errorf("SecretValues lacks the path part %q", s)
		}
	}
	echo := `{"message":"no webhook /services/T0001EXAMPLE/` + pathSecret + `/start for ` + shortToken + ` at ab12cd34ef"}`
	for i, c := range []struct {
		status int
		action wire.CloudActionKind
	}{{404, "start"}, {401, "start"}, {409, "start"}, {500, "stop"}, {200, "stop"}} {
		status.Store(int32(c.status))
		body.Store(echo)
		res, err := p.Action(context.Background(), wire.CloudActionParams{Instance: ref("vps"), Action: c.action, RequestID: wire.CloudRequestID(fmt.Sprint("ps-", i))})
		text := fmt.Sprint(err)
		if err == nil {
			raw, _ := json.Marshal(res)
			text = string(raw)
		}
		coretest.NoLeak(t, text, pathSecret, "ab12cd34ef", "T0001EXAMPLE", shortToken)
	}
	if got := hs.got(); len(got) == 0 || got[0].header.Get("Authorization") != "Bearer "+shortToken {
		t.Fatalf("the short token was not sent")
	}
}

// A webhook query value that repeats the host's name or its console link is not blanked out of the answers, and
// the console link stays.
func TestPublicQueryValuesAreNotSecrets(t *testing.T) {
	ad := mustNew(t, Config{WebhookBase: "https://hooks.example.com", Hosts: []string{
		"name=free-vps; address=203.0.113.10; console=https://panel.example.com/vps/vps-12345; start=/start?host=free-vps&id=vps-12345&token=" + queryToken,
	}})
	for _, s := range ad.SecretValues() {
		if s == "free-vps" || s == "vps-12345" {
			t.Fatalf("public value %q registered as a secret", s)
		}
	}
	if !contains(ad.SecretValues(), queryToken) {
		t.Fatalf("the token is not a secret")
	}
	in, err := provider(ad).Instance(context.Background(), wire.CloudInstanceParams{Instance: ref("free-vps"), Locale: "en"})
	if err != nil || in.DisplayName != "free-vps" || in.ConsoleURL != "https://panel.example.com/vps/vps-12345" || !contains(kindStrings(in.Actions), "console") {
		t.Fatalf("instance %+v %v", in, err)
	}
	// Short query values are ordinary words (even under a credential-like key): not registered, so a console link
	// that contains them stays. The full webhook URL always is.
	ad = mustNew(t, Config{WebhookBase: "https://hooks.example.com", Hosts: []string{
		"name=box; console=https://panel.example.com/srv/fast?view=7&code=abc; start=/start?mode=fast&n=7&code=abc&sig=" + queryToken,
	}})
	for _, s := range ad.SecretValues() {
		if s == "fast" || s == "7" || s == "abc" {
			t.Fatalf("short query value %q registered as a secret", s)
		}
	}
	for _, s := range []string{queryToken, "https://hooks.example.com/start?mode=fast&n=7&code=abc&sig=" + queryToken} {
		if !contains(ad.SecretValues(), s) {
			t.Fatalf("SecretValues lacks %q: %v", s, ad.SecretValues())
		}
	}
	in, err = provider(ad).Instance(context.Background(), wire.CloudInstanceParams{Instance: ref("box"), Locale: "en"})
	if err != nil || in.ConsoleURL != "https://panel.example.com/srv/fast?view=7&code=abc" {
		t.Fatalf("console link of box: %+v %v", in, err)
	}
}

// An escaped part of a webhook path reaches the webhook as written (one segment), and an instance id that cannot be
// a host's name (a slash, a percent sign, a space) is not_found without any request.
func TestWebhookPathEscaping(t *testing.T) {
	hs := newHookServer(t, nil)
	ad := mustNew(t, Config{WebhookBase: hs.URL + "/base%20dir", Hosts: []string{
		"name=vps; start=/hooks/a%2Fb%25c%20d/start; stop=" + hs.URL + "/abs/x%2Fy/stop",
	}})
	for _, k := range []wire.CloudActionKind{"start", "stop"} {
		if _, err := ad.Action(context.Background(), core.Action{Ref: ref("vps"), Kind: k, RequestID: wire.CloudRequestID("esc-" + k)}); err != nil {
			t.Fatalf("%s: %v", k, err)
		}
	}
	got := hs.got()
	if len(got) != 2 || got[0].escapedPath != "/base%20dir/hooks/a%2Fb%25c%20d/start" || got[0].path != "/base dir/hooks/a/b%c d/start" ||
		got[1].escapedPath != "/abs/x%2Fy/stop" {
		t.Fatalf("webhook paths %+v", got)
	}
	p := provider(ad)
	for _, id := range []string{"a/b", "a%2Fb", "a b", "vps/../vps", "vps%20"} {
		if _, err := p.Action(context.Background(), wire.CloudActionParams{Instance: ref(id), Action: "start", RequestID: wire.CloudRequestID("bad-" + id)}); coretest.Code(err) != wire.CloudErrorCodeNotFound {
			t.Fatalf("id %q: %v", id, err)
		}
		if _, err := p.Instance(context.Background(), wire.CloudInstanceParams{Instance: ref(id)}); coretest.Code(err) != wire.CloudErrorCodeNotFound {
			t.Fatalf("id %q: %v", id, err)
		}
	}
	if n := len(hs.got()); n != 2 {
		t.Fatalf("an invalid id made %d requests", n-2)
	}
}

// A configuration holds at most MaxInstances-1 hosts: a list of MaxInstances is one the Provider takes as incomplete.
func TestHostLimit(t *testing.T) {
	hosts := make([]string, core.MaxInstances)
	for i := range hosts {
		hosts[i] = fmt.Sprintf("name=h%d", i)
	}
	if _, err := New(Config{Hosts: hosts}); err == nil || !strings.Contains(err.Error(), "at most 499 hosts") {
		t.Fatalf("500 hosts: %v", err)
	}
	ad := mustNew(t, Config{Hosts: hosts[:MaxHosts]})
	out, err := provider(ad).Instances(context.Background(), wire.CloudInstancesParams{Contribution: "test", Locale: "en", Refresh: true})
	if err != nil || len(out.Instances) != MaxHosts || out.Partial {
		t.Fatalf("499 hosts: %d partial %v %v", len(out.Instances), out.Partial, err)
	}
}

// The adapter's own words follow the caller's language; the Provider puts it into the context.
func TestLocalizedWords(t *testing.T) {
	ad := mustNew(t, Config{Hosts: []string{"name=down; address=127.0.0.1; probe=tcp:" + closedPort(t), "name=none; address=203.0.113.10"}})
	p := provider(ad)
	in, err := p.Instance(context.Background(), wire.CloudInstanceParams{Instance: ref("down"), Locale: "zh-CN"})
	if err != nil || in.StateDetail != "它没有响应探测：连接被拒绝。" {
		t.Fatalf("zh-CN detail: %q %v", in.StateDetail, err)
	}
	list, err := p.Instances(context.Background(), wire.CloudInstancesParams{Contribution: "test", Locale: "zh-CN", Refresh: true})
	if err != nil || list.Instances[1].StateDetail != "没有配置探测，所以不知道它的状态。" {
		t.Fatalf("zh-CN list: %+v %v", list, err)
	}
	in, _ = p.Instance(context.Background(), wire.CloudInstanceParams{Instance: ref("down"), Locale: "en"})
	if in.StateDetail != "It did not answer the probe: the connection was refused." {
		t.Fatalf("en detail: %q", in.StateDetail)
	}
	if _, err := p.Snapshots(context.Background(), wire.CloudSnapshotsParams{Instance: ref("none"), Locale: "zh-CN"}); err == nil || !strings.Contains(err.Error(), "自定义主机没有快照。") {
		t.Fatalf("zh-CN snapshots: %v", err)
	}
	if _, err := p.Instance(context.Background(), wire.CloudInstanceParams{Instance: ref("gone"), Locale: "zh-CN"}); coretest.Code(err) != wire.CloudErrorCodeNotFound || !strings.Contains(err.Error(), "没有这台自定义主机。") {
		t.Fatalf("zh-CN not found: %v", err)
	}
}

func kindStrings(ks []wire.CloudActionKind) []string {
	var out []string
	for _, k := range ks {
		out = append(out, string(k))
	}
	return out
}

// Hosts the list's budget left unprobed make the list incomplete: a problem, so the Provider marks it partial.
func TestUnprobedHostsAreAProblem(t *testing.T) {
	block := func(ctx context.Context, network, addr string) (net.Conn, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	ad := mustNew(t, Config{Dial: block, Hosts: []string{
		"name=a; address=203.0.113.10; probe=tcp:22",
		"name=b; address=203.0.113.11; probe=tcp:22",
		"name=c; address=203.0.113.12",
	}})
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	list, problems, err := ad.Instances(ctx)
	if err != nil || len(list) != 3 || !reflect.DeepEqual(problems, []string{"custom (2 hosts not probed in time)"}) {
		t.Fatalf("Instances: %d %v %v", len(list), problems, err)
	}
	if list[0].State != wire.CloudInstanceStateUnknown || list[0].StateDetail != skippedDetail {
		t.Fatalf("unprobed host: %s %q", list[0].State, list[0].StateDetail)
	}
	ctx2, cancel2 := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel2()
	out, err := provider(ad).Instances(ctx2, wire.CloudInstancesParams{Contribution: "test", Locale: "en", Refresh: true})
	if err != nil || !out.Partial {
		t.Fatalf("through the provider: partial %v %v", out.Partial, err)
	}
	coretest.Valid(t, "cloud.instances", out)
	// The probe action says the probe did not run, not that the network is broken.
	ctx3, cancel3 := context.WithCancel(context.Background())
	cancel3()
	res, err := ad.Action(ctx3, core.Action{Ref: ref("a"), Kind: wire.CloudActionKindCustom, Custom: CustomProbe, RequestID: "p", Locale: "en"})
	if err != nil || !strings.Contains(res.Message, "ended before the probe") {
		t.Fatalf("probe action: %+v %v", res, err)
	}
}

func TestRenew(t *testing.T) {
	dir := t.TempDir()
	hs := newHookServer(t, func(path string) (int, string) {
		if path == "/fail/renew" {
			return http.StatusInternalServerError, `{}`
		}
		return http.StatusOK, `{"message":"Invoice paid"}`
	})
	hosts := []string{
		"name=both; expires=2027-01-31; renews=2027-01-24; renew=/both/renew",
		"name=past; expires=2026-12-01; term_days=10",
		"name=hookonly; renew=/hookonly/renew",
		"name=fail; expires=2027-02-01; renew=/fail/renew",
		"name=nothing; address=203.0.113.10",
		"name=renewsonly; renews=2027-01-20T08:00:00Z; term_days=7",
	}
	cfg := Config{WebhookBase: hs.URL, Hosts: hosts, DataDir: dir}
	ad := mustNew(t, cfg)
	renewID := func(ad *adapter, id, rid, locale string) (wire.CloudActionResult, error) {
		return ad.Action(context.Background(), core.Action{Ref: ref(id), Kind: wire.CloudActionKindCustom, Custom: CustomRenew, RequestID: wire.CloudRequestID(rid), Locale: locale})
	}
	renew := func(ad *adapter, id, locale string) (wire.CloudActionResult, error) {
		return renewID(ad, id, "renew-"+id, locale)
	}
	day := func(s string) time.Time { t, _ := parseDate(s); return t }

	res, err := renew(ad, "both", "en")
	if err != nil || res.Status != wire.CloudActionStatusDone {
		t.Fatalf("both: %+v %v", res, err)
	}
	if res.Message != "Renewed: the service now ends on 2027-03-02. Invoice paid" {
		t.Errorf("message %q", res.Message)
	}
	if lc := res.Instance.Lifecycle; lc == nil || !lc.ExpiresAt.Equal(day("2027-03-02")) || !lc.RenewsAt.Equal(day("2027-02-23")) {
		t.Errorf("both lifecycle %+v", res.Instance.Lifecycle)
	}
	if c := hs.got()[0]; c.path != "/both/renew" || c.body["action"] != "renew" || c.body["host"] != "both" {
		t.Errorf("renew webhook %+v", c)
	}

	// A date already past moves from now.
	res, err = renew(ad, "past", "zh-CN")
	if err != nil || res.Message != "已续期：服务到期日为 2027-01-20。" {
		t.Fatalf("past: %+v %v", res, err)
	}
	res, err = renew(ad, "renewsonly", "en")
	if err != nil || res.Message != "Renewed: the next renewal is on 2027-01-27." || !res.Instance.Lifecycle.RenewsAt.Equal(time.Date(2027, 1, 27, 8, 0, 0, 0, time.UTC)) {
		t.Fatalf("renewsonly: %+v %v", res, err)
	}
	res, err = renew(ad, "hookonly", "en")
	if err != nil || res.Status != wire.CloudActionStatusDone || !strings.HasPrefix(res.Message, "The renewal was requested.") {
		t.Fatalf("hookonly: %+v %v", res, err)
	}
	// A failing webhook moves no date.
	if _, err := renew(ad, "fail", "en"); coretest.Code(err) != wire.CloudErrorCodeFailed {
		t.Fatalf("fail: %v", err)
	}
	if in, _ := ad.Instance(context.Background(), ref("fail")); !in.Lifecycle.ExpiresAt.Equal(day("2027-02-01")) {
		t.Fatalf("fail moved the date: %+v", in.Lifecycle)
	}
	if _, err := renew(ad, "nothing", "en"); coretest.Code(err) != wire.CloudErrorCodeUnsupported {
		t.Fatalf("nothing: %v", err)
	}
	if in, _ := ad.Instance(context.Background(), ref("nothing")); contains(customStrings(in.CustomActions), "renew") {
		t.Fatalf("nothing offers renew: %v", in.CustomActions)
	}

	// The state file: 0600, host names and dates only.
	path := filepath.Join(dir, stateFileName)
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Errorf("state file mode %v", st.Mode().Perm())
	}
	raw, _ := os.ReadFile(path)
	if strings.Contains(string(raw), "http") || strings.Contains(string(raw), "renew\"") || strings.Contains(string(raw), "127.0.0.1") {
		t.Errorf("state file holds more than names and dates: %s", raw)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Errorf("temporary files left: %v", entries)
	}

	// A re-created adapter keeps the renewed dates.
	ad2 := mustNew(t, cfg)
	in, err := ad2.Instance(context.Background(), ref("both"))
	if err != nil || !in.Lifecycle.ExpiresAt.Equal(day("2027-03-02")) || !in.Lifecycle.RenewsAt.Equal(day("2027-02-23")) {
		t.Fatalf("after restart: %+v %v", in.Lifecycle, err)
	}
	// The same request id after a restart (the Provider forgot it): the dates it left, no second webhook call.
	calls := len(hs.got())
	res, err = renew(ad2, "both", "en")
	if err != nil || res.Status != wire.CloudActionStatusDone || !res.Instance.Lifecycle.ExpiresAt.Equal(day("2027-03-02")) ||
		res.Message != "Renewed: the service now ends on 2027-03-02." || len(hs.got()) != calls {
		t.Fatalf("replayed renewal: %+v %v (%d webhook calls)", res, err, len(hs.got())-calls)
	}
	// Renewing again with a new request id moves on from the saved date.
	res, err = renewID(ad2, "both", "renew-both-2", "en")
	if err != nil || !res.Instance.Lifecycle.ExpiresAt.Equal(day("2027-04-01")) {
		t.Fatalf("second renewal: %+v %v", res, err)
	}
	// Two renewals at once, with different request ids: each adds a term.
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := renewID(ad2, "both", fmt.Sprint("renew-both-c", i), "en"); err != nil {
				t.Errorf("concurrent renewal: %v", err)
			}
		}()
	}
	wg.Wait()
	if in, _ := ad2.Instance(context.Background(), ref("both")); !in.Lifecycle.ExpiresAt.Equal(day("2027-05-31")) {
		t.Fatalf("concurrent renewals: %+v", in.Lifecycle)
	}
	// A configured date later than the saved one wins.
	cfg.Hosts = append([]string{"name=both; expires=2028-01-01; renews=2027-01-24"}, hosts[1:]...)
	ad3 := mustNew(t, cfg)
	in, _ = ad3.Instance(context.Background(), ref("both"))
	if !in.Lifecycle.ExpiresAt.Equal(day("2028-01-01")) || !in.Lifecycle.RenewsAt.Equal(day("2027-05-24")) {
		t.Fatalf("config later than state: %+v", in.Lifecycle)
	}
}

// TestRenewBusy: a renewal of a host that waits for another renewal of the same host gives up after renewWait with
// invalid_state, before calling the webhook or moving a date, and the host renews normally once the first is done.
func TestRenewBusy(t *testing.T) {
	var calls atomic.Int32
	entered := make(chan struct{}, 4)
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		entered <- struct{}{}
		<-release
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{}`)
	}))
	t.Cleanup(srv.Close)
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	t.Cleanup(unblock) // runs before srv.Close, so a blocked handler never holds the server open

	ad := mustNew(t, Config{WebhookBase: srv.URL, DataDir: t.TempDir(),
		Hosts: []string{"name=vps; expires=2027-01-31; renew=/vps/renew"}})
	renew := func(rid, locale string) (wire.CloudActionResult, error) {
		return ad.Action(context.Background(), core.Action{Ref: ref("vps"), Kind: wire.CloudActionKindCustom,
			Custom: CustomRenew, RequestID: wire.CloudRequestID(rid), Locale: locale})
	}
	type answer struct {
		res wire.CloudActionResult
		err error
	}
	first := make(chan answer, 1)
	go func() {
		res, err := renew("renew-1", "en")
		first <- answer{res, err}
	}()
	<-entered // the first renewal holds the slot inside the webhook

	_, err := renew("renew-2", "en")
	if coretest.Code(err) != wire.CloudErrorCodeInvalidState || !strings.Contains(err.Error(), "Another renewal of this server is still running.") {
		t.Fatalf("second renewal while the first runs: %v", err)
	}
	if _, err := renew("renew-3", "zh-CN"); coretest.Code(err) != wire.CloudErrorCodeInvalidState || !strings.Contains(err.Error(), "另一次续期还在进行") {
		t.Fatalf("zh second renewal: %v", err)
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("webhook called %d times while busy", n)
	}

	unblock()
	a := <-first
	if a.err != nil || a.res.Status != wire.CloudActionStatusDone || !strings.Contains(a.res.Message, "2027-03-02") {
		t.Fatalf("first renewal: %+v %v", a.res, a.err)
	}
	// The slot is free again: a later renewal calls the webhook and moves the date one term on.
	res, err := renew("renew-4", "en")
	if err != nil || res.Status != wire.CloudActionStatusDone || !strings.Contains(res.Message, "2027-04-01") {
		t.Fatalf("later renewal: %+v %v", res, err)
	}
	if n := calls.Load(); n != 2 {
		t.Fatalf("webhook called %d times, want 2", n)
	}
}

func customStrings(ids []wire.CloudCustomActionID) []string {
	var out []string
	for _, id := range ids {
		out = append(out, string(id))
	}
	return out
}

func TestKeepaliveAndLifecycle(t *testing.T) {
	_, up := tcpListener(t)
	dir := t.TempDir()
	cfg := Config{DataDir: dir, Hosts: []string{
		"name=free; address=127.0.0.1; probe=tcp:" + up + "; keepalive_days=5; spot=true; auto_renew=true; size=micro; cpu=0.5; memory_mb=1024; disk_gb=20; arch=arm64; os=Debian 12; region=Frankfurt; zone=fra-1",
	}}
	ad := mustNew(t, cfg)
	in, err := ad.Instance(context.Background(), ref("free"))
	if err != nil {
		t.Fatal(err)
	}
	lc := in.Lifecycle
	if lc == nil || !lc.Spot || !lc.AutoRenew || !lc.KeepaliveAt.Equal(fixedNow.AddDate(0, 0, 5)) {
		t.Fatalf("lifecycle %+v", lc)
	}
	if in.Spec == nil || *in.Spec != (wire.CloudSpec{Size: "micro", Cpu: 0.5, MemoryMb: 1024, DiskGb: 20, Arch: "arm64", OS: "Debian 12"}) || in.Region != "Frankfurt" || in.Zone != "fra-1" {
		t.Fatalf("spec %+v %s %s", in.Spec, in.Region, in.Zone)
	}
	// The last successful probe is remembered across a restart when it no longer answers.
	cfg.Hosts = []string{"name=free; address=127.0.0.1; probe=tcp:" + closedPort(t) + "; keepalive_days=5"}
	ad = mustNew(t, Config{DataDir: dir, Hosts: cfg.Hosts, Now: func() time.Time { return fixedNow.Add(48 * time.Hour) }})
	in, _ = ad.Instance(context.Background(), ref("free"))
	if in.State != wire.CloudInstanceStateStopped || in.Lifecycle == nil || !in.Lifecycle.KeepaliveAt.Equal(fixedNow.AddDate(0, 0, 5)) {
		t.Fatalf("after restart: %s %+v", in.State, in.Lifecycle)
	}
}

func TestProbeAction(t *testing.T) {
	_, up := tcpListener(t)
	ad := mustNew(t, Config{Hosts: []string{
		"name=up; address=127.0.0.1; probe=tcp:" + up,
		"name=down; address=127.0.0.1; probe=tcp:" + closedPort(t),
		"name=none; address=203.0.113.10",
	}})
	probe := func(id, loc string) (wire.CloudActionResult, error) {
		return ad.Action(context.Background(), core.Action{Ref: ref(id), Kind: wire.CloudActionKindCustom, Custom: CustomProbe, RequestID: "p", Locale: loc})
	}
	res, err := probe("up", "en")
	if err != nil || res.Status != wire.CloudActionStatusDone || !strings.HasPrefix(res.Message, "Reachable: it answered in ") || res.Instance.State != wire.CloudInstanceStateRunning {
		t.Fatalf("up: %+v %v", res, err)
	}
	res, err = probe("down", "zh")
	if err != nil || res.Message != "无法连通：连接被拒绝。" || res.Instance.State != wire.CloudInstanceStateStopped {
		t.Fatalf("down: %+v %v", res, err)
	}
	if _, err := probe("none", "en"); coretest.Code(err) != wire.CloudErrorCodeUnsupported {
		t.Fatalf("none: %v", err)
	}
}

func TestThroughProvider(t *testing.T) {
	_, up := tcpListener(t)
	var calls atomic.Int32
	hs := newHookServer(t, func(path string) (int, string) {
		calls.Add(1)
		return http.StatusOK, `{"message":"queued with ` + testToken + `"}`
	})
	ad := mustNew(t, Config{WebhookBase: hs.URL, WebhookToken: testToken, DataDir: t.TempDir(), Hosts: []string{
		"name=vps; title=Free VPS; address=127.0.0.1; private_address=10.0.0.5; probe=tcp:" + up +
			"; start=/vps/start?key=" + queryToken + "; stop=/vps/stop?key=" + queryToken + "; renew=/vps/renew; expires=2027-01-31; console=https://panel.example.com/vps/1",
		"name=plain; address=198.51.100.7; start=https://hooks.example.org/plain/start",
		"name=v6; address=2001:db8::10; probe=tcp:22",
	}, Dial: func(ctx context.Context, network, addr string) (net.Conn, error) {
		if strings.HasPrefix(addr, "[2001:db8") {
			return nil, errors.New("connection refused")
		}
		var d net.Dialer
		return d.DialContext(ctx, network, addr)
	}})
	p := provider(ad)
	secrets := []string{testToken, queryToken, hs.URL + "/vps/start?key=" + queryToken}

	list, err := p.Instances(context.Background(), wire.CloudInstancesParams{Contribution: "test", Locale: "en", Refresh: true})
	if err != nil {
		t.Fatal(err)
	}
	raw := coretest.Valid(t, "cloud.instances", list)
	coretest.NoLeak(t, string(raw), secrets...)
	if len(list.Instances) != 3 || list.Partial {
		t.Fatalf("list %s", raw)
	}
	if v6 := list.Instances[2]; !reflect.DeepEqual(v6.PublicIps, []string{"2001:db8::10"}) || v6.State != wire.CloudInstanceStateStopped {
		t.Fatalf("v6 %+v", v6)
	}

	in, err := p.Instance(context.Background(), wire.CloudInstanceParams{Instance: ref("vps"), Locale: "en"})
	if err != nil {
		t.Fatal(err)
	}
	raw = coretest.Valid(t, "cloud.instance", in)
	coretest.NoLeak(t, string(raw), secrets...)
	if !reflect.DeepEqual(in.PrivateIps, []string{"127.0.0.1", "10.0.0.5"}) || in.DisplayName != "Free VPS" ||
		!reflect.DeepEqual(in.CustomActions, []wire.CloudCustomActionID{CustomProbe, CustomRenew}) {
		t.Fatalf("instance %s", raw)
	}

	// Console: the configured https link, without credentials.
	res, err := p.Action(context.Background(), wire.CloudActionParams{Instance: ref("vps"), Action: "console", RequestID: "c1"})
	if err != nil {
		t.Fatal(err)
	}
	coretest.Valid(t, "cloud.action", res)
	if u, _ := url.Parse(res.URL); u == nil || u.Scheme != "https" || u.User != nil || res.URL != "https://panel.example.com/vps/1" {
		t.Fatalf("console %q", res.URL)
	}
	if _, err := p.Action(context.Background(), wire.CloudActionParams{Instance: ref("plain"), Action: "console", RequestID: "c2"}); coretest.Code(err) != wire.CloudErrorCodeUnsupported {
		t.Fatalf("console without a link: %v", err)
	}

	// sync_ip reads the host now.
	res, err = p.Action(context.Background(), wire.CloudActionParams{Instance: ref("plain"), Action: "sync_ip", RequestID: "s1"})
	if err != nil || res.Instance == nil || !reflect.DeepEqual(res.Instance.PublicIps, []string{"198.51.100.7"}) {
		t.Fatalf("sync_ip %+v %v", res, err)
	}
	coretest.Valid(t, "cloud.action", res)

	// The same request id twice: the webhook is called once.
	params := wire.CloudActionParams{Instance: ref("vps"), Action: "stop", RequestID: "same-1", Locale: "en"}
	for i := 0; i < 2; i++ {
		res, err := p.Action(context.Background(), params)
		if err != nil {
			t.Fatal(err)
		}
		raw := coretest.Valid(t, "cloud.action", res)
		coretest.NoLeak(t, string(raw), secrets...)
		if res.Status != wire.CloudActionStatusAccepted || !strings.Contains(res.Message, "queued with [redacted]") {
			t.Fatalf("stop %s", raw)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("the webhook was called %d times", calls.Load())
	}

	// renew (confirm) through the provider.
	res, err = p.Action(context.Background(), wire.CloudActionParams{Instance: ref("vps"), Action: "custom", Custom: CustomRenew, RequestID: "renew-1", Locale: "en"})
	if err != nil {
		t.Fatal(err)
	}
	raw = coretest.Valid(t, "cloud.action", res)
	coretest.NoLeak(t, string(raw), secrets...)
	if res.Status != wire.CloudActionStatusDone || res.Instance == nil || !strings.Contains(res.Message, "2027-03-02") {
		t.Fatalf("renew %s", raw)
	}
	// probe (read) through the provider.
	res, err = p.Action(context.Background(), wire.CloudActionParams{Instance: ref("vps"), Action: "custom", Custom: CustomProbe, RequestID: "probe-1", Locale: "en"})
	if err != nil || res.Status != wire.CloudActionStatusDone {
		t.Fatalf("probe %+v %v", res, err)
	}
	coretest.Valid(t, "cloud.action", res)

	// Snapshots: unsupported, as an error the wire carries.
	_, err = p.Snapshots(context.Background(), wire.CloudSnapshotsParams{Instance: ref("vps")})
	if coretest.Code(err) != wire.CloudErrorCodeUnsupported {
		t.Fatalf("snapshots %v", err)
	}
	// An action without a webhook, through the provider.
	_, err = p.Action(context.Background(), wire.CloudActionParams{Instance: ref("plain"), Action: "reboot", RequestID: "r-plain"})
	if coretest.Code(err) != wire.CloudErrorCodeUnsupported {
		t.Fatalf("reboot plain %v", err)
	}
}

func TestManyHostsAreProbedConcurrently(t *testing.T) {
	var inFlight, peak atomic.Int32
	release := make(chan struct{})
	var once sync.Once
	dial := func(ctx context.Context, network, addr string) (net.Conn, error) {
		n := inFlight.Add(1)
		defer inFlight.Add(-1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		if n == probeConcurrency {
			once.Do(func() { close(release) })
		}
		select {
		case <-release:
		case <-ctx.Done():
		}
		c1, c2 := net.Pipe()
		c2.Close()
		return c1, nil
	}
	var hosts []string
	for i := 0; i < 40; i++ {
		hosts = append(hosts, "name=h"+strings.Repeat("x", i%3)+string(rune('a'+i%26))+string(rune('a'+i/26))+"; address=203.0.113.10; probe=tcp:22")
	}
	ad := mustNew(t, Config{Hosts: hosts, Dial: dial})
	list, _, err := ad.Instances(context.Background())
	if err != nil || len(list) != 40 {
		t.Fatalf("%d %v", len(list), err)
	}
	for _, in := range list {
		if in.State != wire.CloudInstanceStateRunning {
			t.Fatalf("%s: %s %q", in.ID, in.State, in.StateDetail)
		}
	}
	if peak.Load() != probeConcurrency {
		t.Fatalf("peak concurrency %d", peak.Load())
	}
}
