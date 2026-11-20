package sources

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/hubapi"
	"github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/store"
)

// histHub is the placeholder hub with session history: its live list is empty, its history has what history holds.
// itemsFail makes the next items reads fail (a history row the hub forgot it gave), counted down.
type histHub struct {
	*hub
	history   hubapi.History
	asked     int
	itemsFail int
}

func (h *histHub) History(context.Context, time.Time, time.Time) (hubapi.History, error) {
	h.asked++
	return h.history, nil
}

func (h *histHub) Items(ctx context.Context, machine, session string, q hubapi.ItemsQuery) ([]hubapi.Item, error) {
	if h.itemsFail > 0 {
		h.itemsFail--
		return nil, errors.New("403 not readable")
	}
	return h.hub.Items(ctx, machine, session, q)
}

// The history's sessions are read with the live list's and the seen ones (each once); how fully the history listed
// the day is kept per day, and a day listed in full has no entry.
func TestHistorySessionsAreRead(t *testing.T) {
	gap := hubapi.HistoryGap{Machine: "m1", Source: "runtime", Runtime: "rt-a", Reason: "unsupported"}
	h := &histHub{hub: &hub{items: sessionItems(), sessions: []hubapi.Session{}}, history: hubapi.History{Offered: true,
		Sessions: []hubapi.Session{{Machine: "m1", ID: "h1", Title: "archived work", Started: at(3, 8), Updated: at(3, 10), Archived: true, HistorySource: "runtime"}},
		Gaps:     []hubapi.HistoryGap{gap}}}
	s := With(&kb{}, h, time.UTC)
	st := &store.State{}
	if got := agentRefs(t, s, st, "2026-11-03"); !got["agents/m1/h1"] || len(got) != 1 {
		t.Fatalf("listed: %v", got)
	}
	if text := strings.Join(dayLines(t, s, "agents/m1/h1", "2026-11-03"), "\n"); !strings.Contains(text, "我: build project-x") {
		t.Fatalf("day:\n%s", text)
	}
	s.End(st, "2026-11-03", 1)
	r := st.SessionHistory["2026-11-03"]
	if r == nil || !r.Offered || len(r.Gaps) != 1 || r.Gaps[0] != store.HistoryGap(gap) {
		t.Fatalf("history read: %+v", r)
	}
	if k := st.SeenSessions["m1/h1"]; k == nil || k.Last != "2026-11-03" || k.First != "2026-11-03" || k.Title != "archived work" {
		t.Fatalf("seen: %+v", k)
	}
	// Listed in full on another pass: the entry goes.
	h.history.Gaps, h.history.Complete = nil, true
	_ = agentRefs(t, s, st, "2026-11-03")
	s.End(st, "2026-11-03", 1)
	if _, ok := st.SessionHistory["2026-11-03"]; ok {
		t.Fatalf("a day listed in full keeps an entry: %+v", st.SessionHistory)
	}
}

// A hub without session history (an older one, or a client without it): the live list and the seen sessions are read
// as before, and the day records that the history was not offered.
func TestNoHistoryFallsBack(t *testing.T) {
	h := &histHub{hub: &hub{items: sessionItems()}, history: hubapi.History{Why: "no history in the answer"}}
	s := With(&kb{}, h, time.UTC)
	st := &store.State{}
	if got := agentRefs(t, s, st, "2026-11-03"); !got["agents/m1/s1"] || len(got) != 1 {
		t.Fatalf("listed: %v", got)
	}
	s.End(st, "2026-11-03", 1)
	if r := st.SessionHistory["2026-11-03"]; r == nil || r.Offered || r.Why != "no history in the answer" {
		t.Fatalf("history read: %+v", r)
	}
	plain := With(&kb{}, &hub{items: sessionItems()}, time.UTC) // a hub client with no History at all
	st2 := &store.State{}
	if got := agentRefs(t, plain, st2, "2026-11-03"); !got["agents/m1/s1"] {
		t.Fatalf("plain: %v", got)
	}
	plain.End(st2, "2026-11-03", 1)
	if r := st2.SessionHistory["2026-11-03"]; r == nil || r.Offered {
		t.Fatalf("plain history read: %+v", r)
	}
}

// A history row whose items the hub no longer lets the extension read (it keeps that for a day, in memory) is listed
// again and read once more.
func TestHistoryRowListedAgainWhenUnreadable(t *testing.T) {
	h := &histHub{hub: &hub{items: sessionItems(), sessions: []hubapi.Session{}}, history: hubapi.History{Offered: true,
		Sessions: []hubapi.Session{{Machine: "m1", ID: "h1", Started: at(3, 8), Updated: at(3, 10)}}}, itemsFail: 1}
	s := With(&kb{}, h, time.UTC)
	st := &store.State{}
	_ = agentRefs(t, s, st, "2026-11-03")
	if lines := dayLines(t, s, "agents/m1/h1", "2026-11-03"); len(lines) == 0 || h.asked != 2 {
		t.Fatalf("lines %v, history asked %d times", lines, h.asked)
	}
}

// A session is not asked for on a day before its first item, once a pass read it from the start.
func TestNotBeforeTheFirstItem(t *testing.T) {
	h := &histHub{hub: &hub{items: sessionItems(), sessions: []hubapi.Session{}}, history: hubapi.History{Offered: true}}
	s := With(&kb{}, h, time.UTC)
	st := &store.State{SeenSessions: map[string]*store.SeenSession{"m2/x": {Last: "2026-11-05", First: "2026-11-04"}}}
	if got := agentRefs(t, s, st, "2026-11-03"); got["agents/m2/x"] {
		t.Fatalf("asked before its first item: %v", got)
	}
	if got := agentRefs(t, s, st, "2026-11-04"); !got["agents/m2/x"] {
		t.Fatalf("not asked on its first day: %v", got)
	}
}
