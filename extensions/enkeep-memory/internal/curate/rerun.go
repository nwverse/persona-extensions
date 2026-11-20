package curate

import (
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/schema"
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
	a, err1 := time.Parse("2006-01-02", from)
	b, err2 := time.Parse("2006-01-02", to)
	if err1 != nil || err2 != nil {
		return RerunRequest{}, fmt.Errorf("from and to are days: YYYY-MM-DD")
	}
	if b.Before(a) {
		return RerunRequest{}, fmt.Errorf("to (%s) is before from (%s)", to, from)
	}
	if to >= today {
		return RerunRequest{}, fmt.Errorf("only days that are over can be read again: to must be before %s", today)
	}
	var days []string
	for d := a; !d.After(b); d = d.AddDate(0, 0, 1) {
		days = append(days, d.Format("2006-01-02"))
	}
	if len(days) > RerunMostDays {
		return RerunRequest{}, fmt.Errorf("at most %d days at a time (asked for %d)", RerunMostDays, len(days))
	}
	c.St.Lock()
	defer c.St.Unlock()
	r := RerunRequest{Force: force}
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
	if len(r.Queued) > 0 {
		if err := c.St.Save(); err != nil {
			return RerunRequest{}, err
		}
	}
	return r, nil
}

// RerunPlan is every day to digest again with the current prompt: rerun_range's and the queue's, in order, once each.
// The caller holds the lock or reads a state no one writes.
func (c *Curator) RerunPlan() []string {
	seen := map[string]bool{}
	var out []string
	for _, d := range append(c.Cfg.RerunDays(), c.St.S.RerunQueue...) {
		if !seen[d] {
			seen[d] = true
			out = append(out, d)
		}
	}
	sort.Strings(out)
	return out
}

// RerunProgress is where the asked-for reruns stand.
type RerunProgress struct {
	Prompt  string   // the daily prompt the days are digested with
	Done    []string // planned days digested with it
	Waiting []string // planned days still to run
	Forced  []string // days queued with force that still wait (they run before the others)
	Paused  string   // why the passes wait, and until when ("" when they do not)
	// History says which days' agent sessions the hub's session history could not list in full: a line for the days
	// of a hub without it, and one per day with a source that did not answer or vouch for the window.
	History []string
}

// historyMost is how many days with gaps the status names one by one (the newest).
const historyMost = 10

// historyLines is RerunProgress.History. The caller holds the lock.
func (c *Curator) historyLines() []string {
	var none, gaps []string
	for _, day := range sortedKeys(c.St.S.SessionHistory) {
		h := c.St.S.SessionHistory[day]
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
		why := c.St.S.SessionHistory[none[len(none)-1]].Why
		out = append(out, fmt.Sprintf("%d day(s) read from the hub's live session list only, the hub offering no session history (%s): %s",
			len(none), why, daySpan(none)))
	}
	if len(gaps) > historyMost {
		out = append(out, fmt.Sprintf("%d more day(s) with sources not listed in full, before %s", len(gaps)-historyMost, gaps[len(gaps)-historyMost]))
		gaps = gaps[len(gaps)-historyMost:]
	}
	for _, day := range gaps {
		var parts []string
		for _, g := range c.St.S.SessionHistory[day].Gaps {
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

// RerunStatus is the progress of rerun_range's and the queue's days. It takes the store's lock.
func (c *Curator) RerunStatus() RerunProgress {
	c.St.Lock()
	defer c.St.Unlock()
	p := RerunProgress{Prompt: schema.DailyPrompt, Forced: slices.Clone(c.St.S.ForceReruns)}
	for _, d := range c.RerunPlan() {
		if slices.Contains(c.St.S.Reruns, d+"@"+schema.DailyPrompt) {
			p.Done = append(p.Done, d)
		} else {
			p.Waiting = append(p.Waiting, d)
		}
	}
	p.History = c.historyLines()
	if mp := c.St.S.ModelPause; mp != nil {
		what := "the model is unavailable"
		switch mp.What {
		case "assistant":
			what = "the assistant's computer is not connected"
		case WhatHeld:
			what = "the hub holds the model's connection (model_paused: busy or quota; waiting, not failing)"
		}
		p.Paused = fmt.Sprintf("%s (%s); %s is tried again at %s", what, mp.Reason, mp.Day, mp.NextTry)
	}
	return p
}
