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

// fakeHub answers model.complete as a hub with candidate models does (persona #10398): it tries the models in order;
// each model's answer is a function; a model it has no answer for is not offered. old: a hub before "models";
// off / custom: the user switched the lists off, or replaced them, on the purpose.
type fakeHub struct {
	answers map[string]func() (wire.ModelCompleteResult, error) // by model; "" is the purpose's model
	old     bool
	off     bool
	custom  []string
	calls   [][]string // the models each call named
	limits  []int      // each call's timeout_seconds
}

func (f *fakeHub) call(_ context.Context, params, out any) error {
	b, _ := json.Marshal(params)
	var p struct {
		Models  []string `json:"models"`
		Timeout int      `json:"timeout_seconds"`
	}
	_ = json.Unmarshal(b, &p)
	f.calls = append(f.calls, p.Models)
	f.limits = append(f.limits, p.Timeout)
	if len(p.Models) > 0 && f.old {
		return &ext.Error{Code: wire.ModelErrorCodeInvalidParams, Message: `model.complete: json: unknown field "models"`}
	}
	models := p.Models
	if f.custom != nil && len(models) > 0 {
		models = f.custom
	}
	if len(models) == 0 || f.off {
		r, err := f.answers[""]()
		if err != nil {
			return err
		}
		return put(chainResult{ModelCompleteResult: r}, out)
	}
	var attempts []attempt
	var last error
	for _, m := range models {
		a, ok := f.answers[m]
		if !ok {
			data, _ := json.Marshal(notOffered{Model: m})
			last = &ext.Error{Code: wire.ModelErrorCodeModelUnavailable, Message: "model.complete: no provider offers " + m, Data: data}
			attempts = append(attempts, attempt{Model: m, Code: wire.ModelErrorCodeModelUnavailable, Message: "not offered"})
			continue
		}
		r, err := a()
		if err == nil {
			return put(chainResult{ModelCompleteResult: r, Candidate: m, Attempts: attempts}, out)
		}
		var e *ext.Error
		errors.As(err, &e)
		last = err
		var held wire.ModelPause
		_ = json.Unmarshal(e.Data, &held)
		attempts = append(attempts, attempt{Model: m, Code: e.Code, Message: e.Message, Until: held.Until, Seconds: 2})
	}
	if len(models) == 1 {
		return last
	}
	data, _ := json.Marshal(candidatesFailed{Attempts: attempts})
	return &ext.Error{Code: codeCandidatesFailed, Message: "model.complete: every candidate failed", Data: data}
}

// put hands a result over as the hub's connection does: as JSON, decoded into what the caller reads.
func put(v, out any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, out)
}

func ok(model string) func() (wire.ModelCompleteResult, error) {
	return func() (wire.ModelCompleteResult, error) {
		return wire.ModelCompleteResult{Text: `{"a":1}`, JSON: json.RawMessage(`{"a":1}`), Model: model, StopReason: "end_turn"}, nil
	}
}

func fails(code, msg string) func() (wire.ModelCompleteResult, error) {
	return func() (wire.ModelCompleteResult, error) {
		return wire.ModelCompleteResult{}, &ext.Error{Code: code, Message: msg}
	}
}

type answers = map[string]func() (wire.ModelCompleteResult, error)

func testRoute(t *testing.T) (*Route, string) {
	dir := t.TempDir()
	return &Route{Primary: []string{"flash"}, Long: "big", Short: "small", LongAbove: 5000, Timeout: 180,
		Stats: OpenStats(dir, time.Date(2026, 11, 18, 0, 0, 0, 0, time.UTC))}, dir
}

var shortReq = Request{Step: "triage", System: "sys", User: "short input", MaxTokens: 1000, Schema: json.RawMessage(`{"type":"object"}`)}
var longReq = Request{Step: "daily", System: "sys", User: strings.Repeat("中", 6000), MaxTokens: 1000, Schema: json.RawMessage(`{"type":"object"}`)}

func joined(calls [][]string) string {
	var out []string
	for _, c := range calls {
		out = append(out, "["+strings.Join(c, " ")+"]")
	}
	return strings.Join(out, ",")
}

// The whole chain goes in one call, with the route's timeout for each candidate; the primary answers.
func TestRoutePrimaryAnswers(t *testing.T) {
	rt, _ := testRoute(t)
	f := &fakeHub{answers: answers{"flash": ok("flash-upstream"), "small": ok("small")}}
	resp, err := Hub{Route: rt, call: f.call}.Complete(context.Background(), shortReq)
	if err != nil || resp.Model != "flash-upstream" || resp.Text != `{"a":1}` {
		t.Fatalf("%+v %v", resp, err)
	}
	if joined(f.calls) != "[flash small]" || f.limits[0] != 180 {
		t.Fatalf("calls %s limits %v", joined(f.calls), f.limits)
	}
	s := rt.Stats.Snapshot()
	if s.Calls != 1 || s.FellBack != 0 || s.Models["flash"].Answered != 1 || s.Recent[0].Attempts[0].Upstream != "flash-upstream" {
		t.Fatalf("stats %+v", s)
	}
}

// Every kind of failure of the primary hands over to the short or the long model by the call's size, and the reason
// is counted from the hub's attempts.
//
// ReasonPaused: a candidate held on its connection while the next one, on another, answers: the hub handed over within
// the call, and the answer stands (the hold is counted apart, not as a failure).
func TestRouteFallsBackByLength(t *testing.T) {
	for name, primary := range map[string]func() (wire.ModelCompleteResult, error){
		ReasonError:   fails(wire.ModelErrorCodeModelFailed, "model.complete: 502 upstream"),
		ReasonEmpty:   fails(wire.ModelErrorCodeModelFailed, "model.complete: the reply was empty"),
		ReasonTimeout: fails(wire.ModelErrorCodeTimeout, "model.complete: timed out"),
		ReasonRefused: fails(wire.ModelErrorCodeModelRefused, "model.complete: declined"),
		ReasonFormat:  fails(wire.ModelErrorCodeInvalidOutput, "model.complete: the reply does not match json_schema"),
		ReasonPaused:  fails(wire.ModelErrorCodeModelPaused, "model.complete: paused"),
	} {
		for _, c := range []struct {
			r    Request
			want string
		}{{shortReq, "small"}, {longReq, "big"}} {
			rt, _ := testRoute(t)
			f := &fakeHub{answers: answers{"flash": primary, "small": ok("small"), "big": ok("big")}}
			resp, err := Hub{Route: rt, call: f.call}.Complete(context.Background(), c.r)
			if err != nil || resp.Model != c.want {
				t.Fatalf("%s %s: %+v %v", name, c.r.Step, resp, err)
			}
			if joined(f.calls) != "[flash "+c.want+"]" {
				t.Fatalf("%s: calls %s", name, joined(f.calls))
			}
			s := rt.Stats.Snapshot()
			failed := s.Models["flash"].Failures[name]
			if name == ReasonPaused {
				failed = s.Models["flash"].Paused
			}
			if s.Calls != 1 || s.FellBack != 1 || failed != 1 || s.Models[c.want].AsBackup != 1 {
				t.Fatalf("%s: stats %+v", name, s)
			}
			if got := s.Recent[0]; got.Answered != c.want || !got.FellBack || got.Attempts[0].Reason != name {
				t.Fatalf("%s: recent %+v", name, got)
			}
		}
	}
}

func TestEstimate(t *testing.T) {
	// 6000 Chinese characters, "sys" and the schema in ASCII, 1000 for the answer.
	if got := Estimate(longReq); got < 7000 || got > 7020 {
		t.Fatalf("long %d", got)
	}
	if got := Estimate(Request{User: strings.Repeat("a", 400)}); got != 100+4096 {
		t.Fatalf("ascii %d", got)
	}
	rt, _ := testRoute(t)
	if rt.Size(Estimate(shortReq)) != SizeShort || rt.Size(Estimate(longReq)) != SizeLong {
		t.Fatal("sizes")
	}
}

// A hub that does not take "models" turns the call down; it goes the old way, and so do the next ones.
func TestRouteHubWithoutModels(t *testing.T) {
	rt, _ := testRoute(t)
	f := &fakeHub{old: true, answers: answers{"flash": ok("flash"), "": ok("purpose-model")}}
	h := Hub{Route: rt, call: f.call}
	for i := 0; i < 2; i++ {
		resp, err := h.Complete(context.Background(), shortReq)
		if err != nil || resp.Model != "purpose-model" {
			t.Fatalf("%+v %v", resp, err)
		}
	}
	if joined(f.calls) != "[flash small],[],[]" {
		t.Fatalf("calls %s", joined(f.calls))
	}
	if s := rt.Stats.Snapshot(); s.Calls != 2 || s.Legacy != 2 || s.Models["purpose-model"].Answered != 2 {
		t.Fatalf("stats %+v", s)
	}
}

// No model of the route is offered: the purpose's own model answers, counted as one call.
func TestRouteNoneOffered(t *testing.T) {
	rt, _ := testRoute(t)
	f := &fakeHub{answers: answers{"": ok("purpose-model")}}
	resp, err := Hub{Route: rt, call: f.call}.Complete(context.Background(), longReq)
	if err != nil || resp.Model != "purpose-model" || joined(f.calls) != "[flash big],[]" {
		t.Fatalf("%+v %v %s", resp, err, joined(f.calls))
	}
	s := rt.Stats.Snapshot()
	if s.Calls != 1 || s.Legacy != 1 || s.Models["flash"].Failures[ReasonNotOffered] != 1 || s.Recent[0].Legacy != "no route model offered" {
		t.Fatalf("stats %+v", s)
	}
}

// The user switched the lists off on the purpose: the answer has no candidate, the purpose's model is counted.
func TestRouteCandidatesOff(t *testing.T) {
	rt, _ := testRoute(t)
	f := &fakeHub{off: true, answers: answers{"": ok("purpose-model")}}
	resp, err := Hub{Route: rt, call: f.call}.Complete(context.Background(), shortReq)
	if err != nil || resp.Model != "purpose-model" {
		t.Fatalf("%+v %v", resp, err)
	}
	s := rt.Stats.Snapshot()
	if s.Models["purpose-model"].Answered != 1 || s.Recent[0].Note != "candidates off" || s.Recent[0].Answered != "purpose-model" {
		t.Fatalf("stats %+v", s)
	}
}

// The user's own list: what is counted is the candidate the hub says answered.
func TestRouteUsersOwnList(t *testing.T) {
	rt, _ := testRoute(t)
	f := &fakeHub{custom: []string{"mine"}, answers: answers{"mine": ok("mine")}}
	if _, err := (Hub{Route: rt, call: f.call}).Complete(context.Background(), shortReq); err != nil {
		t.Fatal(err)
	}
	s := rt.Stats.Snapshot()
	if s.Models["mine"].Answered != 1 || s.Models["flash"] != nil || s.Recent[0].Note != "the user's own list" {
		t.Fatalf("stats %+v", s)
	}
}

// The hub cannot run a model for the extension at all: the run waits, as before.
func TestRouteAssistantOffline(t *testing.T) {
	rt, _ := testRoute(t)
	calls := 0
	h := Hub{Route: rt, call: func(context.Context, any, any) error {
		calls++
		return &ext.Error{Code: wire.ModelErrorCodeAssistantOffline, Message: "offline"}
	}}
	if _, err := h.Complete(context.Background(), shortReq); !errors.Is(err, ErrUnavailable) || calls != 1 {
		t.Fatalf("%v %d", err, calls)
	}
}

// Every candidate paused or refusing: the caller gets the error it handles (the day waits; the batch is split).
func TestRouteAllFail(t *testing.T) {
	rt, _ := testRoute(t)
	f := &fakeHub{answers: answers{"flash": fails(wire.ModelErrorCodeModelPaused, "paused"), "small": fails(wire.ModelErrorCodeModelPaused, "paused")}}
	// Held past the call's wait (a budget too small for any wait): ErrPaused, which is ErrUnavailable, not a failure.
	if _, err := (Hub{Route: rt, call: f.call, PauseBudget: time.Nanosecond}).Complete(context.Background(), shortReq); !errors.Is(err, ErrUnavailable) || !errors.Is(err, ErrPaused) {
		t.Fatalf("paused: %v", err)
	}
	f = &fakeHub{answers: answers{"flash": fails(wire.ModelErrorCodeModelFailed, "ai: the model declined to answer"), "small": fails(wire.ModelErrorCodeModelRefused, "declined")}}
	if _, err := (Hub{Route: rt, call: f.call}).Complete(context.Background(), shortReq); !errors.Is(err, ErrRefused) {
		t.Fatalf("refused: %v", err)
	}
	f = &fakeHub{answers: answers{"flash": fails(wire.ModelErrorCodeTimeout, "timed out"), "small": fails(wire.ModelErrorCodeModelFailed, "502")}}
	if _, err := (Hub{Route: rt, call: f.call}).Complete(context.Background(), shortReq); err == nil || errors.Is(err, ErrRefused) || errors.Is(err, ErrUnavailable) {
		t.Fatalf("errors: %v", err)
	}
	if s := rt.Stats.Snapshot(); s.Failed != 2 || s.Paused != 1 || s.Models["small"].Failures[ReasonError] != 1 || s.Models["flash"].Failures[ReasonPaused] != 0 {
		t.Fatalf("stats %+v", s)
	}
}

// No candidate matches the schema: the old call's reply goes to the step's own checks, as before routes. With one
// candidate, the reply the hub returned anyway does.
func TestRouteNonconforming(t *testing.T) {
	bad := func(text string) func() (wire.ModelCompleteResult, error) {
		return func() (wire.ModelCompleteResult, error) {
			data, _ := json.Marshal(wire.ModelInvalidOutput{Problem: "missing", Text: text})
			return wire.ModelCompleteResult{}, &ext.Error{Code: wire.ModelErrorCodeInvalidOutput, Message: "bad", Data: data}
		}
	}
	rt, _ := testRoute(t)
	f := &fakeHub{answers: answers{"flash": bad(`{"x":1}`), "small": bad(`{"y":2}`), "": bad(`{"z":3}`)}}
	resp, err := Hub{Route: rt, call: f.call}.Complete(context.Background(), shortReq)
	if err != nil || resp.Text != `{"z":3}` || resp.Nonconforming == "" || joined(f.calls) != "[flash small],[]" {
		t.Fatalf("%+v %v %s", resp, err, joined(f.calls))
	}
	one := &Route{Short: "small", Stats: OpenStats(t.TempDir(), time.Now())}
	f = &fakeHub{answers: answers{"small": bad(`{"y":2}`)}}
	resp, err = Hub{Route: one, call: f.call}.Complete(context.Background(), shortReq)
	if err != nil || resp.Text != `{"y":2}` || resp.Model != "small" || joined(f.calls) != "[small]" {
		t.Fatalf("one: %+v %v %s", resp, err, joined(f.calls))
	}
}

// Without a route the call names no model, as before.
func TestNoRouteIsTheOldCall(t *testing.T) {
	f := &fakeHub{answers: answers{"": ok("purpose-model")}}
	resp, err := Hub{call: f.call}.Complete(context.Background(), shortReq)
	if err != nil || resp.Model != "purpose-model" || joined(f.calls) != "[]" || f.limits[0] != 300 {
		t.Fatalf("%+v %v %s %v", resp, err, joined(f.calls), f.limits)
	}
	if (&Route{}).Enabled() || (*Route)(nil).Enabled() {
		t.Fatal("an empty route is off")
	}
}

// The stats outlive a restart, every call is a line of the calls log, and the summary says the counts.
func TestStatsPersist(t *testing.T) {
	rt, dir := testRoute(t)
	f := &fakeHub{answers: answers{"flash": fails(wire.ModelErrorCodeTimeout, "timed out"), "small": ok("small")}}
	for i := 0; i < 3; i++ {
		if _, err := (Hub{Route: rt, call: f.call}).Complete(context.Background(), shortReq); err != nil {
			t.Fatal(err)
		}
	}
	s := OpenStats(dir, time.Now()).Snapshot()
	if s.Calls != 3 || s.FellBack != 3 || s.Since != "2026-11-18T00:00:00Z" || s.Models["flash"].Failures[ReasonTimeout] != 3 {
		t.Fatalf("reopened %+v", s)
	}
	sum := s.Summary()
	for _, w := range []string{"3, answered by a fallback 3 (100.0%)", "flash: 3 attempt(s), 0 answered", "timeout 3", "small: 3 attempt(s), 3 answered (3 as fallback)"} {
		if !strings.Contains(sum, w) {
			t.Errorf("summary lacks %q:\n%s", w, sum)
		}
	}
	file, err := os.Open(filepath.Join(dir, CallsFile))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	n := 0
	for sc := bufio.NewScanner(file); sc.Scan(); n++ {
		var c Call
		if err := json.Unmarshal(sc.Bytes(), &c); err != nil || c.Answered != "small" || c.Attempts[0].Reason != ReasonTimeout || c.Attempts[0].Code != "timeout" {
			t.Fatalf("line %d: %s %v", n, sc.Text(), err)
		}
	}
	if n != 3 {
		t.Fatalf("lines %d", n)
	}
}

// A candidate not offered and one not matching the schema: the old call's reply goes to the step's own checks, as
// when every candidate did not match (the unit is read, not lost).
func TestRouteNotOfferedThenNonconforming(t *testing.T) {
	data, _ := json.Marshal(wire.ModelInvalidOutput{Problem: "$.summary[1]: unexpected property", Text: `{"z":3}`})
	rt, _ := testRoute(t)
	f := &fakeHub{answers: answers{
		"small": fails(wire.ModelErrorCodeInvalidOutput, "the reply does not match json_schema"),
		"": func() (wire.ModelCompleteResult, error) {
			return wire.ModelCompleteResult{}, &ext.Error{Code: wire.ModelErrorCodeInvalidOutput, Message: "bad", Data: data}
		},
	}}
	resp, err := Hub{Route: rt, call: f.call}.Complete(context.Background(), shortReq)
	if err != nil || resp.Text != `{"z":3}` || resp.Nonconforming == "" || joined(f.calls) != "[flash small],[]" {
		t.Fatalf("%+v %v %s", resp, err, joined(f.calls))
	}
	s := rt.Stats.Snapshot()
	if s.Calls != 1 || s.Legacy != 1 || s.Failed != 0 || s.Recent[0].Legacy != "no candidate matched the schema" ||
		s.Models["flash"].Failures[ReasonNotOffered] != 1 || s.Models["small"].Failures[ReasonFormat] != 1 {
		t.Fatalf("stats %+v", s)
	}
}

// A call that names its own models (the periodic overview's) sends only those, never the route's primary.
func TestRouteRequestModels(t *testing.T) {
	rt, _ := testRoute(t)
	f := &fakeHub{answers: answers{"flash": ok("flash"), "small": ok("small"), "big": ok("big")}}
	r := shortReq
	r.Models = []string{"big", "small"}
	resp, err := Hub{Route: rt, call: f.call}.Complete(context.Background(), r)
	if err != nil || resp.Model != "big" {
		t.Fatalf("%+v %v", resp, err)
	}
	if joined(f.calls) != "[big small]" {
		t.Fatalf("calls %s", joined(f.calls))
	}
	// A route without models of its own still takes the request's.
	f2 := &fakeHub{answers: answers{"small": ok("small")}}
	r.Models = []string{"small"}
	if _, err := (Hub{Route: &Route{}, call: f2.call}).Complete(context.Background(), r); err != nil || joined(f2.calls) != "[small]" {
		t.Fatalf("calls %s %v", joined(f2.calls), err)
	}
}
