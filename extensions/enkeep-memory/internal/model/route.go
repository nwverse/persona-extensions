package model

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/nwverse/persona/sdk/go/ext"
	"github.com/nwverse/persona/sdk/go/wire"
)

// Route is the chain of models a call goes through (docs/model-routes.md): the primary models first, in order, then
// one fallback picked by how long the call is: Long for a call whose estimated size (the input and the room for the
// answer, Estimate) is above LongAbove, Short for the rest.
//
// The whole chain goes to the hub in one call, in model.complete's "models" (persona #10398): the hub tries the
// candidates in order and says which answered (candidate, model) and which failed before it (attempts). The user can
// switch the list off or replace it on the extension's purpose, so what is counted is what the hub says answered, not
// what was asked for. A hub without the field turns the call down as invalid params: the route then sends it the old
// way, on the model of the extension's purpose, and asks again after legacyRetry. A nil Route, or one with no models,
// is the old behaviour.
type Route struct {
	Primary   []string // tried first, in order
	Long      string   // the fallback of a long call
	Short     string   // the fallback of a short call
	LongAbove int      // estimated tokens; a call above it is long
	Timeout   int      // seconds each candidate may take (timeout_seconds); 0: the step's own limit
	Stats     *Stats   // nil: nothing counted
	Logf      func(format string, args ...any)
	Now       func() time.Time

	mu          sync.Mutex
	legacyUntil time.Time // the hub turned "models" down: calls go the old way until then
}

// legacyRetry is how long the route keeps sending calls the old way after a hub turned "models" down: a hub upgrade
// restarts the extension anyway, so this only bounds how long a hub that learns the field mid-run is not used.
const legacyRetry = time.Hour

// Failure reasons: why a candidate did not answer (each counted per model in Stats), from the hub's error codes.
const (
	ReasonError       = "error"       // model_failed: an upstream or network error
	ReasonEmpty       = "empty"       // model_failed: the reply was empty
	ReasonTimeout     = "timeout"     // timeout: the candidate took longer than timeout_seconds
	ReasonFormat      = "format"      // invalid_output: no match with the schema after the hub's second try
	ReasonRefused     = "refused"     // model_refused, or model_failed saying the model declined
	ReasonUnavailable = "unavailable" // the hub cannot run a model for the extension (assistant offline, no permission)
	ReasonPaused      = "paused"      // model_paused: the hub holds the connection for quota or load; nothing was sent (pause.go)
	ReasonNotOffered  = "not_offered" // model_unavailable: no provider the user set up offers the model
)

// Size classes of a call.
const (
	SizeLong  = "long"
	SizeShort = "short"
)

// Enabled says whether calls go through the chain.
func (rt *Route) Enabled() bool { return rt != nil && len(rt.models(SizeShort)) > 0 }

// Estimate is a call's size in tokens: the input (the system prompt, the user turn and the answer's schema, which the
// hub puts into the system prompt) and the room the call asks for its answer (MaxTokens; 4096, the hub's default,
// when unset). The input is counted as a quarter token per ASCII byte and one token per other character (Chinese
// text runs at about a token a character), so it errs on the long side for Chinese.
func Estimate(r Request) int {
	in := 0
	for _, s := range []string{r.System, r.User, string(r.Schema)} {
		ascii := 0
		for i := 0; i < len(s); {
			c, n := utf8.DecodeRuneInString(s[i:])
			if c < utf8.RuneSelf {
				ascii++
			} else {
				in++
			}
			i += n
		}
		in += (ascii + 3) / 4
	}
	out := r.MaxTokens
	if out <= 0 {
		out = 4096
	}
	return in + out
}

// Size is the class of a call of est tokens.
func (rt *Route) Size(est int) string {
	if rt.LongAbove > 0 && est > rt.LongAbove {
		return SizeLong
	}
	return SizeShort
}

// models is the chain of a call of the size: the primary models, then the size's fallback; each once, at most 8 (the
// hub's limit).
func (rt *Route) models(size string) []string {
	var out []string
	add := func(m string) {
		m = strings.TrimSpace(m)
		for _, x := range out {
			if x == m {
				return
			}
		}
		if m != "" && len(out) < 8 {
			out = append(out, m)
		}
	}
	for _, m := range rt.Primary {
		add(m)
	}
	if size == SizeLong {
		add(rt.Long)
	} else {
		add(rt.Short)
	}
	return out
}

func (rt *Route) now() time.Time {
	if rt.Now != nil {
		return rt.Now()
	}
	return time.Now()
}

func (rt *Route) legacy() bool {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return rt.now().Before(rt.legacyUntil)
}

func (rt *Route) setLegacy() {
	rt.mu.Lock()
	rt.legacyUntil = rt.now().Add(legacyRetry)
	rt.mu.Unlock()
}

func (rt *Route) logf(format string, args ...any) {
	if rt.Logf != nil {
		rt.Logf(format, args...)
	}
}

// Attempt is one candidate's try in a call, as Stats keeps it.
type Attempt struct {
	Model    string  `json:"model"`              // the candidate (the purpose's model, for a call sent the old way)
	Upstream string  `json:"upstream,omitempty"` // the model the upstream said answered, when it differs
	Reason   string  `json:"reason,omitempty"`   // empty: it answered
	Code     string  `json:"code,omitempty"`     // the hub's error code
	Message  string  `json:"message,omitempty"`  // the hub's message, cut short
	Secs     float64 `json:"secs"`
}

// reason is the failure reason of an error code and message.
func reason(code, msg string) string {
	switch code {
	case wire.ModelErrorCodeTimeout:
		return ReasonTimeout
	case wire.ModelErrorCodeInvalidOutput:
		return ReasonFormat
	case wire.ModelErrorCodeModelRefused:
		return ReasonRefused
	case wire.ModelErrorCodeModelPaused:
		return ReasonPaused
	case wire.ModelErrorCodeModelUnavailable:
		return ReasonNotOffered
	case wire.ModelErrorCodeModelFailed:
		if refused(msg) {
			return ReasonRefused
		}
		if strings.Contains(strings.ToLower(msg), "empty") {
			return ReasonEmpty
		}
		return ReasonError
	}
	if code == "" {
		return ReasonError
	}
	return code
}

func failed(a attempt) Attempt {
	return Attempt{Model: a.Model, Reason: reason(a.Code, a.Message), Code: a.Code, Message: cut(a.Message, 160), Secs: a.Seconds}
}

func cut(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

// complete sends r with its chain in one call; legacy sends it the old way. call makes model.complete with the
// models given.
func (rt *Route) complete(ctx context.Context, r Request, call func(context.Context, Request, []string) (chainResult, error),
	legacy func(context.Context, Request) (Response, error)) (Response, error) {
	est := Estimate(r)
	size := rt.Size(est)
	if rt.legacy() {
		return rt.old(ctx, r, est, size, legacy, "hub without models")
	}
	chain := rt.models(size)
	if len(r.Models) > 0 {
		chain = r.Models[:min(len(r.Models), 8)]
	}
	rr := r
	if rt.Timeout > 0 {
		rr.Timeout = rt.Timeout
	}
	began := rt.now()
	out, err := call(ctx, rr, chain)
	secs := rt.now().Sub(began).Seconds()
	if err == nil {
		var attempts []Attempt
		before := 0.0
		for _, a := range out.Attempts {
			attempts = append(attempts, failed(a))
			before += a.Seconds
		}
		ans := Attempt{Model: out.Candidate, Secs: secs - before}
		if ans.Secs < 0 {
			ans.Secs = 0
		}
		if out.Model != out.Candidate {
			ans.Upstream = out.Model
		}
		note := ""
		switch {
		case out.Candidate == "": // the user switched the lists off on the purpose: its own model answered
			ans.Model, ans.Upstream, note = cmpOr(out.Model, "purpose"), "", "candidates off"
		case !contains(chain, out.Candidate):
			note = "the user's own list"
		}
		if out.StopReason == "refusal" {
			ans.Reason = ReasonRefused
			rt.done(ctx, began, r, est, size, append(attempts, ans), "", false, note)
			return Response{}, fmt.Errorf("%w: stop_reason refusal", ErrRefused)
		}
		resp := response(r, out.ModelCompleteResult)
		if resp.Model == "" {
			resp.Model = ans.Model
		}
		rt.done(ctx, began, r, est, size, append(attempts, ans), ans.Model, len(attempts) > 0, note, resp.InTokens, resp.OutTokens)
		return resp, nil
	}
	var e *ext.Error
	if !errors.As(err, &e) { // the connection to the hub, or ctx: nothing the chain can help with
		rt.done(ctx, began, r, est, size, []Attempt{{Model: strings.Join(chain, "|"), Reason: ReasonError, Message: cut(err.Error(), 160), Secs: secs}}, "", false, "")
		return Response{}, err
	}
	var attempts []Attempt
	var held *PausedError // a candidate the hub held: the call waits and is asked again (pause.go)
	switch e.Code {
	case wire.ModelErrorCodeInvalidParams:
		if strings.Contains(e.Message, `"models"`) { // json: unknown field "models"
			rt.setLegacy()
			return rt.old(ctx, r, est, size, legacy, "hub without models")
		}
		attempts = []Attempt{{Model: chain[0], Reason: ReasonError, Code: e.Code, Message: cut(e.Message, 160), Secs: secs}}
	case wire.ErrorCodeUnknownMethod, wire.ErrorCodeUnsupported, wire.ErrorCodePermissionDenied, wire.ModelErrorCodeAssistantOffline:
		// The hub cannot run any model for the extension: the run waits, as before.
		rt.done(ctx, began, r, est, size, []Attempt{{Model: strings.Join(chain, "|"), Reason: ReasonUnavailable, Code: e.Code, Message: cut(e.Message, 160), Secs: secs}}, "", false, "")
		return Response{}, fmt.Errorf("%w: %s", ErrUnavailable, e.Message)
	case codeCandidatesFailed:
		var data candidatesFailed
		_ = json.Unmarshal(e.Data, &data)
		for _, a := range data.Attempts {
			attempts = append(attempts, failed(a))
		}
		held, _ = heldAttempts(data.Attempts, rt.now())
	default: // one candidate, failing with its own code
		m := chain[0]
		var no notOffered
		if e.Code == wire.ModelErrorCodeModelUnavailable && json.Unmarshal(e.Data, &no) == nil && no.Model != "" {
			m = no.Model
		}
		attempts = []Attempt{{Model: m, Reason: reason(e.Code, e.Message), Code: e.Code, Message: cut(e.Message, 160), Secs: secs}}
		held, _ = pausedOf(e.Code, e.Message, e.Data, rt.now())
		if e.Code == wire.ModelErrorCodeInvalidOutput {
			if resp, ok := fromInvalidOutput(e.Data); ok { // the reply the hub returned anyway goes to the step's checks
				resp.Model = cmpOr(resp.Model, m)
				rt.done(ctx, began, r, est, size, attempts, resp.Model, false, "reply kept though it does not match", resp.InTokens, resp.OutTokens)
				return resp, nil
			}
		}
	}
	if len(attempts) == 0 {
		attempts = []Attempt{{Model: strings.Join(chain, "|"), Reason: reason(e.Code, e.Message), Code: e.Code, Message: cut(e.Message, 160), Secs: secs}}
	}
	if held != nil {
		// Held, not failed: no fallback to the old way or anything else; Hub.Complete waits and asks the chain again.
		rt.held(began, r, est, size, attempts, held)
		return Response{}, held
	}
	switch {
	case every(attempts, ReasonNotOffered):
		// None of the route's models is offered (another user's providers, say): the purpose's own model is.
		return rt.old(ctx, r, est, size, legacy, "no route model offered", attempts...)
	case onlyFormatOrNotOffered(attempts):
		// No candidate matched the schema (the others were not offered): the reply the hub had is not in the data of
		// model_candidates_failed, so the old call asks the purpose's model, whose reply comes back even when it does
		// not match, for the step's own checks of each item, as before routes (0.2.0-beta.33; before, a chain with a
		// candidate not offered and one not matching failed the call, and the unit went unread).
		return rt.old(ctx, r, est, size, legacy, "no candidate matched the schema", attempts...)
	}
	rt.done(ctx, began, r, est, size, attempts, "", false, "")
	last := attempts[len(attempts)-1]
	switch last.Reason {
	case ReasonRefused:
		return Response{}, fmt.Errorf("%w: %s", ErrRefused, e.Message)
	case ReasonUnavailable, ReasonNotOffered:
		return Response{}, fmt.Errorf("%w: %s", ErrUnavailable, e.Message)
	}
	return Response{}, err
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func every(attempts []Attempt, reason string) bool {
	for _, a := range attempts {
		if a.Reason != reason {
			return false
		}
	}
	return len(attempts) > 0
}

// onlyFormatOrNotOffered: every candidate either did not match the schema or was not offered, and one did not match.
func onlyFormatOrNotOffered(attempts []Attempt) bool {
	format := false
	for _, a := range attempts {
		switch a.Reason {
		case ReasonFormat:
			format = true
		case ReasonNotOffered:
		default:
			return false
		}
	}
	return format
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// old sends the call the old way, on the purpose's model, and counts it, after the chain's attempts if there were any.
func (rt *Route) old(ctx context.Context, r Request, est int, size string, legacy func(context.Context, Request) (Response, error),
	why string, before ...Attempt) (Response, error) {
	start := rt.now()
	resp, err := legacy(ctx, r)
	a := Attempt{Model: cmpOr(resp.Model, "purpose"), Secs: rt.now().Sub(start).Seconds()}
	p, paused := Held(err)
	switch {
	case paused:
		a.Reason, a.Code, a.Message = ReasonPaused, wire.ModelErrorCodeModelPaused, cut(p.Message, 160)
	case err != nil:
		a.Reason, a.Message = ReasonError, cut(err.Error(), 160)
	}
	if rt.Stats != nil {
		c := Call{At: start.UTC().Format(time.RFC3339), Step: r.Step, Est: est, Size: size, Attempts: append(before, a), Legacy: why, Paused: paused,
			Class: ClassOf(ctx)}
		if err == nil {
			c.Answered, c.In, c.Out = a.Model, resp.InTokens, resp.OutTokens
		}
		rt.Stats.add(c)
	}
	var tried []string
	for _, b := range before {
		tried = append(tried, outcome(b))
	}
	rt.logf("enkeep-memory: model call %s: est %d tokens (%s), old way (%s): %s", r.Step, est, size, why,
		strings.Join(append(tried, outcome(a)), ", "))
	return resp, err
}

// held counts and logs a call the hub held: not a failure and not an answer; it is asked again after the hold.
func (rt *Route) held(began time.Time, r Request, est int, size string, attempts []Attempt, p *PausedError) {
	var tried []string
	for _, a := range attempts {
		tried = append(tried, outcome(a))
	}
	rt.logf("enkeep-memory: model call %s: est %d tokens (%s): held by the hub until %s; tried %s", r.Step, est, size,
		p.Until.UTC().Format(time.RFC3339), strings.Join(tried, ", "))
	if rt.Stats != nil {
		rt.Stats.add(Call{At: began.UTC().Format(time.RFC3339), Step: r.Step, Est: est, Size: size, Attempts: attempts,
			Paused: true, Until: p.Until.UTC().Format(time.RFC3339)})
	}
}

// done counts and logs a call that went through the chain.
func (rt *Route) done(ctx context.Context, began time.Time, r Request, est int, size string, attempts []Attempt, answered string, fellBack bool, note string, tok ...int) {
	var tried []string
	for _, a := range attempts {
		tried = append(tried, outcome(a))
	}
	if note != "" {
		note = " (" + note + ")"
	}
	switch {
	case answered != "" && fellBack:
		rt.logf("enkeep-memory: model call %s: est %d tokens (%s): answered by %s after fallback%s; tried %s", r.Step, est,
			size, answered, note, strings.Join(tried, ", "))
	case answered != "":
		rt.logf("enkeep-memory: model call %s: est %d tokens (%s): answered by %s%s; tried %s", r.Step, est, size, answered,
			note, strings.Join(tried, ", "))
	default:
		rt.logf("enkeep-memory: model call %s: est %d tokens (%s): no candidate answered%s; tried %s", r.Step, est, size,
			note, strings.Join(tried, ", "))
	}
	if rt.Stats != nil {
		c := Call{At: began.UTC().Format(time.RFC3339), Step: r.Step, Est: est, Size: size, Attempts: attempts,
			Answered: answered, FellBack: fellBack, Note: strings.Trim(note, " ()"), Class: ClassOf(ctx)}
		if len(tok) == 2 {
			c.In, c.Out = tok[0], tok[1]
		}
		rt.Stats.add(c)
	}
}

func outcome(a Attempt) string {
	m := a.Model
	if a.Upstream != "" {
		m += " (" + a.Upstream + ")"
	}
	if a.Reason == "" {
		return fmt.Sprintf("%s ok %.0fs", m, a.Secs)
	}
	return fmt.Sprintf("%s %s %.0fs", m, a.Reason, a.Secs)
}

// Stats is what the route counted since Since, kept in a file of the extension's data directory
// (model-routes.json) so it outlives restarts: per model, the attempts, answers and failures by reason; per call,
// whether a fallback answered; the most recent calls.
type Stats struct {
	mu   sync.Mutex
	path string
	c    Counts
	// Loc is the time zone the cost lines' days are in (nil: UTC).
	Loc *time.Location
}

// Counts is what Stats counted: a plain value, safe to pass around.
type Counts struct {
	Since    string                 `json:"since"`
	Calls    int                    `json:"calls"`     // calls through the chain or the old way (held ones not counted)
	FellBack int                    `json:"fell_back"` // calls a fallback model answered
	Failed   int                    `json:"failed"`    // calls no model answered (held ones not counted)
	Legacy   int                    `json:"legacy"`    // calls sent the old way (a hub without models)
	Paused   int                    `json:"paused"`    // tries the hub held (model_paused): waited out and asked again
	Models   map[string]*ModelCount `json:"models"`
	Recent   []Call                 `json:"recent"` // the last recentCalls calls, oldest first
	// The cost line (design v2 §4.7, 0.2.0-beta.38): calls and tokens per local day, in all and for the reruns
	// apart, and since CostSince.
	CostSince string              `json:"cost_since,omitempty"`
	Days      map[string]*DayCost `json:"days,omitempty"` // YYYY-MM-DD in the hub's time zone
	Total     Cost                `json:"total"`
	Rerun     Cost                `json:"rerun"`
}

// DayCost is one day's cost: every call, and the reruns' among them.
type DayCost struct {
	All   Cost `json:"all"`
	Rerun Cost `json:"rerun"`
}

// Cost is calls and tokens: per model the calls it answered with their input and output tokens; FellBack the calls
// a fallback answered, Failed the calls none answered (held ones are not calls).
type Cost struct {
	Calls    int                   `json:"calls"`
	FellBack int                   `json:"fell_back"`
	Failed   int                   `json:"failed"`
	In       int                   `json:"in"`
	Out      int                   `json:"out"`
	Models   map[string]*ModelCost `json:"models,omitempty"`
}

// ModelCost is one model's answered calls and tokens.
type ModelCost struct {
	Calls int `json:"calls"`
	In    int `json:"in"`
	Out   int `json:"out"`
}

// costDays is how many days the cost lines are kept.
const costDays = 200

func (c *Cost) add(call Call) {
	c.Calls++
	switch {
	case call.Answered == "":
		c.Failed++
	case call.FellBack:
		c.FellBack++
	}
	c.In += call.In
	c.Out += call.Out
	if call.Answered != "" {
		if c.Models == nil {
			c.Models = map[string]*ModelCost{}
		}
		m := c.Models[call.Answered]
		if m == nil {
			m = &ModelCost{}
			c.Models[call.Answered] = m
		}
		m.Calls++
		m.In += call.In
		m.Out += call.Out
	}
}

// Line is the cost in one line: calls, tokens per model, fallbacks and failures.
func (c Cost) Line() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%d call(s), in %d / out %d tokens", c.Calls, c.In, c.Out)
	names := make([]string, 0, len(c.Models))
	for n := range c.Models {
		names = append(names, n)
	}
	sort.Strings(names)
	var parts []string
	for _, n := range names {
		m := c.Models[n]
		parts = append(parts, fmt.Sprintf("%s %d (in %d / out %d)", n, m.Calls, m.In, m.Out))
	}
	if len(parts) > 0 {
		fmt.Fprintf(&b, " [%s]", strings.Join(parts, "; "))
	}
	fmt.Fprintf(&b, ", fallbacks %d, failed %d", c.FellBack, c.Failed)
	return b.String()
}

// ModelCount is one model's attempts. Paused is the tries the hub held (model_paused): not attempts, not failures.
type ModelCount struct {
	Attempts int            `json:"attempts"`
	Answered int            `json:"answered"`
	AsBackup int            `json:"as_backup"` // answers it gave as a fallback
	Paused   int            `json:"paused,omitempty"`
	Failures map[string]int `json:"failures,omitempty"`
	Secs     float64        `json:"secs"` // the time its attempts took, together
}

// Call is one call as Stats keeps it.
type Call struct {
	At       string    `json:"at"`
	Step     string    `json:"step"`
	Est      int       `json:"est"`
	Size     string    `json:"size"`
	Attempts []Attempt `json:"attempts"`
	Answered string    `json:"answered,omitempty"`
	FellBack bool      `json:"fell_back,omitempty"`
	Legacy   string    `json:"legacy,omitempty"` // why it went the old way
	Note     string    `json:"note,omitempty"`   // candidates off, the user's own list, a reply kept though it does not match
	Paused   bool      `json:"paused,omitempty"` // the hub held it (model_paused): asked again after Until, not a failure
	Until    string    `json:"until,omitempty"`  // RFC 3339: when the hold lets go
	Class    string    `json:"class,omitempty"`  // regular | rerun (the cost line counts reruns apart)
	In       int       `json:"in,omitempty"`     // input tokens of the answer, as the hub said (cache reads included)
	Out      int       `json:"out,omitempty"`    // output tokens
}

const recentCalls = 50

// StatsFile is the name of the stats' file in the data directory.
const StatsFile = "model-routes.json"

// OpenStats reads the stats kept in dir (none yet: empty ones, from now).
func OpenStats(dir string, now time.Time) *Stats {
	s := &Stats{path: filepath.Join(dir, StatsFile)}
	if b, err := os.ReadFile(s.path); err == nil {
		_ = json.Unmarshal(b, &s.c)
	}
	if s.c.Since == "" {
		s.c.Since = now.UTC().Format(time.RFC3339)
	}
	if s.c.Models == nil {
		s.c.Models = map[string]*ModelCount{}
	}
	return s
}

func (s *Stats) add(c Call) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := &s.c
	if n.Models == nil {
		n.Models = map[string]*ModelCount{}
	}
	switch {
	case c.Paused:
		n.Paused++
	default:
		n.Calls++
		switch {
		case c.Legacy != "":
			n.Legacy++
		case c.FellBack && c.Answered != "":
			n.FellBack++
		}
		if c.Answered == "" {
			n.Failed++
		}
	}
	for i, a := range c.Attempts {
		m := n.Models[a.Model]
		if m == nil {
			m = &ModelCount{}
			n.Models[a.Model] = m
		}
		if a.Reason == ReasonPaused { // nothing was sent: not an attempt of the model's
			m.Paused++
			continue
		}
		m.Attempts++
		m.Secs += a.Secs
		if a.Reason == "" {
			m.Answered++
			if i > 0 {
				m.AsBackup++
			}
		} else {
			if m.Failures == nil {
				m.Failures = map[string]int{}
			}
			m.Failures[a.Reason]++
		}
	}
	if !c.Paused {
		day := c.At
		if t, err := time.Parse(time.RFC3339, c.At); err == nil {
			loc := s.Loc
			if loc == nil {
				loc = time.UTC
			}
			day = t.In(loc).Format("2006-01-02")
		}
		if len(day) > 10 {
			day = day[:10]
		}
		if n.Days == nil {
			n.Days = map[string]*DayCost{}
		}
		if n.CostSince == "" {
			n.CostSince = day
		}
		d := n.Days[day]
		if d == nil {
			d = &DayCost{}
			n.Days[day] = d
		}
		d.All.add(c)
		n.Total.add(c)
		if c.Class == ClassRerun {
			d.Rerun.add(c)
			n.Rerun.add(c)
		}
		if len(n.Days) > costDays {
			keys := make([]string, 0, len(n.Days))
			for k := range n.Days {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys[:len(keys)-costDays] {
				delete(n.Days, k)
			}
		}
	}
	n.Recent = append(n.Recent, c)
	if len(n.Recent) > recentCalls {
		n.Recent = append([]Call(nil), n.Recent[len(n.Recent)-recentCalls:]...)
	}
	if s.path != "" {
		if b, err := json.MarshalIndent(n, "", " "); err == nil {
			_ = writeFile(s.path, b)
		}
		appendCall(filepath.Join(filepath.Dir(s.path), CallsFile), c)
	}
}

// CallsFile is the log of every call (one JSON line each: step, estimate, size, each model's attempt and why it
// failed, the model that answered; never what was sent or answered), beside the stats; at callsMost bytes it moves
// to CallsFile+".1", which keeps one older log.
const CallsFile = "model-calls.jsonl"

const callsMost = 2 << 20

func appendCall(path string, c Call) {
	if fi, err := os.Stat(path); err == nil && fi.Size() > callsMost {
		_ = os.Rename(path, path+".1")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	b, _ := json.Marshal(c)
	_, _ = f.Write(append(b, '\n'))
}

// Snapshot is a copy of the counts, safe to read while calls go on.
func (s *Stats) Snapshot() Counts {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, _ := json.Marshal(s.c)
	var c Counts
	_ = json.Unmarshal(b, &c)
	return c
}

// CostLines are the cost lines for the status tool and the run log: the day asked for, the total since the first
// counted day, and the reruns' total (design v2 §4.7).
func (s Counts) CostLines(day string) []string {
	d := s.Days[day]
	if d == nil {
		d = &DayCost{}
	}
	since := s.CostSince
	if since == "" {
		since = day
	}
	return []string{
		fmt.Sprintf("Cost %s: %s; of which reruns: %s.", day, d.All.Line(), d.Rerun.Line()),
		fmt.Sprintf("Cost since %s: %s.", since, s.Total.Line()),
		fmt.Sprintf("Reruns since %s: %s.", since, s.Rerun.Line()),
	}
}

// Summary says the stats in a few lines: the calls, the fallback rate, and each model's attempts.
func (s Counts) Summary() string {
	var b strings.Builder
	rate := 0.0
	if s.Calls > 0 {
		rate = float64(s.FellBack) / float64(s.Calls) * 100
	}
	fmt.Fprintf(&b, "Model calls since %s: %d, answered by a fallback %d (%.1f%%), unanswered %d, sent the old way %d.",
		s.Since, s.Calls, s.FellBack, rate, s.Failed, s.Legacy)
	if s.Paused > 0 {
		fmt.Fprintf(&b, " Held by the hub (model_paused, busy or quota; waited out and asked again, not unanswered): %d.", s.Paused)
	}
	names := make([]string, 0, len(s.Models))
	for n := range s.Models {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		m := s.Models[n]
		fmt.Fprintf(&b, "\n- %s: %d attempt(s), %d answered (%d as fallback)", n, m.Attempts, m.Answered, m.AsBackup)
		if m.Paused > 0 {
			fmt.Fprintf(&b, ", held %d", m.Paused)
		}
		if len(m.Failures) > 0 {
			var rs []string
			for r := range m.Failures {
				rs = append(rs, r)
			}
			sort.Strings(rs)
			var parts []string
			for _, r := range rs {
				parts = append(parts, fmt.Sprintf("%s %d", r, m.Failures[r]))
			}
			fmt.Fprintf(&b, ", failed: %s", strings.Join(parts, ", "))
		}
		if m.Attempts > 0 {
			fmt.Fprintf(&b, ", %.0fs on average", m.Secs/float64(m.Attempts))
		}
	}
	return b.String()
}
