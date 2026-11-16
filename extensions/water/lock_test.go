package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// parkedHub parks the low-distraction GET until unblock is called and counts notifications.
// Only POST /api/v1/notify counts: dev hosts run port probes that send GET / to new local listeners.
type parkedHub struct {
	srv         *httptest.Server
	lnStarted   chan struct{}
	release     chan struct{}
	lnOnce      sync.Once
	releaseOnce sync.Once
	posts       atomic.Int32
}

func newParkedHub(t *testing.T) *parkedHub {
	t.Helper()
	h := &parkedHub{lnStarted: make(chan struct{}), release: make(chan struct{})}
	h.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/push/ln-mode":
			h.lnOnce.Do(func() { close(h.lnStarted) })
			<-h.release
			io.WriteString(w, `{"enabled":false}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/notify":
			h.posts.Add(1)
			io.WriteString(w, `{"sent":true}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(func() {
		h.unblock()
		h.srv.Close()
	})
	return h
}

func (h *parkedHub) unblock() { h.releaseOnce.Do(func() { close(h.release) }) }

// within receives from ch, failing the test if nothing arrives within two seconds.
func within[T any](t *testing.T, ch chan T, failure string) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(2 * time.Second):
		t.Fatal(failure)
	}
	var zero T
	return zero
}

// A page action must not wait behind a render that is parked on the low-distraction read.
func TestDrinkCompletesWhileRenderParkedOnLowDistraction(t *testing.T) {
	hub := newParkedHub(t)
	now := instant("2026-11-16T10:00:00Z")
	a := testApp(t, defaults(), newHub(hub.srv.URL, "test-token", []string{"notify"}), now)
	rendered := make(chan error, 1)
	go func() {
		_, err := a.render(context.Background(), request("", "", nil))
		rendered <- err
	}()
	within(t, hub.lnStarted, "render never reached the low-distraction read")
	acted := make(chan error, 1)
	go func() {
		_, err := a.action(context.Background(), request("drink", "", nil))
		acted <- err
	}()
	if err := within(t, acted, "drink waited behind the parked render"); err != nil {
		t.Fatal(err)
	}
	hub.unblock()
	if err := within(t, rendered, "render did not finish after release"); err != nil {
		t.Fatal(err)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.s.Records) != 1 {
		t.Fatalf("drink not recorded: %d records", len(a.s.Records))
	}
}

// A parked low-distraction read must not hold the scheduler back, and no reminder may go out
// once the clock has moved past the end of the window.
func TestSchedulerNotHeldByParkedRenderAndStaysInWindow(t *testing.T) {
	hub := newParkedHub(t)
	start := instant("2026-11-16T20:59:59Z")
	a := testApp(t, defaults(), newHub(hub.srv.URL, "test-token", []string{"notify"}), start)
	var clock atomic.Int64
	clock.Store(start.UnixMilli())
	a.now = func() time.Time { return time.UnixMilli(clock.Load()) }
	rendered := make(chan error, 1)
	go func() {
		_, err := a.render(context.Background(), request("", "", nil))
		rendered <- err
	}()
	within(t, hub.lnStarted, "render never reached the low-distraction read")
	clock.Store(instant("2026-11-16T21:00:02Z").UnixMilli())
	passed := make(chan error, 1)
	go func() { passed <- a.pass(context.Background()) }()
	if err := within(t, passed, "scheduler waited behind the parked render"); err != nil {
		t.Fatal(err)
	}
	if n := hub.posts.Load(); n != 0 {
		t.Fatalf("%d reminder(s) sent after the window closed", n)
	}
	hub.unblock()
	if err := within(t, rendered, "render did not finish after release"); err != nil {
		t.Fatal(err)
	}
}

// The window can close after a reminder is decided and reserved. The check right before the
// request must stop it, and the reservation must not stay behind.
func TestReminderRechecksWindowAfterReserving(t *testing.T) {
	hub := newParkedHub(t)
	start := instant("2026-11-16T20:59:59Z")
	a := testApp(t, defaults(), newHub(hub.srv.URL, "test-token", []string{"notify"}), start)
	var reads atomic.Int32
	a.now = func() time.Time {
		if reads.Add(1) == 1 {
			return start
		}
		return instant("2026-11-16T21:00:02Z")
	}
	if err := a.pass(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := hub.posts.Load(); n != 0 {
		t.Fatalf("%d reminder(s) sent after the window closed", n)
	}
	if a.s.Pending != nil || !a.s.LastReminder.IsZero() {
		t.Fatalf("attempt left behind: pending %#v, last reminder %s", a.s.Pending, a.s.LastReminder)
	}
}

// Every clock read in a scheduler pass must happen while mu is held, so the decision uses the
// time as it is after any wait for the lock.
func TestSchedulerReadsClockOnlyWhileHoldingLock(t *testing.T) {
	hub := newParkedHub(t)
	now := instant("2026-11-16T10:00:00Z")
	a := testApp(t, defaults(), newHub(hub.srv.URL, "test-token", []string{"notify"}), now)
	a.now = func() time.Time {
		if a.mu.TryLock() {
			a.mu.Unlock()
			t.Error("scheduler read the clock without holding mu")
		}
		return now
	}
	if err := a.pass(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := hub.posts.Load(); n != 1 {
		t.Fatalf("the due reminder was not sent: %d posts", n)
	}
}
