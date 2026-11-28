package virtualizor

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"unicode"

	"github.com/nwverse/persona-extensions/extensions/cloud-hosts/internal/core"
	"github.com/nwverse/persona-extensions/extensions/cloud-hosts/internal/coretest"
	"github.com/nwverse/persona/sdk/go/wire"
)

func hasNonASCII(s string) bool {
	return strings.ContainsFunc(s, func(c rune) bool { return c > unicode.MaxASCII })
}

func asCoreError(t *testing.T, err error) *core.Error {
	t.Helper()
	var e *core.Error
	if !errors.As(err, &e) {
		t.Fatalf("%v (%T) is not a *core.Error", err, err)
	}
	return e
}

// An id with a slash, a percent sign and a space is never a vpsid: it is not_found without a request.
func TestOddIDNeverReachesThePanel(t *testing.T) {
	m, srv := newMock(t)
	a := newAdapter(t, srv, allCreds())
	ctx := context.Background()
	ref := wire.CloudInstanceRef{Account: "main", ID: "1/2%3 4"}
	if _, err := a.Instance(ctx, ref); coretest.Code(err) != wire.CloudErrorCodeNotFound {
		t.Errorf("instance: %v", err)
	}
	if _, err := a.Action(ctx, core.Action{Ref: ref, Kind: wire.CloudActionKindStart, RequestID: "odd"}); coretest.Code(err) != wire.CloudErrorCodeNotFound {
		t.Errorf("action: %v", err)
	}
	if n := m.count(""); n != 0 {
		t.Fatalf("an odd id reached the panel: %v", m.calls)
	}
}

func TestTransientOnlyForRateLimits(t *testing.T) {
	m, srv := newMock(t)
	a := newAdapter(t, srv, allCreds())
	ref := wire.CloudInstanceRef{Account: "main", ID: "101"}
	for _, tc := range []struct {
		status    int
		body      string
		transient bool
	}{
		{http.StatusTooManyRequests, `{"act": "start"}`, true},
		{http.StatusOK, `{"error": ["Too many requests. Please slow down"], "act": "start"}`, true},
		{http.StatusInternalServerError, `<html>Internal error</html>`, false},
		{http.StatusBadGateway, `<html>bad gateway</html>`, false},
		{http.StatusOK, `{"error": ["Power actions are disabled"], "act": "start"}`, false},
	} {
		m.mu.Lock()
		m.raw = func(w http.ResponseWriter, _ string) bool {
			w.WriteHeader(tc.status)
			fmt.Fprint(w, tc.body)
			return true
		}
		m.mu.Unlock()
		_, err := a.Action(context.Background(), core.Action{Ref: ref, Kind: wire.CloudActionKindStart, RequestID: "t"})
		if e := asCoreError(t, err); e.Transient != tc.transient {
			t.Errorf("%d %s: transient %v, want %v", tc.status, tc.body, e.Transient, tc.transient)
		}
	}

	// Through the Provider: a 500 keeps the request id's record, so a retry is answered without a second request.
	p := coretest.Provider(a, nil, secrets...)
	m.mu.Lock()
	m.raw = func(w http.ResponseWriter, _ string) bool {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprint(w, `<html>Internal error</html>`)
		return true
	}
	m.mu.Unlock()
	before := m.count("POST start")
	params := wire.CloudActionParams{Instance: ref, Action: wire.CloudActionKindStart, RequestID: "keep-500"}
	for i := 0; i < 2; i++ {
		if _, err := p.Action(context.Background(), params); coretest.Code(err) != wire.CloudErrorCodeFailed {
			t.Fatalf("try %d: %v", i, err)
		}
	}
	if n := m.count("POST start") - before; n != 1 {
		t.Fatalf("start sent %d times, want 1", n)
	}
}

func TestErrorsAreNotClipped(t *testing.T) {
	long := strings.Repeat("word ", 80) + "end"
	if e := vzError(long, true); !strings.HasSuffix(e.Message, "end") || strings.Contains(e.Message, "…") {
		t.Fatalf("clipped: %q", e.Message)
	}
}

func TestLocalized(t *testing.T) {
	_, srv := newMock(t)
	p := coretest.Provider(newAdapter(t, srv, allCreds()), nil, secrets...)
	ctx := context.Background()
	for _, loc := range []string{"en", "zh-CN"} {
		zh := loc == "zh-CN"
		in, err := p.Instance(ctx, wire.CloudInstanceParams{Instance: wire.CloudInstanceRef{Account: "main", ID: "103"}, Locale: loc})
		if err != nil {
			t.Fatal(err)
		}
		if in.StateDetail == "" || hasNonASCII(in.StateDetail) != zh {
			t.Errorf("%s: %q", loc, in.StateDetail)
		}
		_, err = p.Instance(ctx, wire.CloudInstanceParams{Instance: wire.CloudInstanceRef{Account: "dead", ID: "101"}, Locale: loc})
		if coretest.Code(err) != wire.CloudErrorCodeNotSignedIn || hasNonASCII(err.Error()) != zh {
			t.Errorf("signed out %s: %v", loc, err)
		}
		_, err = p.Snapshots(ctx, wire.CloudSnapshotsParams{Instance: wire.CloudInstanceRef{Account: "main", ID: "101"}, Locale: loc})
		if coretest.Code(err) != wire.CloudErrorCodeUnsupported || hasNonASCII(err.Error()) != zh {
			t.Errorf("snapshots %s: %v", loc, err)
		}
	}
}

func TestSecretValues(t *testing.T) {
	_, srv := newMock(t)
	a := newAdapter(t, srv, allCreds()).(core.SecretHolder)
	if got := strings.Join(a.SecretValues(), ","); got != strings.Join(secrets, ",") {
		t.Errorf("secrets %q", got)
	}
}
