package curate

import (
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/entry"
	"github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/export"
	"github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/schema"
	"github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/store"
)

// Reruns asked for by the assistant or an Agent (the enkeep_rerun tool): a one-off "digest these days again with the
// current daily prompt", queued in the state instead of changing a setting. The days run as rerun_range's do: one at
// a time, completed local days only, once per prompt, waiting while the model or the assistant's computer is away.

// RerunMostDays is how many days one request may queue (as rerun_range).
const RerunMostDays = 31

// RerunRequest is what a request queued and what it left out.
type RerunRequest struct {
	Queued  []string // days newly queued
	Already []string // days already digested with the current prompt, or already queued
	Force   bool     // queued with force: read again even if already digested with the current prompt
}

// QueueRerun queues the days from..to (YYYY-MM-DD, inclusive) to be digested again with the current daily prompt.
// Only days before today; at most RerunMostDays; a day already digested with the current prompt or already queued is
// not queued again. With force, a day is queued although it already carries the current prompt's marker (only a day
// already force-queued is left out); its units digested before are taken from the ledger, so only what was not read
// calls the model. It takes the store's lock.
func (c *Curator) QueueRerun(from, to, today string, force bool) (RerunRequest, error) {
	return c.QueueRerunWithin(from, to, today, force, 0)
}

// ErrBusy: a run holds the store's lock longer than the caller waits.
var ErrBusy = errors.New("a curation pass is running")

// ValidateRerunDays checks from..to (YYYY-MM-DD, inclusive, before today, at most RerunMostDays) and returns the
// expanded day list. Pure; takes no lock.
func ValidateRerunDays(from, to, today string) ([]string, error) {
	a, err1 := time.Parse("2006-01-02", from)
	b, err2 := time.Parse("2006-01-02", to)
	if err1 != nil || err2 != nil {
		return nil, fmt.Errorf("from and to are days: YYYY-MM-DD")
	}
	if b.Before(a) {
		return nil, fmt.Errorf("to (%s) is before from (%s)", to, from)
	}
	if to >= today {
		return nil, fmt.Errorf("only days that are over can be read again: to must be before %s", today)
	}
	var days []string
	for d := a; !d.After(b); d = d.AddDate(0, 0, 1) {
		days = append(days, d.Format("2006-01-02"))
	}
	if len(days) > RerunMostDays {
		return nil, fmt.Errorf("at most %d days at a time (asked for %d)", RerunMostDays, len(days))
	}
	return days, nil
}

// QueueRerunLocked merges from..to into State.RerunQueue / State.ForceReruns without saving. Caller holds c.St.Lock().
func (c *Curator) QueueRerunLocked(from, to, today string, force bool) (RerunRequest, error) {
	days, err := ValidateRerunDays(from, to, today)
	if err != nil {
		return RerunRequest{}, err
	}
	r := RerunRequest{Force: force}
	if len(c.rerunPlanWaiting(c.St.S)) == 0 && len(c.St.S.ForceReruns) == 0 {
		// A new batch: its gain is counted from the entries usable now.
		c.St.S.RerunBase = &store.RerunBase{At: c.now().UTC().Format(time.RFC3339), Usable: usableIDs(c.St.S)}
	}
	for _, d := range days {
		if force {
			if slices.Contains(c.St.S.ForceReruns, d) {
				r.Already = append(r.Already, d)
				continue
			}
			c.St.S.ForceReruns = append(c.St.S.ForceReruns, d)
			r.Queued = append(r.Queued, d)
			continue
		}
		if slices.Contains(c.St.S.Reruns, d+"@"+schema.DailyPrompt) || slices.Contains(c.St.S.RerunQueue, d) {
			r.Already = append(r.Already, d)
			continue
		}
		c.St.S.RerunQueue = append(c.St.S.RerunQueue, d)
		r.Queued = append(r.Queued, d)
	}
	sort.Strings(c.St.S.RerunQueue)
	sort.Strings(c.St.S.ForceReruns)
	return r, nil
}

// QueueRerunWithin is QueueRerun waiting at most wait for the store's lock (0: as long as it takes); ErrBusy when a
// run holds it longer.
func (c *Curator) QueueRerunWithin(from, to, today string, force bool, wait time.Duration) (RerunRequest, error) {
	if _, err := ValidateRerunDays(from, to, today); err != nil {
		return RerunRequest{}, err
	}
	if wait <= 0 {
		c.St.Lock()
	} else if !c.St.LockWithin(wait) {
		return RerunRequest{}, ErrBusy
	}
	defer c.St.Unlock()
	r, err := c.QueueRerunLocked(from, to, today, force)
	if err != nil {
		return RerunRequest{}, err
	}
	if len(r.Queued) > 0 {
		if err := c.St.Save(); err != nil {
			return RerunRequest{}, err
		}
		c.Publish()
	}
	return r, nil
}

// RerunPlan is every day to digest again with the current prompt: rerun_range's and the queue's, in order, once each.
// The caller holds the lock or reads a state no one writes.
func (c *Curator) RerunPlan() []string { return c.rerunPlan(c.St.S) }

func (c *Curator) rerunPlan(st *store.State) []string {
	seen := map[string]bool{}
	var out []string
	for _, d := range append(c.Cfg.RerunDays(), st.RerunQueue...) {
		if !seen[d] {
			seen[d] = true
			out = append(out, d)
		}
	}
	sort.Strings(out)
	return out
}

// rerunPlanWaiting are the planned days not yet digested with the current prompt.
func (c *Curator) rerunPlanWaiting(st *store.State) []string {
	var out []string
	for _, d := range c.rerunPlan(st) {
		if !slices.Contains(st.Reruns, d+"@"+schema.DailyPrompt) {
			out = append(out, d)
		}
	}
	return out
}

// usableIDs are the entries the exits may use now (export.Usable), sorted.
func usableIDs(st *store.State) []string {
	var out []string
	for id, e := range st.Entries {
		if export.Usable(st, e) {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}

// rerunGain counts the entries usable now that were not when the batch was queued and rest on a day the reruns
// read (at least one piece of evidence dated on a done day).
func rerunGain(st *store.State, done []string) int {
	if st.RerunBase == nil || len(done) == 0 {
		return 0
	}
	n := 0
	for id, e := range st.Entries {
		if !export.Usable(st, e) {
			continue
		}
		if _, was := slices.BinarySearch(st.RerunBase.Usable, id); was {
			continue
		}
		if onDays(st, e, done, 0) {
			n++
		}
	}
	return n
}

// onDays: e or one of its members (to depth 2) has evidence dated on one of days (sorted).
func onDays(st *store.State, e *entry.Entry, days []string, depth int) bool {
	for _, x := range e.Evidence {
		if _, ok := slices.BinarySearch(days, x.Day); ok {
			return true
		}
	}
	if depth >= 2 {
		return false
	}
	for _, id := range e.Members {
		if m := st.Entries[id]; m != nil && onDays(st, m, days, depth+1) {
			return true
		}
	}
	return false
}

// RerunProgress is where the asked-for reruns stand.
type RerunProgress struct {
	Gain    int      // entries usable now, not when the batch was queued, resting on a day the reruns read
	Prompt  string   // the daily prompt the days are digested with
	Done    []string // planned days digested with it
	Waiting []string // planned days still to run
	Forced  []string // days queued with force that still wait (they run before the others)
	Paused  string   // why the passes wait, and until when ("" when they do not)
	// History says which days' agent sessions the hub's session history could not list in full: a line for the days
	// of a hub without it, and one per day with a source that did not answer or vouch for the window.
	History []string
	At      time.Time // when the state read was taken (the pages' snapshot); zero when read from the store itself
	Running *Progress // the pass in progress, if any
}

// historyMost is how many days with gaps the status names one by one (the newest).
const historyMost = 10

// historyLines is RerunProgress.History of st.
func historyLines(st *store.State) []string {
	var none, gaps []string
	for _, day := range sortedKeys(st.SessionHistory) {
		h := st.SessionHistory[day]
		switch {
		case h == nil:
		case !h.Offered:
			none = append(none, day)
		case len(h.Gaps) > 0:
			gaps = append(gaps, day)
		}
	}
	var out []string
	if len(none) > 0 {
		why := st.SessionHistory[none[len(none)-1]].Why
		out = append(out, fmt.Sprintf("%d day(s) read from the hub's live session list only, the hub offering no session history (%s): %s",
			len(none), why, daySpan(none)))
	}
	if len(gaps) > historyMost {
		out = append(out, fmt.Sprintf("%d more day(s) with sources not listed in full, before %s", len(gaps)-historyMost, gaps[len(gaps)-historyMost]))
		gaps = gaps[len(gaps)-historyMost:]
	}
	for _, day := range gaps {
		var parts []string
		for _, g := range st.SessionHistory[day].Gaps {
			src := g.Source
			if g.Runtime != "" {
				src += " " + g.Runtime
			}
			what := "not complete"
			if !g.Honored {
				what = "not honored"
			}
			if g.Reason != "" {
				what += " (" + g.Reason + ")"
			}
			parts = append(parts, fmt.Sprintf("machine %s, source %s: %s", cmpOr(g.Machine, "?"), cmpOr(src, "?"), what))
		}
		out = append(out, day+" sessions not listed in full: "+strings.Join(parts, "; ")+" (read as far as the live list and the sessions seen before have them)")
	}
	return out
}

// daySpan is days as a short text: every day when few, else the first and last with the count.
func daySpan(days []string) string {
	if len(days) <= 6 {
		return strings.Join(days, ", ")
	}
	return fmt.Sprintf("%s … %s (%d days)", days[0], days[len(days)-1], len(days))
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// RerunStatus is the progress of rerun_range's and the queue's days, read from the pages' snapshot (Live): a daily
// pass holds the store's lock for its whole run, tens of minutes, and the hub gives up on a tool call long before.
// At is when the state read was taken; the zero time when it is the store's own (no snapshot, under its lock).
func (c *Curator) RerunStatus() RerunProgress {
	st, at, done := c.Read()
	defer done()
	p := RerunProgress{Prompt: schema.DailyPrompt, Forced: slices.Clone(st.ForceReruns), At: at}
	for _, d := range c.rerunPlan(st) {
		if slices.Contains(st.Reruns, d+"@"+schema.DailyPrompt) {
			p.Done = append(p.Done, d)
		} else {
			p.Waiting = append(p.Waiting, d)
		}
	}
	p.Gain = rerunGain(st, p.Done)
	p.History = historyLines(st)
	if mp := st.ModelPause; mp != nil {
		what := "the model is unavailable"
		switch mp.What {
		case "assistant":
			what = "the assistant's computer is not connected"
		case WhatHeld:
			what = "the hub holds the model's connection (model_paused: busy or quota; waiting, not failing)"
		}
		p.Paused = fmt.Sprintf("%s (%s); %s is tried again at %s", what, mp.Reason, mp.Day, mp.NextTry)
	}
	if prog, ok := c.Live.Progress(); ok {
		p.Running = &prog
	}
	return p
}

// Read is the state to show or search without waiting for a run: the last snapshot (Live) with its time, never the
// store's own while a run holds its lock. Only a curator without a snapshot (one built by hand in a test, a trial)
// reads the store, under its lock; done releases what Read took.
func (c *Curator) Read() (st *store.State, at time.Time, done func()) {
	if s := c.Live.Snapshot(); s != nil && s.State != nil {
		return s.State, s.At, func() {}
	}
	c.St.Lock()
	return c.St.S, time.Time{}, c.St.Unlock
}
