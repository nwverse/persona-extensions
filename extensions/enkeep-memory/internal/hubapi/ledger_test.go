package hubapi

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/nwverse/persona/sdk/go/ext"
)

// assistant.entries.list (persona #13106): pages by after until has_more is false, never sends the legacy cursor or
// created_at, maps a missing method to I1 and permission_denied to the scope it names; an empty page (no personal
// entry visible from a hub container) is an empty list, not an error. Synthetic entries only.
func TestLedgerRPC(t *testing.T) {
	h := &HTTP{}
	var w *WaitingHub
	if _, err := h.Ledger(context.Background(), LedgerQuery{By: "user"}); !errors.As(err, &w) || w.Change != "I1" {
		t.Fatalf("without an RPC: %v", err)
	}
	var calls []map[string]any
	pages := []string{
		`{"entries":[{"id":"l1","type":"rule","text":"占位规矩","quote":"先跑门禁再合并","by":"user","trust":"user_verbatim","status":"active",` +
			`"source":{"machine":"computer-example","session":"s1"},"relayed_via":[],"at":"2026-11-20T10:00:00Z","expires_at":null,"supersedes":"","tag":[],` +
			`"scope":{"executor":"any","personal":true}}],"after":"OPAQUE-1","has_more":true}`,
		`{"entries":[{"id":"l2","type":"rule","text":"占位规矩二","quote":"汇报只写结论","by":"user","trust":"user_relayed","status":"active",` +
			`"relayed_via":["s2"],"at":"2026-11-21T10:00:00Z","expires_at":null,"supersedes":"l1","tag":["probe"]}],"after":"OPAQUE-2","has_more":false}`,
	}
	h.Call = func(_ context.Context, method string, params any, out any) error {
		if method != LedgerMethod {
			t.Fatalf("method %s", method)
		}
		p := params.(map[string]any)
		calls = append(calls, p)
		if _, ok := p["cursor"]; ok {
			t.Fatal("legacy cursor sent")
		}
		if _, ok := p["created_at"]; ok {
			t.Fatal("legacy created_at sent")
		}
		return json.Unmarshal([]byte(pages[len(calls)-1]), out)
	}
	es, err := h.Ledger(context.Background(), LedgerQuery{By: "user"})
	if err != nil || len(calls) != 2 || calls[1]["after"] != "OPAQUE-1" || calls[0]["by"] != "user" || calls[0]["limit"] != ledgerPage {
		t.Fatalf("pages: %v %v", calls, err)
	}
	// l2 is a probe: left out, and a probe supersedes nothing, so l1 holds.
	if len(es) != 1 || es[0].ID != "l1" || es[0].At.IsZero() || es[0].Source.Session != "s1" || !es[0].Scope.Personal {
		t.Fatalf("entries: %+v", es)
	}

	h.Call = func(_ context.Context, _ string, _ any, out any) error {
		return json.Unmarshal([]byte(`{"entries":[],"after":"","has_more":false}`), out)
	}
	if es, err := h.Ledger(context.Background(), LedgerQuery{By: "user"}); err != nil || len(es) != 0 {
		t.Fatalf("no personal entry visible: %v %v", es, err)
	}
	h.Call = func(context.Context, string, any, any) error {
		return &ext.Error{Message: "no", Code: "permission_denied", Data: json.RawMessage(`{"scope":"assistant.entries.read"}`)}
	}
	var d *LedgerDenied
	if _, err := h.Ledger(context.Background(), LedgerQuery{}); !errors.As(err, &d) || d.Scope != LedgerScope {
		t.Fatalf("denied: %v", err)
	}
	h.Call = func(context.Context, string, any, any) error { return &ext.Error{Message: "no", Code: "unknown_method"} }
	if _, err := h.Ledger(context.Background(), LedgerQuery{}); !errors.Is(err, ErrWaitingHub) {
		t.Fatalf("unknown method: %v", err)
	}
}
