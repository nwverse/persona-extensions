package main

import (
	"context"
	"encoding/json"
	"github.com/nwverse/persona/sdk/go/ext"
	"github.com/nwverse/persona/sdk/go/exttest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/curate"
	"github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/entry"
	"github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/kbsrc"
	"github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/model"
	"github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/schema"
	"github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/store"
	"github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/stub"
)

func toolApp(t *testing.T) (*app, *store.Store) {
	t.Helper()
	fx, err := kbsrc.LoadFixture("testdata/sample")
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cfg := curate.Defaults()
	cfg.Mode, cfg.AdmitAllBelowTokens, cfg.BackfillDays = "shadow", 0, 0
	now := time.Date(2026, 11, 9, 4, 0, 0, 0, time.UTC)
	a := newApp()
	a.c = &curate.Curator{Src: fx, Model: &stub.Model{}, St: st, Cfg: cfg, Loc: time.UTC, Now: func() time.Time { return now }}
	return a, st
}

func call(t *testing.T, a *app, name string, args any) (string, error) {
	t.Helper()
	raw, _ := json.Marshal(map[string]any{"contribution": connectorID, "name": name, "args": args})
	out, err := a.connector(t.Context(), "tools.call", raw)
	if err != nil {
		return "", err
	}
	return out.(map[string]any)["text"].(string), nil
}

// The connector lists seven tools; the rerun tool queues the days once, refuses days not over and more than 31, and the
// status tool says what is done and what waits.
func TestRerunTool(t *testing.T) {
	a, st := toolApp(t)
	list, err := a.connector(t.Context(), "tools.list", nil)
	if err != nil || len(list.(map[string]any)["tools"].([]tool)) != 7 {
		t.Fatalf("tools.list: %v %v", list, err)
	}
	st.S.Reruns = []string{"2026-11-03@" + schema.DailyPrompt} // done with the current prompt already
	text, err := call(t, a, "enkeep_rerun", map[string]string{"from": "2026-11-02", "to": "2026-11-04"})
	if err != nil || !strings.Contains(text, "Queued 2 day(s)") || !strings.Contains(text, "skipped: 2026-11-03") {
		t.Fatalf("rerun: %q %v", text, err)
	}
	if !slices.Equal(st.S.RerunQueue, []string{"2026-11-02", "2026-11-04"}) {
		t.Fatalf("queue: %v", st.S.RerunQueue)
	}
	if text, _ := call(t, a, "enkeep_rerun", map[string]string{"from": "2026-11-02", "to": "2026-11-02"}); !strings.Contains(text, "Queued 0") {
		t.Fatalf("asked twice: %q", text)
	}
	for _, bad := range []map[string]string{
		{"from": "2026-11-08", "to": "2026-11-09"},           // today is not over
		{"from": "2026-09-01", "to": "2026-10-15"},           // more than 31 days
		{"from": "2026-11-04", "to": "2026-11-02"},           // backwards
		{"from": "yesterday", "to": "2026-11-02"},            // not a day
		{"from": "2026-11-01", "to": "2026-11-02", "x": "y"}, // unknown field
	} {
		if _, err := call(t, a, "enkeep_rerun", bad); err == nil {
			t.Errorf("accepted %v", bad)
		}
	}
	status, err := call(t, a, "enkeep_rerun_status", map[string]any{})
	if err != nil || !strings.Contains(status, "0 day(s) done, 2 waiting") || !strings.Contains(status, "2026-11-02, 2026-11-04") {
		t.Fatalf("status: %q %v", status, err)
	}
	if !strings.Contains(status, "Model route: off") {
		t.Fatalf("status without a route: %q", status)
	}
	a.route = curate.Defaults().Route()
	a.route.Stats = model.OpenStats(t.TempDir(), time.Date(2026, 11, 18, 0, 0, 0, 0, time.UTC))
	status, _ = call(t, a, "enkeep_rerun_status", map[string]any{})
	for _, w := range []string{"Model route: no primary model; fallback above 16000 estimated tokens claude-sonnet-5-5, else claude-haiku-5-5.",
		"Model calls since 2026-11-18T00:00:00Z: 0, answered by a fallback 0 (0.0%)",
		// design v2 §4.7: the cost lines, the call limit and the reruns' progress.
		"Cost 2026-11-09: 0 call(s)", "Reruns since", "Call limit: at most 4 at once and 20 a minute",
		"Rerun progress: 0 day(s) done, 2 left, 0 new usable"} {
		if !strings.Contains(status, w) {
			t.Fatalf("status lacks %q: %q", w, status)
		}
	}
}

// Days the tool queued run like rerun_range's, once each, and the status says they are done.
func TestRerunToolDaysRun(t *testing.T) {
	a, st := toolApp(t)
	if _, err := call(t, a, "enkeep_rerun", map[string]string{"from": "2026-11-03", "to": "2026-11-04"}); err != nil {
		t.Fatal(err)
	}
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
	runs := 0
	for _, r := range st.S.Runs {
		if r.Kind == "daily" && (r.Period == "2026-11-03" || r.Period == "2026-11-04") {
			runs++
		}
	}
	if runs != 2 {
		t.Errorf("each queued day once, got %d runs", runs)
	}
	// Once they are digested and filed they leave the queue; their markers stay, so asking again queues nothing.
	if len(st.S.RerunQueue) != 0 {
		t.Errorf("queue after the reruns: %v", st.S.RerunQueue)
	}
	status, _ := call(t, a, "enkeep_rerun_status", map[string]any{})
	if !strings.Contains(status, "0 day(s) done, 0 waiting") {
		t.Errorf("status: %q", status)
	}
	if text, _ := call(t, a, "enkeep_rerun", map[string]string{"from": "2026-11-03", "to": "2026-11-04"}); !strings.Contains(text, "Queued 0") {
		t.Errorf("asked again: %q", text)
	}
}

// The resettle tool reports a dry run by default, applies on dry_run false, and refuses unknown arguments.
func TestResettleTool(t *testing.T) {
	a, st := toolApp(t)
	st.S.ActiveDays = []string{"2026-11-01", "2026-11-02"}
	st.S.Entries = map[string]*entry.Entry{"e": {ID: "e", Grain: entry.Action, Facet: entry.Workflow, Origin: entry.Behavior,
		Status: entry.Dormant, First: "2026-11-01", Last: "2026-11-02",
		Evidence: []entry.Evidence{{Ref: "chats/a", Day: "2026-11-01"}, {Ref: "chats/b", Day: "2026-11-02"}}}}
	text, err := call(t, a, "enkeep_resettle", map[string]any{})
	if err != nil || !strings.Contains(text, "Dry run as of 2026-11-09") || !strings.Contains(text, "dormant 1 → 0") ||
		!strings.Contains(text, "workflow 0 → 1") || !strings.Contains(text, "dormant→stable 1") {
		t.Fatalf("dry run: %q %v", text, err)
	}
	if st.S.Entries["e"].Status != entry.Dormant {
		t.Fatal("the dry run changed the entry")
	}
	if text, err = call(t, a, "enkeep_resettle", map[string]any{"dry_run": false}); err != nil || !strings.Contains(text, "Applied") ||
		st.S.Entries["e"].Status != entry.Stable {
		t.Fatalf("apply: %q %v", text, err)
	}
	if text, _ = call(t, a, "enkeep_resettle", map[string]any{"dry_run": false}); !strings.Contains(text, "0 entr(ies) changed") {
		t.Fatalf("again: %q", text)
	}
	if _, err := call(t, a, "enkeep_resettle", map[string]any{"apply": true}); err == nil {
		t.Fatal("accepted an unknown argument")
	}
}

// The program says it has the manifest's connector, so the hub does not fall back to the manifest with a warning.
func TestInitDeclaresTheConnector(t *testing.T) {
	var got []map[string]any
	if err := json.Unmarshal(manifestConnectors(), &got); err != nil || len(got) != 1 || got[0]["id"] != connectorID {
		t.Fatalf("connectors: %s %v", manifestConnectors(), err)
	}
	caps, _ := got[0]["capabilities"].([]any)
	if !slices.Contains(caps, any("tools")) {
		t.Fatalf("capabilities: %v", caps)
	}
}

// The hub's own connector checks (tool names start with the connector's id and "_", each has a description and an
// object schema, status answers): what keeps the tools on the assistant's list.
func TestConnectorConformance(t *testing.T) {
	a, _ := toolApp(t)
	// The connector side only: the full program's Init starts the passes, which would still write to the test's
	// directories when it is cleaned up.
	p := program(a)
	p.Init = func(context.Context, *ext.Init, *ext.Hub) (map[string]any, error) {
		return map[string]any{"connectors": manifestConnectors()}, nil
	}
	exttest.CheckConnectors(t, manifest, exttest.Serve(t, p))
}

// force queues a day already read with the current prompt; it runs again once, then leaves the force list; the status
// says which days wait with force.
func TestRerunToolForce(t *testing.T) {
	a, st := toolApp(t)
	st.S.Reruns = []string{"2026-11-03@" + schema.DailyPrompt}
	if text, _ := call(t, a, "enkeep_rerun", map[string]string{"from": "2026-11-03", "to": "2026-11-03"}); !strings.Contains(text, "Queued 0") {
		t.Fatalf("without force: %q", text)
	}
	text, err := call(t, a, "enkeep_rerun", map[string]any{"from": "2026-11-03", "to": "2026-11-03", "force": true})
	if err != nil || !strings.Contains(text, "Queued 1 day(s) with force") || !slices.Equal(st.S.ForceReruns, []string{"2026-11-03"}) {
		t.Fatalf("force: %q %v %v", text, err, st.S.ForceReruns)
	}
	if text, _ := call(t, a, "enkeep_rerun", map[string]any{"from": "2026-11-03", "to": "2026-11-03", "force": true}); !strings.Contains(text, "Already queued with force") {
		t.Fatalf("force twice: %q", text)
	}
	if _, err := call(t, a, "enkeep_rerun", map[string]any{"from": "2026-11-09", "to": "2026-11-09", "force": true}); err == nil {
		t.Fatal("force accepted a day that is not over")
	}
	status, _ := call(t, a, "enkeep_rerun_status", map[string]any{})
	if !strings.Contains(status, "Queued with force (read again although already read with this prompt), waiting: 2026-11-03.") {
		t.Fatalf("status: %q", status)
	}
	for i := 0; i < 8 && len(st.S.ForceReruns) > 0; i++ {
		a.tick(t.Context())
		for j := 0; j < 600; j++ {
			a.mu.Lock()
			idle := a.running == ""
			a.mu.Unlock()
			if idle {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	runs := 0
	for _, r := range st.S.Runs {
		if r.Kind == "daily" && r.Period == "2026-11-03" {
			runs++
		}
	}
	if runs != 1 || len(st.S.ForceReruns) != 0 {
		t.Fatalf("forced day: %d run(s), force list %v", runs, st.S.ForceReruns)
	}
}

// enkeep_recall finds an entry by a project name and counts the call, never the query; a card's evaluation is not
// returned. enkeep_mark_wrong takes an entry out by id, recorded as the assistant's, with no card.
func TestRecallAndMarkWrongTools(t *testing.T) {
	a, st := toolApp(t)
	add := func(id string, f entry.Facet, text string) *entry.Entry {
		e := &entry.Entry{ID: id, Facet: f, Grain: entry.Class, Text: text, Status: entry.Stable, Origin: entry.Behavior,
			Count: 2, Kinds: []string{"S3"}, Last: "2026-11-08"}
		st.S.Entries[id] = e
		return e
	}
	if st.S.Entries == nil {
		st.S.Entries = map[string]*entry.Entry{}
	}
	w := add("o-0000000001", entry.World, "占位项目甲的发布窗口在每周四晚上")
	w.Subject = "project:占位项目甲"
	add("i-0000000001", entry.Interaction, "部署验证时只回复 OK（占位错条目）")
	ev := add("p-0000000001", entry.People, "占位项目甲的负责人做事拖沓（占位评价）")
	ev.Part = entry.PartUserEval

	text, err := call(t, a, "enkeep_recall", map[string]any{"query": "占位项目甲"})
	if err != nil || !strings.Contains(text, "发布窗口在每周四晚上") || !strings.Contains(text, "id:o-0000000001") || strings.Contains(text, "拖沓") {
		t.Fatalf("recall: %q %v", text, err)
	}
	if _, err := call(t, a, "enkeep_recall", map[string]any{"query": "完全无关的占位词语"}); err != nil {
		t.Fatal(err)
	}
	if _, err := call(t, a, "enkeep_recall", map[string]any{"query": "x", "facets": []string{"nope"}}); err == nil {
		t.Fatal("an unknown facet was taken")
	}
	keys, days := a.recallLog().Days(7)
	if len(keys) != 1 || keys[0] != "2026-11-09" || days[0].Calls != 2 || days[0].Empty != 1 || days[0].Facets["world"] == 0 {
		t.Fatalf("recall counts: %v %+v", keys, days)
	}
	raw, _ := os.ReadFile(filepath.Join(st.Dir, store.RecallFile))
	if !strings.Contains(string(raw), `"calls":2`) || strings.Contains(string(raw), "占位项目甲") {
		t.Fatalf("recall.json: %s", raw)
	}
	if st.S.Recall != nil {
		t.Fatal("counts kept in the state")
	}

	text, err = call(t, a, "enkeep_mark_wrong", map[string]any{"ids": []string{"id:i-0000000001", "i-missing"}, "reason": "测试提示，不是用户的规矩"})
	if err != nil || !strings.Contains(text, "Marked wrong: 1") || !strings.Contains(text, "Not found: i-missing") {
		t.Fatalf("mark_wrong: %q %v", text, err)
	}
	e := st.S.Entries["i-0000000001"]
	if e.Live() || e.Verdicts[len(e.Verdicts)-1].By != "assistant" || e.Verdicts[len(e.Verdicts)-1].Note == "" {
		t.Fatalf("entry after mark_wrong: %+v", e.Verdicts)
	}
	if _, err := call(t, a, "enkeep_mark_wrong", map[string]any{"ids": []string{"i-0000000001"}}); err == nil {
		t.Fatal("a mark without a reason was taken")
	}
	text, _ = call(t, a, "enkeep_rerun_status", map[string]any{})
	if !strings.Contains(text, "enkeep_recall: 2026-11-09 2 call(s), 1 empty") {
		t.Fatalf("status lacks the recall counts: %q", text)
	}
}

// While a pass holds the store's lock (a daily run holds it for tens of minutes), the tools answer at once: the status
// and recall from the pages' snapshot, mark_wrong queued and applied when the lock frees, rerun and resettle "busy".
func TestToolsWhileAPassRuns(t *testing.T) {
	a, st := toolApp(t)
	toolWait = 100 * time.Millisecond
	t.Cleanup(func() { toolWait = 5 * time.Second })
	a.c.Live = &curate.Live{}
	st.S.Entries = map[string]*entry.Entry{"i-0000000001": {ID: "i-0000000001", Facet: entry.Interaction, Grain: entry.Class,
		Text: "部署验证时只回复 OK（占位错条目）", Status: entry.Stable, Origin: entry.Behavior, Count: 2, Kinds: []string{"S3"}, Last: "2026-11-08"}}
	st.S.RerunQueue = []string{"2026-11-01"}
	st.Lock()
	a.c.Publish()
	// the pass starts: it holds the lock from here
	a.mu.Lock()
	a.running = "daily 2026-11-08"
	a.mu.Unlock()

	quick := func(name string, args any) (string, error) {
		t.Helper()
		start := time.Now()
		text, err := call(t, a, name, args)
		if d := time.Since(start); d > 2*time.Second {
			t.Fatalf("%s waited %v for the run", name, d)
		}
		return text, err
	}
	text, err := quick("enkeep_rerun_status", map[string]any{})
	if err != nil || !strings.Contains(text, "1 waiting") || !strings.Contains(text, "Running now: daily 2026-11-08") || !strings.Contains(text, "State as of") {
		t.Fatalf("status during a run: %q %v", text, err)
	}
	if text, err = quick("enkeep_recall", map[string]any{"query": "部署验证"}); err != nil || !strings.Contains(text, "id:i-0000000001") {
		t.Fatalf("recall during a run: %q %v", text, err)
	}
	if _, err = quick("enkeep_rerun", map[string]any{"from": "2026-11-02", "to": "2026-11-02"}); err == nil || !strings.Contains(err.Error(), "busy: daily 2026-11-08") {
		t.Fatalf("rerun during a run: %v", err)
	}
	if _, err = quick("enkeep_resettle", map[string]any{}); err == nil || !strings.Contains(err.Error(), "busy") {
		t.Fatalf("resettle during a run: %v", err)
	}
	text, err = quick("enkeep_mark_wrong", map[string]any{"ids": []string{"i-0000000001", "i-missing"}, "reason": "测试提示，不是用户的规矩"})
	if err != nil || !strings.Contains(text, "Queued") || !strings.Contains(text, "i-missing") {
		t.Fatalf("mark_wrong during a run: %q %v", text, err)
	}
	if !st.S.Entries["i-0000000001"].Live() {
		t.Fatal("marked while the run held the lock")
	}
	// the pass ends: the queued mark is applied
	a.mu.Lock()
	a.running = ""
	a.mu.Unlock()
	st.Unlock()
	deadline := time.Now().Add(5 * time.Second)
	for {
		st.Lock()
		live := st.S.Entries["i-0000000001"].Live()
		st.Unlock()
		_, qerr := os.Stat(filepath.Join(st.Dir, marksFile))
		if !live && os.IsNotExist(qerr) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("queued mark not applied: live=%v queue=%v", live, qerr)
		}
		time.Sleep(20 * time.Millisecond)
	}
	st.Lock()
	v := st.S.Entries["i-0000000001"].Verdicts
	st.Unlock()
	if v[len(v)-1].By != "assistant" || v[len(v)-1].Note == "" {
		t.Fatalf("queued verdict: %+v", v)
	}
}
