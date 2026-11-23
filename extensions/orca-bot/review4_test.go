package main

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// Tests for the fourth review of #958 (items A–E).

func (f *fakeOrca) set(fn func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn()
}

// A. An unsure hand-over Orca never took does not block the bot: once the hub gives it up, the Mac looks at the chat,
// finds it empty and lets it go; the next to-do gets the chat.
func TestAnUnsureClaimOrcaNeverTookIsLetGo(t *testing.T) {
	w := newWorld(t, workConfig())
	w.pick("bot-alpha")
	w.orca.set(func() { w.orca.sendUnknown = true }) // Orca answers every replay "operation unknown", takes nothing
	w.newTodo("todo_x", "prepare x", "", "work")
	w.created("todo_x")
	if r := w.record("todo_x"); r.State != "pending" || r.LastCode != "orca_unsure" || r.Attempts != 0 {
		t.Fatalf("todo_x: %+v", r)
	}
	if h := w.nodeHandoff("todo_x"); !h.Unsure {
		t.Fatalf("not unsure: %+v", h)
	}
	w.clock.add(maxAge + time.Minute) // the hub gives todo_x up and tells the Mac
	w.app.retryDue(w.ctx, false)
	if r := w.record("todo_x"); r.State != "failed" {
		t.Fatalf("todo_x not given up: %+v", r)
	}
	if w.nodeHandoff("todo_x").TodoID != "" {
		t.Fatal("the Mac kept the unsure hand-over after Orca showed an empty chat")
	}
	w.orca.set(func() { w.orca.sendUnknown = false })
	w.newTodo("todo_y", "prepare y", "", "work")
	w.created("todo_y")
	if r := w.record("todo_y"); r.State != "sent" {
		t.Fatalf("todo_y: %+v", r)
	}
}

func TestAnUnsureClaimIsSettledWhenItBlocks(t *testing.T) {
	w := newWorld(t, workConfig())
	w.orca.sendUnknown = true
	if _, err := w.node.rpc(w.ctx, "handoff", handoffWithNotes(t, "todo_x", "")); errCode(err) != "orca_unsure" {
		t.Fatalf("todo_x: %v", err)
	}
	w.orca.set(func() { w.orca.sendUnknown = false })
	if _, err := w.node.rpc(w.ctx, "handoff", handoffWithNotes(t, "todo_y", "")); errCode(err) != "orca_busy" {
		t.Fatalf("an unsure claim was let go at once: %v", err) // not before nodeStaleClaim
	}
	w.clock.add(nodeStaleClaim + time.Second)
	if _, err := w.node.rpc(w.ctx, "handoff", handoffWithNotes(t, "todo_y", "")); err != nil {
		t.Fatalf("todo_y blocked by a chat Orca shows empty: %v", err)
	}
	if h := w.nodeHandoff("todo_x"); h.SessionID != "" || h.Unsure {
		t.Fatalf("todo_x kept the chat: %+v", h)
	}
}

func TestAnUnsureClaimOrcaTookIsFollowed(t *testing.T) {
	w := newWorld(t, workConfig())
	w.orca.dropSend = true // taken, the answer lost: the chat has work in progress
	if _, err := w.node.rpc(w.ctx, "handoff", handoffWithNotes(t, "todo_x", "")); err == nil {
		t.Fatal("a send without an answer succeeded")
	}
	w.orca.set(func() { w.orca.dropSend = false })
	w.clock.add(nodeStaleClaim + time.Second)
	if _, err := w.node.rpc(w.ctx, "handoff", handoffWithNotes(t, "todo_y", "")); errCode(err) != "orca_busy" {
		t.Fatalf("todo_y: %v", err)
	}
	if h := w.nodeHandoff("todo_x"); !h.Sent || h.Unsure {
		t.Fatalf("todo_x not counted as sent: %+v", h)
	}
	if n := len(w.orca.sent()); n != 1 {
		t.Fatalf("%d sends", n)
	}
}

// B. A Mac that is down does not starve the others' abandon calls.
func TestAbandonCallsCountOnlyCallsMade(t *testing.T) {
	w := newWorld(t, workConfig())
	w.app.mu.Lock()
	w.app.st.Abandoned = map[string]abandonEntry{}
	for i := 0; i < abandonPerPass; i++ { // the oldest, for a Mac that does not answer
		w.app.st.Abandoned["todo_d"+string(rune('a'+i))] = abandonEntry{Machine: "mac-down", At: w.clock.now()}
	}
	for i := 0; i < 3; i++ {
		w.app.st.Abandoned["todo_u"+string(rune('a'+i))] = abandonEntry{Machine: testMac, At: w.clock.now().Add(time.Minute)}
	}
	w.app.mu.Unlock()
	w.app.tellAbandoned(w.ctx)
	w.app.mu.Lock()
	left := len(w.app.st.Abandoned)
	w.app.mu.Unlock()
	if left != abandonPerPass {
		t.Fatalf("%d left: the answering Mac was starved", left)
	}
}

// D. The expiry digest survives a failed notification, and names no bot.
func TestTheExpiryDigestSurvivesAFailedNotification(t *testing.T) {
	w := newWorld(t, workConfig())
	w.pick("bot-alpha")
	w.link.down.Store(true)
	w.newTodo("todo_a", "prepare a", "", "work")
	w.created("todo_a")
	w.newTodo("todo_b", "prepare b", "", "work")
	w.created("todo_b")
	w.hub.mu.Lock()
	w.hub.notifyErr = errors.New("http_503")
	w.hub.mu.Unlock()
	w.clock.add(maxAge + time.Minute)
	w.app.retryDue(w.ctx, false)
	w.hub.mu.Lock()
	w.hub.notifyErr = nil
	w.hub.mu.Unlock()
	w.clock.add(time.Minute)
	w.app.retryDue(w.ctx, false)
	w.hub.mu.Lock()
	defer w.hub.mu.Unlock()
	if len(w.hub.notices) != 1 || !strings.Contains(w.hub.notices[0].Body, "2 条") {
		t.Fatalf("notices: %+v", w.hub.notices)
	}
	if strings.Contains(w.hub.notices[0].Body, "Example Helper") {
		t.Fatalf("the digest names a bot: %s", w.hub.notices[0].Body)
	}
}

// E. An unrelated outcome in between: a later bot_not_found starts over instead of pausing at once.
func TestNotFoundAtStartsOverAfterAnotherOutcome(t *testing.T) {
	w := newWorld(t, workConfig())
	w.pick("bot-alpha")
	saved := w.orca.bots
	w.orca.set(func() { w.orca.bots = saved[1:] })
	w.newTodo("todo_1", "prepare one", "", "work")
	w.created("todo_1") // bot_unconfirmed
	w.link.down.Store(true)
	w.clock.add(waitRetry)
	w.app.retryDue(w.ctx, false) // offline: unrelated
	w.link.down.Store(false)
	w.clock.add(waitRetry)
	w.app.retryDue(w.ctx, false) // bot_not_found again, but the first one no longer counts
	if ok, _ := w.app.ready(); !ok {
		t.Fatal("paused on one bot_not_found after an unrelated outcome")
	}
	if r := w.record("todo_1"); r.LastCode != "bot_unconfirmed" {
		t.Fatalf("record: %+v", r)
	}
}
