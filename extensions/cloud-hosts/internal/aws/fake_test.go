package aws

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// Synthetic credentials: obviously not real.
const (
	keyDefault    = "AKIAEXAMPLEEXAMPLE00"
	secretDefault = "test-secret-key-0001/EXAMPLEKEYEXAMPLEKEY"
	keyOps        = "ASIAEXAMPLEEXAMPLE01"
	secretOps     = "test-secret-key-0002/EXAMPLEKEYEXAMPLEKEY"
	tokenOps      = "test-session-token-0001-EXAMPLE"
)

const testCredentials = `# a shared credentials file
[default]
aws_access_key_id = ` + keyDefault + `
aws_secret_access_key = ` + secretDefault + `

[profile ops]
aws_access_key_id = ` + keyOps + `
aws_secret_access_key = ` + secretOps + `
aws_session_token = ` + tokenOps + `

[incomplete]
aws_access_key_id = AKIAEXAMPLEEXAMPLE99
`

var allSecrets = []string{keyDefault, secretDefault, keyOps, secretOps, tokenOps}

var testNow = time.Date(2026, 11, 27, 9, 30, 0, 0, time.UTC)

// call is one request the fake AWS received, its signature already checked.
type call struct {
	Service, Region, Key string
	Op                   string         // EC2's Action or Lightsail's operation
	Form                 url.Values     // EC2
	JSON                 map[string]any // Lightsail
}

// fakeAWS serves EC2 and Lightsail for every region at one address, checking every request's SigV4 signature.
type fakeAWS struct {
	t   *testing.T
	srv *httptest.Server

	mu     sync.Mutex
	calls  []call
	handle func(c call) (status int, body string)
}

func newFake(t *testing.T, handle func(c call) (int, string)) *fakeAWS {
	t.Helper()
	f := &fakeAWS{t: t, handle: handle}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeAWS) config(regions, lsRegions []string) Config {
	return Config{Credentials: testCredentials, Regions: regions, LightsailRegions: lsRegions,
		HTTP: f.srv.Client(), Now: func() time.Time { return testNow },
		Endpoint: func(service, region string) string { return f.srv.URL }}
}

func (f *fakeAWS) count(op string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if c.Op == op {
			n++
		}
	}
	return n
}

func (f *fakeAWS) last(op string) call {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := len(f.calls) - 1; i >= 0; i-- {
		if f.calls[i].Op == op {
			return f.calls[i]
		}
	}
	f.t.Fatalf("no %s call", op)
	return call{}
}

func (f *fakeAWS) serve(w http.ResponseWriter, r *http.Request) {
	t := f.t
	body, _ := io.ReadAll(r.Body)
	if r.Method != http.MethodPost || r.URL.Path != "/" || r.URL.RawQuery != "" {
		t.Errorf("unexpected request %s %s", r.Method, r.URL)
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	// The signature: recomputed from what arrived, with the secret of the key it names.
	auth := r.Header.Get("Authorization")
	rest, ok := strings.CutPrefix(auth, "AWS4-HMAC-SHA256 Credential=")
	parts := strings.Split(rest, ", ")
	if !ok || len(parts) != 3 {
		t.Errorf("bad Authorization %q", auth)
		w.WriteHeader(http.StatusForbidden)
		return
	}
	scope := strings.Split(parts[0], "/")
	signed := strings.Split(strings.TrimPrefix(parts[1], "SignedHeaders="), ";")
	if len(scope) != 5 || scope[4] != "aws4_request" {
		t.Errorf("bad scope %q", parts[0])
		return
	}
	key, date, region, service := scope[0], scope[1], scope[2], scope[3]
	creds := map[string]credentials{
		keyDefault: {AccessKeyID: keyDefault, SecretAccessKey: secretDefault},
		keyOps:     {AccessKeyID: keyOps, SecretAccessKey: secretOps, SessionToken: tokenOps},
	}[key]
	if creds.AccessKeyID == "" {
		t.Errorf("unknown key in %q", auth)
		return
	}
	at, err := time.Parse(amzDateFormat, r.Header.Get("X-Amz-Date"))
	if err != nil || !at.Equal(testNow) || date != "20261127" {
		t.Errorf("bad X-Amz-Date %q / scope date %q", r.Header.Get("X-Amz-Date"), date)
	}
	if r.Header.Get("X-Amz-Security-Token") != creds.SessionToken {
		t.Errorf("X-Amz-Security-Token = %q, want %q", r.Header.Get("X-Amz-Security-Token"), creds.SessionToken)
	}
	if want := authorization(r.Method, r.Host, r.URL, r.Header, signed, hashHex(body), creds, region, service, at); want != auth {
		t.Errorf("signature mismatch\n got %s\nwant %s", auth, want)
		w.WriteHeader(http.StatusForbidden)
		return
	}
	for _, h := range []string{"host", "x-amz-date", "content-type"} {
		if !strings.Contains(parts[1], h) {
			t.Errorf("%s is not signed: %s", h, parts[1])
		}
	}
	c := call{Service: service, Region: region, Key: key}
	switch service {
	case "ec2":
		if ct := r.Header.Get("Content-Type"); ct != "application/x-www-form-urlencoded; charset=utf-8" {
			t.Errorf("EC2 Content-Type = %q", ct)
		}
		c.Form, _ = url.ParseQuery(string(body))
		c.Op = c.Form.Get("Action")
		if c.Form.Get("Version") != "2016-11-15" {
			t.Errorf("EC2 Version = %q", c.Form.Get("Version"))
		}
	case "lightsail":
		if ct := r.Header.Get("Content-Type"); ct != "application/x-amz-json-1.1" {
			t.Errorf("Lightsail Content-Type = %q", ct)
		}
		target := r.Header.Get("X-Amz-Target")
		if !strings.Contains(parts[1], "x-amz-target") {
			t.Errorf("X-Amz-Target is not signed")
		}
		op, ok := strings.CutPrefix(target, "Lightsail_20161128.")
		if !ok {
			t.Errorf("X-Amz-Target = %q", target)
		}
		c.Op = op
		if err := json.Unmarshal(body, &c.JSON); err != nil {
			t.Errorf("Lightsail body %q: %v", body, err)
		}
	default:
		t.Errorf("unexpected service %q", service)
		return
	}
	f.mu.Lock()
	f.calls = append(f.calls, c)
	f.mu.Unlock()
	status, out := f.handle(c)
	if service == "ec2" {
		w.Header().Set("Content-Type", "text/xml;charset=UTF-8")
	} else {
		w.Header().Set("Content-Type", "application/x-amz-json-1.1")
	}
	w.WriteHeader(status)
	_, _ = io.WriteString(w, out)
}
