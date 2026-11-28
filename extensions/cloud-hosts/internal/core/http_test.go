package core

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/sdk/go/wire"
)

func get(t *testing.T, u string) *http.Request {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		t.Fatal(err)
	}
	return req
}

func TestDoReadsTheAnswer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") != r.URL.Query().Get("ua") {
			http.Error(w, "user agent "+r.Header.Get("User-Agent"), http.StatusBadRequest)
			return
		}
		w.Header().Set("X-Test", "1")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	resp, err := Do(context.Background(), nil, get(t, srv.URL+"/x?ua="+userAgent))
	if err != nil {
		t.Fatal(err)
	}
	if !resp.OK() || resp.Status != http.StatusAccepted || resp.Header.Get("X-Test") != "1" {
		t.Fatalf("%+v", resp)
	}
	var v struct{ OK bool }
	if err := resp.JSON(&v); err != nil || !v.OK {
		t.Fatalf("%v %+v", err, v)
	}
	// A user agent of the adapter's own is kept.
	req := get(t, srv.URL+"/x?ua=custom-agent")
	req.Header.Set("User-Agent", "custom-agent")
	if resp, err := Do(context.Background(), srv.Client(), req); err != nil || resp.Status != http.StatusAccepted {
		t.Fatalf("%+v %v", resp, err)
	}
}

func TestResponseJSON(t *testing.T) {
	v := map[string]int{"keep": 1}
	if err := (&Response{Body: []byte("  \n")}).JSON(&v); err != nil || v["keep"] != 1 {
		t.Fatalf("an empty body: %v %v", err, v)
	}
	err := (&Response{Body: []byte("<html>key=test-secret</html>")}).JSON(&v)
	if e := AsError(err); e == nil || e.Code != wire.CloudErrorCodeFailed || strings.Contains(e.Message, "test-secret") {
		t.Fatalf("%v", err)
	}
	for status, ok := range map[int]bool{199: false, 200: true, 204: true, 299: true, 300: false, 404: false} {
		if (&Response{Status: status}).OK() != ok {
			t.Errorf("OK(%d)", status)
		}
	}
}

func TestDoBoundsTheBody(t *testing.T) {
	var size atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(bytes.Repeat([]byte("a"), int(size.Load())))
	}))
	defer srv.Close()
	size.Store(MaxResponse)
	resp, err := Do(context.Background(), srv.Client(), get(t, srv.URL))
	if err != nil || len(resp.Body) != MaxResponse {
		t.Fatalf("exactly the limit: %v", err)
	}
	size.Store(MaxResponse + 1)
	_, err = Do(context.Background(), srv.Client(), get(t, srv.URL+"/p?key=test-secret-key-0001"))
	e := AsError(err)
	if e == nil || e.Code != wire.CloudErrorCodeFailed || !strings.Contains(e.Message, "127.0.0.1") ||
		strings.Contains(e.Message, "test-secret-key-0001") || strings.Contains(e.Message, "/p") {
		t.Fatalf("%+v", e)
	}
}

func TestDoTransportErrorNamesOnlyTheHost(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	u := srv.URL + "/v1/servers/secret-path?api_key=test-secret-key-0001&x=y"
	srv.Close()
	_, err := Do(context.Background(), NewHTTPClient(HTTPOptions{}), get(t, u))
	e := AsError(err)
	if e == nil || e.Code != wire.CloudErrorCodeFailed || !e.Transient || e.Message != "127.0.0.1 could not be reached" {
		t.Fatalf("%+v", e)
	}
	for _, bad := range []string{"test-secret-key-0001", "api_key", "secret-path", "/v1"} {
		if strings.Contains(e.Message, bad) {
			t.Fatalf("%q in %q", bad, e.Message)
		}
	}
}

func TestDoTimeouts(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer srv.Close()
	// The caller's deadline.
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	_, err := Do(ctx, srv.Client(), get(t, srv.URL+"/?token=test-token-0001"))
	// Not transient: a request that timed out may have reached the vendor.
	if e := AsError(err); e == nil || e.Transient || e.Message != "127.0.0.1 did not answer in time" {
		t.Fatalf("%+v", e)
	}
	// The client's own timeout.
	c := NewHTTPClient(HTTPOptions{Timeout: 50 * time.Millisecond})
	_, err = Do(context.Background(), c, get(t, srv.URL+"/?token=test-token-0001"))
	if e := AsError(err); e == nil || e.Transient || e.Message != "127.0.0.1 did not answer in time" {
		t.Fatalf("%+v", e)
	}
	if NewHTTPClient(HTTPOptions{}).Timeout != 20*time.Second {
		t.Fatal("the default timeout")
	}
}

func TestNoRedirectIsFollowed(t *testing.T) {
	var hits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1) }))
	defer target.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/steal", http.StatusFound)
	}))
	defer srv.Close()
	req := get(t, srv.URL)
	req.Header.Set("Authorization", "Bearer test-token-0001")
	resp, err := Do(context.Background(), NewHTTPClient(HTTPOptions{}), req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Status != http.StatusFound || resp.Header.Get("Location") != target.URL+"/steal" || hits.Load() != 0 {
		t.Fatalf("status %d, target hit %d times", resp.Status, hits.Load())
	}
}

func fingerprint(srv *httptest.Server) string {
	sum := sha256.Sum256(srv.Certificate().Raw)
	return hex.EncodeToString(sum[:])
}

func colons(h string) string {
	var parts []string
	for i := 0; i < len(h); i += 2 {
		parts = append(parts, strings.ToUpper(h[i:i+2]))
	}
	return strings.Join(parts, ":")
}

func TestPinSHA256(t *testing.T) {
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("ok")) }))
	srv.Config.ErrorLog = log.New(io.Discard, "", 0) // the refused handshakes below are expected
	srv.StartTLS()
	defer srv.Close()
	pin := fingerprint(srv)
	for _, form := range []string{pin, colons(pin), " " + strings.ToUpper(pin) + " ", "sha256:" + pin} {
		resp, err := Do(context.Background(), NewHTTPClient(HTTPOptions{PinSHA256: form}), get(t, srv.URL))
		if err != nil || string(resp.Body) != "ok" {
			t.Fatalf("pin %q: %v", form, err)
		}
	}
	wrong := strings.Repeat("ab", 32)
	_, err := Do(context.Background(), NewHTTPClient(HTTPOptions{PinSHA256: wrong}), get(t, srv.URL+"/?k=test-token-0001"))
	if e := AsError(err); e == nil || e.Message != "127.0.0.1 presented a certificate that was not accepted" {
		t.Fatalf("a wrong pin: %+v", e)
	}
	// Without a pin a self-signed certificate is refused.
	_, err = Do(context.Background(), NewHTTPClient(HTTPOptions{}), get(t, srv.URL))
	if e := AsError(err); e == nil || e.Message != "127.0.0.1 presented a certificate that was not accepted" {
		t.Fatalf("no pin: %+v", e)
	}
}

func TestNormalizePin(t *testing.T) {
	h := strings.Repeat("0123456789abcdef", 4)
	for in, want := range map[string]string{
		h:                        h,
		strings.ToUpper(h):       h,
		colons(h):                h,
		"  " + colons(h) + "\n":  h,
		"sha256:" + h:            h,
		"sha256=" + h:            h,
		h[:62]:                   "",
		h + "00":                 "",
		strings.Repeat("zz", 32): "",
		"":                       "",
	} {
		if got := normalizePin(in); got != want {
			t.Errorf("normalizePin(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestBugNormalizePinUppercasePrefix(t *testing.T) {
	h := strings.Repeat("0123456789abcdef", 4)
	for _, in := range []string{"SHA256:" + h, "SHA256 Fingerprint=" + colons(h), "sha256 Fingerprint=" + colons(h)} {
		if got := normalizePin(in); got != h {
			t.Errorf("normalizePin(%q) = %q", in, got)
		}
	}
}

func TestStatusError(t *testing.T) {
	for _, c := range []struct {
		status    int
		code      wire.CloudErrorCode
		transient bool
	}{
		{401, wire.CloudErrorCodeNotSignedIn, false}, {403, wire.CloudErrorCodeNotSignedIn, false},
		{404, wire.CloudErrorCodeNotFound, false}, {410, wire.CloudErrorCodeNotFound, false},
		{409, wire.CloudErrorCodeInvalidState, false}, {423, wire.CloudErrorCodeInvalidState, false},
		{429, wire.CloudErrorCodeRateLimited, true},
		{500, wire.CloudErrorCodeFailed, false}, {502, wire.CloudErrorCodeFailed, false}, {503, wire.CloudErrorCodeFailed, false},
		{400, wire.CloudErrorCodeFailed, false}, {422, wire.CloudErrorCodeFailed, false}, {302, wire.CloudErrorCodeFailed, false},
	} {
		e := StatusError("Example", c.status, "")
		if e.Code != c.code || e.Transient != c.transient || e.Message != "Example answered "+http.StatusText(c.status) {
			t.Errorf("%d: %+v", c.status, e)
		}
	}
	if e := StatusError("Example", 599, ""); e.Message != "Example answered an error" {
		t.Errorf("%q", e.Message)
	}
	e := StatusError("Example", 400, "bad\nrequest "+strings.Repeat("x", 300))
	if !strings.HasPrefix(e.Message, "Example: bad request x") || strings.Contains(e.Message, "\n") || len([]rune(e.Message)) != len("Example: bad request ")+300 {
		t.Errorf("%q", e.Message)
	}
}

func TestJoinURL(t *testing.T) {
	q := url.Values{"page": {"2"}, "name": {"a b"}}
	for _, c := range []struct {
		base, path string
		query      url.Values
		want       string
	}{
		{"https://api.example.com", "servers", nil, "https://api.example.com/servers"},
		{"https://api.example.com/", "/servers", nil, "https://api.example.com/servers"},
		{"https://api.example.com/v2/", "/servers/1", q, "https://api.example.com/v2/servers/1?name=a+b&page=2"},
		{" https://user:pw@panel.example.org:8443/base#frag ", "api/x", nil, "https://panel.example.org:8443/base/api/x"},
		{"http://127.0.0.1:8080/api?old=1", "list", nil, "http://127.0.0.1:8080/api/list"},
	} {
		u, err := JoinURL(c.base, c.path, c.query)
		if err != nil || u.String() != c.want {
			t.Errorf("JoinURL(%q, %q) = %v %v, want %q", c.base, c.path, u, err, c.want)
		}
	}
	for _, bad := range []string{"", "api.example.com", "ftp://api.example.com", "http://", "https:///x", "javascript:x", "://x"} {
		if u, err := JoinURL(bad, "x", nil); err == nil || AsError(err).Code != wire.CloudErrorCodeFailed {
			t.Errorf("JoinURL(%q) = %v", bad, u)
		}
	}
}

func TestConsoleBase(t *testing.T) {
	for in, want := range map[string]string{
		"https://panel.example.org/":                   "https://panel.example.org",
		" https://u:p@panel.example.org:8443/x/?k=1#f": "https://panel.example.org:8443/x",
		"https://panel.example.org":                    "https://panel.example.org",
		"http://panel.example.org/":                    "",
		"panel.example.org":                            "",
		"https://":                                     "",
		"":                                             "",
	} {
		if got := ConsoleBase(in); got != want {
			t.Errorf("ConsoleBase(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestBugJoinURLReescapesAnEscapedPath(t *testing.T) {
	u, err := JoinURL("https://api.example.com", "/servers/"+url.PathEscape("a/b c"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := u.String(), "https://api.example.com/servers/a%2Fb%20c"; got != want {
		t.Fatalf("JoinURL = %q, want %q", got, want)
	}
}

func TestBugDoFollowsRedirectsWithACallersClient(t *testing.T) {
	var hits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1) }))
	defer target.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/steal", http.StatusTemporaryRedirect)
	}))
	defer srv.Close()
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/api", strings.NewReader("key=test-secret-key-0001"))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-API-Key", "test-token-0001")
	resp, err := Do(context.Background(), &http.Client{}, req)
	if err != nil {
		t.Fatal(err)
	}
	if hits.Load() != 0 || resp.Status != http.StatusTemporaryRedirect {
		t.Fatalf("status %d, target hit %d times", resp.Status, hits.Load())
	}
}
