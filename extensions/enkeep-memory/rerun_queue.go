package main

// Reruns enkeep_rerun could not write into state.json at once: a daily or fast pass holds the store's lock for
// minutes, and the hub's tool call would otherwise return busy after the user approved the card. The requests wait
// in reruns-queue.json beside the state and are merged into State.RerunQueue / State.ForceReruns as soon as the lock
// frees (and before tick picks the next day to run, or at the next start if the process stopped first).

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/curate"
	"github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/schema"
	"github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/store"
)

const rerunsFile = "reruns-queue.json"

// queuedRerun is one enkeep_rerun call waiting for the store's lock.
type queuedRerun struct {
	From  string `json:"from"`
	To    string `json:"to"`
	Today string `json:"today"`
	Force bool   `json:"force,omitempty"`
	At    string `json:"at"`
}

func (a *app) rerunsPath() string { return filepath.Join(a.c.St.Dir, rerunsFile) }

// readReruns returns the pending rerun requests; the caller holds a.rerunsMu.
func (a *app) readReruns() []queuedRerun {
	raw, err := os.ReadFile(a.rerunsPath())
	if err != nil {
		return nil
	}
	var q []queuedRerun
	if err := json.Unmarshal(raw, &q); err != nil {
		a.logf("enkeep-memory: %s: %v", rerunsFile, err)
		return nil
	}
	return q
}

// pendingRerunDays lists days waiting in reruns-queue.json (not yet merged into state.json), split into normal and
// force-queued days. Never waits for the store's lock.
func (a *app) pendingRerunDays() (normal, forced []string) {
	a.rerunsMu.Lock()
	q := a.readReruns()
	a.rerunsMu.Unlock()
	if len(q) == 0 {
		return nil, nil
	}
	seenN, seenF := map[string]bool{}, map[string]bool{}
	for _, r := range q {
		days, err := curate.ValidateRerunDays(r.From, r.To, r.Today)
		if err != nil {
			continue
		}
		for _, d := range days {
			if r.Force {
				if !seenF[d] {
					seenF[d] = true
					forced = append(forced, d)
				}
			} else if !seenN[d] {
				seenN[d] = true
				normal = append(normal, d)
			}
		}
	}
	sort.Strings(normal)
	sort.Strings(forced)
	return normal, forced
}

// queueRerun persists an enkeep_rerun request beside state.json while a pass holds the store's lock, kicks off
// applyReruns to merge it as soon as the lock frees, and answers "Queued ..." at once.
func (a *app) queueRerun(from, to, today string, force bool) (string, error) {
	days, err := curate.ValidateRerunDays(from, to, today)
	if err != nil {
		return "", err
	}
	a.rerunsMu.Lock()
	prev := a.readReruns()
	q := append(prev, queuedRerun{From: from, To: to, Today: today, Force: force, At: time.Now().UTC().Format(time.RFC3339)})
	b, err := json.Marshal(q)
	if err == nil {
		err = os.WriteFile(a.rerunsPath(), b, 0o600)
	}
	a.rerunsMu.Unlock()
	if err != nil {
		return "", err
	}
	go a.applyReruns()

	// Preview queued vs already-done/queued from the last published snapshot plus any earlier pending requests.
	prevNormal, prevForced := map[string]bool{}, map[string]bool{}
	for _, r := range prev {
		ds, derr := curate.ValidateRerunDays(r.From, r.To, r.Today)
		if derr != nil {
			continue
		}
		for _, d := range ds {
			if r.Force {
				prevForced[d] = true
			} else {
				prevNormal[d] = true
			}
		}
	}
	var snapState *store.State
	if snap := a.c.Live.Snapshot(); snap != nil {
		snapState = snap.State
	}
	var queued, already []string
	for _, d := range days {
		if force {
			if (snapState != nil && slices.Contains(snapState.ForceReruns, d)) || prevForced[d] {
				already = append(already, d)
			} else {
				queued = append(queued, d)
			}
		} else {
			if (snapState != nil && (slices.Contains(snapState.Reruns, d+"@"+schema.DailyPrompt) || slices.Contains(snapState.RerunQueue, d))) || prevNormal[d] {
				already = append(already, d)
			} else {
				queued = append(queued, d)
			}
		}
	}

	what := "a curation pass"
	if r := a.runningNow(); r != "" {
		what = r
	}
	var out strings.Builder
	if force {
		fmt.Fprintf(&out, "Queued %d day(s) with force to read again with the current prompt, even if already read with it", len(queued))
	} else {
		fmt.Fprintf(&out, "Queued %d day(s) to read again with the current prompt", len(queued))
	}
	if len(queued) > 0 {
		fmt.Fprintf(&out, ": %s", span(queued))
	}
	fmt.Fprintf(&out, " (%s is running; they will join the rerun queue as soon as it ends).", what)
	if len(already) > 0 {
		if force {
			fmt.Fprintf(&out, " Already queued with force, skipped: %s.", span(already))
		} else {
			fmt.Fprintf(&out, " Already done or queued, skipped: %s.", span(already))
		}
	}
	out.WriteString(" They run one at a time in the background; enkeep_rerun_status shows progress.")
	return out.String(), nil
}

// applyReruns merges any pending requests from reruns-queue.json into State.RerunQueue / State.ForceReruns without
// disturbing days already queued, saves and publishes the snapshot, then trims the applied requests from the file.
// Lock order: never hold a.rerunsMu while waiting for a.c.St.Lock(), so concurrent enkeep_rerun calls never block.
func (a *app) applyReruns() {
	a.rerunsMu.Lock()
	hasPending := len(a.readReruns()) > 0
	a.rerunsMu.Unlock()
	if !hasPending {
		return
	}
	a.c.St.Lock()
	defer a.c.St.Unlock()
	for {
		a.rerunsMu.Lock()
		q := a.readReruns()
		a.rerunsMu.Unlock()
		if len(q) == 0 {
			return
		}
		totalQueued := 0
		for _, req := range q {
			r, err := a.c.QueueRerunLocked(req.From, req.To, req.Today, req.Force)
			if err != nil {
				a.logf("enkeep-memory: queued rerun %s..%s: %v", req.From, req.To, err)
				continue
			}
			totalQueued += len(r.Queued)
		}
		if totalQueued > 0 {
			if err := a.c.St.Save(); err != nil {
				a.logf("enkeep-memory: queued reruns save: %v", err) // keep the queue file to retry on next tick/start
				return
			}
			a.c.Publish()
		}
		// Trim only the prefix we applied so any request appended during St.Save() is preserved and processed next.
		a.rerunsMu.Lock()
		cur := a.readReruns()
		n := min(len(q), len(cur))
		rest := cur[n:]
		var werr error
		if len(rest) == 0 {
			if err := os.Remove(a.rerunsPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
				werr = err
			}
		} else {
			b, err := json.Marshal(rest)
			if err == nil {
				err = os.WriteFile(a.rerunsPath(), b, 0o600)
			}
			werr = err
		}
		a.rerunsMu.Unlock()
		if werr != nil {
			a.logf("enkeep-memory: %s: %v", rerunsFile, werr)
			return
		}
		a.logf("enkeep-memory: queued reruns merged: %d day(s) added from %d request(s)", totalQueued, len(q))
		if len(rest) == 0 {
			return
		}
	}
}
