package main

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/sdk/go/ext"
)

// Tests for the third review of #958 (items 1–8).

// 1. A send Orca took without answering (it timed out, the connection dropped) keeps its chat: no age frees it, nor
// abandon; the next hand-over gets orca_busy, and the retry replays into the same chat, never a new one.
func TestAnUnsureSendKeepsItsChat(t *testing.T) {
	w := newWorld(t, workConfig())
	w.orca.dropSend = true
	if _, err := w.node.rpc(w.ctx, "handoff", handoffWithNotes(t, "todo_x", "")); err == nil {
		t.Fatal("a send without an answer succeeded")
	}
	if h := w.nodeHandoff("todo_x"); !h.Unsure || h.Sent {
		t.Fatalf("todo_x: %+v", h)
	}
	w.orca.mu.Lock()
	w.orca.dropSend = false
	w.orca.mu.Unlock()
	w.clock.add(nodeStaleClaim + time.Minute)
	raw := []byte(`{"todo_id":"todo_x"}`)
	if res, err := w.node.rpc(w.ctx, "abandon", raw); err != nil || res.(map[string]any)["released"] != false {
		t.Fatalf("abandon of an unsure hand-over: %v %v", res, err)
	}
	if _, err := w.node.rpc(w.ctx, "handoff", handoffWithNotes(t, "todo_y", "")); errCode(err) != "orca_busy" {
		t.Fatalf("todo_y took the unsure chat: %v", err)
	}
	res, err := w.node.rpc(w.ctx, "handoff", handoffWithNotes(t, "todo_x", ""))
	if err != nil {
		t.Fatal(err)
	}
	sent := w.orca.sent()
	if len(sent) != 1 || len(w.orca.sessions) != 1 || res.(*handoffResult).SessionID != sent[0].Session {
		t.Fatalf("todo_x sent again: %d sends, %d chats", len(sent), len(w.orca.sessions))
	}
}

// 1. A claim ages from the end of its attempt, not its start.
func TestAClaimAgesFromTheEndOfItsAttempt(t *testing.T) {
	w := newWorld(t, workConfig())
	w.orca.onCall["agentSession.history"] = func() { w.clock.add(nodeStaleClaim + time.Minute) } // a slow attempt
	w.orca.refuse["agentSession.send"] = "invalid_argument"                                      // then a clear refusal
	if _, err := w.node.rpc(w.ctx, "handoff", handoffWithNotes(t, "todo_x", "")); err == nil {
		t.Fatal("a refused send succeeded")
	}
	w.orca.mu.Lock()
	delete(w.orca.onCall, "agentSession.history")
	delete(w.orca.refuse, "agentSession.send")
	w.orca.mu.Unlock()
	if _, err := w.node.rpc(w.ctx, "handoff", handoffWithNotes(t, "todo_y", "")); errCode(err) != "orca_busy" {
		t.Fatalf("a claim whose attempt just ended was taken over: %v", err)
	}
}

// 2. Expiries over hours are one notification a window, with the count, under the window's own id.
func TestExpiriesAreDigestedAcrossPasses(t *testing.T) {
	w := newWorld(t, workConfig())
	w.pick("bot-alpha")
	w.link.down.Store(true)
	for i := 0; i < 3; i++ {
		id := "todo_" + string(rune('a'+i))
		w.newTodo(id, "prepare "+id, "", "work")
		w.created(id)
		w.clock.add(time.Hour)
	}
	w.clock.add(maxAge - 3*time.Hour + time.Minute) // todo_a is past 48h
	w.app.retryDue(w.ctx, false)
	w.clock.add(time.Hour) // todo_b
	w.app.retryDue(w.ctx, false)
	w.clock.add(time.Hour) // todo_c
	w.app.retryDue(w.ctx, false)
	if got := w.hub.noticeTitles(); len(got) != 1 {
		t.Fatalf("%d notifications within one window", len(got))
	}
	w.clock.add(expiryWindow)
	w.app.retryDue(w.ctx, false)
	w.hub.mu.Lock()
	defer w.hub.mu.Unlock()
	if len(w.hub.notices) != 2 || !strings.Contains(w.hub.notices[1].Body, "2 条") || w.hub.notices[0].ID == w.hub.notices[1].ID {
		t.Fatalf("notices: %+v", w.hub.notices)
	}
}

// 3. Telling the Mac of given-up hand-overs is bounded, and stops at a Mac that does not answer.
func TestTellingAbandonedIsBounded(t *testing.T) {
	w := newWorld(t, workConfig())
	w.app.mu.Lock()
	w.app.st.Abandoned = map[string]abandonEntry{}
	for i := 0; i < 25; i++ {
		w.app.st.Abandoned["todo_"+string(rune('a'+i))] = abandonEntry{Machine: testMac, At: w.clock.now()}
	}
	w.app.mu.Unlock()
	w.link.down.Store(true)
	w.app.tellAbandoned(w.ctx)
	if n := w.link.abandons.Load(); n != 1 {
		t.Fatalf("%d calls to a Mac that does not answer", n)
	}
	w.link.down.Store(false)
	w.link.abandons.Store(0)
	w.app.tellAbandoned(w.ctx)
	if n := w.link.abandons.Load(); n != abandonPerPass {
		t.Fatalf("%d calls in one pass", n)
	}
}

// 4. Entries the Mac was never told of are dropped after abandonKept.
func TestAbandonedEntriesArePruned(t *testing.T) {
	w := newWorld(t, workConfig())
	w.app.mu.Lock()
	w.app.st.Abandoned = map[string]abandonEntry{"todo_old": {Machine: testMac, At: w.clock.now()}}
	w.app.mu.Unlock()
	w.clock.add(abandonKept + time.Hour)
	w.app.mu.Lock()
	w.app.save()
	n := len(w.app.st.Abandoned)
	w.app.mu.Unlock()
	if n != 0 {
		t.Fatal("an old entry was kept")
	}
}

// 5. Orca's bots_disabled keeps its code, and it waits.
func TestBotsDisabledIsNamedAndWaits(t *testing.T) {
	err := nodeErr(&orcaError{Method: "bots.session.start", Code: "bots_disabled"})
	if errCode(err) != "bots_disabled" || !waiting(errCode(err)) {
		t.Fatalf("code %q", errCode(err))
	}
}

// 6. A failure without a code is the host's (the Mac away, the call dropped), and so is the node's not_running: they
// wait instead of using up attempts.
func TestFailuresWithoutACodeWait(t *testing.T) {
	for _, fail := range []error{errors.New("machine is offline"), &ext.Error{Code: "not_running", Message: "plugin not running"}} {
		w := newWorld(t, workConfig())
		w.pick("bot-alpha")
		w.link.mu.Lock()
		w.link.failWith = fail
		w.link.mu.Unlock()
		w.newTodo("todo_1", "prepare one", "", "work")
		w.created("todo_1")
		for i := 0; i < maxAttempts+2; i++ {
			w.clock.add(waitRetry)
			w.app.retryDue(w.ctx, false)
		}
		if r := w.record("todo_1"); r.State != "pending" || r.Attempts != 0 {
			t.Fatalf("%v: %+v", fail, r)
		}
	}
}

// 7. bot_not_found pauses only when said on two attempts waitRetry apart.
func TestBotNotFoundNeedsTwoAttemptsApart(t *testing.T) {
	w := newWorld(t, workConfig())
	w.pick("bot-alpha")
	saved := w.orca.bots
	w.orca.mu.Lock()
	w.orca.bots = saved[1:] // gone for a moment (Orca restarting, the list read half-way)
	w.orca.mu.Unlock()
	w.newTodo("todo_1", "prepare one", "", "work")
	w.created("todo_1")
	if ok, _ := w.app.ready(); !ok {
		t.Fatal("paused on one attempt")
	}
	w.orca.mu.Lock()
	w.orca.bots = saved // back
	w.orca.mu.Unlock()
	w.clock.add(waitRetry)
	w.app.retryDue(w.ctx, false)
	if ok, _ := w.app.ready(); !ok || len(w.orca.sent()) != 1 {
		t.Fatalf("ready %v, %d sends", ok, len(w.orca.sent()))
	}
}

// 8. A write at the limit's boundary is taken: the reservation of the one before is gone once it is written.
func TestAWriteAtTheBoundaryIsTaken(t *testing.T) {
	w := newWorld(t, workConfig())
	w.pick("bot-alpha")
	w.newTodo("todo_1", "prepare one", "", "work")
	w.created("todo_1")
	r := w.record("todo_1")
	w.app.mu.Lock()
	for i := 0; i < writesPerTodo-2; i++ {
		w.app.st.Records["todo_1"].Refs = append(w.app.st.Records["todo_1"].Refs, "orca-bot:old"+string(rune('a'+i)))
	}
	w.app.mu.Unlock()
	w.link.mu.Lock()
	w.link.slowRes = 500 * time.Millisecond // the node program is slow to take the answer
	w.link.mu.Unlock()
	go w.app.writeback(w.ctx, testMac, writebackEvent{TodoID: "todo_1", SessionID: r.SessionID, BotID: "bot-alpha",
		Ref: "orca-bot:nine", Text: "ninth"})
	w.eventually("the ninth written", func() bool { return len(w.record("todo_1").Refs) == writesPerTodo-1 })
	w.app.writeback(w.ctx, testMac, writebackEvent{TodoID: "todo_1", SessionID: r.SessionID, BotID: "bot-alpha",
		Ref: "orca-bot:ten", Text: "tenth"})
	if n := len(w.record("todo_1").Refs); n != writesPerTodo {
		t.Fatalf("%d written: the tenth was refused", n)
	}
}
