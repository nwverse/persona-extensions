package solusvm

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

// odd is an id with a slash, a percent sign and a space: never a SolusVM id.
const odd = "1/2%3 4"

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

func TestV2OddIDNeverReachesThePanel(t *testing.T) {
	m, srv := newV2Mock(t)
	a := newV2(t, srv, v2All())
	ctx := context.Background()
	ref := wire.CloudInstanceRef{Account: "main", ID: odd}
	before := m.count("")
	if _, err := a.Instance(ctx, ref); coretest.Code(err) != wire.CloudErrorCodeNotFound {
		t.Errorf("instance: %v", err)
	}
	if _, err := a.Snapshots(ctx, ref); coretest.Code(err) != wire.CloudErrorCodeNotFound {
		t.Errorf("snapshots: %v", err)
	}
	if _, err := a.Action(ctx, core.Action{Ref: ref, Kind: wire.CloudActionKindStart, RequestID: "odd"}); coretest.Code(err) != wire.CloudErrorCodeNotFound {
		t.Errorf("action: %v", err)
	}
	if n := m.count(""); n != before {
		t.Fatalf("an odd id reached the panel: %v", m.calls)
	}
}

func TestV1OddIDNeverReachesThePanel(t *testing.T) {
	m, srv := newV1Mock(t)
	a := newV1(t, srv, v1Creds("web="+v1KeyWeb+":"+v1HashWeb))
	ref := wire.CloudInstanceRef{Account: V1Account, ID: odd}
	if _, err := a.Instance(context.Background(), ref); coretest.Code(err) != wire.CloudErrorCodeNotFound {
		t.Errorf("instance: %v", err)
	}
	if len(m.calls) != 0 {
		t.Fatalf("an odd id reached the panel: %v", m.calls)
	}
}

func TestV2TransientOnlyForRateLimits(t *testing.T) {
	m, srv := newV2Mock(t)
	a := newV2(t, srv, v2All())
	ref := wire.CloudInstanceRef{Account: "main", ID: "101"}
	alpha := m.servers[v2TokenMain][0]
	for _, tc := range []struct {
		status    int
		body      string
		transient bool
	}{
		{429, `{"message": "Too Many Attempts."}`, true},
		{500, `{"message": "Server Error"}`, false},
		{502, `<html>bad gateway</html>`, false},
		{503, `{"message": "The compute resource was not reached."}`, false},
		{422, `{"message": "Server is already started."}`, false},
	} {
		m.mu.Lock()
		alpha.failure = &v2Failure{tc.status, tc.body}
		m.mu.Unlock()
		_, err := a.Action(context.Background(), core.Action{Ref: ref, Kind: wire.CloudActionKindStop, RequestID: "t"})
		if e := asCoreError(t, err); e.Transient != tc.transient {
			t.Errorf("%d %s: transient %v, want %v", tc.status, tc.body, e.Transient, tc.transient)
		}
	}

	// Through the Provider: a 500 keeps the request id's record, so a retry is answered without a second request.
	p := coretest.Provider(a, nil, v2Secrets...)
	m.mu.Lock()
	alpha.failure = &v2Failure{500, `{"message": "Server Error"}`}
	m.mu.Unlock()
	params := wire.CloudActionParams{Instance: ref, Action: wire.CloudActionKindStop, RequestID: "keep-500"}
	before := m.count("POST /api/v1/servers/101/stop")
	for i := 0; i < 2; i++ {
		if _, err := p.Action(context.Background(), params); coretest.Code(err) != wire.CloudErrorCodeFailed {
			t.Fatalf("try %d: %v", i, err)
		}
	}
	if n := m.count("POST /api/v1/servers/101/stop") - before; n != 1 {
		t.Fatalf("stop sent %d times, want 1", n)
	}
}

func TestV2ErrorsAreNotClipped(t *testing.T) {
	m, srv := newV2Mock(t)
	a := newV2(t, srv, v2All())
	long := strings.Repeat("word ", 80) + "end"
	m.mu.Lock()
	m.servers[v2TokenMain][0].failure = &v2Failure{422, fmt.Sprintf(`{"message": %q}`, long)}
	m.mu.Unlock()
	_, err := a.Action(context.Background(), core.Action{Ref: wire.CloudInstanceRef{Account: "main", ID: "101"},
		Kind: wire.CloudActionKindStop, RequestID: "long"})
	if e := asCoreError(t, err); !strings.HasSuffix(e.Message, "end") || strings.Contains(e.Message, "…") {
		t.Fatalf("clipped: %q", e.Message)
	}
}

func TestV2ReasonPhrase(t *testing.T) {
	if e := v2Error("", &core.Response{Status: http.StatusBadGateway, Reason: "Backend Down"}); !strings.Contains(e.Message, "Backend Down") ||
		e.Code != wire.CloudErrorCodeFailed || e.Transient {
		t.Fatalf("%+v", e)
	}
}

func TestV1TransientOnlyForRateLimits(t *testing.T) {
	m, srv := newV1Mock(t)
	a := newV1(t, srv, v1Creds("web="+v1KeyWeb+":"+v1HashWeb))
	ref := wire.CloudInstanceRef{Account: V1Account, ID: "web"}
	for _, tc := range []struct {
		status    int
		body      string
		transient bool
	}{
		{http.StatusTooManyRequests, "", true},
		{http.StatusOK, "<status>error</status><statusmsg>Too many requests, slow down</statusmsg>", true},
		{http.StatusInternalServerError, "", false},
		{http.StatusBadGateway, "<html>gateway</html>", false},
		{http.StatusOK, "<status>error</status><statusmsg>Reboot is not allowed</statusmsg>", false},
	} {
		m.mu.Lock()
		m.raw = func(w http.ResponseWriter, _ string) bool {
			w.WriteHeader(tc.status)
			fmt.Fprint(w, tc.body)
			return true
		}
		m.mu.Unlock()
		_, err := a.Action(context.Background(), core.Action{Ref: ref, Kind: wire.CloudActionKindReboot, RequestID: "t"})
		if e := asCoreError(t, err); e.Transient != tc.transient {
			t.Errorf("%d %s: transient %v, want %v", tc.status, tc.body, e.Transient, tc.transient)
		}
	}
	long := strings.Repeat("word ", 80) + "end"
	if e := v1Error("", long, true); !strings.HasSuffix(e.Message, "end") {
		t.Fatalf("clipped: %q", e.Message)
	}
}

func TestLocalizedStateDetails(t *testing.T) {
	ctx := context.Background()
	_, srv := newV2Mock(t)
	p := coretest.Provider(newV2(t, srv, "main="+v2TokenMain), nil, v2Secrets...)
	for _, loc := range []string{"en", "zh-CN"} {
		in, err := p.Instance(ctx, wire.CloudInstanceParams{Instance: wire.CloudInstanceRef{Account: "main", ID: "103"}, Locale: loc})
		if err != nil {
			t.Fatal(err)
		}
		if in.StateDetail == "" || hasNonASCII(in.StateDetail) != (loc == "zh-CN") {
			t.Errorf("v2 %s: %q", loc, in.StateDetail)
		}
		_, err = p.Instance(ctx, wire.CloudInstanceParams{Instance: wire.CloudInstanceRef{Account: "main", ID: "999"}, Locale: loc})
		if coretest.Code(err) != wire.CloudErrorCodeNotFound {
			t.Fatalf("v2 %s: %v", loc, err)
		}
		if _, err := p.Instance(ctx, wire.CloudInstanceParams{Instance: wire.CloudInstanceRef{Account: "main", ID: odd}, Locale: loc}); coretest.Code(err) != wire.CloudErrorCodeNotFound ||
			hasNonASCII(err.Error()) != (loc == "zh-CN") {
			t.Errorf("v2 odd id %s: %v", loc, err)
		}
	}

	m1, srv1 := newV1Mock(t)
	m1.vps[v1KeyWeb].vmstat = "suspended"
	p1 := coretest.Provider(newV1(t, srv1, v1Creds("web="+v1KeyWeb+":"+v1HashWeb)), nil, v1Secrets...)
	for _, loc := range []string{"en", "zh-CN"} {
		in, err := p1.Instance(ctx, wire.CloudInstanceParams{Instance: wire.CloudInstanceRef{Account: V1Account, ID: "web"}, Locale: loc})
		if err != nil {
			t.Fatal(err)
		}
		if in.StateDetail == "" || hasNonASCII(in.StateDetail) != (loc == "zh-CN") {
			t.Errorf("v1 %s: %q", loc, in.StateDetail)
		}
		_, err = p1.Snapshots(ctx, wire.CloudSnapshotsParams{Instance: wire.CloudInstanceRef{Account: V1Account, ID: "web"}, Locale: loc})
		if coretest.Code(err) != wire.CloudErrorCodeUnsupported || hasNonASCII(err.Error()) != (loc == "zh-CN") {
			t.Errorf("v1 snapshots %s: %v", loc, err)
		}
	}
}

func TestSecretValues(t *testing.T) {
	_, srv := newV1Mock(t)
	a1 := newV1(t, srv, v1Creds("web="+v1KeyWeb+":"+v1HashWeb, "nat="+v1KeyNat+":"+v1HashNat)).(core.SecretHolder)
	if got := strings.Join(a1.SecretValues(), ","); got != strings.Join([]string{v1KeyWeb, v1HashWeb, v1KeyNat, v1HashNat}, ",") {
		t.Errorf("v1 secrets %q", got)
	}
	a2 := newV2(t, srv, "main="+v2TokenMain+"\nalt="+v2TokenAlt).(core.SecretHolder)
	if got := strings.Join(a2.SecretValues(), ","); got != v2TokenMain+","+v2TokenAlt {
		t.Errorf("v2 secrets %q", got)
	}
}
