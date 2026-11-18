package main

import (
	"bufio"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// isNodePortProbe, nodePortProbeAgents and fixturePortProbe are copied from
// extensions/discord/probe_compat_test.go, which docs/development.md requires for synthetic fixtures.
// Keep the two copies in step. The discord copy is the reference for the negative controls below.

// These public compatibility identifiers belong only to the synthetic fixture.
// A resident node uses this complete unauthenticated shape to discover dev ports.
var nodePortProbeAgents = []string{"persona-ports", "jarvis-ports"}

func isNodePortProbe(r *http.Request) bool {
	if r.Method != http.MethodGet || r.URL == nil || r.RequestURI != "/" || r.URL.Path != "/" ||
		r.URL.RawPath != "" || r.URL.RawQuery != "" || r.URL.User != nil || r.ProtoMajor != 1 || r.ProtoMinor != 1 ||
		r.ContentLength != 0 || len(r.TransferEncoding) != 0 || r.Body != nil && r.Body != http.NoBody || len(r.Header) != 3 {
		return false
	}
	for name, values := range r.Header {
		if len(values) != 1 {
			return false
		}
		switch name {
		case "User-Agent", "Accept", "Connection":
		default:
			return false
		}
	}
	if r.Header.Get("Accept") != "text/html" || r.Header.Get("Connection") != "close" {
		return false
	}
	for _, agent := range nodePortProbeAgents {
		if r.UserAgent() == agent {
			return true
		}
	}
	return false
}

func fixturePortProbe(agent string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("User-Agent", agent)
	r.Header.Set("Accept", "text/html")
	r.Header.Set("Connection", "close")
	return r
}

// fakeServer starts a synthetic server that answers with serveFake(h). Every fake hub or listener in
// this package starts through it, so the probe rule lives in one place.
func fakeServer(h http.HandlerFunc) *httptest.Server {
	return httptest.NewServer(serveFake(h))
}

// serveFake answers an exact node port probe with 204 and never passes it to h, so the probe is neither
// counted nor asserted on. Every other request reaches h unchanged.
func serveFake(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if isNodePortProbe(r) {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		h(w, r)
	}
}

// Exact-shape check: the complete probe is excluded, and each near miss is still counted and answered by h.
func TestFakeServerExcludesOnlyNodePortProbes(t *testing.T) {
	for _, agent := range nodePortProbeAgents {
		t.Run(agent, func(t *testing.T) {
			var calls atomic.Int32
			w := httptest.NewRecorder()
			serveFake(func(http.ResponseWriter, *http.Request) { calls.Add(1) })(w, fixturePortProbe(agent))
			if w.Code != http.StatusNoContent || calls.Load() != 0 {
				t.Fatalf("node probe: status %d, requests %d", w.Code, calls.Load())
			}
		})
	}
	cases := []struct {
		name string
		edit func(*http.Request)
	}{
		{"ordinary-root", func(r *http.Request) { r.Header.Set("User-Agent", "example-client") }},
		{"post-root", func(r *http.Request) { r.Method = http.MethodPost }},
		{"api", func(r *http.Request) {
			r.URL.Path, r.RequestURI = "/api/v10/applications/@me", "/api/v10/applications/@me"
		}},
		{"query", func(r *http.Request) { r.URL.RawQuery, r.RequestURI = "x=1", "/?x=1" }},
		{"escaped-root", func(r *http.Request) { r.URL.RawPath, r.RequestURI = "%2F", "%2F" }},
		{"body", func(r *http.Request) {
			r.Body = http.NoBody
			r.ContentLength = int64(len("payload"))
		}},
		{"empty-reader-body", func(r *http.Request) { r.Body = httptest.NewRequest(http.MethodGet, "/", strings.NewReader("")).Body }},
		{"authorization", func(r *http.Request) { r.Header.Set("Authorization", "Bearer test-token") }},
		{"empty-authorization", func(r *http.Request) { r.Header.Set("Authorization", "") }},
		{"proxy-authorization", func(r *http.Request) { r.Header.Set("Proxy-Authorization", "example") }},
		{"extra-header", func(r *http.Request) { r.Header.Set("X-Example", "value") }},
		{"duplicate-agent", func(r *http.Request) { r.Header.Add("User-Agent", nodePortProbeAgents[0]) }},
		{"wrong-accept", func(r *http.Request) { r.Header.Set("Accept", "application/json") }},
		{"http2", func(r *http.Request) { r.ProtoMajor, r.ProtoMinor = 2, 0 }},
		{"cookie", func(r *http.Request) { r.Header.Set("Cookie", "example=value") }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			r := fixturePortProbe(nodePortProbeAgents[0])
			tc.edit(r)
			w := httptest.NewRecorder()
			serveFake(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.WriteHeader(http.StatusAccepted)
			})(w, r)
			if calls.Load() != 1 || w.Code != http.StatusAccepted {
				t.Fatalf("ordinary request was excluded or altered: status %d, requests %d", w.Code, calls.Load())
			}
		})
	}
}

// The same split on real sockets: the server's own parser must produce the shape the fixture describes.
func TestFakeServerOnTheWire(t *testing.T) {
	var calls atomic.Int32
	srv := fakeServer(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusAccepted)
	})
	defer srv.Close()
	probe := "GET / HTTP/1.1\r\nHost: fake\r\nUser-Agent: persona-ports\r\nAccept: text/html\r\nConnection: close\r\n\r\n"
	if status := rawExchange(t, srv, probe); status != http.StatusNoContent || calls.Load() != 0 {
		t.Fatalf("node probe on the wire: status %d, requests %d", status, calls.Load())
	}
	ordinary := "GET / HTTP/1.1\r\nHost: fake\r\nUser-Agent: example-client\r\nAccept: text/html\r\nConnection: close\r\n\r\n"
	if status := rawExchange(t, srv, ordinary); status != http.StatusAccepted || calls.Load() != 1 {
		t.Fatalf("ordinary request on the wire: status %d, requests %d", status, calls.Load())
	}
}

// rawExchange sends request bytes to srv and returns the status code of the reply.
func rawExchange(t *testing.T, srv *httptest.Server, request string) int {
	t.Helper()
	conn, err := net.Dial("tcp", srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(conn, request); err != nil {
		t.Fatal(err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	return resp.StatusCode
}
