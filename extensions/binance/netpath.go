package main

// The way out to Binance. Without proxy_url, requests go through the environment's proxy (the hub's egress proxy in
// the sandbox), which lets only the manifest's Binance hosts through. With proxy_url (an HTTP proxy on the hub
// machine, http://host.persona.internal:<port>), each connection is a chain: an HTTP CONNECT tunnel through the egress
// proxy to that proxy, then a second CONNECT inside the tunnel to the Binance host. Outside a sandbox (no environment
// proxy), host.persona.internal is this machine's loopback.

import (
	"bufio"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// hostAlias is the egress proxy's name for the hub machine (docs: "Local services on the hub machine").
const hostAlias = "host.persona.internal"

// parseProxyURL reads proxy_url: http://host.persona.internal:<port>, no user info, path or query. Only the hub
// machine's alias is accepted: the manifest grants host.persona.internal and nothing else for a proxy.
func parseProxyURL(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return nil, errors.New("proxy_url is not a valid URL")
	}
	if !strings.EqualFold(u.Scheme, "http") {
		return nil, errors.New("proxy_url must be an http:// proxy (HTTP CONNECT)")
	}
	if u.User != nil {
		return nil, errors.New("proxy_url must not carry a username or password")
	}
	if (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("proxy_url must not have a path, query or fragment")
	}
	if !strings.EqualFold(u.Hostname(), hostAlias) {
		return nil, errors.New("proxy_url must be http://" + hostAlias + ":<port> (a proxy on the hub machine)")
	}
	if u.Port() == "" {
		return nil, errors.New("proxy_url needs a port")
	}
	return &url.URL{Scheme: "http", Host: net.JoinHostPort(hostAlias, u.Port())}, nil
}

// upstreamFunc names the HTTP proxy the program must go through (the hub's egress proxy), nil for none.
type upstreamFunc func() (*url.URL, error)

func envUpstream() (*url.URL, error) {
	req := &http.Request{URL: &url.URL{Scheme: "https", Host: "api.binance.com"}}
	return http.ProxyFromEnvironment(req)
}

// dialer opens tunnels to Binance hosts through proxy (via upstream when it names one).
type dialer struct {
	proxy    *url.URL
	upstream upstreamFunc
	attempts int
	timeout  time.Duration
	base     func(ctx context.Context, network, addr string) (net.Conn, error)
}

func newDialer(proxy *url.URL, upstream upstreamFunc) *dialer {
	d := &net.Dialer{KeepAlive: 30 * time.Second}
	// The proxy may retry its own upstream (5 s × 3), so one attempt gets 20 s; two attempts in all.
	return &dialer{proxy: proxy, upstream: upstream, attempts: 2, timeout: 20 * time.Second, base: d.DialContext}
}

func (d *dialer) DialContext(ctx context.Context, _, addr string) (net.Conn, error) {
	var last error
	for i := 0; i < d.attempts; i++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		c, err := d.dialOnce(ctx, addr)
		if err == nil {
			return c, nil
		}
		last = err
		var fatal *fatalDialError
		if errors.As(err, &fatal) {
			break
		}
	}
	return nil, fmt.Errorf("proxy: %w", last)
}

// fatalDialError is a refusal retrying will not fix.
type fatalDialError struct{ msg string }

func (e *fatalDialError) Error() string { return e.msg }

func (d *dialer) dialOnce(ctx context.Context, addr string) (net.Conn, error) {
	actx, cancel := context.WithTimeout(ctx, d.timeout)
	defer cancel()
	var up *url.URL
	if d.upstream != nil {
		var err error
		if up, err = d.upstream(); err != nil {
			return nil, &fatalDialError{"environment proxy is invalid"}
		}
	}
	var conn net.Conn
	var err error
	if up != nil {
		conn, err = d.base(actx, "tcp", hostPort(up))
		if err != nil {
			return nil, errors.New("cannot reach the hub's egress proxy")
		}
		if err = connect(actx, conn, d.proxy.Host, up.User, "egress proxy"); err != nil {
			_ = conn.Close()
			return nil, err
		}
	} else {
		conn, err = d.base(actx, "tcp", net.JoinHostPort("127.0.0.1", d.proxy.Port()))
		if err != nil {
			return nil, errors.New("cannot reach the proxy on this machine")
		}
	}
	if err = connect(actx, conn, addr, nil, "proxy"); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return conn, nil
}

func hostPort(u *url.URL) string {
	if u.Port() == "" {
		return net.JoinHostPort(u.Hostname(), "80")
	}
	return u.Host
}

// connect sends CONNECT target on conn and reads the answer; deadline-bound by ctx.
func connect(ctx context.Context, conn net.Conn, target string, user *url.Userinfo, who string) error {
	stop := context.AfterFunc(ctx, func() { _ = conn.SetDeadline(time.Unix(1, 0)) })
	defer func() {
		stop()
		_ = conn.SetDeadline(time.Time{})
	}()
	req := "CONNECT " + target + " HTTP/1.1\r\nHost: " + target + "\r\n"
	if user != nil {
		pw, _ := user.Password()
		req += "Proxy-Authorization: Basic " + base64.StdEncoding.EncodeToString([]byte(user.Username()+":"+pw)) + "\r\n"
	}
	if _, err := io.WriteString(conn, req+"\r\n"); err != nil {
		return errors.New(who + ": write failed")
	}
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, &http.Request{Method: http.MethodConnect})
	if err != nil {
		return errors.New(who + ": closed the tunnel (timeout or reset)")
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		msg := fmt.Sprintf("%s answered %d to CONNECT %s: %s", who, resp.StatusCode, target, strings.TrimSpace(string(body)))
		if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusProxyAuthRequired {
			return &fatalDialError{msg}
		}
		return errors.New(msg)
	}
	if br.Buffered() > 0 {
		return errors.New(who + ": sent data before the tunnel opened")
	}
	return nil
}

// newHTTPClient is the client for Binance: through proxy when set, else the environment's proxy.
func newHTTPClient(proxy *url.URL, upstream upstreamFunc, timeout time.Duration) *http.Client {
	t := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          8,
		IdleConnTimeout:       60 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 15 * time.Second,
	}
	if proxy != nil {
		t.Proxy = nil
		t.DialContext = newDialer(proxy, upstream).DialContext
	}
	return &http.Client{Transport: t, Timeout: timeout}
}
