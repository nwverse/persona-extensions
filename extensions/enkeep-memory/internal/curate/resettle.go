package curate

import (
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/entry"
	"github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/store"
)

// Resettling (the enkeep_resettle tool): settle every entry's status again, as of today, on the user's real active
// days. Every daily pass already settles all live entries (dormant ones too), so after the days a broken model left
// empty are digested again the statuses follow by themselves; this is the one-off check and repair an Agent can run
// and read: it says what would move, and moves it only when asked to.
//
// A real active day is one the daily pass really read with the model. A day the ledger proves was not (it has daily
// runs and none of them called the model) and no entry has evidence on is not counted: before 0.2.0-beta.21 such
// days were recorded as active and put entries to sleep early. A day without any run in the ledger is kept, since
// nothing proves it empty.

// ResettleReport is what a resettle found (dry run) or did.
type ResettleReport struct {
	AsOf         string
	Applied      bool
	ActiveDays   int      // active days before
	DroppedDays  []string // active days that were not real (removed when applied)
	Before       map[entry.Status]int
	After        map[entry.Status]int
	StableBefore map[entry.Facet]int // live stable entries per facet
	StableAfter  map[entry.Facet]int
	Moves        map[string]int // "dormant→candidate": how many entries
}

// Changed is how many entries changed status.
func (r ResettleReport) Changed() int {
	n := 0
	for _, v := range r.Moves {
		n += v
	}
	return n
}

// Resettle settles every entry again as of today on the real active days; apply=false changes nothing, not even the
// event log. It is idempotent: a second call finds nothing to move. It takes the store's lock.
func (c *Curator) Resettle(today string, apply bool) (ResettleReport, error) {
	return c.ResettleWithin(today, apply, 0)
}

// ResettleWithin is Resettle waiting at most wait for the store's lock (0: as long as it takes); ErrBusy when a run
// holds it longer.
func (c *Curator) ResettleWithin(today string, apply bool, wait time.Duration) (ResettleReport, error) {
	if wait <= 0 {
		c.St.Lock()
	} else if !c.St.LockWithin(wait) {
		return ResettleReport{}, ErrBusy
	}
	defer c.St.Unlock()
	r := ResettleReport{AsOf: today, Applied: apply, ActiveDays: len(c.St.S.ActiveDays)}
	r.Before, r.StableBefore = statusCounts(c.St.S)
	before := map[string]entry.Status{}
	for id, e := range c.St.S.Entries {
		before[id] = e.Status
	}
	r.DroppedDays = emptyActiveDays(c.St.S)

	work := c
	if !apply {
		clone, err := cloneState(c.St.S)
		if err != nil {
			return r, err
		}
		cc := *c
		cc.St = &store.Store{Dir: c.St.Dir, S: clone, Now: c.St.Now, NoEvents: true}
		work = &cc
	}
	s := work.St.S
	s.ActiveDays = slices.DeleteFunc(s.ActiveDays, func(d string) bool { return slices.Contains(r.DroppedDays, d) })
	run := &store.Run{ID: "resettle-" + today, Counts: map[string]int{}}
	work.settleAll(today, run)

	r.After, r.StableAfter = statusCounts(s)
	r.Moves = map[string]int{}
	for id, e := range s.Entries {
		if from := before[id]; from != e.Status {
			r.Moves[fmt.Sprintf("%s→%s", from, e.Status)]++
		}
	}
	if apply && (r.Changed() > 0 || len(r.DroppedDays) > 0) {
		if err := c.St.Save(); err != nil {
			return r, err
		}
	}
	return r, nil
}

// statusCounts counts all entries by status (as the state reading does) and the live stable ones by facet.
func statusCounts(s *store.State) (map[entry.Status]int, map[entry.Facet]int) {
	st, fa := map[entry.Status]int{}, map[entry.Facet]int{}
	for _, e := range s.Entries {
		st[e.Status]++
		if e.Status == entry.Stable && e.Live() {
			fa[e.Facet]++
		}
	}
	return st, fa
}

// emptyActiveDays are the active days the ledger proves were not read with the model and no entry rests on.
func emptyActiveDays(s *store.State) []string {
	runs, called := map[string]bool{}, map[string]bool{}
	for _, r := range s.Runs {
		if r.Kind != "daily" || r.DryRun {
			continue
		}
		runs[r.Period] = true
		if r.Counts["model_calls"]+r.Counts["digested"]+r.Counts["digest_unchanged"] > 0 {
			called[r.Period] = true
		}
	}
	seen := map[string]bool{}
	for _, e := range s.Entries {
		for _, d := range e.ActiveDays {
			seen[d] = true
		}
		for _, ev := range e.Evidence {
			seen[ev.Day] = true
		}
	}
	var out []string
	for _, d := range s.ActiveDays {
		if runs[d] && !called[d] && !seen[d] {
			out = append(out, d)
		}
	}
	return out
}

func cloneState(s *store.State) (*store.State, error) {
	b, err := json.Marshal(s)
	if err != nil {
		return nil, err
	}
	out := &store.State{}
	if err := json.Unmarshal(b, out); err != nil {
		return nil, err
	}
	return out, nil
}
