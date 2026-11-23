package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// fakeOrca is a stand-in for the Orca desktop app's runtime: a unix socket speaking its line protocol, and the
// orca-runtime.json that names it. Every bot, chat and message is synthetic; no real Orca is reached.
type fakeOrca struct {
	t        *testing.T
	dir      string
	metaPath string
	l        net.Listener

	mu        sync.Mutex
	token     string
	runtimeID string
	protocol  int
	minClient int
	bots      []map[string]any
	enabled   bool
	sessions  []string // made by bots.session.start, in order
	working   map[string]bool
	turnState map[string]string
	sends     []fakeSend
	ops       map[string]string // clientOperationId → fingerprint
	calls     map[string]int
	refuse    map[string]string // method → error code to answer
}

type fakeSend struct {
	Session, OpID, Text string
}

func newFakeOrca(t *testing.T) *fakeOrca {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "obo") // a short path: a unix socket path holds at most 103 bytes on macOS
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	f := &fakeOrca{t: t, dir: dir, metaPath: filepath.Join(dir, "orca-runtime.json"), token: "tok-1", runtimeID: "rt-1",
		protocol: 3, minClient: 1, enabled: true, working: map[string]bool{}, turnState: map[string]string{},
		ops: map[string]string{}, calls: map[string]int{}, refuse: map[string]string{},
		bots: []map[string]any{
			{"id": "bot-alpha", "name": "Example Helper", "agent": "claude", "paused": false},
			{"id": "bot-beta", "name": "Second Bot", "agent": "codex", "paused": true},
		}}
	f.listen()
	return f
}

func (f *fakeOrca) listen() {
	sock := filepath.Join(f.dir, fmt.Sprintf("r%s.sock", f.runtimeID))
	_ = os.Remove(sock)
	l, err := net.Listen("unix", sock)
	if err != nil {
		f.t.Fatal(err)
	}
	f.l = l
	f.t.Cleanup(func() { l.Close() })
	f.writeMeta(sock)
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go f.serve(c)
		}
	}()
}

func (f *fakeOrca) writeMeta(sock string) {
	f.mu.Lock()
	meta := map[string]any{"runtimeId": f.runtimeID, "authToken": f.token, "transports": []any{
		map[string]any{"kind": "websocket", "endpoint": "ws://127.0.0.1:1"},
		map[string]any{"kind": "unix", "endpoint": sock},
	}}
	f.mu.Unlock()
	b, _ := json.Marshal(meta)
	if err := os.WriteFile(f.metaPath, b, 0o600); err != nil {
		f.t.Fatal(err)
	}
}

// restart is Orca quitting and starting again: a new socket, runtime and token, the old socket gone.
func (f *fakeOrca) restart() {
	f.l.Close()
	f.mu.Lock()
	f.token, f.runtimeID = f.token+"x", f.runtimeID+"x"
	f.mu.Unlock()
	f.listen()
}

// stop is Orca quitting: its socket refuses, its metadata file stays behind.
func (f *fakeOrca) stop() { f.l.Close() }

func (f *fakeOrca) serve(c net.Conn) {
	defer c.Close()
	r := bufio.NewReader(c)
	for {
		line, err := r.ReadBytes('\n')
		if err != nil {
			return
		}
		var req struct {
			ID        string          `json:"id"`
			AuthToken string          `json:"authToken"`
			Method    string          `json:"method"`
			Params    json.RawMessage `json:"params"`
		}
		if json.Unmarshal(line, &req) != nil {
			c.Write([]byte(`{"id":"unknown","ok":false,"error":{"code":"bad_request","message":"bad json"}}` + "\n"))
			continue
		}
		c.Write([]byte(`{"_keepalive":true}` + "\n"))
		res, code := f.answer(req.AuthToken, req.Method, req.Params)
		f.mu.Lock()
		rid := f.runtimeID
		f.mu.Unlock()
		env := map[string]any{"id": req.ID, "_meta": map[string]any{"runtimeId": rid}}
		if code != "" {
			env["ok"], env["error"] = false, map[string]any{"code": code, "message": code}
		} else {
			env["ok"], env["result"] = true, res
		}
		b, _ := json.Marshal(env)
		c.Write(append(b, '\n'))
	}
}

func (f *fakeOrca) answer(token, method string, raw json.RawMessage) (any, string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls[method]++
	if token != f.token {
		return nil, "unauthorized"
	}
	if code := f.refuse[method]; code != "" {
		return nil, code
	}
	var p map[string]any
	_ = json.Unmarshal(raw, &p)
	switch method {
	case "status.get":
		return map[string]any{"appVersion": "9.9.9-test", "runtimeProtocolVersion": f.protocol,
			"minCompatibleRuntimeClientVersion": f.minClient}, ""
	case "bots.list":
		return map[string]any{"bots": f.bots, "botsEnabled": f.enabled}, ""
	case "bots.session.start":
		id, _ := p["botId"].(string)
		for _, b := range f.bots {
			if b["id"] == id {
				s := fmt.Sprintf("claude_chat_%d", len(f.sessions)+1)
				f.sessions = append(f.sessions, s)
				return map[string]any{"sessionId": s}, ""
			}
		}
		return nil, "bot_not_found"
	case "agentSession.history":
		s, _ := p["sessionId"].(string)
		if !contains(f.sessions, s) {
			return nil, "agent_session_not_found"
		}
		page := map[string]any{"fence": 7, "working": f.working[s], "actionablePromptIds": []string{}}
		if st := f.turnState[s]; st != "" {
			page["latestTurn"] = map[string]any{"itemId": "turn-1", "observedAt": 1, "turn": map[string]any{"turnId": "t1", "state": st}}
		}
		return map[string]any{"ok": true, "page": page}, ""
	case "agentSession.send":
		env, _ := p["envelope"].(map[string]any)
		s, _ := env["sessionId"].(string)
		op, _ := env["clientOperationId"].(string)
		fp, _ := env["payloadFingerprint"].(string)
		want, err := orcaFingerprint("agentSession.send", s, map[string]any{"body": p["body"], "delivery": p["delivery"]})
		if err != nil || fp != want {
			return map[string]any{"ok": false, "refusal": map[string]any{"code": "agent_session_fingerprint_mismatch"}}, ""
		}
		if prev, ok := f.ops[op]; ok { // a replay answers as the first time
			if prev != fp {
				return map[string]any{"ok": false, "refusal": map[string]any{"code": "agent_session_operation_conflict"}}, ""
			}
			return map[string]any{"ok": true}, ""
		}
		f.ops[op] = fp
		body, _ := p["body"].(map[string]any)
		blocks, _ := body["blocks"].([]any)
		text := ""
		if len(blocks) == 1 {
			b, _ := blocks[0].(map[string]any)
			text, _ = b["text"].(string)
		}
		f.sends = append(f.sends, fakeSend{s, op, text})
		f.working[s] = true
		return map[string]any{"ok": true}, ""
	}
	return nil, "method_not_found"
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func (f *fakeOrca) finish(session, state string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.working[session] = false
	f.turnState[session] = state
}

func (f *fakeOrca) sent() []fakeSend {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]fakeSend(nil), f.sends...)
}

func (f *fakeOrca) client() *orcaClient {
	return newOrcaClient(func() string { return f.metaPath })
}
