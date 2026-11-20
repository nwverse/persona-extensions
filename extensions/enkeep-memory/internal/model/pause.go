package model

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strconv"
	"time"

	"github.com/nwverse/persona/sdk/go/wire"
)

// model_paused (0.2.0-beta.34): the hub holds the connection the call would go out on, because the upstream said the
// account is busy or its quota is used up. Nothing was sent. The hub holds the whole connection (address and key) for
// every extension and every purpose: the first hold is 5 seconds, a busy answer in a row doubles it, at most 5
// minutes, and the error's data says until when (ModelPause.until; a candidate's attempt carries it as until). The
// route's candidates usually share that connection, so moving on to the next one does not help: the call waits until
// the hold lets go and is asked again. Only a model that is not offered, a timeout, a reply that does not match and
// the like hand over to the next candidate, as the hub does within the call.
//
// The wait is bounded (Hub.PauseBudget) and ends with ctx. A call still held after it returns a *PausedError, which
// is ErrPaused and ErrUnavailable: a background step puts its unit back (the day stays open and runs again after
// Until), it is not a failure and is not logged as an error.

// ErrPaused: the hub held the model's connection for longer than the call may wait. Every PausedError is also
// ErrUnavailable, so a step that only knows that one keeps the unit for later, as it always did.
var ErrPaused = errors.New("model held by the hub")

// PausedError is a call the hub held (model_paused) past the wait the call may make.
type PausedError struct {
	Until   time.Time // when the hold lets go, as the hub said; zero when it did not say
	Message string    // the hub's message
}

func (e *PausedError) Error() string {
	// Starts as ErrUnavailable does: callers take the hub's message from what follows it.
	return ErrUnavailable.Error() + ": " + e.Message
}

func (e *PausedError) Unwrap() []error { return []error{ErrPaused, ErrUnavailable} }

// pauseDefault is the wait when the hub said neither until when nor how many seconds.
const pauseDefault = 10 * time.Second

// pauseMost is the longest single wait: the hub's longest hold.
const pauseMost = 5 * time.Minute

// PauseBudgetDefault is how long one call waits for holds in all, when Hub.PauseBudget is 0: two of the hub's
// longest holds. Past it the call returns *PausedError and the step keeps its unit for the next pass.
const PauseBudgetDefault = 10 * time.Minute

var secondsIn = regexp.MustCompile(`(\d+)\s*(?:秒|s\b|sec|second)`)

// pauseUntil is when a hold lets go: the hub's until (RFC 3339), else the seconds its message names, else
// pauseDefault, from now.
func pauseUntil(until, msg string, now time.Time) time.Time {
	if t, err := time.Parse(time.RFC3339, until); err == nil && !t.IsZero() {
		return t
	}
	if m := secondsIn.FindStringSubmatch(msg); m != nil {
		if n, err := strconv.Atoi(m[1]); err == nil && n > 0 {
			return now.Add(time.Duration(n) * time.Second)
		}
	}
	return now.Add(pauseDefault)
}

// pausedOf is the hold an error of the old call (no models) says, if it says one.
func pausedOf(code, msg string, data json.RawMessage, now time.Time) (*PausedError, bool) {
	if code != wire.ModelErrorCodeModelPaused {
		return nil, false
	}
	var p wire.ModelPause
	_ = json.Unmarshal(data, &p)
	return &PausedError{Until: pauseUntil(p.Until, msg, now), Message: msg}, true
}

// heldAttempts is the hold of a chain whose candidates failed, when one of them was held: the latest until of the
// held ones. A chain where a candidate was held is asked again whole after it, whatever the others did: the held one
// was never sent, and it comes first again.
func heldAttempts(as []attempt, now time.Time) (*PausedError, bool) {
	var out *PausedError
	for _, a := range as {
		if a.Code != wire.ModelErrorCodeModelPaused {
			continue
		}
		u := pauseUntil(a.Until, a.Message, now)
		if out == nil || u.After(out.Until) {
			out = &PausedError{Until: u, Message: a.Message}
		}
	}
	return out, out != nil
}

// waitHeld waits for holds and asks again, within the budget: once returns the call's answer; a *PausedError from it
// is waited out and asked again while the wait fits in what is left of the budget.
func (h Hub) waitHeld(ctx context.Context, r Request, once func(context.Context, Request) (Response, error)) (Response, error) {
	budget := h.PauseBudget
	if budget <= 0 {
		budget = PauseBudgetDefault
	}
	waited := time.Duration(0)
	for {
		resp, err := once(ctx, r)
		var p *PausedError
		if !errors.As(err, &p) {
			return resp, err
		}
		d := p.Until.Sub(h.clock())
		if d < time.Second {
			d = time.Second
		}
		d = min(d, pauseMost)
		if waited+d > budget {
			h.logf("enkeep-memory: model call %s: held by the hub (%s) until %s; waited %s already, the unit waits for the next pass",
				r.Step, p.Message, p.Until.UTC().Format(time.RFC3339), waited.Round(time.Second))
			return resp, err
		}
		h.logf("enkeep-memory: model call %s: held by the hub (%s); waiting %s, then asking again", r.Step, p.Message, d.Round(time.Second))
		if err := h.pause(ctx, d); err != nil {
			return Response{}, err
		}
		waited += d
	}
}

func (h Hub) clock() time.Time {
	if h.now != nil {
		return h.now()
	}
	return time.Now()
}

// pause waits d or until ctx ends.
func (h Hub) pause(ctx context.Context, d time.Duration) error {
	if h.sleep != nil {
		return h.sleep(ctx, d)
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func (h Hub) logf(format string, args ...any) {
	if h.Logf != nil {
		h.Logf(format, args...)
	} else if h.Route != nil {
		h.Route.logf(format, args...)
	}
}

// Held is the hold err says, when it is one (a *PausedError).
func Held(err error) (*PausedError, bool) {
	var p *PausedError
	ok := errors.As(err, &p)
	return p, ok
}
