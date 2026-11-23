package main

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestFingerprintMatchesOrca(t *testing.T) {
	// The vector Persona core pins against Orca (server/internal/orcart), with its synthetic chat id.
	fields := map[string]any{"body": map[string]any{"kind": "message", "role": "user",
		"blocks": []any{map[string]any{"type": "text", "text": "hello"}}}}
	got, err := orcaFingerprint("agentSession.send", "claude_0b0e9c8e_6f1a_4c3e_9d2b_4a5f6e7d8c9b", fields)
	if err != nil {
		t.Fatal(err)
	}
	if want := "6960bc9ad67080004eaddcc7cff600cf8043ab224aec8730355443f83a931cd8"; got != want {
		t.Errorf("fingerprint %s, want %s", got, want)
	}
}

func TestOperationID(t *testing.T) {
	at := time.UnixMilli(1793850000000)
	re := regexp.MustCompile(`^\d{13}-[0-9a-f]{32}$`)
	a, b := orcaOperationID(at, "orca-bot:todo_1:bot-alpha"), orcaOperationID(at, "orca-bot:todo_1:bot-alpha")
	if !re.MatchString(a) || a != b || !strings.HasPrefix(a, "1793850000000-") {
		t.Errorf("keyed ids: %s %s", a, b)
	}
}

func TestOrcaClientSpeaksTheRuntime(t *testing.T) {
	f := newFakeOrca(t)
	c := f.client()
	ctx := context.Background()
	v, err := c.Status(ctx)
	if err != nil || v != "9.9.9-test" {
		t.Fatalf("status: %q %v", v, err)
	}
	bots, enabled, err := c.Bots(ctx)
	if err != nil || !enabled || len(bots) != 2 || bots[0].ID != "bot-alpha" || bots[0].Name != "Example Helper" || !bots[1].Paused {
		t.Fatalf("bots: %+v %v %v", bots, enabled, err)
	}
	sid, err := c.StartSession(ctx, "bot-alpha")
	if err != nil || sid == "" {
		t.Fatalf("start: %q %v", sid, err)
	}
	if _, err := c.StartSession(ctx, "bot-gone"); orcaCode(err) != "bot_not_found" {
		t.Fatalf("a bot that is gone: %v", err)
	}
	h, err := c.Head(ctx, sid)
	if err != nil || h.Fence != 7 || h.Working == nil || *h.Working {
		t.Fatalf("head: %+v %v", h, err)
	}
	op := orcaOperationID(time.Now(), "k")
	if err := c.Send(ctx, sid, h.Fence, op, "hello <todo>\n"); err != nil {
		t.Fatalf("send: %v", err)
	}
	// The same operation again is a replay, not a second message.
	if err := c.Send(ctx, sid, h.Fence, op, "hello <todo>\n"); err != nil {
		t.Fatalf("replay: %v", err)
	}
	if s := f.sent(); len(s) != 1 || s[0].Text != "hello <todo>" || s[0].Session != sid {
		t.Fatalf("sends: %+v", s)
	}
	// The same operation with other words is refused, never sent.
	if err := c.Send(ctx, sid, h.Fence, op, "other words"); orcaCode(err) != "agent_session_operation_conflict" {
		t.Fatalf("conflicting replay: %v", err)
	}
	h, _ = c.Head(ctx, sid)
	if h.Working == nil || !*h.Working {
		t.Fatalf("working after a send: %+v", h)
	}
}

func TestOrcaClientRereadsTheTokenWhenOrcaRestarts(t *testing.T) {
	f := newFakeOrca(t)
	c := f.client()
	ctx := context.Background()
	if _, err := c.Status(ctx); err != nil {
		t.Fatal(err)
	}
	f.restart() // a new socket and a new token: the client's are stale
	if _, _, err := c.Bots(ctx); err != nil {
		t.Fatalf("after a restart: %v", err)
	}
	// The token alone rotating (same socket) is answered unauthorized, then read again.
	f.mu.Lock()
	f.token = "rotated"
	f.mu.Unlock()
	sock := ""
	if m, err := c.readMeta(); err == nil {
		sock = m.socket
	}
	f.writeMeta(sock)
	if _, _, err := c.Bots(ctx); err != nil {
		t.Fatalf("after the token changed: %v", err)
	}
}

func TestOrcaClientFailsClosed(t *testing.T) {
	ctx := context.Background()
	t.Run("not running", func(t *testing.T) {
		f := newFakeOrca(t)
		f.stop()
		if _, err := f.client().Status(ctx); !errors.Is(err, errOrcaNotRunning) {
			t.Fatalf("stopped Orca: %v", err)
		}
		missing := newOrcaClient(func() string { return "/nonexistent/orca-runtime.json" })
		if _, err := missing.Status(ctx); !errors.Is(err, errOrcaNotRunning) {
			t.Fatalf("no metadata: %v", err)
		}
	})
	t.Run("old protocol", func(t *testing.T) {
		f := newFakeOrca(t)
		f.protocol = 2
		if _, _, err := f.client().Bots(ctx); !errors.Is(err, errOrcaIncompatible) {
			t.Fatalf("protocol 2: %v", err)
		}
		if f.calls["bots.list"] != 0 {
			t.Fatal("bots.list was sent to an incompatible runtime")
		}
	})
	t.Run("too new for this client", func(t *testing.T) {
		f := newFakeOrca(t)
		f.minClient = 4
		if _, err := f.client().Status(ctx); !errors.Is(err, errOrcaIncompatible) {
			t.Fatalf("min client 4: %v", err)
		}
	})
	t.Run("no such method", func(t *testing.T) {
		f := newFakeOrca(t)
		f.refuse["bots.list"] = "method_not_found"
		if _, _, err := f.client().Bots(ctx); !errors.Is(err, errOrcaIncompatible) {
			t.Fatalf("no bots.list: %v", err)
		}
	})
}
