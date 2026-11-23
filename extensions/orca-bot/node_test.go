package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func handoffRaw(t *testing.T, todo, bot, op string) json.RawMessage {
	t.Helper()
	raw, _ := json.Marshal(handoffParams{TodoID: todo, BotID: bot, OpID: op, Text: "prepare " + todo,
		Todo: todoSnapshot{Title: "title of " + todo}})
	return raw
}

func TestNodeHandOverIsIdempotent(t *testing.T) {
	w := newWorld(t, workConfig())
	op := orcaOperationID(w.clock.now(), "k1")
	for i := 0; i < 2; i++ {
		res, err := w.node.rpc(w.ctx, "handoff", handoffRaw(t, "todo_1", "bot-alpha", op))
		if err != nil {
			t.Fatal(err)
		}
		if got := res.(*handoffResult); got.SessionID == "" || got.Already != (i == 1) {
			t.Fatalf("attempt %d: %+v", i, got)
		}
	}
	w.startNode()
	if res, err := w.node.rpc(w.ctx, "handoff", handoffRaw(t, "todo_1", "bot-alpha", op)); err != nil || !res.(*handoffResult).Already {
		t.Fatalf("after a restart: %+v %v", res, err)
	}
	if len(w.orca.sent()) != 1 || len(w.orca.sessions) != 1 {
		t.Fatalf("sends %d, chats %d", len(w.orca.sent()), len(w.orca.sessions))
	}
}

func TestNodeRetryReusesTheChat(t *testing.T) {
	w := newWorld(t, workConfig())
	w.orca.refuse["agentSession.send"] = "runtime_busy"
	op := orcaOperationID(w.clock.now(), "k1")
	if _, err := w.node.rpc(w.ctx, "handoff", handoffRaw(t, "todo_1", "bot-alpha", op)); err == nil {
		t.Fatal("a busy Orca accepted it")
	}
	delete(w.orca.refuse, "agentSession.send")
	if _, err := w.node.rpc(w.ctx, "handoff", handoffRaw(t, "todo_1", "bot-alpha", op)); err != nil {
		t.Fatal(err)
	}
	if len(w.orca.sessions) != 1 || len(w.orca.sent()) != 1 {
		t.Fatalf("chats %d sends %d", len(w.orca.sessions), len(w.orca.sent()))
	}
}

func TestNodeRefusesBadHandOvers(t *testing.T) {
	w := newWorld(t, workConfig())
	if _, err := w.node.rpc(w.ctx, "handoff", handoffRaw(t, "todo/../x", "bot-alpha", "op")); err == nil {
		t.Fatal("an odd to-do id was taken")
	}
	_, err := w.node.rpc(w.ctx, "handoff", handoffRaw(t, "todo_1", "bot-gone", "op"))
	if err == nil || errCode(err) != "bot_not_found" {
		t.Fatalf("a bot that is gone: %v", err)
	}
	if _, err := w.node.rpc(w.ctx, "nope", nil); err == nil {
		t.Fatal("an unknown method")
	}
}

func TestNodeFollowsTheTurnToItsEnd(t *testing.T) {
	w := newWorld(t, workConfig())
	var events []finishedEvent
	w.node.emit = func(ev string, p any) error {
		if ev == "orcabot.finished" {
			events = append(events, p.(finishedEvent))
		}
		return nil
	}
	res, err := w.node.rpc(w.ctx, "handoff", handoffRaw(t, "todo_1", "bot-alpha", "op-1"))
	if err != nil {
		t.Fatal(err)
	}
	sid := res.(*handoffResult).SessionID
	w.node.pollOnce(w.ctx)
	if len(events) != 0 || !w.nodeHandoff("todo_1").SawWorking {
		t.Fatalf("while working: %+v", events)
	}
	w.orca.finish(sid, "completed")
	w.node.pollOnce(w.ctx)
	if len(events) != 1 || events[0].State != "completed" || events[0].SessionID != sid || events[0].BotID != "bot-alpha" {
		t.Fatalf("finish: %+v", events)
	}
	w.node.pollOnce(w.ctx) // too soon to tell again
	w.clock.add(nodeReemit)
	w.node.pollOnce(w.ctx)
	if len(events) != 2 {
		t.Fatalf("not told again: %+v", events)
	}
	raw, _ := json.Marshal(map[string]string{"todo_id": "todo_1"})
	if _, err := w.node.rpc(w.ctx, "finished_ack", raw); err != nil {
		t.Fatal(err)
	}
	w.clock.add(nodeReemit)
	w.node.pollOnce(w.ctx)
	if len(events) != 2 {
		t.Fatalf("told after the acknowledgement: %+v", events)
	}
	// It survives a restart: still finished and acknowledged, nothing more to tell.
	w.startNode()
	if h := w.nodeHandoff("todo_1"); !h.Finished || !h.Acked {
		t.Fatalf("after a restart: %+v", h)
	}
}

func TestNodeFinishesAChatThatIsGoneOrTooLong(t *testing.T) {
	w := newWorld(t, workConfig())
	var states []string
	w.node.emit = func(ev string, p any) error {
		if f, ok := p.(finishedEvent); ok {
			states = append(states, f.TodoID+"="+f.State)
		}
		return nil
	}
	if _, err := w.node.rpc(w.ctx, "handoff", handoffRaw(t, "todo_gone", "bot-alpha", "op-1")); err != nil {
		t.Fatal(err)
	}
	if _, err := w.node.rpc(w.ctx, "handoff", handoffRaw(t, "todo_long", "bot-alpha", "op-2")); err != nil {
		t.Fatal(err)
	}
	w.orca.mu.Lock()
	w.orca.sessions = w.orca.sessions[1:] // the first chat was deleted in Orca
	w.orca.mu.Unlock()
	w.node.pollOnce(w.ctx)
	w.clock.add(nodeWatchFor + time.Minute)
	w.node.pollOnce(w.ctx)
	got := strings.Join(states, ",")
	if !strings.Contains(got, "todo_gone=unavailable") || !strings.Contains(got, "todo_long=timeout") {
		t.Fatalf("states: %s", got)
	}
}

func TestNodeQuietStartWaitsForTheTurn(t *testing.T) {
	w := newWorld(t, workConfig())
	res, err := w.node.rpc(w.ctx, "handoff", handoffRaw(t, "todo_1", "bot-alpha", "op-1"))
	if err != nil {
		t.Fatal(err)
	}
	sid := res.(*handoffResult).SessionID
	w.orca.finish(sid, "completed") // idle at once, never seen working
	w.node.pollOnce(w.ctx)
	if w.nodeHandoff("todo_1").Finished {
		t.Fatal("finished before the turn could have started")
	}
	w.clock.add(nodeQuietStart)
	w.node.pollOnce(w.ctx)
	if !w.nodeHandoff("todo_1").Finished {
		t.Fatal("not finished after the quiet start")
	}
}

func TestNodeSocketAndLink(t *testing.T) {
	w := newWorld(t, workConfig())
	st, err := os.Stat(w.node.socket)
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("socket: %v %v", st, err)
	}
	link := filepath.Join(w.nodeD, "bin", "orca-bot")
	target, err := os.Readlink(link)
	exe, _ := os.Executable()
	if real, e := filepath.EvalSymlinks(exe); e == nil {
		exe = real
	}
	if err != nil || target != exe {
		t.Fatalf("link: %q %v (want %q)", target, err, exe)
	}
	// Opening again keeps the link (an upgrade points it at the new program).
	if l2, err := keepLink(w.nodeD); err != nil || l2 != link {
		t.Fatalf("keep: %q %v", l2, err)
	}
	s := w.node.status(context.Background())
	if !s.OrcaRunning || s.Link != link || s.Socket != w.node.socket {
		t.Fatalf("status: %+v", s)
	}
}

func TestSocketPathFallsBackWhenTooLong(t *testing.T) {
	long := filepath.Join("/tmp", strings.Repeat("d", 120))
	p, err := socketPath(long)
	if err != nil {
		t.Fatal(err)
	}
	if len(p) > maxSocketPath || !strings.HasPrefix(p, "/tmp/orca-bot-") {
		t.Fatalf("path: %s", p)
	}
}
