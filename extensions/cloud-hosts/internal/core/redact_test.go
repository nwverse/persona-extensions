package core

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/nwverse/persona/sdk/go/wire"
)

func TestRedactorWholeAndShort(t *testing.T) {
	r := &Redactor{}
	r.Add("test-token-0001", "short", "", "   ", "  test-token-0002  ")
	got := r.Redact("a test-token-0001 b test-token-0001 c short test-token-0002")
	if got != "a [redacted] b [redacted] c short [redacted]" {
		t.Fatalf("%q", got)
	}
	if r.Redact("") != "" {
		t.Fatal("empty")
	}
	var nilR *Redactor
	if nilR.Redact("test-token-0001") != "test-token-0001" {
		t.Fatal("a nil Redactor changes nothing")
	}
	// Adding again keeps one copy.
	r.Add("test-token-0001")
	r.mu.RLock()
	n := len(r.secrets)
	r.mu.RUnlock()
	if n != 2 {
		t.Fatalf("%d secrets", n)
	}
	// Exactly minSecret characters counts.
	r.Add("abcdef")
	if r.Redact("x abcdef y") != "x [redacted] y" {
		t.Fatal("a 6 character secret was not redacted")
	}
	r.Add("abcde")
	if r.Redact("abcde") != "abcde" {
		t.Fatal("a 5 character value was redacted")
	}
}

func TestRedactorLongestFirst(t *testing.T) {
	r := &Redactor{}
	r.Add("key-0001")           // a prefix of the next one, added first
	r.Add("key-0001-secret-99") // the whole one
	if got := r.Redact("x key-0001-secret-99 y key-0001 z"); got != "x [redacted] y [redacted] z" {
		t.Fatalf("%q", got)
	}
}

func TestRedactorPEMLines(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatal(err)
	}
	pemText := string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
	crlf := strings.ReplaceAll(pemText, "\n", "\r\n")
	r := &Redactor{}
	r.Add(crlf)
	lines := strings.Split(strings.TrimSpace(pemText), "\n")
	body := lines[1 : len(lines)-1]
	for _, l := range body {
		if len(l) < minSecret {
			continue // a short last line is below what is treated as a credential
		}
		if got := r.Redact("vendor said: " + l + " end"); strings.Contains(got, l) {
			t.Fatalf("a PEM line leaked: %q", got)
		}
	}
	if got := r.Redact(crlf); strings.Contains(got, body[0]) {
		t.Fatalf("the whole key leaked: %q", got)
	}
	// The armour lines are not secrets.
	if got := r.Redact(lines[0]); got != lines[0] {
		t.Fatalf("the header was redacted: %q", got)
	}
}

func TestRedactorConcurrent(t *testing.T) {
	r := &Redactor{}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(2)
		go func(i int) {
			defer wg.Done()
			r.Add(fmt.Sprintf("test-token-%04d", i))
		}(i)
		go func() {
			defer wg.Done()
			_ = r.Redact("test-token-0003 test-token-0004")
		}()
	}
	wg.Wait()
	if got := r.Redact("test-token-0007"); got != "[redacted]" {
		t.Fatalf("%q", got)
	}
}

func TestOneLine(t *testing.T) {
	for _, c := range []struct {
		in   string
		max  int
		want string
	}{
		{"  a\nb\r\nc\td  ", 0, "a b c d"},
		{"a\x00b\x7fc\u0085d", 0, "a b c d"},
		{"a    b", 0, "a b"},
		{"abcdef", 4, "abc…"},
		{"abcd", 4, "abcd"},
		{"ééééé", 3, "éé…"},
		{"", 5, ""},
		{"\n\n", 5, ""},
		{"a b", 0, "a b"},
	} {
		if got := OneLine(c.in, c.max); got != c.want {
			t.Errorf("OneLine(%q, %d) = %q, want %q", c.in, c.max, got, c.want)
		}
	}
}

func TestAsError(t *testing.T) {
	if AsError(nil) != nil {
		t.Fatal("nil")
	}
	e := Capacity("full")
	if AsError(e) != e {
		t.Fatal("an *Error is itself")
	}
	if got := AsError(fmt.Errorf("ctx: %w", e)); got != e {
		t.Fatal("a wrapped *Error is found")
	}
	for _, err := range []error{context.DeadlineExceeded, context.Canceled, fmt.Errorf("x: %w", context.DeadlineExceeded)} {
		got := AsError(err)
		if got.Code != wire.CloudErrorCodeFailed || got.Transient || got.Message != "the vendor did not answer in time" {
			t.Fatalf("%v -> %+v", err, got)
		}
	}
	got := AsError(errors.New("boom"))
	if got.Code != wire.CloudErrorCodeFailed || got.Transient || got.Message != "boom" {
		t.Fatalf("%+v", got)
	}
}

func TestConstructors(t *testing.T) {
	for want, e := range map[wire.CloudErrorCode]*Error{
		wire.CloudErrorCodeNotFound: NotFound("x %d", 1), wire.CloudErrorCodeUnsupported: Unsupported("x %d", 1),
		wire.CloudErrorCodeInvalidState: InvalidState("x %d", 1), wire.CloudErrorCodeNotSignedIn: NotSignedIn("x %d", 1),
		wire.CloudErrorCodeRateLimited: RateLimited("x %d", 1), wire.CloudErrorCodeCapacity: Capacity("x %d", 1),
		wire.CloudErrorCodeRefused: Refused("x %d", 1), wire.CloudErrorCodeFailed: Failed("x %d", 1),
	} {
		if e.Code != want || e.Error() != "x 1" || e.Transient != (want == wire.CloudErrorCodeRateLimited) {
			t.Errorf("%s: %+v", want, e)
		}
	}
}

func TestT(t *testing.T) {
	for loc, want := range map[string]string{"zh": "zh", "zh-CN": "zh", "zh_TW": "zh", "ZH-Hans": "zh", "en": "en", "": "en",
		"zhx": "en", "de-DE": "en"} {
		if got := T(loc, "en", "zh"); got != want {
			t.Errorf("T(%q) = %q", loc, got)
		}
	}
	if T("zh", "en", "") != "en" {
		t.Error("no zh falls back to en")
	}
}

func TestOffers(t *testing.T) {
	a := []wire.CloudActionKind{wire.CloudActionKindStart, wire.CloudActionKindSyncIp}
	if !Offers(a, wire.CloudActionKindSyncIp) || Offers(a, wire.CloudActionKindStop) || Offers(nil, wire.CloudActionKindStart) {
		t.Fatal("Offers")
	}
}

func TestBugTypedNilErrorAnswersEmptySuccess(t *testing.T) {
	f := &fake{instance: func(wire.CloudInstanceRef) (wire.CloudInstance, error) {
		var e *Error
		return wire.CloudInstance{}, e
	}}
	p := newProvider(f, nil, customs)
	in, err := p.Instance(context.Background(), wire.CloudInstanceParams{Instance: refA})
	if err == nil {
		t.Fatalf("an empty instance was answered as a success: %+v", in)
	}
}
