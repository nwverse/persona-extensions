package curate

import (
	"fmt"
	"slices"
	"sort"
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
}

// QueueRerun queues the days from..to (YYYY-MM-DD, inclusive) to be digested again with the current daily prompt.
// Only days before today; at most RerunMostDays; a day already digested with the current prompt or already queued is
// not queued again. It takes the store's lock.
func (c *Curator) QueueRerun(from, to, today string) (RerunRequest, error) {
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
	var r RerunRequest
	for _, d := range days {
		if slices.Contains(c.St.S.Reruns, d+"@"+schema.DailyPrompt) || slices.Contains(c.St.S.RerunQueue, d) {
			r.Already = append(r.Already, d)
			continue
		}
		c.St.S.RerunQueue = append(c.St.S.RerunQueue, d)
		r.Queued = append(r.Queued, d)
	}
	sort.Strings(c.St.S.RerunQueue)
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
	Paused  string   // why the passes wait, and until when ("" when they do not)
}

// RerunStatus is the progress of rerun_range's and the queue's days. It takes the store's lock.
func (c *Curator) RerunStatus() RerunProgress {
	c.St.Lock()
	defer c.St.Unlock()
	p := RerunProgress{Prompt: schema.DailyPrompt}
	for _, d := range c.RerunPlan() {
		if slices.Contains(c.St.S.Reruns, d+"@"+schema.DailyPrompt) {
			p.Done = append(p.Done, d)
		} else {
			p.Waiting = append(p.Waiting, d)
		}
	}
	if mp := c.St.S.ModelPause; mp != nil {
		what := "the model is unavailable"
		if mp.What == "assistant" {
			what = "the assistant's computer is not connected"
		}
		p.Paused = fmt.Sprintf("%s (%s); %s is tried again at %s", what, mp.Reason, mp.Day, mp.NextTry)
	}
	return p
}
