package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"strings"
	"testing"
	"time"
)

// Tests for the findings of the review of #958, one or more per item.

// 1. Orca answers an empty chat already open instead of a new one, and a chat stays empty after a send until its turn
// opens: a second to-do must not land in the first one's chat.
func TestTwoTodosNeverShareAChat(t *testing.T) {
	w := newWorld(t, workConfig())
	w.pick("bot-alpha")
	w.newTodo("todo_a", "prepare a", "", "work")
	w.created("todo_a")
	w.newTodo("todo_b", "prepare b", "", "work")
	w.created("todo_b") // Orca offers todo_a's chat again: refused, tried later
	if n := len(w.orca.sent()); n != 1 {
		t.Fatalf("%d sends", n)
	}
	if r := w.record("todo_b"); r.State != "pending" || r.LastCode != "orca_busy" || r.Attempts != 0 {
		t.Fatalf("todo_b: %+v", r)
	}
	w.orca.openTurn(w.orca.sent()[0].Session) // the bot starts on todo_a: its chat is no longer empty
	w.clock.add(waitRetry)
	w.app.retryDue(w.ctx, false)
	sent := w.orca.sent()
	if len(sent) != 2 || sent[0].Session == sent[1].Session {
		t.Fatalf("sends: %+v", sent)
	}
}

func TestHandOverFallsBackToFenceOneWhenANewChatHasNoHistory(t *testing.T) {
	w := newWorld(t, workConfig())
	w.orca.refuse["agentSession.history"] = "runtime_error" // a chat without journal state yet (Orca's readHead throws)
	if _, err := w.node.rpc(w.ctx, "handoff", handoffRaw(t, "todo_1", "bot-alpha", "k1")); err != nil {
		t.Fatalf("handoff: %v", err)
	}
	if n := len(w.orca.sent()); n != 1 {
		t.Fatalf("%d sends", n)
	}
}

// 2. The turn's end: only a final turn of the send counts.
func TestTurnEndNeedsAFinalTurnOfTheSend(t *testing.T) {
	sent := time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)
	h := &nodeHandoff{OpID: "1793850000000-0123456789abcdef0123456789abcdef", SentAt: sent}
	yes, no := true, false
	at := func(d time.Duration) int64 { return sent.Add(d).UnixMilli() }
	cases := []struct {
		name           string
		head           orcaHead
		state, outcome string
	}{
		{"working", orcaHead{Working: &yes}, "", ""},
		{"no turn shown (hidden after a restart)", orcaHead{Working: &no}, "", ""},
		{"an older turn", orcaHead{Working: &no, Turn: &orcaTurn{State: "completed", ObservedAt: at(-time.Hour)}}, "", ""},
		{"running but not working", orcaHead{Working: &no, Turn: &orcaTurn{State: "running", ObservedAt: at(time.Second)}}, "interrupted", ""},
		{"completed", orcaHead{Working: &no, Turn: &orcaTurn{State: "completed", Outcome: "success", ObservedAt: at(time.Second)}}, "completed", "success"},
		{"the send's own item", orcaHead{Working: &no, Turn: &orcaTurn{State: "completed", UserItemID: "orca:" + h.OpID}}, "completed", ""},
		{"a bare op id is not Orca's key", orcaHead{Working: &no, Turn: &orcaTurn{State: "completed", UserItemID: h.OpID}}, "", ""},
		{"unverifiable", orcaHead{Working: &no, Turn: &orcaTurn{State: "unverifiable", RequestedAt: at(0)}}, "unverifiable", ""},
		{"older host, running", orcaHead{Turn: &orcaTurn{State: "running", ObservedAt: at(time.Second)}}, "", ""},
		{"older host, no turn", orcaHead{}, "", ""},
		{"older host, done", orcaHead{Turn: &orcaTurn{State: "completed", StartedAt: at(time.Second)}}, "completed", ""},
		{"older host, an older turn", orcaHead{Turn: &orcaTurn{State: "completed", StartedAt: at(-time.Minute)}}, "", ""},
	}
	for _, c := range cases {
		s, o := turnEnd(&c.head, h)
		if s != c.state || o != c.outcome {
			t.Errorf("%s: %q %q, want %q %q", c.name, s, o, c.state, c.outcome)
		}
	}
}

func TestAHiddenTurnKeepsWaiting(t *testing.T) {
	w := newWorld(t, workConfig())
	w.pick("bot-alpha")
	w.newTodo("todo_1", "prepare one", "", "work")
	w.created("todo_1")
	sid := w.orca.sent()[0].Session
	no := false
	w.orca.setTurn(sid, &no, nil, w.clock.now()) // Orca restarted and hides the turn it cannot vouch for
	w.clock.add(time.Hour)
	w.node.pollOnce(w.ctx)
	time.Sleep(30 * time.Millisecond)
	if w.nodeHandoff("todo_1").Finished || w.noticeCount() != 0 {
		t.Fatal("a turn nobody showed was taken for done")
	}
}

func TestOutcomesHaveTheirOwnWords(t *testing.T) {
	r := &record{TodoID: "todo_1", Title: "T", BotName: "B"}
	done := finishNotice(r, "completed", "success")
	for _, c := range []struct{ state, outcome, want string }{
		{"unverifiable", "", "无法确认"},
		{"completed", "failure", "出错"},
		{"completed", "cancellation", "中断"},
		{"interrupted", "", "中断"},
	} {
		n := finishNotice(r, c.state, c.outcome)
		if n.Body == done.Body || !strings.Contains(n.Title+n.Body, c.want) {
			t.Errorf("%s/%s: %+v", c.state, c.outcome, n)
		}
	}
}

func TestAFailedTurnIsNotifiedAsSuch(t *testing.T) {
	w := newWorld(t, workConfig())
	w.pick("bot-alpha")
	w.newTodo("todo_1", "prepare one", "", "work")
	w.created("todo_1")
	w.orca.finishWith(w.orca.sent()[0].Session, "completed", "failure")
	w.node.pollOnce(w.ctx)
	w.eventually("the notification", func() bool { return w.noticeCount() == 1 })
	if got := w.hub.noticeTitles(); !strings.Contains(got[0], "出错") {
		t.Fatalf("notice: %v", got)
	}
}

// 3. A bot waiting on an approval or a question is told once, and followed on.
func TestABotWaitingOnTheOwnerIsToldOnce(t *testing.T) {
	w := newWorld(t, workConfig())
	w.pick("bot-alpha")
	w.newTodo("todo_1", "prepare one", "", "work")
	w.created("todo_1")
	sid := w.orca.sent()[0].Session
	w.orca.openTurn(sid)
	w.orca.setPrompts(sid, 1)
	w.node.pollOnce(w.ctx) // one poll is not yet a wait
	time.Sleep(30 * time.Millisecond)
	if w.noticeCount() != 0 {
		t.Fatal("told after one poll")
	}
	w.node.pollOnce(w.ctx)
	w.eventually("the needs-you notification", func() bool { return w.noticeCount() == 1 })
	w.clock.add(nodeReemit)
	w.node.pollOnce(w.ctx) // told again while the wait lasts: the hub tells the owner once
	time.Sleep(30 * time.Millisecond)
	if w.noticeCount() != 1 || !strings.Contains(w.hub.noticeTitles()[0], "在等你") {
		t.Fatalf("notices: %v", w.hub.noticeTitles())
	}
	if w.nodeHandoff("todo_1").Finished {
		t.Fatal("a wait ended the following")
	}
	w.orca.setPrompts(sid, 0)
	w.orca.finish(sid, "completed")
	w.node.pollOnce(w.ctx)
	w.eventually("the finish", func() bool { return w.noticeCount() == 2 })
}

// 4. The Mac or Orca being away does not use up attempts; a hand-over waits up to maxAge, then fails with one
// notification; the Mac coming back retries at once.
func TestAnOfflineMacKeepsTheTodoWaiting(t *testing.T) {
	w := newWorld(t, workConfig())
	w.pick("bot-alpha")
	w.link.down.Store(true)
	w.newTodo("todo_1", "prepare one", "", "work")
	w.created("todo_1")
	for i := 0; i < maxAttempts+3; i++ {
		w.clock.add(waitRetry)
		w.app.retryDue(w.ctx, false)
	}
	r := w.record("todo_1")
	if r.State != "pending" || r.Attempts != 0 || r.LastCode != "offline" {
		t.Fatalf("after many tries offline: %+v", r)
	}
	w.link.down.Store(false)
	w.startNode() // the Mac comes back: its hello retries at once, before NextTry
	w.eventually("the hand-over after hello", func() bool { return len(w.orca.sent()) == 1 })

	w.link.down.Store(true)
	w.newTodo("todo_2", "prepare two", "", "work")
	w.created("todo_2")
	w.clock.add(maxAge + time.Minute)
	w.app.retryDue(w.ctx, false)
	if r := w.record("todo_2"); r.State != "failed" || !r.FailNotified {
		t.Fatalf("after two days: %+v", r)
	}
	if got := w.hub.noticeTitles(); len(got) != 1 || !strings.Contains(got[0], "没交给") {
		t.Fatalf("notices: %v", got)
	}
}

func TestAPausedBotWaits(t *testing.T) {
	w := newWorld(t, workConfig())
	w.pick("bot-beta") // paused in Orca
	w.newTodo("todo_1", "prepare one", "", "work")
	w.created("todo_1")
	if r := w.record("todo_1"); r.State != "pending" || r.LastCode != "bot_paused" || r.Attempts != 0 || len(w.orca.sessions) != 0 {
		t.Fatalf("paused: %+v, chats %d", r, len(w.orca.sessions))
	}
	w.orca.mu.Lock()
	w.orca.bots[1]["paused"] = false
	w.orca.mu.Unlock()
	w.clock.add(waitRetry)
	w.app.retryDue(w.ctx, false)
	if len(w.orca.sent()) != 1 {
		t.Fatal("not sent once the bot was resumed")
	}
}

// 5. A write-back the hub never heard is told again until answered, not taken for a duplicate.
func TestALostWriteBackIsToldAgain(t *testing.T) {
	w := newWorld(t, workConfig())
	w.pick("bot-alpha")
	w.newTodo("todo_1", "prepare one", "", "work")
	w.created("todo_1")
	sid := w.orca.sent()[0].Session
	real := w.node.emit
	w.node.mu.Lock()
	w.node.emit = func(string, any) error { return nil } // the hub program is away: node.event is dropped
	w.node.ackWait = 50 * time.Millisecond
	w.node.mu.Unlock()
	m := w.mcp("bot-alpha", sid)
	if r := toJSON(m.call("todo_write_preparation", "todo_1", "the summary")); !strings.Contains(r, "pending") {
		t.Fatalf("first: %s", r)
	}
	if r := toJSON(m.call("todo_write_preparation", "todo_1", "the summary")); strings.Contains(r, "duplicate") {
		t.Fatalf("a write never written was called a duplicate: %s", r)
	}
	if d, _ := w.hub.counts(); d != 0 {
		t.Fatal("written while the hub was away")
	}
	w.node.mu.Lock()
	w.node.emit = real
	w.node.mu.Unlock()
	w.clock.add(nodeReemit)
	w.node.pollOnce(w.ctx)
	w.eventually("the write", func() bool { d, _ := w.hub.counts(); return d == 1 })
	w.eventually("its answer", func() bool {
		ws := w.nodeHandoff("todo_1").Writes
		return len(ws) == 1 && ws[0].Status == "written" && ws[0].Text == ""
	})
	if r := toJSON(m.call("todo_write_preparation", "todo_1", "the summary")); !strings.Contains(r, "duplicate") {
		t.Fatalf("after it was written: %s", r)
	}
}

// 6. What is forwarded: the owner's own new to-dos, few at a time.
func TestAgentTasksAndQuestionsAreLeftAlone(t *testing.T) {
	w := newWorld(t, workConfig())
	w.pick("bot-alpha")
	task := w.newTodo("todo_task", "prepare task", "", "work")
	task.Origin = "assistant-chat-1"
	w.hub.todos[0] = task
	w.newTodo("todo_ask_card1", "prepare? a question", "", "work")
	w.created("todo_task")
	w.created("todo_ask_card1")
	w.app.reconcile(w.ctx)
	if len(w.orca.sent()) != 0 {
		t.Fatal("an agent task or a question was forwarded")
	}
	cfg := workConfig()
	cfg.IncludeTasks = true
	w.setConfig(cfg)
	w.clock.add(time.Second)
	task2 := w.newTodo("todo_task2", "prepare task 2", "", "work")
	task2.Origin = "assistant-chat-1"
	w.hub.todos[0] = task2
	w.created("todo_task2")
	if len(w.orca.sent()) != 1 {
		t.Fatal("an agent task not forwarded with tasks included")
	}
}

func TestHandOversAreCapped(t *testing.T) {
	cfg := workConfig()
	cfg.PerHour = 2
	w := newWorld(t, cfg)
	w.pick("bot-alpha")
	for i := 0; i < 3; i++ {
		id := "todo_" + string(rune('a'+i))
		w.newTodo(id, "prepare "+id, "", "work")
		w.orca.openTurnAll()
		w.created(id)
	}
	if n := len(w.orca.sent()); n != 2 || w.record("todo_c") != nil {
		t.Fatalf("over the hourly cap: %d sends", n)
	}
	w.clock.add(time.Hour)
	w.app.reconcile(w.ctx)
	if n := len(w.orca.sent()); n != 3 {
		t.Fatalf("after the hour: %d sends", n)
	}

	w2 := newWorld(t, config{Machines: []string{testMac}, Project: "work", PerHour: 100})
	w2.pick("bot-alpha")
	for i := 0; i < perPass+3; i++ { // every event dropped
		w2.newTodo("todo_p"+string(rune('a'+i)), "prepare", "", "work")
	}
	w2.orca.autoOpen = true
	w2.app.reconcile(w2.ctx)
	if n := len(w2.orca.sent()); n != perPass {
		t.Fatalf("one reconcile started %d", n)
	}
	w2.app.reconcile(w2.ctx)
	if n := len(w2.orca.sent()); n != perPass+3 {
		t.Fatalf("the next started the rest: %d", n)
	}
}

func TestOnlyNewTodosThatMatchedWhenMade(t *testing.T) {
	w := newWorld(t, workConfig())
	w.pick("bot-alpha")
	w.newTodo("todo_old", "prepare old", "", "work") // its event was dropped, and the Mac was away for days
	w.clock.add(maxAge + time.Hour)
	w.app.reconcile(w.ctx)
	if w.record("todo_old") != nil {
		t.Fatal("a to-do older than two days was forwarded")
	}
	edited := w.newTodo("todo_edit", "lunch", "", "work")
	w.created("todo_edit") // seen when made: it did not match
	edited.Title = "prepare lunch"
	w.hub.todos[0] = edited
	w.app.reconcile(w.ctx)
	if w.record("todo_edit") != nil {
		t.Fatal("a to-do edited to match later was forwarded")
	}
	if !strings.Contains(buildRequest(todoSnapshot{ID: "todo_x"}), "不要在 Persona 里新建待办") {
		t.Fatal("the request does not tell the bot to leave Persona's to-dos alone")
	}
}

// 7. Stopping or switching drops what still waited; a bot Orca no longer has pauses forwarding.
func TestStoppingOrSwitchingDropsWaitingHandOvers(t *testing.T) {
	w := newWorld(t, workConfig())
	w.pick("bot-alpha")
	w.orca.stop()
	w.newTodo("todo_1", "prepare one", "", "work")
	w.created("todo_1")
	w.app.act(w.ctx, "clear")
	if r := w.record("todo_1"); r.State != "failed" {
		t.Fatalf("after stopping: %+v", r)
	}
	w.orca.restart()
	w.clock.add(time.Hour)
	w.app.retryDue(w.ctx, false)
	if len(w.orca.sent()) != 0 {
		t.Fatal("sent after forwarding stopped")
	}

	w.pick("bot-alpha")
	w.orca.stop()
	w.newTodo("todo_2", "prepare two", "", "work")
	w.created("todo_2")
	w.orca.restart()
	w.orca.mu.Lock()
	w.orca.bots = append(w.orca.bots, map[string]any{"id": "bot-gamma", "name": "Third", "agent": "claude", "paused": false})
	w.orca.mu.Unlock()
	w.pick("bot-gamma")
	w.clock.add(time.Hour)
	w.app.retryDue(w.ctx, false)
	if r := w.record("todo_2"); r.State != "failed" || len(w.orca.sent()) != 0 {
		t.Fatalf("after switching: %+v, %d sends", r, len(w.orca.sent()))
	}
}

func TestAGoneBotPausesForwardingUntilPickedAgain(t *testing.T) {
	w := newWorld(t, workConfig())
	w.pick("bot-alpha")
	saved := w.orca.bots
	w.orca.mu.Lock()
	w.orca.bots = saved[1:]
	w.orca.mu.Unlock()
	w.newTodo("todo_1", "prepare one", "", "work")
	w.created("todo_1")
	w.clock.add(waitRetry) // confirmed by a second attempt, later
	w.app.retryDue(w.ctx, false)
	if ok, missing := w.app.ready(); ok || !strings.Contains(missing, "暂停") {
		t.Fatalf("still forwarding: %v %q", ok, missing)
	}
	w.orca.mu.Lock()
	w.orca.bots = saved // it came back (or the owner made it again)
	w.orca.mu.Unlock()
	w.newTodo("todo_2", "prepare two", "", "work")
	w.created("todo_2")
	if w.record("todo_2") != nil {
		t.Fatal("forwarded while paused")
	}
	w.pick("bot-alpha")
	w.newTodo("todo_3", "prepare three", "", "work")
	w.created("todo_3")
	if len(w.orca.sent()) != 1 {
		t.Fatal("not forwarding after the bot was picked again")
	}
}

// 8. The MCP command's reach: its token, its chat, and the owner's wider scope.
func TestMCPNeedsItsTokenAndItsChat(t *testing.T) {
	w := newWorld(t, workConfig())
	w.pick("bot-alpha")
	w.newTodo("todo_1", "prepare one", "the notes", "work")
	w.created("todo_1")
	sid := w.orca.sent()[0].Session

	forged := &mcpServer{socket: w.node.socket, bot: "bot-alpha", token: "guessed", session: sid}
	if r := toJSON(forged.call("todo_get", "todo_1", "")); !strings.Contains(r, "--token") {
		t.Fatalf("a wrong token: %s", r)
	}
	shared := w.mcp("bot-alpha", "") // a tool process several chats share: no chat known
	for _, c := range [][2]string{{"todo_list", ""}, {"todo_get", "todo_1"}, {"todo_write_preparation", "todo_1"}} {
		if r := toJSON(shared.call(c[0], c[1], "x")); !strings.Contains(r, "isError") {
			t.Fatalf("%s without a chat: %s", c[0], r)
		}
	}
	if toast := w.app.act(w.ctx, "scope:wide"); !strings.Contains(toast, "放宽") {
		t.Fatal(toast)
	}
	if r := toJSON(shared.call("todo_list", "", "")); !strings.Contains(r, "todo_1") {
		t.Fatalf("with the wider scope: %s", r)
	}
	w.app.act(w.ctx, "scope:chat")
	if r := toJSON(shared.call("todo_list", "", "")); !strings.Contains(r, "isError") {
		t.Fatalf("narrowed again: %s", r)
	}
}

func TestNotesLeaveTheMacOnceDone(t *testing.T) {
	w := newWorld(t, workConfig())
	w.pick("bot-alpha")
	w.newTodo("todo_1", "prepare one", "SECRET-NOTES", "work")
	w.created("todo_1")
	w.orca.finish(w.orca.sent()[0].Session, "completed")
	w.node.pollOnce(w.ctx)
	w.eventually("the acknowledgement", func() bool { return w.nodeHandoff("todo_1").Acked })
	b, _ := os.ReadFile(w.node.statePath())
	if strings.Contains(string(b), "SECRET-NOTES") {
		t.Fatal("the notes stayed in the node's state")
	}
}

// 9. The write-back says its source.
func TestWriteBackIsAnOutcomeDecision(t *testing.T) {
	w := newWorld(t, workConfig())
	w.pick("bot-alpha")
	w.newTodo("todo_1", "prepare one", "", "work")
	w.created("todo_1")
	m := w.mcp("bot-alpha", w.orca.sent()[0].Session)
	m.call("todo_write_preparation", "todo_1", "summary")
	w.eventually("the write", func() bool { d, _ := w.hub.counts(); return d == 1 })
	raw, _ := json.Marshal(w.hub.decisions[0].D)
	if !strings.Contains(string(raw), `"source":"outcome"`) {
		t.Fatalf("payload: %s", raw)
	}
}

// Nits.
func TestFinishesOfUnknownTodosAreAcknowledged(t *testing.T) {
	w := newWorld(t, workConfig())
	w.pick("bot-alpha")
	w.newTodo("todo_1", "prepare one", "", "work")
	w.created("todo_1")
	w.app.mu.Lock()
	delete(w.app.st.Records, "todo_1") // the hub lost it
	w.app.mu.Unlock()
	w.orca.finish(w.orca.sent()[0].Session, "completed")
	w.node.pollOnce(w.ctx)
	w.eventually("the acknowledgement", func() bool { return w.nodeHandoff("todo_1").Acked })
}

func TestAFinishIsNotToldForever(t *testing.T) {
	w := newWorld(t, workConfig())
	told := 0
	w.node.emit = func(ev string, p any) error {
		if ev == "orcabot.finished" {
			told++
		}
		return nil
	}
	res, err := w.node.rpc(w.ctx, "handoff", handoffRaw(t, "todo_1", "bot-alpha", "k1"))
	if err != nil {
		t.Fatal(err)
	}
	w.orca.finish(res.(*handoffResult).SessionID, "completed")
	w.node.pollOnce(w.ctx)
	w.clock.add(nodeTellFor + time.Minute)
	w.node.pollOnce(w.ctx)
	w.clock.add(nodeReemit)
	w.node.pollOnce(w.ctx)
	if told != 1 || !w.nodeHandoff("todo_1").Acked {
		t.Fatalf("told %d times", told)
	}
}

func TestNotificationRetriesAreCappedAndHeldIsSaid(t *testing.T) {
	w := newWorld(t, workConfig())
	w.pick("bot-alpha")
	w.newTodo("todo_1", "prepare one", "", "work")
	w.created("todo_1")
	w.hub.mu.Lock()
	w.hub.notifyErr = errors.New("http_503")
	w.hub.mu.Unlock()
	w.orca.finish(w.orca.sent()[0].Session, "completed")
	for i := 0; i < maxNotifyAttempts+2; i++ {
		w.clock.add(nodeReemit)
		w.node.pollOnce(w.ctx)
		time.Sleep(20 * time.Millisecond)
	}
	w.eventually("giving up", func() bool { return w.nodeHandoff("todo_1").Acked })
	if r := w.record("todo_1"); r.State != "finished" || !strings.Contains(r.NotifyNote, "没能发出去") {
		t.Fatalf("record: %+v", r)
	}

	w2 := newWorld(t, workConfig())
	w2.pick("bot-alpha")
	w2.hub.held = true
	w2.newTodo("todo_1", "prepare one", "", "work")
	w2.created("todo_1")
	w2.orca.finish(w2.orca.sent()[0].Session, "completed")
	w2.node.pollOnce(w2.ctx)
	w2.eventually("the finish", func() bool { r := w2.record("todo_1"); return r.State == "finished" })
	if r := w2.record("todo_1"); !strings.Contains(r.NotifyNote, "推送分级") || !strings.Contains(viewJSON(t, w2), "推送分级") {
		t.Fatalf("held: %+v", r)
	}
}

func TestTheMCPSocketBoundsARequest(t *testing.T) {
	w := newWorld(t, workConfig())
	c, err := net.Dial("unix", w.node.socket)
	if err != nil {
		t.Fatal(err)
	}
	big := make([]byte, mcpMaxLine+1024)
	for i := range big {
		big[i] = 'x'
	}
	_, _ = c.Write(append(big, '\n'))
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	if line, _ := bufio.NewReader(c).ReadString('\n'); line != "" {
		t.Fatalf("answered an oversized request: %q", line[:min(80, len(line))])
	}
	c.Close()
	if r := toJSON(w.mcp("bot-alpha", "claude_chat_1").call("todo_list", "", "")); strings.Contains(r, "连不上") {
		t.Fatalf("the socket stopped serving: %s", r)
	}
}

func TestOrcaClientRefusesAnUntrustedSocketAndAnotherRuntime(t *testing.T) {
	f := newFakeOrca(t)
	if err := os.Chmod(f.dir, 0o777); err != nil {
		t.Fatal(err)
	}
	if _, err := f.client().Status(context.Background()); !errors.Is(err, errOrcaIncompatible) || f.calls["status.get"] != 0 {
		t.Fatalf("a socket in a directory others can write: %v (%d calls)", err, f.calls["status.get"])
	}
	_ = os.Chmod(f.dir, 0o700)

	g := newFakeOrca(t)
	g.otherRT = true
	if _, _, err := g.client().Bots(context.Background()); err == nil {
		t.Fatal("an answer of another runtime was taken")
	}
}
