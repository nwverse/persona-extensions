package main

// The tools the assistant and Agents may call (connector "enkeep", capability tools): queue days to be digested
// again with the current daily prompt, and see how far the reruns got. A rerun is a one-off operation, not a setting:
// it reads the user's own sources again inside the extension, sends nothing anywhere and deletes nothing; what it finds
// goes through the same thresholds and review as any day's, and each day runs once per prompt, so asking twice does
// nothing more. rerun_range (the setting) works as before. enkeep_resettle settles every entry's status again on the
// real active days (curate/resettle.go), as a dry run unless asked to apply.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/curate"
	"github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/entry"
	exits "github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/export"
	"github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/model"
	"github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/recall"
	"github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/store"
	"github.com/nwverse/persona/sdk/go/ext"
)

// toolWait is how long a tool that changes the state waits for the store's lock before it says a pass is running
// (or, enkeep_mark_wrong, queues its marks): the hub gives up on a tool call after a while, a daily pass holds the
// lock for tens of minutes.
var toolWait = 5 * time.Second // a variable for tests

// busy turns curate.ErrBusy into what the caller is told: which pass runs, and to call again when it is over.
func (a *app) busy(err error) error {
	if !errors.Is(err, curate.ErrBusy) {
		return err
	}
	what := "a curation pass"
	if r := a.runningNow(); r != "" {
		what = r
	}
	return fmt.Errorf("busy: %s is running and holds the state; call again when it is over (enkeep_rerun_status shows it)", what)
}

// runningNow is the pass this process runs, "" when none.
func (a *app) runningNow() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.running
}

// connectorID is the manifest's connector id. The hub keeps only the tools whose names start with it and "_"
// (connector.owned), so the tools are enkeep_*.
const connectorID = "enkeep"

type tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"input_schema"`
}

var rerunTools = []tool{
	{Name: "enkeep_rerun", Description: "Queue past days to be read again with enkeep memory's current extraction prompt " +
		"(for example after the model was down or the prompt changed). Days run one at a time in the background, " +
		"each at most once per prompt version; days already read with the current prompt are skipped. At most 31 days " +
		"per call, only days before today. force: true reads the days again even if already read with the current prompt " +
		"(for a day that lost a unit to a model error); units read before are reused, only what was not read calls the " +
		"model. Returns what was queued; use enkeep_rerun_status for progress.",
		InputSchema: map[string]any{"type": "object", "required": []string{"from", "to"}, "properties": map[string]any{
			"from":  map[string]any{"type": "string", "pattern": `^\d{4}-\d{2}-\d{2}$`, "description": "first day, YYYY-MM-DD"},
			"to":    map[string]any{"type": "string", "pattern": `^\d{4}-\d{2}-\d{2}$`, "description": "last day, YYYY-MM-DD, before today"},
			"force": map[string]any{"type": "boolean", "default": false, "description": "read the days again even if already read with the current prompt"},
		}}},
	{Name: "enkeep_rerun_status", Description: "Show how far enkeep memory's reruns got: the days queued (by enkeep_rerun " +
		"or the rerun_range setting), which are done with the current prompt, which are waiting, what is running now, " +
		"and whether the passes wait for the model or the assistant's computer; also the model route: calls per model, " +
		"fallbacks and why.",
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{}}},
	{Name: "enkeep_resettle", Description: "Settle every enkeep memory entry's status (candidate, stable, shifting, dormant, " +
		"retired) again as of today, counting only the days the extraction really read with the model. Use it to check " +
		"or repair statuses after reruns, for example when entries went dormant because days without model output were " +
		"counted. dry_run (default true) only reports the counts before and after and the stable entries per facet; " +
		"dry_run false applies them. Idempotent: a second call moves nothing. Deletes nothing.",
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{
			"dry_run": map[string]any{"type": "boolean", "default": true, "description": "true: report only; false: apply"},
		}}},
	{Name: "enkeep_recall", Description: "Look up what enkeep memory has curated about the user: a person, a project, a " +
		"system, machine or account, a kind of task or a fork where you would choose for the user. Use it before " +
		"answering about a specific person, project or system, and after the conversation was compacted. Returns up to " +
		"limit lines (about 4000 characters), best match first, each with its facet, status (稳定 stable, 在变 shifting, " +
		"候选 candidate, 较早 dormant), last day seen, source kinds and entry id. Candidates and dormant entries are less " +
		"certain. Evaluations of and inferences about people are never returned. Entries in a category (health, " +
		"finance, relationships, emotion, legal_id) are returned only when categories names every one of theirs: name " +
		"one only when the user's request is about it. Nothing is sent anywhere and the query is not kept.",
		InputSchema: map[string]any{"type": "object", "required": []string{"query"}, "properties": map[string]any{
			"query": map[string]any{"type": "string", "description": "names or words to look for (at most 200 characters), e.g. a project name, a person's name, a machine, a kind of task"},
			"facets": map[string]any{"type": "array", "items": map[string]any{"type": "string",
				"enum": []string{"interaction", "tools", "judgement", "workflow", "world", "people", "persona"}},
				"description": "only these facets: interaction (working with the AI), tools, judgement (default judgements), workflow (ways of working), world (projects and facts), people, persona (about the user)"},
			"limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 30, "default": 12},
			"categories": map[string]any{"type": "array", "items": map[string]any{"type": "string",
				"enum": entry.CategoryNames},
				"description": "also return entries in these categories, when the request is about them: health, finance, relationships, emotion, legal_id (identity papers, legal matters). Empty: general entries only"},
		}}},
	{Name: "enkeep_seed_import", Description: "Import the user's own profile and rules confirmed elsewhere (another " +
		"memory, a notes file, a profile platform) into enkeep memory as seeds, in the general format: a source name and " +
		"items {id, kind (rule | guardrail | preference | fact), text, confirmed (YYYY-MM-DD), and optionally facet, hard, " +
		"category, scope (global | project:<name>), domain (work | life), effective, expiring (recent | role), sensitive}. " +
		"Previews by default: says how many items would be new, merged with an entry the curator has, updated, the same " +
		"as before, or skipped and why, writing nothing; apply: true writes. Items in a category (health, finance, " +
		"relationships, emotion, legal_id; by category, keywords and a model's reading) are imported tagged and used by " +
		"scene; items with a credential or not confirmed at the source are never imported. New seeds start as candidates and reach the exits only once the " +
		"user's own records bear them out.",
		InputSchema: map[string]any{"type": "object", "required": []string{"source", "items"}, "properties": map[string]any{
			"source": map[string]any{"type": "string", "description": "a short name of the source, without spaces or slashes (e.g. enkeep)"},
			"items": map[string]any{"type": "array", "description": "at most 500 items", "items": map[string]any{"type": "object",
				"required": []string{"id", "kind", "text", "confirmed"}, "properties": map[string]any{
					"id": map[string]any{"type": "string"}, "kind": map[string]any{"type": "string", "enum": []string{"rule", "guardrail", "preference", "fact"}},
					"text": map[string]any{"type": "string"}, "confirmed": map[string]any{"type": "string"},
					"facet": map[string]any{"type": "string", "enum": []string{"interaction", "tools", "judgement", "workflow", "world", "persona"}},
					"hard":  map[string]any{"type": "boolean"}, "category": map[string]any{"type": "string"},
					"scope": map[string]any{"type": "string"}, "domain": map[string]any{"type": "string", "enum": []string{"work", "life"}},
					"effective": map[string]any{"type": "string"}, "expiring": map[string]any{"type": "string", "enum": []string{"", "recent", "role"}},
					"sensitive": map[string]any{"type": "string"},
				}}},
			"apply": map[string]any{"type": "boolean", "default": false, "description": "false (default): preview only; true: write"},
		}}},
	{Name: "enkeep_seed_status", Description: "Show enkeep memory's seeds: whether seeds are on as a source, the seed " +
		"entries by source and status (a candidate waits for the user's own records to bear it out), by facet, how many " +
		"reach the exits now, the curator's own entries a seed confirmed, and every import written.",
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{}}},
	{Name: "enkeep_mark_wrong", Description: "Mark enkeep memory entries wrong, for example a line in the assistant's " +
		"「关于用户（由 enkeep-memory 整理）」 block that is not the user's rule (a task prompt, a relay or a test prompt " +
		"taken for the user's words). No card goes to the user's phone: the entries leave every exit at once and stay as " +
		"counter-examples, recorded as marked by the assistant with the reason; the user can mark them right on the " +
		"enkeep memory page to bring them back. Give entry ids from enkeep_recall. Deletes nothing.",
		InputSchema: map[string]any{"type": "object", "required": []string{"ids", "reason"}, "properties": map[string]any{
			"ids":    map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "1 to 20 entry ids (id:… in enkeep_recall's lines)"},
			"reason": map[string]any{"type": "string", "description": "why the entries are wrong (at most 300 characters)"},
		}}},
}

func (a *app) connector(ctx context.Context, method string, raw json.RawMessage) (any, error) {
	var in struct {
		Contribution string          `json:"contribution"`
		Name         string          `json:"name"`
		Args         json.RawMessage `json:"args"`
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &in); err != nil {
			return nil, &ext.Error{Code: "invalid", Message: "invalid connector parameters"}
		}
	}
	if in.Contribution != "" && in.Contribution != connectorID {
		return nil, ext.NoContribution(in.Contribution)
	}
	switch method {
	case "status":
		return map[string]any{"state": "ok", "account": "enkeep 记忆"}, nil
	case "tools.list":
		return map[string]any{"tools": rerunTools}, nil
	case "tools.call":
		text, err := a.callTool(ctx, in.Name, in.Args)
		if err != nil {
			return nil, &ext.Error{Code: "invalid", Message: err.Error()}
		}
		return map[string]any{"text": text}, nil
	}
	return nil, ext.UnknownMethod("connector." + method)
}

func strictDecode(raw json.RawMessage, out any) error {
	if len(raw) == 0 || string(raw) == "null" {
		raw = json.RawMessage(`{}`)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		return fmt.Errorf("invalid arguments: %w", err)
	}
	return nil
}

func (a *app) callTool(ctx context.Context, name string, raw json.RawMessage) (string, error) {
	switch name {
	case "enkeep_recall":
		return a.recallTool(raw)
	case "enkeep_mark_wrong":
		return a.markWrongTool(ctx, raw)
	case "enkeep_seed_import":
		var in struct {
			Source string            `json:"source"`
			Items  []curate.SeedItem `json:"items"`
			Apply  bool              `json:"apply"`
		}
		if err := strictDecode(raw, &in); err != nil {
			return "", err
		}
		f := curate.SeedFormat{Source: in.Source, Items: in.Items}
		if err := curate.CheckSeeds(&f); err != nil {
			return "", err
		}
		if in.Apply && !entry.On(a.seedSwitches(), entry.K21Seeds) {
			return "", errors.New("seeds are switched off as a source (K21 on the enkeep memory page): nothing imported")
		}
		r, err := a.c.ImportSeeds(ctx, f, in.Apply, toolWait)
		if err != nil {
			return "", a.busy(err)
		}
		return r.Text(), nil
	case "enkeep_seed_status":
		if err := strictDecode(raw, &struct{}{}); err != nil {
			return "", err
		}
		return a.c.Seeds(exits.Usable).Text(), nil
	case "enkeep_rerun":
		var in struct {
			From, To string
			Force    bool
		}
		if err := strictDecode(raw, &in); err != nil {
			return "", err
		}
		r, err := a.c.QueueRerunWithin(strings.TrimSpace(in.From), strings.TrimSpace(in.To), a.localDay(), in.Force, toolWait)
		if err != nil {
			return "", a.busy(err)
		}
		var b strings.Builder
		if r.Force {
			fmt.Fprintf(&b, "Queued %d day(s) with force to read again with the current prompt, even if already read with it", len(r.Queued))
		} else {
			fmt.Fprintf(&b, "Queued %d day(s) to read again with the current prompt", len(r.Queued))
		}
		if len(r.Queued) > 0 {
			fmt.Fprintf(&b, ": %s", span(r.Queued))
		}
		b.WriteString(".")
		if len(r.Already) > 0 {
			if r.Force {
				fmt.Fprintf(&b, " Already queued with force, skipped: %s.", span(r.Already))
			} else {
				fmt.Fprintf(&b, " Already done or queued, skipped: %s.", span(r.Already))
			}
		}
		b.WriteString(" They run one at a time in the background; enkeep_rerun_status shows progress.")
		return b.String(), nil
	case "enkeep_rerun_status":
		if err := strictDecode(raw, &struct{}{}); err != nil {
			return "", err
		}
		p := a.c.RerunStatus() // the pages' snapshot: never waits for a run's lock
		var b strings.Builder
		fmt.Fprintf(&b, "Prompt %s: %d day(s) done, %d waiting.", p.Prompt, len(p.Done), len(p.Waiting))
		fmt.Fprintf(&b, " Rerun progress: %d day(s) done, %d left, %d new usable entr(ies) resting on the days read since the batch was queued.",
			len(p.Done), len(p.Waiting)+len(p.Forced), p.Gain)
		if len(p.Waiting) > 0 {
			fmt.Fprintf(&b, " Waiting: %s.", span(p.Waiting))
		}
		if len(p.Forced) > 0 {
			fmt.Fprintf(&b, " Queued with force (read again although already read with this prompt), waiting: %s.", span(p.Forced))
		}
		if pr := p.Running; pr != nil {
			fmt.Fprintf(&b, " Running now: %s %s", pr.Kind, pr.Period)
			if pr.Stage != "" {
				fmt.Fprintf(&b, ", %s %d/%d", pr.Stage, pr.Done, pr.Total)
			}
			fmt.Fprintf(&b, ", since %s.", pr.Started.UTC().Format(time.RFC3339))
		} else if running := a.runningNow(); running != "" {
			fmt.Fprintf(&b, " Running now: %s.", running)
		}
		if !p.At.IsZero() {
			fmt.Fprintf(&b, " (State as of %s.)", p.At.UTC().Format(time.RFC3339))
		}
		if p.Paused != "" {
			fmt.Fprintf(&b, " Paused: %s.", p.Paused)
		}
		for _, l := range p.History {
			b.WriteString("\nSession history: " + l + ".")
		}
		if l := a.recallCounts(); l != "" {
			b.WriteString("\n" + l)
		}
		b.WriteString("\n" + a.modelText())
		return b.String(), nil
	case "enkeep_resettle":
		in := struct {
			DryRun *bool `json:"dry_run"`
		}{}
		if err := strictDecode(raw, &in); err != nil {
			return "", err
		}
		apply := in.DryRun != nil && !*in.DryRun
		r, err := a.c.ResettleWithin(a.localDay(), apply, toolWait)
		if err != nil {
			return "", a.busy(err)
		}
		return resettleText(r), nil
	}
	return "", errors.New("unknown tool " + name)
}

// seedSwitches are the switches as the pages' snapshot has them.
func (a *app) seedSwitches() map[string]entry.SwitchSetting {
	st, _, done := a.c.Read()
	defer done()
	return st.Switches
}

// modelText says the model route and what it counted (docs/model-routes.md).
func (a *app) modelText() string {
	rt := a.route
	if rt == nil {
		return "Model route: off (model_route purpose): every call on the model of the extension's purpose in the hub's model settings."
	}
	var b strings.Builder
	primary := strings.Join(rt.Primary, " → ")
	if primary == "" {
		primary = "no primary model"
	}
	fmt.Fprintf(&b, "Model route: %s; fallback above %d estimated tokens %s, else %s.", primary, rt.LongAbove, rt.Long, rt.Short)
	if rt.Stats != nil {
		snap := rt.Stats.Snapshot()
		b.WriteString("\n" + snap.Summary())
		today := a.localDay()
		yesterday := today
		if t, err := time.Parse("2006-01-02", today); err == nil {
			yesterday = t.AddDate(0, 0, -1).Format("2006-01-02")
		}
		lines := snap.CostLines(today)
		b.WriteString("\n" + strings.Join(lines, "\n"))
		b.WriteString("\n" + snap.CostLines(yesterday)[0])
	}
	out, waiting, minute := model.Shared().Stats()
	fmt.Fprintf(&b, "\nCall limit: at most %d at once and %d a minute (reruns wait behind the daily and weekly passes); now %d out, %d waiting, %d in the last minute.",
		model.LimitConcurrent, model.LimitPerMinute, out, waiting, minute)
	if l := a.c.LedgerState(); l != "" {
		b.WriteString("\nLedger (assistant.entries.list): " + l + ".")
	}
	return b.String()
}

// localDay is the local day in the curator's time zone.
func (a *app) localDay() string {
	now := time.Now
	if a.c.Now != nil {
		now = a.c.Now
	}
	return now().In(a.c.Loc).Format("2006-01-02")
}

// span says a list of days briefly: "2026-10-29..2026-11-13 (16)" when they follow each other, else the list.
func span(days []string) string {
	if len(days) == 0 {
		return ""
	}
	consecutive := true
	for i := 1; i < len(days); i++ {
		a, _ := time.Parse("2006-01-02", days[i-1])
		if a.AddDate(0, 0, 1).Format("2006-01-02") != days[i] {
			consecutive = false
			break
		}
	}
	if consecutive && len(days) > 2 {
		return fmt.Sprintf("%s..%s (%d)", days[0], days[len(days)-1], len(days))
	}
	return strings.Join(days, ", ")
}

// resettleText says a resettle's result: the five statuses before and after, the stable entries per facet, and what
// moved.
func resettleText(r curate.ResettleReport) string {
	var b strings.Builder
	if r.Applied {
		fmt.Fprintf(&b, "Applied as of %s: %d entr(ies) changed status.", r.AsOf, r.Changed())
	} else {
		fmt.Fprintf(&b, "Dry run as of %s (nothing changed): %d entr(ies) would change status.", r.AsOf, r.Changed())
	}
	fmt.Fprintf(&b, " Active days: %d", r.ActiveDays)
	if len(r.DroppedDays) > 0 {
		fmt.Fprintf(&b, ", not counted (no model output): %s", span(r.DroppedDays))
	}
	b.WriteString(".\nStatus (before → after):")
	for _, s := range []entry.Status{entry.Stable, entry.Candidate, entry.Dormant, entry.Retired, entry.Shifting} {
		fmt.Fprintf(&b, " %s %d → %d;", s, r.Before[s], r.After[s])
	}
	b.WriteString("\nStable per facet (before → after):")
	for _, f := range entry.Facets {
		fmt.Fprintf(&b, " %s %d → %d;", f, r.StableBefore[f], r.StableAfter[f])
	}
	if len(r.Moves) > 0 {
		moves := make([]string, 0, len(r.Moves))
		for m, n := range r.Moves {
			moves = append(moves, fmt.Sprintf("%s %d", m, n))
		}
		sort.Strings(moves)
		b.WriteString("\nMoves: " + strings.Join(moves, ", ") + ".")
	}
	if !r.Applied && r.Changed()+len(r.DroppedDays) > 0 {
		b.WriteString("\nCall again with dry_run false to apply.")
	}
	return b.String()
}

// recallTool answers enkeep_recall (internal/recall) and counts the call for the day: calls, empty answers, hits per
// facet and status, never the query.
func (a *app) recallTool(raw json.RawMessage) (string, error) {
	var in struct {
		Query      string   `json:"query"`
		Facets     []string `json:"facets"`
		Limit      int      `json:"limit"`
		Categories []string `json:"categories"`
	}
	if err := strictDecode(raw, &in); err != nil {
		return "", err
	}
	in.Query = strings.TrimSpace(in.Query)
	if in.Query == "" {
		return "", errors.New("query is empty")
	}
	if len([]rune(in.Query)) > 200 {
		return "", errors.New("query: at most 200 characters")
	}
	q := recall.Query{Text: in.Query, Limit: in.Limit}
	for _, f := range in.Facets {
		x, ok := recall.FacetOf(f)
		if !ok {
			return "", fmt.Errorf("unknown facet %q", f)
		}
		q.Facets = append(q.Facets, x)
	}
	for _, n := range in.Categories {
		if entry.CategoryOf(n) == "" || n == entry.SensFamily {
			return "", fmt.Errorf("unknown category %q", n)
		}
		q.Categories = append(q.Categories, n)
	}
	st, _, done := a.c.Read() // the snapshot: a run holds the store's lock for its whole pass
	res := recall.Search(st, q)
	done()
	var facets, statuses []string
	for _, h := range res.Hits {
		facets = append(facets, string(h.Facet))
		statuses = append(statuses, string(h.Status))
	}
	if err := a.recallLog().Count(a.localDay(), facets, statuses); err != nil {
		a.logf("enkeep-memory: recall counts: %v", err)
	}
	if len(res.Hits) == 0 {
		if res.Pool == 0 {
			return "enkeep memory has no curated entries to look in yet.", nil
		}
		return fmt.Sprintf("Nothing in enkeep memory matches %q (%d entries searched). Try a shorter name or another word.", in.Query, res.Pool), nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d line(s) from enkeep memory, best match first (稳定 = settled; 候选/较早 = less certain):\n", len(res.Hits))
	for _, h := range res.Hits {
		b.WriteString(recall.Format(h))
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

// recallLog is the counts' file beside the state (store.RecallLog), opened on first use when init did not.
func (a *app) recallLog() *store.RecallLog {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.recalls == nil {
		a.recalls = store.OpenRecall(a.c.St.Dir, nil)
	}
	return a.recalls
}

// recallCounts says enkeep_recall's counts of the last 7 days, for enkeep_rerun_status: "" when it was never called.
func (a *app) recallCounts() string {
	keys, days := a.recallLog().Days(7)
	if len(keys) == 0 {
		return ""
	}
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = fmt.Sprintf("%s %d call(s), %d empty", k, days[i].Calls, days[i].Empty)
	}
	return "enkeep_recall: " + strings.Join(parts, "; ") + "."
}

// markWrongTool answers enkeep_mark_wrong: each entry marked wrong by the assistant with the reason, then the exits
// compiled at once without the entries (as the page's "不对" does) and the state saved.
func (a *app) markWrongTool(ctx context.Context, raw json.RawMessage) (string, error) {
	var in struct {
		IDs    []string `json:"ids"`
		Reason string   `json:"reason"`
	}
	if err := strictDecode(raw, &in); err != nil {
		return "", err
	}
	in.Reason = strings.TrimSpace(in.Reason)
	if len(in.IDs) == 0 || len(in.IDs) > 20 {
		return "", errors.New("ids: 1 to 20 entry ids")
	}
	if in.Reason == "" || len([]rune(in.Reason)) > 300 {
		return "", errors.New("reason: 1 to 300 characters")
	}
	for i, id := range in.IDs {
		in.IDs[i] = strings.TrimPrefix(strings.TrimSpace(id), "id:")
	}
	if !a.c.St.LockWithin(toolWait) {
		return a.queueMarks(in.IDs, in.Reason)
	}
	defer a.c.St.Unlock()
	var marked, missing []string
	for _, id := range in.IDs {
		e, err := a.c.MarkWrongBy(id, "assistant", in.Reason)
		if err != nil {
			missing = append(missing, id)
			continue
		}
		marked = append(marked, fmt.Sprintf("%s (%s)", id, cut(recall.Line(e), 60)))
	}
	if len(marked) > 0 {
		a.withdraw(ctx)
		if err := a.c.St.Save(); err != nil {
			return "", err
		}
		a.c.Publish()
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Marked wrong: %d entr(ies)", len(marked))
	if len(marked) > 0 {
		b.WriteString(": " + strings.Join(marked, "; "))
	}
	b.WriteString(".")
	if len(missing) > 0 {
		fmt.Fprintf(&b, " Not found: %s.", strings.Join(missing, ", "))
	}
	if len(marked) > 0 {
		b.WriteString(" They are out of the exits now (the assistant's block follows the hub's own schedule); the user can mark them right on the enkeep memory page.")
	}
	return b.String(), nil
}

func cut(s string, n int) string {
	r := []rune(strings.Join(strings.Fields(s), " "))
	if len(r) <= n {
		return string(r)
	}
	return string(r[:n]) + "…"
}
