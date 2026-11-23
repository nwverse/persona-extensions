package main

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/sdk/go/ext"
)

// Tests for the second review of #958 (items A–G).

func handoffWithNotes(t *testing.T, todo, notes string) json.RawMessage {
	t.Helper()
	raw, _ := json.Marshal(handoffParams{TodoID: todo, BotID: "bot-alpha",
		OpID: orcaOperationID(time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC), todo), Text: "prepare " + todo,
		Todo: todoSnapshot{Title: "title of " + todo, Notes: notes}})
	return raw
}

// A. A hand-over that never sent must not keep its chat: the hub tells the Mac when it gives up.
func TestAGivenUpHandOverReleasesItsChat(t *testing.T) {
	w := newWorld(t, workConfig())
	w.pick("bot-alpha")
	w.orca.refuse["agentSession.send"] = "invalid_argument" // Orca keeps refusing the send: an ordinary refusal
	w.newTodo("todo_1", "prepare one", "", "work")
	w.created("todo_1")
	for i := 0; i < maxAttempts; i++ {
		w.clock.add(3 * time.Hour)
		w.app.retryDue(w.ctx, false)
	}
	if r := w.record("todo_1"); r.State != "failed" {
		t.Fatalf("not given up: %+v", r)
	}
	if w.nodeHandoff("todo_1").TodoID != "" {
		t.Fatal("the Mac still holds the given-up hand-over")
	}
	delete(w.orca.refuse, "agentSession.send")
	w.newTodo("todo_2", "prepare two", "", "work")
	w.created("todo_2")
	if r := w.record("todo_2"); r.State != "sent" {
		t.Fatalf("the next to-do: %+v", r)
	}
	if len(w.orca.sessions) != 1 {
		t.Fatalf("%d chats: the empty one should have been used", len(w.orca.sessions))
	}
}

func TestAStaleClaimIsTakenOver(t *testing.T) {
	w := newWorld(t, workConfig())
	w.orca.refuse["agentSession.send"] = "invalid_argument"
	if _, err := w.node.rpc(w.ctx, "handoff", handoffWithNotes(t, "todo_1", "")); err == nil {
		t.Fatal("a refused send succeeded")
	}
	delete(w.orca.refuse, "agentSession.send")
	if _, err := w.node.rpc(w.ctx, "handoff", handoffWithNotes(t, "todo_2", "")); err == nil || errCode(err) != "orca_busy" {
		t.Fatalf("a fresh claim was taken over: %v", err)
	}
	w.clock.add(nodeStaleClaim + time.Second) // longer than the hub waits for an answer: todo_1's claim is stale
	if _, err := w.node.rpc(w.ctx, "handoff", handoffWithNotes(t, "todo_2", "")); err != nil {
		t.Fatalf("a stale claim still held the chat: %v", err)
	}
	if h := w.nodeHandoff("todo_1"); h.SessionID != "" {
		t.Fatalf("the stale holder kept the chat: %+v", h)
	}
}

func TestNeverSentHandOversArePruned(t *testing.T) {
	w := newWorld(t, workConfig())
	w.orca.refuse["agentSession.send"] = "invalid_argument"
	_, _ = w.node.rpc(w.ctx, "handoff", handoffWithNotes(t, "todo_1", "SECRET-NOTES"))
	w.clock.add(nodeUnsentKept + time.Hour)
	raw, _ := json.Marshal(map[string]any{"bot_id": "bot-alpha", "wide": false})
	if _, err := w.node.rpc(w.ctx, "set_scope", raw); err != nil { // any save
		t.Fatal(err)
	}
	b, _ := os.ReadFile(w.node.statePath())
	if w.nodeHandoff("todo_1").TodoID != "" || strings.Contains(string(b), "SECRET-NOTES") {
		t.Fatal("a never-sent hand-over was kept, notes and all")
	}
}

// B. A long outage does not flood: the cap counts what was sent, and expiries make one notification.
func TestABacklogGoesOutAtTheHourlyRate(t *testing.T) {
	cfg := workConfig()
	cfg.PerHour = 2
	w := newWorld(t, cfg)
	w.pick("bot-alpha")
	w.orca.autoOpen = true
	w.link.down.Store(true) // the Mac is away for three hours, two new to-dos each hour
	for h := 0; h < 3; h++ {
		for i := 0; i < 2; i++ {
			id := "todo_" + string(rune('a'+h)) + string(rune('0'+i))
			w.newTodo(id, "prepare "+id, "", "work")
			w.created(id)
		}
		w.clock.add(time.Hour)
	}
	w.link.down.Store(false)
	w.app.retryDue(w.ctx, true) // the Mac says hello
	if n := len(w.orca.sent()); n != 2 {
		t.Fatalf("the backlog went out at once: %d sends", n)
	}
	w.clock.add(time.Hour)
	w.app.retryDue(w.ctx, false)
	if n := len(w.orca.sent()); n != 4 {
		t.Fatalf("an hour later: %d sends", n)
	}
}

func TestExpiriesAreOneNotification(t *testing.T) {
	w := newWorld(t, workConfig())
	w.pick("bot-alpha")
	w.link.down.Store(true)
	for i := 0; i < 4; i++ {
		id := "todo_" + string(rune('a'+i))
		w.newTodo(id, "prepare "+id, "", "work")
		w.created(id)
	}
	w.clock.add(maxAge + time.Minute)
	w.app.retryDue(w.ctx, false)
	got := w.hub.noticeTitles()
	if len(got) != 1 || !strings.Contains(w.hub.notices[0].Body, "4 条") {
		t.Fatalf("notices: %+v", w.hub.notices)
	}
	for i := 0; i < 4; i++ {
		if r := w.record("todo_" + string(rune('a'+i))); r.State != "failed" {
			t.Fatalf("not failed: %+v", r)
		}
	}
	w.app.mu.Lock()
	n := len(w.app.st.Abandon)
	w.app.mu.Unlock()
	if n != 4 {
		t.Fatalf("%d left to tell the Mac", n)
	}
	w.link.down.Store(false)
	w.app.retryDue(w.ctx, false)
	w.app.mu.Lock()
	n = len(w.app.st.Abandon)
	w.app.mu.Unlock()
	if n != 0 {
		t.Fatalf("the Mac was not told: %d", n)
	}
}

func TestARetryPassIsBounded(t *testing.T) {
	cfg := workConfig()
	cfg.PerHour = 100
	w := newWorld(t, cfg)
	w.pick("bot-alpha")
	w.link.down.Store(true)
	for i := 0; i < perPass+4; i++ {
		id := "todo_" + string(rune('a'+i))
		w.newTodo(id, "prepare "+id, "", "work")
		w.created(id)
	}
	w.link.handoffs.Store(0)
	w.clock.add(waitRetry)
	w.app.retryDue(w.ctx, false)
	if n := w.link.handoffs.Load(); n > perPass {
		t.Fatalf("one pass tried %d hand-overs", n)
	}
}

// C. A write-back nobody answers is given up after a day.
func TestAStalePendingWriteBackIsGivenUp(t *testing.T) {
	w := newWorld(t, workConfig())
	w.pick("bot-alpha")
	w.newTodo("todo_1", "prepare one", "", "work")
	w.created("todo_1")
	told := 0
	w.node.mu.Lock()
	w.node.emit = func(ev string, _ any) error {
		if ev == "orcabot.writeback" {
			told++
		}
		return nil
	}
	w.node.ackWait = 20 * time.Millisecond
	w.node.mu.Unlock()
	w.mcp("bot-alpha", w.orca.sent()[0].Session).call("todo_write_preparation", "todo_1", "summary")
	w.clock.add(nodeTellFor + time.Minute)
	w.node.pollOnce(w.ctx)
	w.clock.add(nodeReemit)
	w.node.pollOnce(w.ctx)
	ws := w.nodeHandoff("todo_1").Writes
	if len(ws) != 1 || ws[0].Status != "refused" || ws[0].Text != "" || told != 1 {
		t.Fatalf("writes %+v, told %d", ws, told)
	}
}

// E. The notes leave the Mac when it gives up telling a finish.
func TestNotesLeaveWhenAFinishIsGivenUp(t *testing.T) {
	w := newWorld(t, workConfig())
	w.node.emit = func(string, any) error { return nil }
	res, err := w.node.rpc(w.ctx, "handoff", handoffWithNotes(t, "todo_1", "SECRET-NOTES"))
	if err != nil {
		t.Fatal(err)
	}
	w.orca.finish(res.(*handoffResult).SessionID, "completed")
	w.node.pollOnce(w.ctx)
	w.clock.add(nodeTellFor + time.Minute)
	w.node.pollOnce(w.ctx)
	if h := w.nodeHandoff("todo_1"); !h.Acked || h.Todo.Notes != "" {
		t.Fatalf("given up: %+v", h)
	}
}

// F. One bot_not_found does not pause forwarding while bots.list still has the bot.
func TestOneBotNotFoundIsConfirmedFirst(t *testing.T) {
	w := newWorld(t, workConfig())
	w.pick("bot-alpha")
	w.orca.refuse["bots.session.start"] = "bot_not_found" // a passing refusal; bots.list still lists it
	w.newTodo("todo_1", "prepare one", "", "work")
	w.created("todo_1")
	if ok, missing := w.app.ready(); !ok {
		t.Fatalf("paused on one answer: %s", missing)
	}
	if r := w.record("todo_1"); r.State != "pending" || r.Attempts != 1 {
		t.Fatalf("record: %+v", r)
	}
	delete(w.orca.refuse, "bots.session.start")
	w.clock.add(time.Hour)
	w.app.retryDue(w.ctx, false)
	if len(w.orca.sent()) != 1 {
		t.Fatal("not sent once Orca answered")
	}
}

// G. The same write-back asked twice at once gets the one answer; two writes at once keep the limit; codes are
// structured.
func TestTheSameWriteBackTwiceAtOnce(t *testing.T) {
	w := newWorld(t, workConfig())
	w.pick("bot-alpha")
	w.newTodo("todo_1", "prepare one", "", "work")
	w.created("todo_1")
	w.hub.mu.Lock()
	w.hub.slow = 200 * time.Millisecond
	w.hub.mu.Unlock()
	w.node.mu.Lock()
	w.node.ackWait = 1500 * time.Millisecond
	w.node.mu.Unlock()
	m := w.mcp("bot-alpha", w.orca.sent()[0].Session)
	var wg sync.WaitGroup
	out := make([]string, 2)
	for i := range out {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			out[i] = toJSON(m.call("todo_write_preparation", "todo_1", "the same summary"))
		}(i)
	}
	wg.Wait()
	for _, o := range out {
		if strings.Contains(o, `"pending"`) || strings.Contains(o, "isError") {
			t.Fatalf("answers: %v", out)
		}
	}
	if d, _ := w.hub.counts(); d != 1 {
		t.Fatalf("%d decisions", d)
	}
}

func TestTwoWritesAtOnceKeepTheLimit(t *testing.T) {
	w := newWorld(t, workConfig())
	w.pick("bot-alpha")
	w.newTodo("todo_1", "prepare one", "", "work")
	w.created("todo_1")
	r := w.record("todo_1")
	w.app.mu.Lock()
	for i := 0; i < writesPerTodo-1; i++ {
		w.app.st.Records["todo_1"].Refs = append(w.app.st.Records["todo_1"].Refs, "orca-bot:old"+string(rune('a'+i)))
	}
	w.app.mu.Unlock()
	w.hub.mu.Lock()
	w.hub.slow = 200 * time.Millisecond
	w.hub.mu.Unlock()
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			w.app.writeback(w.ctx, testMac, writebackEvent{TodoID: "todo_1", SessionID: r.SessionID, BotID: "bot-alpha",
				Ref: "orca-bot:new" + string(rune('a'+i)), Text: "write " + string(rune('a'+i))})
		}(i)
	}
	wg.Wait()
	if d, _ := w.hub.counts(); d != 1 {
		t.Fatalf("%d decisions past the limit of %d", d, writesPerTodo)
	}
}

func TestErrCodeIsStructured(t *testing.T) {
	cases := []struct {
		err  error
		want string
	}{
		{&ext.Error{Code: "offline", Message: "machine m is offline"}, "offline"},
		{&ext.Error{Code: "node_error", Message: "orca_busy: Orca offered a chat"}, "orca_busy"},
		{&ext.Error{Code: "runtime_error", Message: "orca agentSession.send: x: the bot_not_found answer"}, ""},
		{errors.New("orca_not_running somewhere in the words"), ""},
	}
	for _, c := range cases {
		if got := errCode(c.err); got != c.want {
			t.Errorf("%v: %q, want %q", c.err, got, c.want)
		}
	}
}

func TestAnAttemptWaitsForRoomUnderTheCap(t *testing.T) {
	cfg := workConfig()
	cfg.PerHour = 1
	w := newWorld(t, cfg)
	w.pick("bot-alpha")
	w.orca.autoOpen = true
	w.link.down.Store(true)
	w.newTodo("todo_1", "prepare one", "", "work")
	w.created("todo_1")
	w.newTodo("todo_2", "prepare two", "", "work")
	w.created("todo_2")
	w.link.down.Store(false)
	w.app.attempt(w.ctx, "todo_1")
	w.app.attempt(w.ctx, "todo_2") // straight in, past retryDue: the cap still holds
	if n := len(w.orca.sent()); n != 1 {
		t.Fatalf("%d sends under a cap of 1", n)
	}
	if r := w.record("todo_2"); r.State != "pending" || !r.NextTry.After(w.clock.now()) {
		t.Fatalf("todo_2: %+v", r)
	}
}
