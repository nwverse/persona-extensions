package main

import (
	"slices"
	"testing"
	"time"

	"github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/curate"
	"github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/kbsrc"
	"github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/schema"
	"github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/store"
	"github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/stub"
)

func TestRerunDays(t *testing.T) {
	c := curate.Defaults()
	if len(c.RerunDays()) != 0 {
		t.Error("unset: no days")
	}
	c.RerunRange = "2026-11-04..2026-11-10"
	if d := c.RerunDays(); len(d) != 7 || d[0] != "2026-11-04" || d[6] != "2026-11-10" {
		t.Errorf("days: %v", d)
	}
	for _, bad := range []string{"2026-11-10..2026-11-04", "x..y", "2026-11-04"} {
		c.RerunRange = bad
		if len(c.RerunDays()) != 0 {
			t.Errorf("%q is not a range", bad)
		}
	}
}

// rerun_range digests each day of the range again with the current daily prompt, once each: what an earlier prompt
// produced stays as it was (a rejected item stays rejected), and a second pass does nothing more.
func TestRerunRange(t *testing.T) {
	fx, err := kbsrc.LoadFixture("testdata/sample")
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	st.S.Items["i-old"] = &store.Item{ID: "i-old", Class: store.Fact, Claim: "an old item", Status: "rejected", Prompt: "daily/3"}
	st.S.Items["i-keep"] = &store.Item{ID: "i-keep", Class: store.Fact, Claim: "another old item", Status: "candidate", Prompt: "daily/3"}
	cfg := curate.Defaults()
	cfg.Mode, cfg.AdmitAllBelowTokens = "shadow", 0
	cfg.RerunRange = "2026-11-03..2026-11-04"
	cfg.BackfillDays = 0                                // the first-run replay is tested on its own (backfill_test.go)
	now := time.Date(2026, 11, 9, 4, 0, 0, 0, time.UTC) // after daily_at; yesterday (11-08) is not in the range
	a := newApp()
	a.c = &curate.Curator{Src: fx, Model: &stub.Model{}, St: st, Cfg: cfg, Loc: time.UTC, Now: func() time.Time { return now }}
	settle := func() {
		for i := 0; i < 600; i++ {
			a.mu.Lock()
			idle := a.running == ""
			a.mu.Unlock()
			if idle {
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatal("a run never ended")
	}
	for i := 0; i < 8; i++ {
		a.tick(t.Context())
		settle()
	}
	for _, day := range []string{"2026-11-03", "2026-11-04"} {
		if !slices.Contains(st.S.Reruns, day+"@"+schema.DailyPrompt) {
			t.Errorf("%s not digested again: %v", day, st.S.Reruns)
		}
	}
	runs := 0
	for _, r := range st.S.Runs {
		if r.Kind == "daily" && (r.Period == "2026-11-03" || r.Period == "2026-11-04") {
			runs++
		}
	}
	if runs != 2 {
		t.Errorf("each day once, got %d daily runs of the range", runs)
	}
	if st.S.Items["i-old"].Status != "rejected" || st.S.Items["i-keep"].Status != "candidate" {
		t.Error("an item an earlier prompt produced changed")
	}
}
