package hubapi

import (
	"context"
	"encoding/json"
	"net/url"
	"strconv"
	"time"
)

// Session history (nwverse/persona#11529): GET /api/v1/sessions?include_inactive=1&since=… lists the sessions active
// in a time window, archived, finished and offline ones too, not only the recent ones every runtime keeps in its live
// list. A hub before it has no "history" in its answer (or turns the query down): the caller then reads the live list
// only, as before.

// HistoryHub is a hub that can list session history. HTTP is one; a hub without it (offline, the tests' None) is not.
type HistoryHub interface {
	History(ctx context.Context, since, until time.Time) (History, error)
}

// History is a window's sessions and how fully the hub could list them.
type History struct {
	// Offered: the hub answered with history. False: an older hub (no "history" in the answer, or the query turned
	// down); Sessions is then empty and the caller reads the live list only.
	Offered bool
	// Why is why history is not offered, when it is not ("" when it is).
	Why      string
	Sessions []Session // the window's rows, deduplicated, health-space and subagent rows and automated runs left out
	Skipped  int       // rows left out as automated runs (origin "automation", or no message of the user's)
	Complete bool      // every source answered and vouched for the whole window
	Gaps     []HistoryGap
}

// HistoryGap is a source that did not list the window in full: not honored (reason unsupported, failed,
// node_unsupported, offline) or not complete.
type HistoryGap struct {
	Machine string `json:"machine,omitempty"`
	Source  string `json:"source,omitempty"`  // claude | codex | runtime
	Runtime string `json:"runtime,omitempty"` // the runtime's id, for a runtime source
	Honored bool   `json:"honored"`
	Reason  string `json:"reason,omitempty"`
}

// historyRow is a row of a history page: the session as the live list writes it, and the history fields.
type historyRow struct {
	hubSession
	Archived     bool      `json:"archived"`
	Source       string    `json:"history_source"`
	Runtime      string    `json:"history_runtime"`
	ActiveAt     time.Time `json:"active_at"`
	Origin       string    `json:"origin"`
	UserMessages *int      `json:"user_messages"`
}

// historyPageLimit is the page size asked for: the hub's largest.
const historyPageLimit = 500

// History lists the sessions active in [since, until) (until zero: now), every page, newest first as the hub gives
// them. A session's time is its last activity (active_at, else updated_at, else started_at), so a window from a day's
// start to now has every session active on that day or after. Rows of a scheduled run (origin "automation", or
// user_messages 0) are left out; a row that does not say (the node's own Claude Code and Codex sessions) is kept.
// An error is only a failure to reach the hub; an older hub is History{Offered: false}.
func (h *HTTP) History(ctx context.Context, since, until time.Time) (History, error) {
	q := url.Values{"include_inactive": {"1"}, "since": {since.UTC().Format(time.RFC3339)},
		"limit": {strconv.Itoa(historyPageLimit)}}
	if !until.IsZero() {
		q.Set("until", until.UTC().Format(time.RFC3339))
	}
	out := History{Complete: true}
	seen := map[string]bool{}
	gapSeen := map[HistoryGap]bool{}
	for page := 0; page < 1000; page++ {
		var r struct {
			Sessions []historyRow    `json:"sessions"`
			History  json.RawMessage `json:"history"`
		}
		if err := h.get(ctx, "sessions", "/api/v1/sessions", q, &r); err != nil {
			if ctx.Err() != nil {
				return History{}, ctx.Err()
			}
			if page == 0 {
				// An older hub reads since as the indexed list's checkpoint and may turn an RFC 3339 time down.
				return History{Why: err.Error()}, nil
			}
			return out, err
		}
		if len(r.History) == 0 || string(r.History) == "null" {
			if page == 0 {
				return History{Why: "the hub's answer has no history (a hub before session history)"}, nil
			}
			break
		}
		var meta struct {
			Complete   bool   `json:"complete"`
			NextCursor string `json:"next_cursor"`
			Sources    []struct {
				HistoryGap
				Complete bool `json:"complete"`
			} `json:"sources"`
		}
		if err := json.Unmarshal(r.History, &meta); err != nil {
			return out, err
		}
		out.Offered = true
		out.Complete = out.Complete && meta.Complete
		for _, src := range meta.Sources {
			if src.Honored && src.Complete {
				continue
			}
			out.Complete = false
			if g := src.HistoryGap; !gapSeen[g] { // every page names the sources again
				gapSeen[g] = true
				out.Gaps = append(out.Gaps, g)
			}
		}
		for _, row := range r.Sessions {
			key := row.Machine + "/" + row.ID
			if IsHealth(row.Assistant, row.Space) || row.Subagent || seen[key] {
				continue
			}
			seen[key] = true
			if row.Origin == "automation" || (row.UserMessages != nil && *row.UserMessages == 0) {
				out.Skipped++
				continue
			}
			se := row.session()
			if !row.ActiveAt.IsZero() {
				se.Updated = row.ActiveAt // archiving may move updated_at; the last activity is what dates the session
			}
			se.Archived, se.HistorySource, se.HistoryRuntime = row.Archived, row.Source, row.Runtime
			out.Sessions = append(out.Sessions, se)
		}
		if meta.NextCursor == "" {
			break
		}
		q.Set("cursor", meta.NextCursor)
	}
	return out, nil
}
