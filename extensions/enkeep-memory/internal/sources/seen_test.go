package sources

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/hubapi"
	"github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/store"
)

// A session a pass found in the hub's list is kept (State.SeenSessions, 0.2.0-beta.34): once its runtime no longer
// lists it (it lists only recent ones), a rerun of a day up to the session's last active day still reads it, under
// its name; a day after that does not ask for it.
func TestSeenSessionsAreReadAfterTheyLeaveTheList(t *testing.T) {
	h := &hub{items: sessionItems()}
	s := With(&kb{}, h, time.UTC)
	st := &store.State{}
	ctx := context.Background()

	s.Begin(st, "2026-11-03")
	_, _ = s.Recent(ctx, day3, time.Time{})
	_ = dayLines(t, s, "agents/m1/s1", "2026-11-03")
	s.End(st, "2026-11-03", 1)
	seen := st.SeenSessions["m1/s1"]
	if seen == nil || seen.Last != "2026-11-04" || seen.Title != "project-x" || len(st.SeenSessions) != 1 {
		t.Fatalf("seen sessions: %+v", st.SeenSessions) // the assistant's and the health space's are not kept
	}

	h.sessions = []hubapi.Session{} // the runtime lists it no more
	s.Begin(st, "2026-11-03")
	es, _ := s.Recent(ctx, day3, time.Time{})
	if !refs(es)["agents/m1/s1"] {
		t.Fatalf("a seen session is not read again: %v", refs(es))
	}
	for _, e := range es {
		if e.Kind == KindAgents && !strings.Contains(e.Name, "project-x") {
			t.Errorf("name: %q", e.Name)
		}
	}
	if text := strings.Join(dayLines(t, s, "agents/m1/s1", "2026-11-03"), "\n"); !strings.Contains(text, "我: build project-x") {
		t.Fatalf("rerun day:\n%s", text)
	}
	s.End(st, "2026-11-03", 1)
	if st.SeenSessions["m1/s1"].Last != "2026-11-04" {
		t.Fatalf("kept: %+v", st.SeenSessions["m1/s1"])
	}

	s.Begin(st, "2026-11-05")
	es, _ = s.Recent(ctx, at(5, 0), time.Time{})
	if refs(es)["agents/m1/s1"] {
		t.Fatal("a session read on a day after its last active one")
	}
}

// agentRefs is the agent sessions a pass of day lists.
func agentRefs(t *testing.T, s *Source, st *store.State, day string) map[string]bool {
	t.Helper()
	s.Begin(st, day)
	since, _ := time.ParseInLocation("2006-01-02", day, time.UTC)
	es, _ := s.Recent(context.Background(), since, time.Time{})
	out := map[string]bool{}
	for ref := range refs(es) {
		if strings.HasPrefix(ref, KindAgents+"/") {
			out[ref] = true
		}
	}
	return out
}

// Sessions read before the record was kept are known from their cursors and the admitted units, up to the last day
// read; any runtime's, not one's in particular.
func TestSeenSessionsFromCursorsAndAdmitted(t *testing.T) {
	h := &hub{items: sessionItems(), sessions: []hubapi.Session{}}
	s := With(&kb{}, h, time.UTC)
	st := &store.State{
		Cursors:  map[string]string{"S3/m2/old": "4|2026-11-02", "S1/x": "1|2026-11-09"},
		Admitted: map[string][]string{"2026-11-01": {"agents/m3/adm", "chats/lark-dm-1"}},
	}
	if got := agentRefs(t, s, st, "2026-11-01"); !got["agents/m2/old"] || !got["agents/m3/adm"] || len(got) != 2 {
		t.Fatalf("11-01: %v", got)
	}
	if got := agentRefs(t, s, st, "2026-11-02"); !got["agents/m2/old"] || got["agents/m3/adm"] || len(got) != 1 {
		t.Fatalf("11-02: %v", got)
	}
	if got := agentRefs(t, s, st, "2026-11-03"); len(got) != 0 {
		t.Fatalf("11-03: %v", got)
	}
}
