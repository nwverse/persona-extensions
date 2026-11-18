package curate

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/entry"
	"github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/store"
)

// resettleCurator: 50 active days; the user really had data on the first 10, the last 40 were recorded as active by
// daily runs that never reached the model (the bug before 0.2.0-beta.21). An entry seen on day 10 was put to sleep.
func resettleCurator(t *testing.T) (*Curator, *entry.Entry) {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ds := days(50)
	st.S.ActiveDays = ds
	for i, d := range ds {
		calls := 3
		if i >= 10 {
			calls = 0
		}
		st.S.Runs = append(st.S.Runs, &store.Run{Kind: "daily", Period: d, Finished: "x", Counts: map[string]int{"model_calls": calls}})
	}
	e := &entry.Entry{ID: "e1", Grain: entry.Action, Facet: entry.Judgement, Origin: entry.Behavior, Status: entry.Dormant,
		First: ds[9], Last: ds[9], Evidence: []entry.Evidence{{Ref: "chats/a", Day: ds[9]}}}
	quiet := &entry.Entry{ID: "e2", Grain: entry.Action, Facet: entry.Judgement, Origin: entry.Behavior, Status: entry.Stable,
		First: ds[0], Last: ds[1], Evidence: []entry.Evidence{{Ref: "chats/b", Day: ds[0]}, {Ref: "chats/c", Day: ds[1]}}}
	st.S.Entries = map[string]*entry.Entry{"e1": e, "e2": quiet}
	if err := st.Save(); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 2, 23, 12, 0, 0, 0, time.UTC)
	return &Curator{St: st, Loc: time.UTC, Now: func() time.Time { return now }}, e
}

// A dry run reports the days not counted and the entry that wakes up, and changes nothing: not the state, not the
// file, not the event log. Applying moves it; a second call finds nothing to move.
func TestResettle(t *testing.T) {
	c, e := resettleCurator(t)
	today := days(50)[49]
	before, _ := os.ReadFile(filepath.Join(c.St.Dir, "state.json"))

	r, err := c.Resettle(today, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.DroppedDays) != 40 || r.Moves["dormant→candidate"] != 1 || r.Changed() != 1 || r.Applied {
		t.Fatalf("dry run: %+v", r)
	}
	if r.Before[entry.Dormant] != 1 || r.After[entry.Dormant] != 0 || r.After[entry.Candidate] != 1 {
		t.Fatalf("counts: %+v → %+v", r.Before, r.After)
	}
	if e.Status != entry.Dormant || len(c.St.S.ActiveDays) != 50 {
		t.Fatalf("the dry run changed the state: %s, %d days", e.Status, len(c.St.S.ActiveDays))
	}
	after, _ := os.ReadFile(filepath.Join(c.St.Dir, "state.json"))
	if string(before) != string(after) {
		t.Fatal("the dry run wrote the state")
	}
	if _, err := os.Stat(filepath.Join(c.St.Dir, "events.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("the dry run wrote events: %v", err)
	}

	r, err = c.Resettle(today, true)
	if err != nil || r.Changed() != 1 || e.Status != entry.Candidate || len(c.St.S.ActiveDays) != 10 {
		t.Fatalf("apply: %+v %v, %s, %d days", r, err, e.Status, len(c.St.S.ActiveDays))
	}
	if r.StableBefore[entry.Judgement] != 1 || r.StableAfter[entry.Judgement] != 1 {
		t.Fatalf("stable per facet: %v → %v", r.StableBefore, r.StableAfter)
	}
	r, err = c.Resettle(today, true)
	if err != nil || r.Changed() != 0 || len(r.DroppedDays) != 0 {
		t.Fatalf("again: %+v %v", r, err)
	}
}

// A day without any run in the ledger, or with evidence on it, is kept: nothing proves it empty.
func TestResettleKeepsUnprovenDays(t *testing.T) {
	c, _ := resettleCurator(t)
	ds := days(50)
	c.St.S.Runs = c.St.S.Runs[:45] // no ledger for the last 5 days
	c.St.S.Entries["e3"] = &entry.Entry{ID: "e3", Grain: entry.Action, Facet: entry.World, Origin: entry.Behavior,
		Status: entry.Candidate, First: ds[20], Last: ds[20], Evidence: []entry.Evidence{{Ref: "chats/d", Day: ds[20]}}}
	r, err := c.Resettle(ds[49], false)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.DroppedDays) != 34 { // days 11..45 without day 21
		t.Fatalf("dropped %d: %v", len(r.DroppedDays), r.DroppedDays)
	}
}
