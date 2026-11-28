package core

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/nwverse/persona/sdk/go/wire"
)

// MaxResponse is the most an adapter reads of one answer.
const MaxResponse = 8 << 20

// userAgent names the program to the vendors: nothing of the user or the hub.
const userAgent = "persona-cloud-hosts"

// HTTPOptions are NewHTTPClient's.
type HTTPOptions struct {
	// Timeout of one request (20 s when zero), under the hub's own bound for the call.
	Timeout time.Duration
	// PinSHA256, when set, is the SHA-256 fingerprint (hex, colons and spaces allowed) of the server's certificate:
	// the connection is accepted only for that certificate, whoever signed it. For panels with a self-signed one.
	PinSHA256 string
}

// NewHTTPClient is the client adapters use: the hub's egress proxy from the environment, no cookie jar, and no
// redirect followed (a redirect would carry the Authorization header to wherever the answer points).
func NewHTTPClient(o HTTPOptions) *http.Client {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.MaxIdleConnsPerHost = 4
	if pin := normalizePin(o.PinSHA256); pin != "" {
		t.TLSClientConfig = &tls.Config{
			MinVersion:         tls.VersionTLS12,
			InsecureSkipVerify: true, // the pin below is the verification
			VerifyPeerCertificate: func(raw [][]byte, _ [][]*x509.Certificate) error {
				if len(raw) == 0 {
					return errors.New("the server sent no certificate")
				}
				sum := sha256.Sum256(raw[0])
				if hex.EncodeToString(sum[:]) != pin {
					return errors.New("the server's certificate is not the pinned one")
				}
				return nil
			},
		}
	}
	timeout := o.Timeout
	if timeout <= 0 {
		timeout = 20 * time.Second
	}
	return &http.Client{Transport: t, Timeout: timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func normalizePin(s string) string {
	// As a browser, openssl ("SHA256 Fingerprint=AB:CD…") or pvenode shows it.
	s = strings.NewReplacer("sha256 fingerprint", "", "sha256", "", "sha-256", "", ":", "", " ", "", "=", "").Replace(strings.ToLower(strings.TrimSpace(s)))
	if len(s) != 64 {
		return ""
	}
	if _, err := hex.DecodeString(s); err != nil {
		return ""
	}
	return s
}

// Response is a vendor's answer, read whole.
type Response struct {
	Status int
	Reason string // the status line's words after the code: some panels put their error there
	Header http.Header
	Body   []byte
}

// OK reports a 2xx status.
func (r *Response) OK() bool { return r.Status >= 200 && r.Status < 300 }

// JSON decodes the body into v (an empty body leaves v as it is).
func (r *Response) JSON(v any) error {
	if len(bytes.TrimSpace(r.Body)) == 0 {
		return nil
	}
	if err := json.Unmarshal(r.Body, v); err != nil {
		return Failed("the vendor's answer could not be read")
	}
	return nil
}

// Do sends req and reads the answer whole. A transport failure is a failed error that names the host only, never
// the path or the query, where some panels carry their keys. It is transient only when the connection was never made
// (a dial error or the sandbox's refusal).
func Do(ctx context.Context, c *http.Client, req *http.Request) (*Response, error) {
	if c == nil {
		c = NewHTTPClient(HTTPOptions{})
	} else {
		// Whatever client was given, no redirect is followed: one would carry the request's body and its own
		// authorization headers to wherever the answer points.
		noRedirect := *c
		noRedirect.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		c = &noRedirect
	}
	if req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", userAgent)
	}
	resp, err := c.Do(req.WithContext(ctx))
	if err != nil {
		return nil, transportError(req.URL, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxResponse+1))
	if err != nil {
		return nil, transportError(req.URL, err)
	}
	if len(body) > MaxResponse {
		return nil, Failed("%s answered more than this extension reads", req.URL.Hostname())
	}
	reason := strings.TrimSpace(strings.TrimPrefix(resp.Status, strconv.Itoa(resp.StatusCode)))
	return &Response{Status: resp.StatusCode, Reason: reason, Header: resp.Header, Body: body}, nil
}

func transportError(u *url.URL, err error) *Error {
	what := "could not be reached"
	var ue *url.Error
	switch {
	case errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &ue) && ue.Timeout()):
		what = "did not answer in time"
	case strings.Contains(err.Error(), "persona sandbox egress"):
		what = "is not an address this extension may reach (check the extension's settings and its allowed hosts)"
	case strings.Contains(err.Error(), "certificate"):
		what = "presented a certificate that was not accepted"
	}
	// Only a connection that was never made is certain not to have reached the vendor.
	var op *net.OpError
	unsent := errors.As(err, &op) && op.Op == "dial" || strings.Contains(err.Error(), "persona sandbox egress")
	return &Error{Code: wire.CloudErrorCodeFailed, Message: u.Hostname() + " " + what, Transient: unsent}
}

// StatusError is the error for an HTTP status no vendor rule claimed first: 401 and 403 not_signed_in, 404
// not_found, 409 and 423 invalid_state, 429 rate_limited, anything else failed. detail is the vendor's own words
// (may be empty); vendor names it for people. It is the fallback: a vendor that uses a status for something else
// (a 403 for a quota, a 404 for a missing endpoint rather than a missing instance, a 500 for a refusal) must be
// classified by the adapter first. The message is not clipped here: the Provider clips after redacting.
func StatusError(vendor string, status int, detail string) *Error {
	detail = OneLine(detail, 0)
	msg := vendor + " answered " + http.StatusText(status)
	if http.StatusText(status) == "" {
		msg = vendor + " answered an error"
	}
	if detail != "" {
		msg = vendor + ": " + detail
	}
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return NotSignedIn("%s", msg)
	case status == http.StatusNotFound || status == http.StatusGone:
		return NotFound("%s", msg)
	case status == http.StatusConflict || status == http.StatusLocked:
		return InvalidState("%s", msg)
	case status == http.StatusTooManyRequests:
		return RateLimited("%s", msg)
	}
	return Failed("%s", msg)
}

// JoinURL is base with path appended (base's own path kept, one slash between) and query set. path is in its
// escaped form: an adapter escapes each id it puts in (url.PathEscape), so an id with a slash or a percent sign stays
// one segment and is not escaped twice.
func JoinURL(base, path string, query url.Values) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(base))
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return nil, Failed("the configured address is not a usable URL")
	}
	u.User = nil
	escaped := strings.TrimRight(u.EscapedPath(), "/") + "/" + strings.TrimLeft(path, "/")
	plain, err := url.PathUnescape(escaped)
	if err != nil {
		return nil, Failed("the request's address could not be built")
	}
	u.Path, u.RawPath = plain, escaped
	u.RawQuery = query.Encode()
	u.Fragment = ""
	return u, nil
}

// ConsoleBase is the https address of a panel for console links: base without credentials, query or fragment, and ""
// when base is not https (a plain http panel has no link the hub would pass on).
func ConsoleBase(base string) string {
	u, err := url.Parse(strings.TrimSpace(base))
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return ""
	}
	u.User, u.RawQuery, u.Fragment = nil, "", ""
	u.Path = strings.TrimRight(u.Path, "/")
	return u.String()
}
