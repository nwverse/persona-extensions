package model

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/sdk/go/ext"
	"github.com/nwverse/persona/sdk/go/wire"
)

// fakeClock is a clock that a wait moves forward, for the hold tests.
type fakeClock struct {
	t     time.Time
	waits []time.Duration
}

func (c *fakeClock) now() time.Time { return c.t }

func (c *fakeClock) sleep(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.waits = append(c.waits, d)
	c.t = c.t.Add(d)
	return nil
}

var t0 = time.Date(2026, 11, 19, 3, 30, 0, 0, time.UTC)

// held is a model_paused as the hub sends it: until in the data when until is not zero.
func held(c *fakeClock, msg string, until time.Duration) func() (wire.ModelCompleteResult, error) {
	return func() (wire.ModelCompleteResult, error) {
		e := &ext.Error{Code: wire.ModelErrorCodeModelPaused, Message: msg}
		if until > 0 {
			e.Data, _ = json.Marshal(wire.ModelPause{Reason: "busy", Until: c.t.Add(until).Format(time.RFC3339)})
		}
		return wire.ModelCompleteResult{}, e
	}
}

// heldTimes answers held n times, then ok.
func heldTimes(c *fakeClock, n int, until time.Duration, then func() (wire.ModelCompleteResult, error)) func() (wire.ModelCompleteResult, error) {
	h := held(c, "model.complete: 并发已满，10 秒后重试", until)
	return func() (wire.ModelCompleteResult, error) {
		if n > 0 {
			n--
			return h()
		}
		return then()
	}
}

// Every candidate on the held connection: the call waits until the hold lets go and asks the whole chain again; it
// is counted as held, not unanswered, and no candidate is called a failure.
func TestHeldChainWaitsUntilAndAsksAgain(t *testing.T) {
	rt, dir := testRoute(t)
	c := &fakeClock{t: t0}
	rt.Now = c.now
	f := &fakeHub{answers: answers{
		"flash": heldTimes(c, 1, 30*time.Second, ok("flash")),
		"small": held(c, "model.complete: 并发已满，10 秒后重试", 30*time.Second),
	}}
	resp, err := Hub{Route: rt, call: f.call, now: c.now, sleep: c.sleep}.Complete(context.Background(), shortReq)
	if err != nil || resp.Model != "flash" {
		t.Fatalf("answer %+v %v", resp, err)
	}
	if len(c.waits) != 1 || c.waits[0] != 30*time.Second {
		t.Fatalf("waits %v, want one of 30s (the hub's until)", c.waits)
	}
	if joined(f.calls) != "[flash small],[flash small]" {
		t.Fatalf("calls %s", joined(f.calls))
	}
	s := rt.Stats.Snapshot()
	if s.Calls != 1 || s.Failed != 0 || s.FellBack != 0 || s.Paused != 1 {
		t.Fatalf("stats %+v", s)
	}
	if s.Models["flash"].Paused != 1 || s.Models["small"].Paused != 1 || len(s.Models["flash"].Failures) != 0 || s.Models["flash"].Attempts != 1 {
		t.Fatalf("models %+v %+v", s.Models["flash"], s.Models["small"])
	}
	if !strings.Contains(s.Summary(), "Held by the hub") || !strings.Contains(s.Summary(), "unanswered 0") {
		t.Fatalf("summary %s", s.Summary())
	}
	// The log line says paused, with the hub's code, as model-calls.jsonl kept it before, but no longer "unavailable".
	fh, _ := os.Open(filepath.Join(dir, CallsFile))
	defer fh.Close()
	sc := bufio.NewScanner(fh)
	var lines []Call
	for sc.Scan() {
		var call Call
		_ = json.Unmarshal(sc.Bytes(), &call)
		lines = append(lines, call)
	}
	if len(lines) != 2 || !lines[0].Paused || lines[0].Attempts[0].Reason != ReasonPaused || lines[0].Attempts[0].Code != wire.ModelErrorCodeModelPaused || lines[1].Answered != "flash" {
		t.Fatalf("calls log %+v", lines)
	}
}

// A hold beside a candidate that is not offered is still a hold: no fallback to the purpose's model.
func TestHeldBesideNotOfferedIsAHold(t *testing.T) {
	rt, _ := testRoute(t)
	c := &fakeClock{t: t0}
	purpose := 0
	f := &fakeHub{answers: answers{
		"flash": heldTimes(c, 1, 5*time.Second, ok("flash")),
		"":      func() (wire.ModelCompleteResult, error) { purpose++; return ok("purpose")() },
	}}
	resp, err := Hub{Route: rt, call: f.call, now: c.now, sleep: c.sleep}.Complete(context.Background(), shortReq)
	if err != nil || resp.Model != "flash" || purpose != 0 {
		t.Fatalf("answer %+v %v, purpose asked %d", resp, err, purpose)
	}
	if len(c.waits) != 1 || c.waits[0] != 5*time.Second {
		t.Fatalf("waits %v", c.waits)
	}
}

// Without until the message's seconds are waited; the old call (no route) waits the same way.
func TestHeldOldCallWaitsTheMessagesSeconds(t *testing.T) {
	c := &fakeClock{t: t0}
	f := &fakeHub{answers: answers{"": heldTimes(c, 2, 0, ok("purpose"))}}
	resp, err := Hub{call: f.call, now: c.now, sleep: c.sleep}.Complete(context.Background(), shortReq)
	if err != nil || resp.Model != "purpose" {
		t.Fatalf("answer %+v %v", resp, err)
	}
	if len(c.waits) != 2 || c.waits[0] != 10*time.Second || c.waits[1] != 10*time.Second {
		t.Fatalf("waits %v, want 10s twice", c.waits)
	}
}

// A hold longer than what is left of the budget is not waited: the call returns ErrPaused (also ErrUnavailable)
// with the hub's until, for the step to keep its unit for later.
func TestHeldPastTheBudget(t *testing.T) {
	rt, _ := testRoute(t)
	c := &fakeClock{t: t0}
	rt.Now = c.now
	f := &fakeHub{answers: answers{"flash": held(c, "busy", 4*time.Minute), "small": held(c, "busy", 4*time.Minute)}}
	_, err := Hub{Route: rt, call: f.call, now: c.now, sleep: c.sleep}.Complete(context.Background(), shortReq)
	p, isHeld := Held(err)
	if !isHeld || !errors.Is(err, ErrPaused) || !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err %v", err)
	}
	if len(c.waits) != 2 || !p.Until.Equal(c.t.Add(4*time.Minute)) {
		t.Fatalf("waits %v, until %v (now %v)", c.waits, p.Until, c.t)
	}
	if s := rt.Stats.Snapshot(); s.Failed != 0 || s.Calls != 0 || s.Paused != 3 {
		t.Fatalf("stats %+v", s)
	}
	// One hold is at most the hub's longest (5 minutes), whatever until says.
	c2 := &fakeClock{t: t0}
	f2 := &fakeHub{answers: answers{"": heldTimes(c2, 1, time.Hour, ok("purpose"))}}
	if _, err := (Hub{call: f2.call, now: c2.now, sleep: c2.sleep}).Complete(context.Background(), shortReq); err != nil || c2.waits[0] != pauseMost {
		t.Fatalf("capped wait %v %v", c2.waits, err)
	}
}

// The wait ends with the context.
func TestHeldWaitEndsWithContext(t *testing.T) {
	c := &fakeClock{t: t0}
	f := &fakeHub{answers: answers{"": held(c, "busy", 30*time.Second)}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := Hub{call: f.call, now: c.now}.Complete(ctx, shortReq)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err %v", err)
	}
}

// A timeout or a reply that does not match still hands over to the next candidate, as before; only the hold waits.
func TestOnlyHoldsWait(t *testing.T) {
	rt, _ := testRoute(t)
	c := &fakeClock{t: t0}
	f := &fakeHub{answers: answers{"flash": fails(wire.ModelErrorCodeTimeout, "timed out"), "small": ok("small")}}
	resp, err := Hub{Route: rt, call: f.call, now: c.now, sleep: c.sleep}.Complete(context.Background(), shortReq)
	if err != nil || resp.Model != "small" || len(c.waits) != 0 {
		t.Fatalf("answer %+v %v waits %v", resp, err, c.waits)
	}
}

func TestPauseUntil(t *testing.T) {
	for _, c := range []struct {
		until, msg string
		want       time.Duration
	}{
		{t0.Add(42 * time.Second).Format(time.RFC3339), "x", 42 * time.Second},
		{"", "model.complete: 并发已满，10 秒后重试", 10 * time.Second},
		{"", "connection held; retry in 7s", 7 * time.Second},
		{"", "held", pauseDefault},
	} {
		if got := pauseUntil(c.until, c.msg, t0).Sub(t0); got != c.want {
			t.Fatalf("%q %q: %v, want %v", c.until, c.msg, got, c.want)
		}
	}
}
