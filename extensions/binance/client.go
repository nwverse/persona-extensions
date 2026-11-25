package main

import (
	"context"
	"crypto/ed25519"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// apiError is Binance's error answer ({"code": -2015, "msg": "…"}) or an HTTP status without one.
type apiError struct {
	Status int
	Code   int
	Msg    string
}

func (e *apiError) Error() string {
	if e.Code != 0 {
		return fmt.Sprintf("Binance %d: %s", e.Code, e.Msg)
	}
	return fmt.Sprintf("Binance HTTP %d: %s", e.Status, e.Msg)
}

var (
	errNoKey     = errors.New("no API key is set: fill in API Key and Ed25519 private key in Settings › Extensions › 币安")
	errNotListed = errors.New("refused: this endpoint is not in the extension's list")
	errBanned    = errors.New("Binance rate limit: waiting before the next request")
)

// signer holds the key; the private key never leaves it.
type signer struct {
	apiKey string
	priv   ed25519.PrivateKey
}

// newSigner reads the API key and a PKCS#8 Ed25519 private key in PEM.
func newSigner(apiKey, privPEM string) (*signer, error) {
	apiKey = strings.TrimSpace(apiKey)
	privPEM = strings.TrimSpace(privPEM)
	if apiKey == "" && privPEM == "" {
		return nil, nil
	}
	if apiKey == "" || privPEM == "" {
		return nil, errors.New("both API Key and Ed25519 private key are needed")
	}
	if len(apiKey) > 128 || strings.ContainsAny(apiKey, " \t\r\n") {
		return nil, errors.New("API Key does not look like a Binance key")
	}
	block, _ := pem.Decode([]byte(privPEM))
	if block == nil {
		return nil, errors.New("Ed25519 private key is not PEM (-----BEGIN PRIVATE KEY-----)")
	}
	k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, errors.New("Ed25519 private key cannot be read (PKCS#8 expected)")
	}
	priv, ok := k.(ed25519.PrivateKey)
	if !ok {
		return nil, errors.New("the private key is not an Ed25519 key")
	}
	return &signer{apiKey: apiKey, priv: priv}, nil
}

// sign is Binance's Ed25519 signature: base64 of the signature over the percent-encoded query string.
func (s *signer) sign(payload string) string {
	return base64.StdEncoding.EncodeToString(ed25519.Sign(s.priv, []byte(payload)))
}

// client calls Binance REST, only the endpoints in the table.
type client struct {
	http   *http.Client
	signer *signer
	now    func() time.Time

	mu       sync.Mutex
	offset   time.Duration // server time - local time
	offsetAt time.Time
	banUntil time.Time
	weight   map[string]string // last X-MBX-USED-WEIGHT-1M by host

	// hostURL maps a host of the table to the URL requests go to (tests point it at a fake server).
	hostURL func(string) string
}

func newClient(hc *http.Client, s *signer) *client {
	return &client{http: hc, signer: s, now: time.Now, weight: map[string]string{}}
}

// public calls an endpoint that needs no key.
func (c *client) public(ctx context.Context, host, path string, q url.Values, out any) error {
	return c.do(ctx, host, "GET", path, q, out, false)
}

// signed calls a signed endpoint of the table with q (timestamp, recvWindow and signature added).
func (c *client) signed(ctx context.Context, host, method, path string, q url.Values, out any) error {
	return c.do(ctx, host, method, path, q, out, true)
}

func (c *client) do(ctx context.Context, host, method, path string, q url.Values, out any, sign bool) error {
	ep, ok := endpointFor(host, method, path)
	if !ok || (ep.Access == accessPublic) == sign {
		return errNotListed
	}
	c.mu.Lock()
	banned := c.now().Before(c.banUntil)
	c.mu.Unlock()
	if banned {
		return errBanned
	}
	if q == nil {
		q = url.Values{}
	}
	if sign {
		if c.signer == nil {
			return errNoKey
		}
		if err := c.syncTime(ctx); err != nil {
			return err
		}
		q.Set("timestamp", strconv.FormatInt(c.serverNow().UnixMilli(), 10))
		q.Set("recvWindow", "10000")
	}
	query := q.Encode()
	if sign {
		query += "&signature=" + url.QueryEscape(c.signer.sign(query))
	}
	u := host + path
	if c.hostURL != nil {
		u = c.hostURL(host) + path
	}
	if query != "" {
		u += "?" + query
	}
	req, err := http.NewRequestWithContext(ctx, method, u, nil)
	if err != nil {
		return err
	}
	if sign {
		req.Header.Set("X-MBX-APIKEY", c.signer.apiKey)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return networkError(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return networkError(err)
	}
	c.note(host, resp)
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Code int    `json:"code"`
			Msg  string `json:"msg"`
		}
		_ = json.Unmarshal(body, &e)
		ae := &apiError{Status: resp.StatusCode, Code: e.Code, Msg: clip(e.Msg, 200)}
		if ae.Msg == "" {
			ae.Msg = clip(strings.TrimSpace(string(body)), 200)
		}
		if e.Code == -1021 { // timestamp outside recvWindow: resync next time
			c.mu.Lock()
			c.offsetAt = time.Time{}
			c.mu.Unlock()
		}
		return ae
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("Binance answer cannot be read: %v", err)
	}
	return nil
}

// note keeps the used weight and backs off on 429 / 418 for Retry-After (at least a minute).
func (c *client) note(host string, resp *http.Response) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if w := resp.Header.Get("X-Mbx-Used-Weight-1m"); w != "" {
		c.weight[host] = w
	}
	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusTeapot {
		wait := time.Minute
		if s, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && time.Duration(s)*time.Second > wait {
			wait = time.Duration(s) * time.Second
		}
		c.banUntil = c.now().Add(wait)
	}
}

// syncTime reads the server's clock at most every 30 minutes.
func (c *client) syncTime(ctx context.Context) error {
	c.mu.Lock()
	fresh := !c.offsetAt.IsZero() && c.now().Sub(c.offsetAt) < 30*time.Minute
	c.mu.Unlock()
	if fresh {
		return nil
	}
	before := c.now()
	var t struct {
		ServerTime int64 `json:"serverTime"`
	}
	if err := c.public(ctx, hostSpot, "/api/v3/time", nil, &t); err != nil {
		return err
	}
	after := c.now()
	mid := before.Add(after.Sub(before) / 2)
	c.mu.Lock()
	c.offset = time.UnixMilli(t.ServerTime).Sub(mid)
	c.offsetAt = after
	c.mu.Unlock()
	return nil
}

func (c *client) serverNow() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now().Add(c.offset)
}

func (c *client) usedWeight() map[string]string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := map[string]string{}
	for k, v := range c.weight {
		out[k] = v
	}
	return out
}

// netError is a request that may not have reached Binance (or whose answer was lost).
type netError struct{ msg string }

func (e *netError) Error() string { return e.msg }

func networkError(err error) error {
	msg := err.Error()
	if errors.Is(err, context.DeadlineExceeded) || strings.Contains(msg, "Timeout") || strings.Contains(msg, "timeout") {
		return &netError{"cannot reach Binance (timeout)"}
	}
	var ue *url.Error
	if errors.As(err, &ue) {
		msg = ue.Err.Error()
	}
	return &netError{"cannot reach Binance: " + clip(msg, 160)}
}

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
