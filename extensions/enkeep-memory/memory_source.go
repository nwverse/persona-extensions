package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/memsrc"
	"github.com/nwverse/persona/sdk/go/ext"
)

// memorySourceID is the memory_sources contribution's id (persona-extension.json).
const memorySourceID = "memory"

// memory answers the hub's memory_sources calls (memory.entries, 0.2.0-beta.42): one page of the curated entries,
// read from the last published snapshot, never from the live state (a run holds the store's lock for its whole pass,
// and the call has 5 seconds). Read-only. Before the first snapshot it answers not_ready.
func (a *app) memory(ctx context.Context, method string, raw json.RawMessage) (any, error) {
	if method != "entries" {
		return nil, ext.UnknownMethod("memory." + method)
	}
	var p memsrc.Params
	if err := strictDecode(raw, &p); err != nil {
		return nil, &ext.Error{Code: "invalid", Message: "invalid memory.entries parameters: " + err.Error()}
	}
	if p.Contribution != memorySourceID {
		return nil, ext.NoContribution(p.Contribution)
	}
	if p.Limit < 1 || p.Limit > memsrc.MaxLimit || len(p.After) > memsrc.MaxAfter {
		return nil, &ext.Error{Code: "invalid", Message: fmt.Sprintf("limit is 1 to %d, after at most %d bytes", memsrc.MaxLimit, memsrc.MaxAfter)}
	}
	res, _, err := a.memoryPage(p)
	if errors.Is(err, memsrc.ErrBadAfter) {
		return nil, &ext.Error{Code: "invalid", Message: err.Error()}
	}
	return res, err
}

// memoryPage is one page from the snapshot, with the pool's counts; not_ready before the first snapshot.
func (a *app) memoryPage(p memsrc.Params) (memsrc.Result, memsrc.Counts, error) {
	snap := a.c.Live.Snapshot()
	if snap == nil || snap.State == nil {
		return memsrc.Result{}, memsrc.Counts{}, &ext.Error{Code: "not_ready", Message: "enkeep memory has not loaded its entries yet"}
	}
	return memsrc.PageWith(snap.State, snap.Profile, a.overviewExtras(), p)
}

// memoryReading reads every page as the hub would (200 a page, at most 5 pages) and says what came back, for
// enkeep_rerun_status: entries, active, standing, tagged, and how long it took.
func (a *app) memoryReading() string {
	t0 := time.Now()
	p := memsrc.Params{Contribution: memorySourceID, Limit: 200}
	var got, active, standing, pages int
	tagged := true
	var n memsrc.Counts
	for pages < 5 {
		res, c, err := a.memoryPage(p)
		if err != nil {
			return "memory.entries: " + err.Error()
		}
		n = c
		pages++
		tagged = tagged && res.Tagged
		for _, e := range res.Entries {
			got++
			if e.Status == "active" {
				active++
			}
			if e.Standing {
				standing++
			}
		}
		if !res.HasMore || res.After == "" {
			break
		}
		p.After = res.After
	}
	return fmt.Sprintf("memory.entries: %d entr(ies) in %d page(s) of %d in the pool (%d active, %d standing), tagged %t "+
		"(%d of the pool read before the 0.2.0-beta.40 tagging rules, tagged by them on reading), in %d ms",
		got, pages, n.Entries, active, standing, tagged, n.Backfilled, time.Since(t0).Milliseconds())
}
