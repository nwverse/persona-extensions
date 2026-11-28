package generic

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"sync/atomic"
	"time"
)

// probeTimeout bounds one probe: with 16 at a time, a long list is still read within the call's budget.
const probeTimeout = 3 * time.Second

// probeConcurrency is how many hosts are probed at once.
const probeConcurrency = 16

// DialFunc opens a connection the way net.Dialer.DialContext does.
type DialFunc func(ctx context.Context, network, addr string) (net.Conn, error)

// The proxy's answers. Their text names neither the proxy nor its credentials.
var (
	errProxyForbidden = errors.New("the network proxy does not allow this address")
	errProxyRefused   = errors.New("the network proxy could not connect to the address")
	errProxyTimeout   = errors.New("the network proxy timed out connecting to the address")
	errProxyUnusable  = errors.New("the network proxy could not be used")
)

// proxyDialer is a DialFunc that opens TCP connections through the HTTP proxy proxy names for them (an HTTP CONNECT),
// and directly when it names none. proxy nil dials directly always. proxy is asked for an https URL first (so
// HTTPS_PROXY wins) and, when it names none, for an http URL (so an environment that only sets HTTP_PROXY is used
// too); NO_PROXY applies to both.
func proxyDialer(proxy func(*http.Request) (*url.URL, error)) DialFunc {
	d := &net.Dialer{}
	ask := func(scheme, addr string) (*url.URL, error) {
		return proxy(&http.Request{Method: http.MethodConnect, URL: &url.URL{Scheme: scheme, Host: addr}, Header: http.Header{}})
	}
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		if proxy != nil && (network == "tcp" || network == "tcp4" || network == "tcp6") {
			pu, err := ask("https", addr)
			if err == nil && pu == nil {
				pu, err = ask("http", addr)
			}
			if err != nil {
				return nil, errProxyUnusable
			}
			if pu != nil {
				return connectVia(ctx, d, pu, addr)
			}
		}
		return d.DialContext(ctx, network, addr)
	}
}

// connectVia opens a tunnel to addr through the HTTP proxy at pu. A 2xx answer is the tunnel; 403 is
// errProxyForbidden; 502 and 503 (the proxy could not reach the address) errProxyRefused; 504 errProxyTimeout; any
// other answer (407, 400, 405, ...) is the proxy's own failure, errProxyUnusable, which says nothing of the host.
func connectVia(ctx context.Context, d *net.Dialer, pu *url.URL, addr string) (net.Conn, error) {
	if pu.Scheme != "http" && pu.Scheme != "https" || pu.Hostname() == "" {
		return nil, errProxyUnusable
	}
	phost := pu.Host
	if pu.Port() == "" {
		if pu.Scheme == "https" {
			phost = net.JoinHostPort(pu.Hostname(), "443")
		} else {
			phost = net.JoinHostPort(pu.Hostname(), "80")
		}
	}
	conn, err := d.DialContext(ctx, "tcp", phost)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errProxyUnusable
	}
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	fail := func(e error) (net.Conn, error) {
		stop()
		conn.Close()
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, e
	}
	if pu.Scheme == "https" {
		tc := tls.Client(conn, &tls.Config{ServerName: pu.Hostname(), MinVersion: tls.VersionTLS12})
		if err := tc.HandshakeContext(ctx); err != nil {
			return fail(errProxyUnusable)
		}
		conn = tc
	}
	req := &http.Request{Method: http.MethodConnect, URL: &url.URL{Opaque: addr}, Host: addr, Header: http.Header{}}
	if pu.User != nil {
		pass, _ := pu.User.Password()
		req.Header.Set("Proxy-Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(pu.User.Username()+":"+pass)))
	}
	if err := req.Write(conn); err != nil {
		return fail(errProxyUnusable)
	}
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, req)
	if err != nil {
		return fail(errProxyUnusable)
	}
	resp.Body.Close()
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
	case resp.StatusCode == http.StatusForbidden:
		return fail(errProxyForbidden)
	case resp.StatusCode == http.StatusBadGateway || resp.StatusCode == http.StatusServiceUnavailable:
		return fail(errProxyRefused)
	case resp.StatusCode == http.StatusGatewayTimeout:
		return fail(errProxyTimeout)
	default:
		return fail(errProxyUnusable)
	}
	if !stop() {
		conn.Close()
		return nil, ctx.Err()
	}
	if br.Buffered() > 0 {
		return &bufConn{Conn: conn, r: br}, nil
	}
	return conn, nil
}

// bufConn is a tunnel whose first bytes were read with the proxy's answer.
type bufConn struct {
	net.Conn
	r *bufio.Reader
}

func (c *bufConn) Read(p []byte) (int, error) { return c.r.Read(p) }

// outcome is what one probe found.
type outcome int

const (
	probeNone      outcome = iota // no probe is configured
	probeAlive                    // it answered
	probeRefused                  // it refused the connection
	probeSlow                     // it did not answer in time
	probeAnswered                 // it accepted the connection, but the HTTP(S) exchange failed (a certificate not accepted, ...)
	probeHTTPError                // an HTTP probe got a 5xx: something answers, but whether the host is up is not known
	probeForbidden                // the sandbox's proxy does not let the extension reach it
	probeNoProxy                  // the proxy could not be used
	probeSkipped                  // the call's budget ran out before it was probed
)

// probeResult is one probe.
type probeResult struct {
	outcome outcome
	rtt     time.Duration
	status  int  // an HTTP probe's status, when it answered
	tlsBad  bool // probeAnswered: the TLS handshake failed (most often: the certificate was not accepted)
	at      time.Time
}

// probe probes h now. ctx is the whole call's; each probe has its own short bound under it.
func (a *adapter) probe(ctx context.Context, h *host) probeResult {
	if h.probe == nil {
		return probeResult{outcome: probeNone}
	}
	if ctx.Err() != nil {
		return probeResult{outcome: probeSkipped}
	}
	pctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	start := time.Now()
	classify := func(err error) probeResult {
		switch {
		case errors.Is(err, errProxyForbidden):
			return probeResult{outcome: probeForbidden}
		case errors.Is(err, errProxyUnusable):
			return probeResult{outcome: probeNoProxy}
		case ctx.Err() != nil:
			return probeResult{outcome: probeSkipped}
		case pctx.Err() != nil || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, errProxyTimeout):
			return probeResult{outcome: probeSlow}
		}
		var ne net.Error
		if errors.As(err, &ne) && ne.Timeout() {
			return probeResult{outcome: probeSlow}
		}
		return probeResult{outcome: probeRefused}
	}
	if h.probe.tcp {
		c, err := a.dial(pctx, "tcp", h.probe.target)
		if err != nil {
			return classify(err)
		}
		rtt := time.Since(start)
		c.Close()
		return probeResult{outcome: probeAlive, rtt: rtt, at: a.now()}
	}
	req, err := http.NewRequestWithContext(pctx, http.MethodGet, h.probe.target, nil)
	if err != nil {
		return probeResult{outcome: probeRefused}
	}
	req.Header.Set("User-Agent", "persona-cloud-hosts")
	// A connection that was opened (the TLS handshake began, or the request got its connection) means the host
	// answered, whatever then failed: a certificate that does not verify (an https probe of a plain IP almost never
	// does) is not a host that is off.
	var connected, tlsBad atomic.Bool
	req = req.WithContext(httptrace.WithClientTrace(pctx, &httptrace.ClientTrace{
		TLSHandshakeStart: func() { connected.Store(true) },
		TLSHandshakeDone: func(_ tls.ConnectionState, err error) {
			if err != nil {
				tlsBad.Store(true)
			}
		},
		GotConn: func(httptrace.GotConnInfo) { connected.Store(true) },
	}))
	resp, err := a.probeClient.Do(req)
	if err != nil {
		if r := classify(err); r.outcome != probeRefused && r.outcome != probeSlow || !connected.Load() {
			return r
		}
		return probeResult{outcome: probeAnswered, tlsBad: tlsBad.Load(), rtt: time.Since(start), at: a.now()}
	}
	rtt := time.Since(start)
	resp.Body.Close()
	if resp.StatusCode >= 500 {
		return probeResult{outcome: probeHTTPError, status: resp.StatusCode, rtt: rtt}
	}
	return probeResult{outcome: probeAlive, status: resp.StatusCode, rtt: rtt, at: a.now()}
}

// newProbeClient is the client of HTTP probes: every connection through dial (so the proxy rule is the TCP
// probe's), a fresh connection each time, no redirect followed, the TLS settings of base's transport kept.
func newProbeClient(base *http.Client, dial DialFunc) *http.Client {
	var t *http.Transport
	if base != nil {
		if bt, ok := base.Transport.(*http.Transport); ok {
			t = bt.Clone()
		}
	}
	if t == nil {
		t = http.DefaultTransport.(*http.Transport).Clone()
	}
	t.Proxy = nil
	t.DialContext = dial
	t.DialTLSContext = nil
	t.DisableKeepAlives = true
	t.ResponseHeaderTimeout = probeTimeout
	return &http.Client{Transport: t, Timeout: probeTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}
