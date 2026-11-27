package main

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/sdk/go/ext"
)

func startWidgetSender(t *testing.T, a *app) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); a.sendWidgets(ctx) }()
	t.Cleanup(func() { cancel(); within(t, done, "widget sender did not stop") })
}

// Await an observed condition rather than assuming a goroutine runs within a sleep interval.
func awaitWidgetCondition(t *testing.T, description string, ready func() bool) {
	t.Helper()
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		if ready() {
			return
		}
		select {
		case <-timer.C:
			t.Fatal(description)
		case <-ticker.C:
		}
	}
}

func awaitWidgetClean(t *testing.T, a *app) {
	t.Helper()
	awaitWidgetCondition(t, "latest widget key was not acknowledged", func() bool {
		a.mu.Lock()
		defer a.mu.Unlock()
		return a.widgetKey == a.widgetPending
	})
}

func TestWidgetInvalidateBackpressure(t *testing.T) {
	now := instant("2026-11-16T10:00:00Z")
	a := testApp(t, defaults(), newHub("", "", nil), now)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	events := make(chan string, 4)
	a.invalidate = func(id string) error {
		close(entered)
		<-release
		events <- id
		return nil
	}
	startWidgetSender(t, a)
	shortResult := func(result chan error, description string) {
		t.Helper()
		select {
		case err := <-result:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(300 * time.Millisecond):
			t.Fatal(description)
		}
	}
	acted := make(chan error, 1)
	go func() { _, err := a.action(context.Background(), request("drink", "", nil)); acted <- err }()
	within(t, entered, "invalidate did not reach the write gate")
	shortResult(acted, "page action blocked by invalidate output while holding app.mu")
	another := make(chan error, 1)
	go func() { _, err := a.action(context.Background(), request("snooze", "", nil)); another <- err }()
	shortResult(another, "later page action blocked by invalidate output")
	rendered := make(chan error, 1)
	go func() {
		_, err := a.renderWidget(context.Background(), &ext.UIRequest{UIParams: widgetRequest("small")})
		rendered <- err
	}()
	shortResult(rendered, "widget render blocked by invalidate output")
	ticked := make(chan error, 1)
	go func() { ticked <- a.pass(context.Background()) }()
	shortResult(ticked, "scheduler tick blocked by invalidate output")
	unblock()
	awaitWidgetClean(t, a)
	if len(events) != 1 {
		t.Fatalf("events=%d, want exactly one after release", len(events))
	}
	if id := <-events; id != widgetID {
		t.Fatal(id)
	}
}

func TestWidgetSenderKeepsLatestChange(t *testing.T) {
	now := instant("2026-11-16T10:00:00Z")
	a := testApp(t, defaults(), newHub("", "", nil), now)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	keys := make(chan string, 4)
	a.invalidate = func(id string) error {
		if id != widgetID {
			return fmt.Errorf("unexpected contribution %q", id)
		}
		a.mu.Lock()
		key := a.widgetPending
		a.mu.Unlock()
		if len(keys) == 0 {
			close(entered)
			<-release
		}
		keys <- key
		return nil
	}
	startWidgetSender(t, a)
	mustAction(t, a, "drink", "", nil)
	within(t, entered, "first invalidate did not reach gate")
	// Changes during the first send must not be erased by its older acknowledgement.
	for i := 0; i < 8; i++ {
		mustAction(t, a, "drink", "", nil)
	}
	a.mu.Lock()
	latest := a.widgetPending
	wakes := len(a.widgetWake)
	a.mu.Unlock()
	if wakes != 1 {
		t.Fatal("changes were not coalesced into one wake")
	}
	unblock()
	awaitWidgetClean(t, a)
	if len(keys) != 2 {
		t.Fatalf("writes=%d, want two coalesced writes", len(keys))
	}
	first, last := <-keys, <-keys
	if first == latest || last != latest {
		t.Fatalf("first=%s last=%s latest=%s", first, last, latest)
	}
}

func TestWidgetSenderRetriesOnTick(t *testing.T) {
	now := instant("2026-11-16T10:00:00Z")
	a := testApp(t, defaults(), newHub("", "", nil), now)
	results := make(chan string, 4)
	first := true
	a.invalidate = func(id string) error {
		results <- id
		if first {
			first = false
			return fmt.Errorf("synthetic output failure")
		}
		return nil
	}
	startWidgetSender(t, a)
	mustAction(t, a, "drink", "", nil)
	if id := within(t, results, "missing failed attempt"); id != widgetID {
		t.Fatal(id)
	}
	a.mu.Lock()
	clean := a.widgetKey == a.widgetPending
	err := a.tick(context.Background(), now)
	a.mu.Unlock()
	if clean {
		t.Fatal("failed send acknowledged")
	}
	if err != nil {
		t.Fatal(err)
	}
	if id := within(t, results, "next tick did not retry"); id != widgetID {
		t.Fatal(id)
	}
	awaitWidgetClean(t, a)
	a.mu.Lock()
	err = a.tick(context.Background(), now)
	a.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 0 {
		t.Fatal("unchanged tick sent another event")
	}
}
