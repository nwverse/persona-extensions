package hubapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"
)

// historyHub answers GET /api/v1/sessions as a hub with session history does (nwverse/persona#11529), in pages; old:
// as a hub before it (the live list, no "history"); refuse: turning the query down as a malformed indexed list query.
type historyHub struct {
	mu      sync.Mutex
	old     bool
	refuse  bool
	queries []url.Values
}

func (f *historyHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.queries = append(f.queries, r.URL.Query())
	f.mu.Unlock()
	if r.URL.Path != "/api/v1/sessions" {
		w.WriteHeader(404)
		return
	}
	if f.refuse {
		w.WriteHeader(400)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": "since: not a revision", "code": "invalid_params"})
		return
	}
	if f.old {
		_ = json.NewEncoder(w).Encode(map[string]any{"sessions": []map[string]any{{"machine_id": "m1", "session_id": "live", "updated_at": "2026-11-03T10:00:00Z"}}})
		return
	}
	um0 := 0
	sources := []map[string]any{
		{"machine": "m1", "source": "claude", "honored": true, "complete": true},
		{"machine": "m1", "source": "runtime", "runtime": "rt-a", "honored": false, "complete": false, "reason": "unsupported"},
		{"machine": "m2", "source": "", "honored": false, "complete": false, "reason": "offline"},
	}
	if r.URL.Query().Get("cursor") == "" {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"sessions": []map[string]any{
				// a runtime's archived session: archiving moved updated_at, active_at is its last activity
				{"machine_id": "m1", "session_id": "arch", "title": "old work", "updated_at": "2026-11-09T00:00:00Z", "archived": true,
					"history_source": "runtime", "history_runtime": "rt-b", "active_at": "2026-11-04T10:00:00Z", "origin": "user", "user_messages": 5},
				// a scheduled run, said two ways
				{"machine_id": "m1", "session_id": "cron", "updated_at": "2026-11-04T09:00:00Z", "history_source": "runtime", "origin": "automation"},
				{"machine_id": "m1", "session_id": "quiet", "updated_at": "2026-11-04T08:00:00Z", "history_source": "runtime", "user_messages": um0},
				{"machine_id": "m1", "session_id": "health", "updated_at": "2026-11-04T07:00:00Z", "assistant": true, "space": "health", "history_source": "claude"},
				{"machine_id": "m1", "session_id": "child", "updated_at": "2026-11-04T06:00:00Z", "is_subagent": true, "history_source": "claude"},
			},
			"history": map[string]any{"since": "2026-11-03T00:00:00Z", "until": "2026-11-19T00:00:00Z", "complete": false,
				"next_cursor": "c-2", "sources": sources},
		})
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{
		"sessions": []map[string]any{
			// the node's own Claude Code session: no origin, no user_messages: kept, not taken for the user's own
			{"machine_id": "m1", "session_id": "cc", "title": "a transcript", "updated_at": "2026-11-03T12:00:00Z",
				"started_at": "2026-11-03T09:00:00Z", "history_source": "claude"},
			{"machine_id": "m1", "session_id": "arch", "updated_at": "2026-11-09T00:00:00Z", "history_source": "runtime"},
		},
		"history": map[string]any{"since": "2026-11-03T00:00:00Z", "until": "2026-11-19T00:00:00Z", "complete": false,
			"next_cursor": "", "sources": sources},
	})
}

// Every page is read, by its cursor; scheduled runs and subagent rows are left out, a health-space row kept with its
// space (0.2.0-beta.40); an archived row is
// dated by its last activity; the sources not listed in full are the gaps, once each.
func TestHTTPHistoryPagesAndGaps(t *testing.T) {
	f := &historyHub{}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	h := &HTTP{Base: srv.URL, Token: "tok-placeholder"}
	since := time.Date(2026, 11, 3, 0, 0, 0, 0, time.UTC)
	got, err := h.History(context.Background(), since, time.Time{})
	if err != nil || !got.Offered || got.Complete {
		t.Fatalf("history %+v %v", got, err)
	}
	if len(f.queries) != 2 {
		t.Fatalf("pages asked: %v", f.queries)
	}
	q := f.queries[0]
	if q.Get("include_inactive") != "1" || q.Get("since") != "2026-11-03T00:00:00Z" || q.Get("limit") != "500" || q.Has("until") || q.Has("cursor") {
		t.Fatalf("first query %v", q)
	}
	if f.queries[1].Get("cursor") != "c-2" || f.queries[1].Get("include_inactive") != "1" {
		t.Fatalf("second query %v", f.queries[1])
	}
	if len(got.Sessions) != 3 || got.Skipped != 2 || got.Sessions[1].ID != "health" || got.Sessions[1].Space != "health" {
		t.Fatalf("sessions %+v skipped %d", got.Sessions, got.Skipped)
	}
	arch, cc := got.Sessions[0], got.Sessions[2]
	if arch.ID != "arch" || !arch.Archived || arch.HistorySource != "runtime" || arch.HistoryRuntime != "rt-b" ||
		!arch.Updated.Equal(time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC)) {
		t.Fatalf("archived row %+v", arch)
	}
	if cc.ID != "cc" || cc.HistorySource != "claude" || cc.ByPersona || !cc.Started.Equal(time.Date(2026, 11, 3, 9, 0, 0, 0, time.UTC)) {
		t.Fatalf("node row %+v", cc)
	}
	if len(got.Gaps) != 2 || got.Gaps[0] != (HistoryGap{Machine: "m1", Source: "runtime", Runtime: "rt-a", Reason: "unsupported"}) ||
		got.Gaps[1] != (HistoryGap{Machine: "m2", Reason: "offline"}) {
		t.Fatalf("gaps %+v", got.Gaps)
	}
	// until goes out when given.
	if _, err := h.History(context.Background(), since, since.AddDate(0, 0, 1)); err != nil || f.queries[2].Get("until") != "2026-11-04T00:00:00Z" {
		t.Fatalf("until: %v %v", f.queries[2], err)
	}
}

// A hub before session history: no "history" in its answer, or the query turned down. Not offered, not an error: the
// caller reads the live list only.
func TestHTTPHistoryOlderHub(t *testing.T) {
	for _, f := range []*historyHub{{old: true}, {refuse: true}} {
		srv := httptest.NewServer(f)
		h := &HTTP{Base: srv.URL, Token: "tok-placeholder"}
		got, err := h.History(context.Background(), time.Date(2026, 11, 3, 0, 0, 0, 0, time.UTC), time.Time{})
		srv.Close()
		if err != nil || got.Offered || got.Why == "" || len(got.Sessions) != 0 {
			t.Fatalf("old %v refuse %v: %+v %v", f.old, f.refuse, got, err)
		}
	}
}
