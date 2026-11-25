package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/convsrc"
	"github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/hubapi"
	"github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/sources"
	"log"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/curate"
	"github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/kbsrc"
	"github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/model"
	"github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/overview"
	"github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/profile"
	"github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/schema"
	"github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/store"
	"github.com/nwverse/persona/sdk/go/ext"
	"github.com/nwverse/persona/sdk/go/wire"
)

type app struct {
	mu        sync.Mutex
	c         *curate.Curator
	hub       *ext.Hub
	running   string // the run in progress, if any
	lastErr   string
	paused    []string     // a.tried keys of passes that found the model unavailable: tried again after the pause
	tried     []string     // days a daily run started since this process did
	member    bool         // a member hub of a group: the curator runs only on the centre
	changed   int          // kb.changed events since start
	dataDir   string       // the extension's data directory (trials leave their report there)
	lastTrial string       // the last trial's summary (dryrun_hub.go)
	source    kbsrc.Source // tests can inject a source before init; production uses the hub
	hubAPI    hubapi.Hub   // the hub beyond the knowledge base: the assistant's runtime, the weekly report's notify
	hubLoc    *time.Location
	rt        string // the assistant's runtime, cached (page_v4.go)
	rtAt      time.Time
	exitDir   string           // where the exit files are when not in the store's directory (the page subcommand)
	route     *model.Route     // the model route; nil: every call on the purpose's model (model_route "purpose")
	recalls   *store.RecallLog // the old enkeep_recall's counts (recall.json, up to 0.2.0-beta.43), with their own lock
	marksMu   sync.Mutex       // the queued marks' file (marks.go)
	done      []doneWrite      // the last actions.do answers by request id (tools.go doOnce)
	ov        *overview.Holder // the periodic overview's state (overview_app.go); nil: off
	lastFast  time.Time        // the correction fast path's last look (curate.RunFast)
}

func newApp() *app { return &app{} }

func (a *app) init(ctx context.Context, in *ext.Init, hub *ext.Hub) (map[string]any, error) {
	cfg, err := curate.ParseConfig(in.InitializeParams.Config)
	if err != nil {
		return nil, err
	}
	info := in.Info()
	dir := info.DataDir
	if dir == "" { // a hub always gives one; without it (tests, a remote run) the state lives in a fresh temp dir
		if dir, err = os.MkdirTemp("", "enkeep-memory-"); err != nil {
			return nil, err
		}
	}
	st, err := store.Open(dir)
	if err != nil {
		return nil, err
	}
	tz := cfg.Timezone
	if tz == "" {
		tz = in.Hub.Timezone
	}
	loc, err := time.LoadLocation(tz)
	if err != nil || tz == "" {
		loc = time.Local
	}
	a.hub = hub
	a.dataDir = info.DataDir
	a.hubLoc = loc // the weekly report goes by the hub's own time zone
	if hl, err := time.LoadLocation(in.Hub.Timezone); err == nil && in.Hub.Timezone != "" {
		a.hubLoc = hl
	}
	a.member = in.Hub.Role == "member"
	// The knowledge base, and the user's Persona conversations (assistant.read; without the grant they are left out).
	// The hub's other sources (agent sessions, cards, to-dos, "about me", memory) over its HTTP API with the
	// extension's token; without one, none of them, each noted on the sources card.
	var collect curate.Collector
	var src kbsrc.Source = a.source
	if src == nil {
		withConversations := convsrc.With(kbsrc.Hub{H: hub, Ext: info.ID}, convsrc.Hub{H: hub}, loc)
		withConversations.Logf = func(f string, args ...any) { log.Printf("enkeep-memory: "+f, args...) }
		var h hubapi.Hub = hubapi.None{}
		if hh := hubapi.FromEnv(); hh != nil {
			hh.Call = hub.Call // assistant.memory (H2) is an RPC, not a route
			h = hh
		}
		a.hubAPI = h
		more := sources.With(withConversations, h, loc)
		more.Logf = withConversations.Logf
		src, collect = more, more
	}
	// The model route (docs/model-routes.md): each call names its model and falls back by length; its counts are kept
	// in model-routes.json beside the state and shown by enkeep_rerun_status.
	route := cfg.Route()
	if route != nil {
		route.Stats = model.OpenStats(dir, time.Now())
		route.Stats.Loc = loc // the cost lines' days are the hub's
		route.Logf = func(f string, args ...any) { log.Printf(f, args...) }
		a.route = route
	}
	a.c = &curate.Curator{Src: src, Model: model.Hub{H: hub, Extension: info.ID, Route: route}, St: st, Cfg: cfg, Loc: loc,
		Lang: in.Hub.Locale, Extension: info.ID, Logf: func(f string, args ...any) { log.Printf(f, args...) }, Live: &curate.Live{},
		Collect: collect, Hub: a.hubAPI, Cards: curate.PeopleCards{},
		// Four calls at once in the daily pass: what the project's limit allows (model.LimitConcurrent, design v2
		// §4.7); every call still waits for a turn of the shared limiter, reruns behind the scheduled passes.
		Workers: model.LimitConcurrent}
	a.openOverview(st.Dir) // before the first snapshot: its light refresh runs on every one
	st.Lock()              // nothing runs yet: the health page's first snapshot
	a.recalls = store.OpenRecall(st.Dir, st.S.Recall)
	st.S.Recall = nil // moved to recall.json; dropped from state.json at its next save
	if prev, err := profile.Load(st.Dir); err == nil && prev == nil && len(st.S.Entries) > 0 {
		lang := cfg.Language
		if lang == "" {
			lang = in.Hub.Locale
		}
		_ = profile.Save(st.Dir, profile.BuildWith(st.S, a.c.ProfileInput, time.Now(), lang))
	}
	a.c.Publish()
	st.Unlock()
	go a.applyMarks(context.WithoutCancel(ctx)) // marks a pass's run left queued when the process stopped
	go a.recover(ctx)
	go a.migrate(ctx)
	if !a.member {
		go a.schedule(ctx)
	}
	// The connector the manifest declares (the rerun tools, tools.go), said back to the hub as the program's own.
	return map[string]any{"connectors": manifestConnectors()}, nil
}

// manifestConnectors is the manifest's contributes.connectors, as written there.
func manifestConnectors() json.RawMessage {
	var m struct {
		Contributes struct {
			Connectors json.RawMessage `json:"connectors"`
		} `json:"contributes"`
	}
	_ = json.Unmarshal(manifest, &m)
	return m.Contributes.Connectors
}

// schedule runs the daily pass at daily_at for the day before, and the weekly pass on Mondays at weekly_at for the
// ISO week before. After a restart it catches up the most recent missed run of each, not every missed one.
func (a *app) schedule(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		a.tick(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// fast runs the correction fast path (curate.RunFast) when no other run is in progress, holding the run slot while
// it does.
func (a *app) fast(ctx context.Context) {
	a.mu.Lock()
	if a.running != "" {
		a.mu.Unlock()
		return
	}
	a.running = "fast"
	a.mu.Unlock()
	if _, err := a.c.RunFast(ctx); err != nil {
		log.Printf("enkeep-memory: fast path: %v", err)
	}
	a.mu.Lock()
	a.running = ""
	a.mu.Unlock()
}

func (a *app) tick(ctx context.Context) {
	c := a.c
	nowFn := c.Now
	if nowFn == nil {
		nowFn = time.Now
	}
	now := nowFn().In(c.Loc)
	at := func(hhmm string) time.Time {
		var h, m int
		fmt.Sscanf(hhmm, "%d:%d", &h, &m)
		return time.Date(now.Year(), now.Month(), now.Day(), h, m, 0, 0, c.Loc)
	}
	yesterday := now.AddDate(0, 0, -1).Format("2006-01-02")
	a.releaseProfiles(ctx)
	// The model was unavailable to the last pass: nothing that needs it starts before the next try (State.ModelPause).
	// Then the day that could not run is tried again: by the replay from where it stopped, by rerun_range or the
	// daily pass as their own rules say.
	if _, wait := c.ModelWait(nowFn()); wait {
		return
	}
	a.retryAfterPause()
	// The first-run replay of the history (4.1) goes first, a day at a time, oldest first; a day that failed is
	// tried again on the next start.
	c.StartBackfill(now.Format("2006-01-02"))
	if next := a.backfillNext(); next != "" && !a.attempted("backfill@"+next) {
		a.start(ctx, "backfill", next)
		return
	}
	if !now.Before(at(c.Cfg.DailyAt)) && !a.ran("daily", yesterday) {
		a.start(ctx, "daily", yesterday)
		return
	}
	// The weekly pass, once due, goes before the reruns (design v2 §4.7: reruns are the low-priority queue; a long
	// backfill must not hold the week's consolidation back).
	monday := now.AddDate(0, 0, -int((now.Weekday()+6)%7))
	lastWeek := curate.ISOWeek(monday.AddDate(0, 0, -1).Format("2006-01-02"))
	weeklyDue := now.Weekday() != time.Monday || !now.Before(at(c.Cfg.WeeklyAt))
	if weeklyDue && !a.ran("weekly", lastWeek) {
		a.start(ctx, "weekly", lastWeek)
		return
	}
	// The periodic overview (overview_app.go): the week's cross-event synthesis once the week's pass has run, and on
	// the first Monday of a month the month's consolidation; before the report, which carries them, and the reruns.
	if weeklyDue && a.ran("weekly", lastWeek) && a.overviewDue(lastWeek) {
		a.start(ctx, kindOverview, lastWeek)
		return
	}
	if weeklyDue {
		if month := a.monthDue(monday, lastWeek); month != "" {
			a.start(ctx, kindOverviewMonth, month)
			return
		}
	}
	// The weekly report to the user, once the week's pass has run (weekly_report.go): not held back by reruns either.
	if err := a.weeklyTick(ctx, time.Now()); err != nil {
		log.Printf("enkeep-memory: weekly report: %v", err)
	}
	// The correction fast path (curate.RunFast): every fast_minutes, before the reruns (they are the low-priority
	// queue; a correction is wanted in this hour, not after a backfill).
	// It runs here, in the tick, when nothing else does: a look that finds nothing costs no model call and must not
	// take a tick from the reruns.
	if every := time.Duration(c.Cfg.FastMinutes) * time.Minute; every > 0 && nowFn().Sub(a.lastFast) >= every {
		a.lastFast = nowFn()
		a.fast(ctx)
	}
	// rerun_range: only completed local days are rerun, one at a time with the current daily prompt. An early daily
	// must not suppress the formal pass after the day ends. Each day runs only once per prompt.
	// The days asked for by the enkeep_rerun tool (State.RerunQueue) run the same way.
	c.St.Lock()
	days := c.RerunPlan()
	c.St.Unlock()
	today := now.Format("2006-01-02")
	for i, day := range days {
		if day >= today {
			days = days[:i]
			break
		}
	}
	// Days queued with force (enkeep_rerun force) run first, although they carry the current prompt's marker.
	c.St.Lock()
	forced := slices.Clone(c.St.S.ForceReruns)
	c.St.Unlock()
	for _, day := range forced {
		if day < today && !a.attempted(day) {
			a.start(ctx, "daily", day)
			return
		}
	}
	for _, day := range days {
		if !a.reran(day) && !a.attempted(day) {
			a.start(ctx, "daily", day)
			return
		}
	}
	// After the days of rerun_range are digested with the current prompt, their judgements are filed once.
	if len(days) > 0 && a.reran(days[len(days)-1]) && !a.synthAfterDaily(days[len(days)-1]) && !a.attempted("synth@"+days[len(days)-1]) {
		a.start(ctx, "synth", days[len(days)-1])
		return
	}
	if len(days) > 0 && a.reran(days[len(days)-1]) && a.synthAfterDaily(days[len(days)-1]) {
		a.pruneRerunQueue()
	}
}

// pruneRerunQueue takes the days that are done out of the enkeep_rerun queue, once the planned reruns are all
// digested and filed (the synth after the last one ran). Their markers in State.Reruns stay, so asking for them
// again still skips them; enkeep_rerun_status then lists only rerun_range's days and what is still waiting.
func (a *app) pruneRerunQueue() {
	a.c.St.Lock()
	queue := slices.Clone(a.c.St.S.RerunQueue)
	a.c.St.Unlock()
	var done []string
	for _, d := range queue {
		if a.reran(d) {
			done = append(done, d)
		}
	}
	if len(done) == 0 {
		return
	}
	a.c.St.Lock()
	a.c.St.S.RerunQueue = slices.DeleteFunc(a.c.St.S.RerunQueue, func(d string) bool { return slices.Contains(done, d) })
	if err := a.c.St.Save(); err != nil {
		log.Printf("enkeep-memory: rerun queue: %v", err)
	}
	a.c.St.Unlock()
	// Days were read again: every entry's status is settled again as of today, counting only the days really
	// read (design v2 §11.3 and D.5, E4: resettle after the reruns, not only at the end of the batch, so the long
	// backfill never leaves entries dormant on days still waiting); what enkeep_resettle applies. Idempotent.
	if r, err := a.c.Resettle(a.localDay(), true); err != nil {
		log.Printf("enkeep-memory: resettle after the reruns: %v", err)
	} else {
		log.Printf("enkeep-memory: resettled after the reruns: %d entr(ies) moved", r.Changed())
	}
}

// backfillNext is the next day of the first-run replay, or "" when there is none to run.
func (a *app) backfillNext() string {
	a.c.St.Lock()
	defer a.c.St.Unlock()
	if b := a.c.St.S.Backfill; b != nil && b.Finished == "" {
		return b.Next
	}
	return ""
}

func (a *app) reran(day string) bool {
	a.c.St.Lock()
	marked := slices.Contains(a.c.St.S.Reruns, day+"@"+schema.DailyPrompt)
	a.c.St.Unlock()
	// A marker alone does not prove the day completed after its local day end.
	return marked && a.ran("daily", day)
}

// validDailyFinishLocked returns the finish time of a daily that really completed after its local day end.
// a.c.St must be locked by the caller.
func (a *app) validDailyFinishLocked(r *store.Run, day string) (time.Time, bool) {
	if r.Kind != "daily" || r.Period != day || r.Finished == "" || r.DryRun {
		return time.Time{}, false
	}
	started, startErr := time.Parse(time.RFC3339, r.Started)
	finished, finishErr := time.Parse(time.RFC3339, r.Finished)
	periodDay, dayErr := time.ParseInLocation("2006-01-02", day, a.c.Loc)
	if startErr != nil || finishErr != nil || dayErr != nil {
		return time.Time{}, false
	}
	if started.Before(periodDay.AddDate(0, 0, 1)) || finished.Before(started) {
		return time.Time{}, false
	}
	return finished, true
}

// synthAfterDaily reports whether a synth started after the latest valid daily for day finished.
func (a *app) synthAfterDaily(day string) bool {
	a.c.St.Lock()
	defer a.c.St.Unlock()
	var latest time.Time
	for _, r := range a.c.St.S.Runs {
		finished, ok := a.validDailyFinishLocked(r, day)
		if ok && finished.After(latest) {
			latest = finished
		}
	}
	if latest.IsZero() {
		return false
	}
	for _, r := range a.c.St.S.Runs {
		if r.Kind != "synth" || r.Period != day || r.Finished == "" || r.DryRun {
			continue
		}
		started, startErr := time.Parse(time.RFC3339, r.Started)
		finished, finishErr := time.Parse(time.RFC3339, r.Finished)
		if startErr == nil && finishErr == nil && !finished.Before(started) && !started.Before(latest) {
			return true
		}
	}
	return false
}

// triedKey is the a.tried key of a pass.
func triedKey(kind, period string) string {
	switch kind {
	case "daily":
		return period
	case "synth", "backfill", kindOverview, kindOverviewMonth, kindBacktest, kindAudit:
		return kind + "@" + period
	}
	return kind + " " + period
}

// retryAfterPause forgets, once the pause is over, that the passes the model was unavailable to were tried: the
// tick starts them again.
func (a *app) retryAfterPause() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.paused) == 0 {
		return
	}
	a.tried = slices.DeleteFunc(a.tried, func(k string) bool { return slices.Contains(a.paused, k) })
	a.paused = nil
}

// attempted says whether a daily run of the day already started since this process did: a rerun that failed is
// not retried every minute, the next start (or a change of settings) tries again.
func (a *app) attempted(day string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return slices.Contains(a.tried, day)
}

func (a *app) ran(kind, period string) bool {
	a.c.St.Lock()
	defer a.c.St.Unlock()
	for _, r := range a.c.St.S.Runs {
		if r.Kind != kind || r.Period != period || r.Finished == "" || r.DryRun {
			continue
		}
		if kind == "daily" {
			if _, ok := a.validDailyFinishLocked(r, period); !ok {
				continue
			}
			return true
		}
		if kind == "synth" {
			started, startErr := time.Parse(time.RFC3339, r.Started)
			finished, finishErr := time.Parse(time.RFC3339, r.Finished)
			day, dayErr := time.ParseInLocation("2006-01-02", period, a.c.Loc)
			if startErr != nil || finishErr != nil || dayErr != nil {
				continue // an unknown time does not prove the synth completed
			}
			if finished.Before(started) || started.Before(day.AddDate(0, 0, 1)) {
				continue // an unfinished or inverted run is not the formal completion for this period
			}
		}
		return true
	}
	return false
}

// start runs a pass in the background; one at a time.
func (a *app) start(ctx context.Context, kind, period string) bool {
	a.mu.Lock()
	if a.running != "" {
		a.mu.Unlock()
		return false
	}
	a.running = kind + " " + period
	if kind == "daily" {
		a.tried = append(a.tried, period)
	}
	if kind == "synth" {
		a.tried = append(a.tried, "synth@"+period)
	}
	if kind == "backfill" {
		a.tried = append(a.tried, "backfill@"+period)
	}
	if strings.HasPrefix(kind, kindOverview) {
		a.tried = append(a.tried, kind+"@"+period)
	}
	a.mu.Unlock()
	// A rerun (a past day read again, its synthesis, the first-run replay) is counted apart in the cost line and waits
	// behind the scheduled passes for a model turn (model.Limiter).
	nowFn := a.c.Now
	if nowFn == nil {
		nowFn = time.Now
	}
	yesterday := nowFn().In(a.c.Loc).AddDate(0, 0, -1).Format("2006-01-02")
	rerun := kind == "backfill" || kind == "synth" || kind == "daily" && period != yesterday
	if rerun {
		ctx = model.WithPriority(model.WithClass(ctx, model.ClassRerun), model.PriorityLow)
	}
	prompt := map[string]string{"daily": schema.DailyPrompt, "backfill": schema.DailyPrompt, "weekly": schema.WeeklyPrompt, "synth": schema.SynthPrompt,
		kindOverview: overview.Prompt, kindOverviewMonth: overview.Prompt, kindBacktest: overview.Prompt, kindAudit: overview.Prompt}[kind]
	log.Printf("enkeep-memory: %s %s starts (prompt=%s ruleset=%s)", kind, period, prompt, schema.Ruleset)
	go func() {
		var err error
		if kind == "backfill" {
			_, err = a.c.BackfillStep(ctx)
		} else if kind == "daily" {
			_, err = a.c.RunDaily(ctx, period, false)
			if err == nil {
				a.c.St.Lock()
				if key := period + "@" + schema.DailyPrompt; !slices.Contains(a.c.St.S.Reruns, key) {
					a.c.St.S.Reruns = append(a.c.St.S.Reruns, key)
				}
				// A day queued with force is done once its daily run completes.
				a.c.St.S.ForceReruns = slices.DeleteFunc(a.c.St.S.ForceReruns, func(d string) bool { return d == period })
				if !rerun {
					a.costLine(period) // the day's cost line in the run log
				}
				err = a.c.St.Save()
				a.c.Publish() // enkeep_rerun_status reads the snapshot: the day shows done at once
				a.c.St.Unlock()
			}
		} else if kind == "synth" {
			_, err = a.c.RunSynth(ctx, period, false)
		} else if strings.HasPrefix(kind, kindOverview) {
			err = a.runOverview(ctx, kind, period)
		} else {
			_, err = a.c.RunWeekly(ctx, period, false)
			if days, werr := curate.WeekDays(period); err == nil && werr == nil {
				_, err = a.c.RunSynth(ctx, days[6], false) // the week's judgements, filed under patterns
			}
		}
		a.mu.Lock()
		a.running = ""
		a.lastErr = ""
		if errors.Is(err, curate.ErrModelDown) {
			a.paused = append(a.paused, triedKey(kind, period)) // tried again after the pause, not only at the next start
		}
		if errors.Is(err, model.ErrPaused) {
			// The hub held the model's connection (busy or quota): the day waits for the hold to let go and runs
			// again; not a failed run (the page says it waits, State.ModelPause).
			log.Printf("enkeep-memory %s %s: waits for the hub's hold: %v", kind, period, err)
			err = nil
		}
		if err != nil {
			a.lastErr = kind + " " + period + "：" + err.Error()
			log.Printf("enkeep-memory %s %s: %v", kind, period, err)
		}
		a.mu.Unlock()
		a.badge()
		if a.hub != nil {
			_ = a.hub.Invalidate("enkeep-memory")
			_ = a.hub.Invalidate("enkeep-memory-today")
		}
	}()
	return true
}

// costLine writes the cost of the calendar day period (in the hub's time zone), the total and the reruns' total
// into the run log, as notes of the day's daily run (design v2 §4.7). The store's lock is held by the caller.
func (a *app) costLine(period string) {
	if a.route == nil || a.route.Stats == nil {
		return
	}
	lines := a.route.Stats.Snapshot().CostLines(period)
	for i := len(a.c.St.S.Runs) - 1; i >= 0; i-- {
		if r := a.c.St.S.Runs[i]; r.Kind == "daily" && r.Period == period && !r.DryRun {
			r.Notes = append(r.Notes, lines...)
			log.Printf("enkeep-memory: %s", strings.Join(lines, " "))
			return
		}
	}
}

// publish takes a new snapshot for the pages after the user changed something. A run that took the lock meanwhile
// takes its own when it ends: no waiting for it here.
func (a *app) publish() {
	if !a.c.St.TryLock() {
		return
	}
	a.c.Publish()
	a.c.St.Unlock()
}

func (a *app) badge() {
	if a.hub == nil {
		return
	}
	n := len(a.pending())
	text := ""
	if n > 0 {
		text = fmt.Sprint(n)
	}
	_ = a.hub.Badge("enkeep-memory", text, "")
}

func (a *app) pending() []*store.Proposal {
	st, done := a.read()
	defer done()
	var out []*store.Proposal
	for _, p := range st.Proposals {
		if p.Status == "pending" || p.Status == "later" {
			out = append(out, p)
		}
	}
	return out
}

// event: kb.forgotten removes everything derived from the forgotten source; kb.changed is only counted (the next
// daily pass reads what changed).
func (a *app) event(ctx context.Context, method string, params json.RawMessage) (any, error) {
	switch method {
	case "kb.forgotten":
		var e wire.KBForgottenEvent
		if err := json.Unmarshal(params, &e); err != nil {
			return nil, err
		}
		n, err := a.c.Forget(e.Source)
		log.Printf("enkeep-memory: source %q forgotten, %d derived records removed", e.Source, n)
		return nil, err
	case "kb.changed":
		a.mu.Lock()
		a.changed++
		a.mu.Unlock()
	case hubapi.LedgerChanged:
		// An owner's "wrong" mark on a ledger entry (persona #13106): a hint only. The next read of the ledger leaves the
		// entry out, so its quote no longer verifies anything. Subscribed once a hub with the ledger is deployed (the
		// manifest cannot name the event before: a hub that does not know it refuses the manifest).
		a.c.LedgerChanged()
	}
	return nil, nil
}

func short(s string, n int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return string(r)
}

// recover replays interrupted proposal writes without holding up initialization. Curator.Recover holds the store's
// lock; individual failures are logged and left for the next recovery or proposal retry.
func (a *app) recover(ctx context.Context) {
	if err := a.c.Recover(ctx); err != nil {
		log.Printf("enkeep-memory: proposal recovery will be tried again: %v", err)
	}
}

// migrate cleans a state written before source roles, once, holding the store's lock (migrateSources). A failure is
// logged and leaves the state as it was: the next start tries again.
func (a *app) migrate(ctx context.Context) {
	st := a.c.St
	st.Lock()
	defer st.Unlock()
	sc, err := migrateSources(ctx, st, a.c.Src, time.Now())
	switch {
	case err != nil:
		log.Printf("enkeep-memory: clean of the old state not done, tried again at the next start: %v", err)
	case sc != nil:
		log.Printf("enkeep-memory: cleaned the old state: %s", cleanSummary(sc))
		a.c.Publish()
	}
}

// releaseProfiles asks the hub whether it takes person profiles (at most once a day, only while a card waits;
// curate.CheckProfileCapability writes nothing to anyone) and lets the waiting cards through when it does.
func (a *app) releaseProfiles(ctx context.Context) {
	a.c.CheckProfileCapability(ctx)
	if !a.c.St.TryLock() {
		return // a run holds the state: the next tick
	}
	defer a.c.St.Unlock()
	if a.c.ReleaseProfiles() > 0 {
		if err := a.c.St.Save(); err != nil {
			log.Printf("enkeep-memory: release person profiles: %v", err)
		}
	}
}
