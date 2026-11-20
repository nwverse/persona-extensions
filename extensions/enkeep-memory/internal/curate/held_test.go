package curate

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/model"
	"github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/stub"
)

// heldDaily is the stub model, with the daily step held by the hub past the call's wait (model_paused) while held.
type heldDaily struct {
	stub.Model
	held  bool
	until time.Time
}

func (f *heldDaily) Complete(ctx context.Context, r model.Request) (model.Response, error) {
	if f.held && r.Step == "daily" {
		return model.Response{}, &model.PausedError{Until: f.until, Message: "model.complete: 并发已满，10 秒后重试"}
	}
	return f.Model.Complete(ctx, r)
}

// A unit the hub held (0.2.0-beta.34) goes back to the queue: the day stays open as when the model is away, but the
// hold is not counted as a failure (no failures in a row, no digest error), the next try is when the hold lets go,
// and the status says it waits. Once it lets go the day runs and the wait is gone.
func TestHeldKeepsTheDayWithoutAFailure(t *testing.T) {
	m := &heldDaily{held: true}
	c, _ := newCurator(t, m, "shadow")
	m.until = c.now().Add(3 * time.Minute)
	ctx := context.Background()
	_, err := c.RunDaily(ctx, "2026-11-03", false)
	if !errors.Is(err, ErrModelDown) || !errors.Is(err, model.ErrPaused) {
		t.Fatalf("held: err %v, want ErrModelDown and model.ErrPaused", err)
	}
	if got := dailyRuns(c.St.S); len(got) != 0 {
		t.Fatalf("a held day counted as run: %v", got)
	}
	p := c.St.S.ModelPause
	if p == nil || p.What != WhatHeld || p.Fails != 0 || p.Day != "2026-11-03" || p.NextTry != m.until.UTC().Format(time.RFC3339) ||
		!strings.Contains(p.Reason, "并发已满") {
		t.Fatalf("pause: %+v", p)
	}
	if next, wait := c.ModelWait(c.now()); !wait || !next.Equal(m.until.UTC().Truncate(time.Second)) {
		t.Fatalf("wait %v until %v", wait, next)
	}
	if s := c.RerunStatus(); !strings.Contains(s.Paused, "holds the model's connection") || !strings.Contains(s.Paused, "not failing") {
		t.Fatalf("status: %q", s.Paused)
	}
	m.held = false
	if _, err := c.RunDaily(ctx, "2026-11-03", false); err != nil {
		t.Fatal(err)
	}
	if c.St.S.ModelPause != nil || strings.Join(dailyRuns(c.St.S), ",") != "2026-11-03" {
		t.Fatalf("after the hold: pause %+v runs %v", c.St.S.ModelPause, dailyRuns(c.St.S))
	}
	for _, r := range c.St.S.Runs {
		if r.Counts["digest_errors"] != 0 {
			t.Fatalf("a hold counted as a digest error: %+v", r.Counts)
		}
	}
}

// A run of a day adds to what earlier runs of it admitted (0.2.0-beta.34): a unit an earlier run read is not lost
// when a rerun no longer sees it. Forgetting a source takes its units off every day.
func TestAdmittedMergesAcrossRuns(t *testing.T) {
	c, _ := newCurator(t, &stub.Model{}, "shadow")
	ctx := context.Background()
	gone := "agents/m9/s-archived" // read by an earlier run, no longer listed by its runtime
	c.St.S.Admitted["2026-11-03"] = []string{gone}
	if _, err := c.RunDaily(ctx, "2026-11-03", false); err != nil {
		t.Fatal(err)
	}
	first := slices.Clone(c.St.S.Admitted["2026-11-03"])
	if !slices.Contains(first, gone) || len(first) < 2 || !slices.IsSorted(first) {
		t.Fatalf("admitted after the rerun: %v", first)
	}
	if _, err := c.RunDaily(ctx, "2026-11-03", false); err != nil {
		t.Fatal(err)
	}
	if got := c.St.S.Admitted["2026-11-03"]; !slices.Equal(got, first) {
		t.Fatalf("a second run changed the list: %v, was %v", got, first)
	}
	lark := slices.ContainsFunc(first, func(r string) bool { return strings.HasPrefix(r, "chats/lark-") })
	if !lark {
		t.Fatalf("the sample's day admits no lark unit: %v", first)
	}
	if _, err := c.Forget("lark"); err != nil {
		t.Fatal(err)
	}
	got := c.St.S.Admitted["2026-11-03"]
	if slices.ContainsFunc(got, func(r string) bool { return strings.HasPrefix(r, "chats/lark-") }) || !slices.Contains(got, gone) {
		t.Fatalf("after forgetting lark: %v", got)
	}
}
