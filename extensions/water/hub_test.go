package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"
	"time"
)

func testApp(t *testing.T, c config, h *hubClient, now time.Time) *app {
	t.Helper()
	a, err := openApp(filepath.Join(t.TempDir(), "water.json"), c, h, now)
	if err != nil {
		t.Fatal(err)
	}
	a.s.Zone = "UTC"
	a.now = func() time.Time { return now }
	return a
}

// tickAt runs one tick at the given time. The clock that the tick reads again before sending is set to
// the same time, so tests that step through several times see one consistent clock.
func tickAt(a *app, ctx context.Context, now time.Time) error {
	a.now = func() time.Time { return now }
	return a.tick(ctx, now)
}

func TestNotificationEntireJSON(t *testing.T) {
	now := instant("2026-11-16T10:00:00Z")
	for _, tc := range []struct {
		name, body string
		ml         int
	}{
		{"empty", "今天还没喝水，先来一杯吧", 0},
		{"progress", "今天已喝 750 / 2000 毫升，还差约 5 杯", 750},
		{"round cups up", "今天已喝 751 / 2000 毫升，还差约 5 杯", 751},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.Method != "POST" || r.URL.Path != "/api/v1/notify" || r.Header.Get("Authorization") != "Bearer test-token" || r.Header.Get("Content-Type") != "application/json" {
					t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
				}
				var got any
				if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
					t.Error(err)
				}
				want := map[string]any{"title": "该喝水了", "body": tc.body, "id": "water-reminder", "level": "active", "ui": "water", "state": "r=1794823200000", "actions": []any{
					map[string]any{"id": "drink", "title": "喝了一杯"}, map[string]any{"id": "snooze", "title": "等会儿"}, map[string]any{"id": "log", "title": "记别的量", "input": map[string]any{"placeholder": "毫升，比如 300"}}}}
				if !reflect.DeepEqual(got, want) {
					t.Errorf("JSON got %#v want %#v", got, want)
				}
				io.WriteString(w, `{"sent":true}`)
			}))
			defer srv.Close()
			s := state{Zone: "UTC"}
			if tc.ml > 0 {
				s.Records = []record{{ID: "d1", At: now, ML: tc.ml}}
			}
			d, err := newHub(srv.URL, "test-token", []string{"notify"}).notify(context.Background(), notification(defaults(), s, now))
			if err != nil || !d.Sent || calls.Load() != 1 {
				t.Fatalf("delivery %v %v", d, err)
			}
		})
	}
}

func TestR11DeliveryPolicy(t *testing.T) {
	for _, tc := range []struct {
		name, body        string
		status, wantCalls int
		wantReason        bool
	}{
		{"sent", `{"sent":true}`, 200, 1, false},
		{"no device", `{"sent":false,"reason":"no_device"}`, 200, 1, true},
		{"filtered", `{"sent":false,"reason":"filtered"}`, 200, 1, true},
		{"muted", `{"sent":false,"reason":"muted"}`, 200, 1, true},
		{"server error", "unavailable", 503, 3, true},
		{"client error", "denied", 403, 1, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/v1/notify" {
					t.Errorf("path %s", r.URL.Path)
				}
				calls.Add(1)
				w.WriteHeader(tc.status)
				io.WriteString(w, tc.body)
			}))
			defer srv.Close()
			now := instant("2026-11-16T10:00:00Z")
			a := testApp(t, defaults(), newHub(srv.URL, "test-token", []string{"notify"}), now)
			for _, sec := range []int{0, 1, 29, 30, 31, 59, 60, 61, 120, 180, 300} {
				if err := tickAt(a, context.Background(), now.Add(time.Duration(sec)*time.Second)); err != nil {
					t.Fatal(err)
				}
				// Restart after every tick: attempt counts and the completed occurrence survive.
				s, err := load(a.path, now.Add(time.Duration(sec)*time.Second))
				if err != nil {
					t.Fatal(err)
				}
				a.s = s
			}
			if int(calls.Load()) != tc.wantCalls {
				t.Fatalf("calls %d, want %d", calls.Load(), tc.wantCalls)
			}
			if a.s.Pending != nil || a.s.LastReminder.IsZero() || (a.s.Reason != "") != tc.wantReason {
				t.Fatalf("state %#v", a.s)
			}
			if tc.status == 200 && tc.wantReason && a.s.Reason != map[string]string{"no device": "no_device", "filtered": "filtered", "muted": "muted"}[tc.name] {
				t.Fatalf("reason %s", a.s.Reason)
			}
			// After abandonment the next ordinary interval is still eligible.
			if err := tickAt(a, context.Background(), a.s.LastReminder.Add(time.Hour)); err != nil {
				t.Fatal(err)
			}
			if int(calls.Load()) != tc.wantCalls+1 {
				t.Fatal("normal interval did not resume")
			}
		})
	}
}

func TestNoScopeNoNetwork(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
	defer srv.Close()
	now := instant("2026-11-16T10:00:00Z")
	h := newHub(srv.URL, "test-token", nil)
	a := testApp(t, defaults(), h, now)
	if err := a.tick(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	if _, err := h.notify(context.Background(), notification(defaults(), state{}, now)); err == nil {
		t.Fatal("missing scope allowed")
	}
	if h.lowDistraction(context.Background(), now) != nil || calls.Load() != 0 {
		t.Fatal("network called without scope")
	}
	res, err := a.action(context.Background(), request("drink", "", nil))
	if err != nil || len(a.s.Records) != 1 || res.Toast == "" {
		t.Fatalf("logging without scope: %v", err)
	}
}

func TestLowDistractionReadAndCache(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
		want       *bool
	}{
		{"on", `{"enabled":true}`, 200, boolPtr(true)},
		{"off", `{"enabled":false}`, 200, boolPtr(false)},
		{"failed", "error", 500, nil},
		{"malformed", "{", 200, nil},
		{"unknown", `{}`, 200, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.Method != "GET" || r.URL.Path != "/api/v1/push/ln-mode" || r.Header.Get("Authorization") != "Bearer test-token" {
					t.Errorf("bad ln request")
				}
				w.WriteHeader(tc.status)
				io.WriteString(w, tc.body)
			}))
			defer srv.Close()
			h := newHub(srv.URL, "test-token", []string{"notify"})
			now := instant("2026-11-16T10:00:00Z")
			for i := 0; i < 2; i++ {
				got := h.lowDistraction(context.Background(), now.Add(time.Duration(i)*time.Minute))
				if !reflect.DeepEqual(got, tc.want) {
					t.Fatalf("ln %v want %v", got, tc.want)
				}
			}
			if calls.Load() != 1 {
				t.Fatal("not cached")
			}
			h.lowDistraction(context.Background(), now.Add(3*time.Minute))
			if calls.Load() != 2 {
				t.Fatal("cache did not expire")
			}
		})
	}
}

func TestReservedLastAttemptIsNotRepeatedAfterCrash(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); io.WriteString(w, `{"sent":true}`) }))
	defer srv.Close()
	now := instant("2026-11-16T10:00:00Z")
	a := testApp(t, defaults(), newHub(srv.URL, "test-token", []string{"notify"}), now)
	a.s.Pending = &pending{At: now.Add(-time.Minute), Notice: notification(a.cfg, a.s, now.Add(-time.Minute)), Attempts: 3, RetryAt: now}
	if err := save(a.path, a.s); err != nil {
		t.Fatal(err)
	}
	a, err := openApp(a.path, a.cfg, a.hub, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := tickAt(a, context.Background(), now); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 0 || a.s.Pending != nil || a.s.LastReminder.IsZero() {
		t.Fatal("repeated last reserved attempt")
	}
}

func TestDisabledReminderAndIdlePruning(t *testing.T) {
	now := instant("2026-11-16T10:00:00Z")
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
	defer srv.Close()
	for _, scope := range []bool{true, false} {
		c := defaults()
		c.Remind = false
		scopes := []string{}
		if scope {
			scopes = []string{"notify"}
		}
		a := testApp(t, c, newHub(srv.URL, "test-token", scopes), now)
		a.s.Records = []record{{ID: "old", At: now.AddDate(0, 0, -400), ML: 250}}
		if err := a.tick(context.Background(), now); err != nil {
			t.Fatal(err)
		}
		if len(a.s.Records) != 0 || calls.Load() != 0 {
			t.Fatal("disabled tick failed")
		}
	}
}

func TestLoggingCancelsRetryAndRestartsInterval(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/push/ln-mode" {
			io.WriteString(w, `{"enabled":false}`)
			return
		}
		calls.Add(1)
		w.WriteHeader(503)
	}))
	defer srv.Close()
	now := instant("2026-11-16T10:00:00Z")
	a := testApp(t, defaults(), newHub(srv.URL, "test-token", []string{"notify"}), now)
	if err := tickAt(a, context.Background(), now); err != nil {
		t.Fatal(err)
	}
	if a.s.Pending == nil {
		t.Fatal("no pending retry")
	}
	mustAction(t, a, "drink", "", nil)
	if err := tickAt(a, context.Background(), now.Add(30*time.Second)); err != nil {
		t.Fatal(err)
	}
	if a.s.Pending != nil || calls.Load() != 1 {
		t.Fatal("retry after drink")
	}
}
func boolPtr(v bool) *bool { return &v }

func TestR9CatchupAndR11TransportBound(t *testing.T) {
	now := instant("2026-11-16T10:00:00Z")
	// A closed fake listener gives a transport failure without contacting an external service.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	srv.Close()
	h := newHub(srv.URL, "test-token", []string{"notify"})
	var calls atomic.Int32
	h.client.Transport = countTransport{base: http.DefaultTransport, calls: &calls}
	a := testApp(t, defaults(), h, now)
	for _, sec := range []int{0, 1, 30, 31, 60, 61, 300} {
		if err := tickAt(a, context.Background(), now.Add(time.Duration(sec)*time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	if calls.Load() != 3 || a.s.Pending != nil {
		t.Fatalf("transport calls %d", calls.Load())
	}
}

type countTransport struct {
	base  http.RoundTripper
	calls *atomic.Int32
}

func (t countTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	t.calls.Add(1)
	return t.base.RoundTrip(r)
}

func TestRetryCannotEscapeWindow(t *testing.T) {
	var calls atomic.Int32
	var tokens []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var n notice
		json.NewDecoder(r.Body).Decode(&n)
		tokens = append(tokens, n.State)
		w.WriteHeader(503)
	}))
	defer srv.Close()
	now := instant("2026-11-16T21:00:00Z")
	a := testApp(t, defaults(), newHub(srv.URL, "test-token", []string{"notify"}), now)
	for _, s := range []string{"2026-11-16T21:00:00Z", "2026-11-16T21:00:30Z", "2026-11-17T08:59:00Z", "2026-11-17T09:00:00Z"} {
		if err := tickAt(a, context.Background(), instant(s)); err != nil {
			t.Fatal(err)
		}
	}
	if calls.Load() != 2 || tokens[0] == tokens[1] || a.s.Pending.Attempts != 1 {
		t.Fatalf("stale retry sent: %v", tokens)
	}
}
