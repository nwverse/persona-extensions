package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/entry"
	"github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/memsrc"
	"github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/overview"
	"github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/store"
)

// Synthetic fixtures only.
func TestCollabMemoryCands(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("in.md", "---\nname: a\nmetadata:\n  type: feedback\n---\n\n11-20 09:10, the user said: just go ahead.\n")
	write("early.md", "---\nname: b\nmetadata:\n  type: feedback\n---\n\n11-02 the user said something.\n")
	write("ref.md", "---\nname: c\nmetadata:\n  type: reference\n---\n\n11-20 a pointer.\n")
	cs, err := collabMemoryCands(dir, "2026-11-16", "2026-11-24")
	if err != nil || len(cs) != 1 || cs[0].Day != "2026-11-20" || cs[0].Source != "memory" || strings.Contains(cs[0].Text, "type: feedback") {
		t.Fatalf("%v %+v", err, cs)
	}
}

// A correction in range becomes a candidate with the request before it and the assistant's move between; a line
// without a cue, out of range, or with no move before it does not.
func TestCollabSessionCands(t *testing.T) {
	dir := t.TempDir()
	jsonl := strings.Join([]string{
		`{"type":"user","timestamp":"2026-11-20T01:00:00Z","message":{"content":"clean up the disk"}}`,
		`{"type":"assistant","timestamp":"2026-11-20T01:00:05Z","message":{"content":[{"type":"text","text":"Shall I start?"},{"type":"tool_use","name":"AskUser","input":{"q":"start?"}}]}}`,
		`{"type":"user","timestamp":"2026-11-20T01:01:00Z","message":{"content":"我说过了，直接做"}}`,
		`{"type":"user","timestamp":"2026-11-20T01:02:00Z","message":{"content":"thanks"}}`,
		`{"type":"user","timestamp":"2026-11-01T01:02:00Z","message":{"content":"别再问了"}}`,
		`{"type":"user","timestamp":"2026-11-20T01:03:00Z","message":{"content":"<system-reminder>又</system-reminder>"}}`,
	}, "\n")
	if err := os.WriteFile(filepath.Join(dir, "s.jsonl"), []byte(jsonl), 0o600); err != nil {
		t.Fatal(err)
	}
	cs, err := collabSessionCands([]string{dir}, "2026-11-16", "2026-11-24")
	if err != nil || len(cs) != 1 {
		t.Fatalf("%v %+v", err, cs)
	}
	c := cs[0].Text
	if !strings.Contains(c, "clean up the disk") || !strings.Contains(c, "[AskUser") || !strings.Contains(c, "我说过了，直接做") {
		t.Errorf("context %q", c)
	}
}

func TestCollabTableEntity(t *testing.T) {
	r := collabResult{Model: "m", Judge: "j", N: 2, Repeats: 1, Entity: []string{"a"},
		Score:  map[string]map[string]map[string]float64{"1400": {"resident": {"avoided": 0.5}}},
		Bytes:  map[string]map[string]int{"1400": {"resident": 1400}},
		PerInc: map[string]map[string]map[string]float64{"1400": {"resident": {"a": 1, "b": 0}}}}
	md := collabTable(r, []string{"resident"})
	if !strings.Contains(md, "entity incidents avoided (1)") || !strings.Contains(md, "| 0/2/0 | 100% |") {
		t.Fatalf("entity column missing:\n%s", md)
	}
	if !collabCue.MatchString("你又搞混人了，这个不是他负责的") {
		t.Fatal("entity cue not matched")
	}
}

// The recall arms bring in what the incident's names point at, before the day; the ablation leaves out the world and
// people entries.
func TestCollabRecall(t *testing.T) {
	st := &store.State{Entries: map[string]*entry.Entry{
		"w-own":  {ID: "w-own", Facet: entry.World, Predicate: "owns", Object: "project-x", Text: "user-b is responsible for project-x", Status: entry.Stable, Count: 3, First: "2026-11-01", Last: "2026-11-10", Tagged: true, Origin: entry.Behavior},
		"j-rule": {ID: "j-rule", Facet: entry.Judgement, Text: "for project-x, release only after the gate", Status: entry.Stable, Count: 3, First: "2026-11-01", Last: "2026-11-10", Tagged: true, Origin: entry.Behavior, Scopes: []entry.Scope{{Kind: entry.ScopeProject, Name: "project-x"}}},
		"j-late": {ID: "j-late", Facet: entry.Judgement, Text: "for project-x, a rule learnt later", Status: entry.Stable, Count: 3, First: "2026-11-20", Last: "2026-11-20", Tagged: true, Origin: entry.Behavior, Scopes: []entry.Scope{{Kind: entry.ScopeProject, Name: "project-x"}}},
	}}
	f := heFilter{leak: map[string]bool{}, units: map[string]bool{}}
	idx := collabNames(st)
	all := idx.recall(st, f, "2026-11-16", "the user asks who should review project-x", "", 2048, true)
	if !strings.Contains(all, "user-b is responsible") || !strings.Contains(all, "release only after the gate") || strings.Contains(all, "learnt later") {
		t.Fatalf("recall = %q", all)
	}
	noent := idx.recall(st, f, "2026-11-16", "the user asks who should review project-x", "", 2048, false)
	if strings.Contains(noent, "user-b is responsible") || !strings.Contains(noent, "release only after the gate") {
		t.Fatalf("ablation = %q", noent)
	}
}

// A world fact that names a repository only in its text is found by that name; a name behind many entries weighs
// less than the repository's own.
func TestCollabRecallByIdentifier(t *testing.T) {
	st := &store.State{Entries: map[string]*entry.Entry{
		"w-repo": {ID: "w-repo", Facet: entry.World, Type: "decision", Text: "repo-shared-x is a shared repository; personal parts get their own", Status: entry.Stable, Count: 1, First: "2026-11-01", Last: "2026-11-01", Tagged: true, Origin: entry.Behavior},
	}}
	for i := 0; i < 30; i++ {
		id := "j-" + strings.Repeat("a", i+1)
		st.Entries[id] = &entry.Entry{ID: id, Facet: entry.Judgement, Text: "general rule " + id, Status: entry.Stable, Count: 5, First: "2026-11-01", Last: "2026-11-10", Tagged: true, Origin: entry.Behavior, Scopes: []entry.Scope{{Kind: entry.ScopeProject, Name: "platform-x"}}}
	}
	f := heFilter{leak: map[string]bool{}, units: map[string]bool{}}
	got := collabNames(st).recall(st, f, "2026-11-16", "the user asks whether to create a repo next to repo-shared-x on platform-x", "", 200, true)
	if !strings.HasPrefix(got, "- repo-shared-x is a shared repository") {
		t.Fatalf("recall = %q", got)
	}
}

func TestHardEvalPatternOutRespectsCutoff(t *testing.T) {
	st := &store.State{Entries: map[string]*entry.Entry{
		"e-before": {
			ID: "e-before", Facet: entry.Judgement, Text: "prefer explicit tests before refactoring",
			Status: entry.Stable, Count: 4, First: "2026-03-01", Last: "2026-03-25",
			Tagged: true, Origin: entry.Behavior,
		},
		"e-future": {
			ID: "e-future", Facet: entry.Judgement, Text: "future rule discovered after cutoff",
			Status: entry.Stable, Count: 4, First: "2026-03-20", Last: "2026-03-20",
			Tagged: true, Origin: entry.Behavior,
		},
	}}
	ov := &overview.State{Patterns: map[string]*overview.Pattern{
		"ov:before": {
			ID: "ov:before", Facet: string(entry.Judgement), Lifecycle: overview.Core,
			Text: "keep pre-cutoff pattern even if sighted later", EvidenceIDs: []string{"e-before"},
			ScenesCount: 2, SourcesCount: 3, LastEvidence: "2026-03-25",
		},
		"ov:future": {
			ID: "ov:future", Facet: string(entry.Judgement), Lifecycle: overview.Core,
			Text: "leaked post-cutoff pattern text", EvidenceIDs: []string{"e-future"},
			ScenesCount: 2, SourcesCount: 3, LastEvidence: "2026-03-20",
		},
		"ov:pruned-future": {
			ID: "ov:pruned-future", Facet: string(entry.Judgement), Lifecycle: overview.Core,
			Text: "leaked pruned post-cutoff pattern", EvidenceIDs: []string{"e-gone"},
			ScenesCount: 2, SourcesCount: 3, LastEvidence: "2026-03-20",
		},
		"ov:mixed": {
			ID: "ov:mixed", Facet: string(entry.Judgement), Lifecycle: overview.Core,
			Text: "leaked mixed pre-and-post-cutoff pattern", EvidenceIDs: []string{"e-before", "e-future"},
			ScenesCount: 2, SourcesCount: 3, LastEvidence: "2026-03-20",
		},
	}}
	f := heFilter{leak: map[string]bool{}, units: map[string]bool{}}
	mems := heMemories(st, ov, nil, f, "2026-03-10", 8000)
	for _, arm := range []string{"overview", "resident"} {
		if strings.Contains(mems[arm], "leaked post-cutoff pattern text") {
			t.Errorf("%s arm leaked post-cutoff pattern:\n%s", arm, mems[arm])
		}
		if strings.Contains(mems[arm], "leaked pruned post-cutoff pattern") {
			t.Errorf("%s arm leaked entry-less post-cutoff pattern:\n%s", arm, mems[arm])
		}
		if strings.Contains(mems[arm], "leaked mixed pre-and-post-cutoff pattern") {
			t.Errorf("%s arm leaked mixed pre/post-cutoff pattern:\n%s", arm, mems[arm])
		}
		if !strings.Contains(mems[arm], "keep pre-cutoff pattern") {
			t.Errorf("%s arm dropped legitimate pre-cutoff pattern:\n%s", arm, mems[arm])
		}
	}
	// Empty cutoff means no cutoff: both entry-backed and entry-less patterns are kept.
	if f.patternOut(st, ov.Patterns["ov:before"], "") {
		t.Error("empty cutoff unexpectedly excluded entry-backed pattern")
	}
	if f.patternOut(st, ov.Patterns["ov:pruned-future"], "") {
		t.Error("empty cutoff unexpectedly excluded entry-less pattern")
	}
}

func TestCollabWindowsAndExclude(t *testing.T) {
	ws, err := parseCollabWindows("2026-11-01..2026-11-15,2026-11-25..2026-11-26", "", "")
	if err != nil || len(ws) != 2 {
		t.Fatalf("parseCollabWindows: %v %+v", err, ws)
	}
	if w, ok := windowFor("2026-11-10", ws); !ok || w != "2026-11-01..2026-11-15" {
		t.Fatalf("expected window 1, got %q %v", w, ok)
	}
	if _, ok := windowFor("2026-11-20", ws); ok {
		t.Fatal("dev-window day unexpectedly matched holdout windows")
	}
	if w, ok := windowFor("2026-11-25", ws); !ok || w != "2026-11-25..2026-11-26" {
		t.Fatalf("expected window 2, got %q %v", w, ok)
	}

	// Balanced candidate cap keeps both windows represented.
	var cands []collabCand
	for i := 0; i < 6; i++ {
		cands = append(cands, collabCand{ID: fmt.Sprintf("a-%d", i), Window: "2026-11-01..2026-11-15"})
	}
	for i := 0; i < 6; i++ {
		cands = append(cands, collabCand{ID: fmt.Sprintf("b-%d", i), Window: "2026-11-25..2026-11-26"})
	}
	bal := collabBalanceCands(cands, ws, 4)
	if len(bal) != 4 {
		t.Fatalf("got %d balanced cands", len(bal))
	}
	cnt := map[string]int{}
	for _, c := range bal {
		cnt[c.Window]++
	}
	if cnt["2026-11-01..2026-11-15"] != 2 || cnt["2026-11-25..2026-11-26"] != 2 {
		t.Fatalf("unbalanced windows: %+v", cnt)
	}

	// Exclusion by ID and by same-day want Jaccard.
	dir := t.TempDir()
	devPath := filepath.Join(dir, "dev.json")
	devSet := collabSet{
		Incidents: []collabIncident{
			{ID: "s-dev001", Day: "2026-11-25", Want: "派活前先检查目标仓库的权限与分支状态"},
		},
	}
	raw, _ := json.Marshal(devSet)
	if err := os.WriteFile(devPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	ex, err := loadCollabExclusions(devPath)
	if err != nil {
		t.Fatal(err)
	}
	if !ex.matches(collabIncident{ID: "s-dev001", Day: "2026-11-02", Want: "different"}) {
		t.Error("expected exact ID exclusion")
	}
	if !ex.matches(collabIncident{ID: "m-other", Day: "2026-11-25", Want: "派活前先检查目标仓库的权限与分支"}) {
		t.Error("expected same-day want exclusion")
	}
	if ex.matches(collabIncident{ID: "m-other", Day: "2026-11-26", Want: "派活前先检查目标仓库的权限与分支状态"}) {
		t.Error("different day should not exclude by want")
	}
}

func TestCollabEntityCue(t *testing.T) {
	for _, s := range []string{
		"你搞错仓库了，不是这个库",
		"这个任务依赖另一个服务，归属搞混了",
		"派错主管了，应该找负责网关的同学",
		"哪台机器跑门禁，主机名弄错了",
	} {
		if !collabEntityCue.MatchString(s) {
			t.Errorf("collabEntityCue missed %q", s)
		}
	}
}

func TestHubDecisionsQuery(t *testing.T) {
	st := &store.State{Entries: map[string]*entry.Entry{
		"e-dispatch": {
			ID: "e-dispatch", Facet: entry.Interaction, Text: "for project-x, verify branch before dispatch",
			Status: entry.Stable, Count: 5, First: "2026-11-01", Last: "2026-11-10", Tagged: true, Origin: entry.Behavior,
			Scopes: []entry.Scope{{Kind: entry.ScopeProject, Name: "project-x"}},
			Impact: entry.Impact{Moment: "dispatch"},
		},
		"e-future": {
			ID: "e-future", Facet: entry.Interaction, Text: "project-x future rule",
			Status: entry.Stable, Count: 5, First: "2026-11-20", Last: "2026-11-20", Tagged: true, Origin: entry.Behavior,
			Scopes: []entry.Scope{{Kind: entry.ScopeProject, Name: "project-x"}},
		},
		"e-sensitive": {
			ID: "e-sensitive", Facet: entry.Interaction, Text: "project-x private finance rule",
			Status: entry.Stable, Count: 5, First: "2026-11-01", Last: "2026-11-10", Tagged: true, Origin: entry.Behavior,
			Categories: []string{"finance"},
		},
	}}
	f := heFilter{leak: map[string]bool{}, units: map[string]bool{}}
	page := collabHubPage1(st, nil, f, "2026-11-15", 4096)
	// Multi-word AND across subject ("project-x") and occasion word ("派活"): must match e-dispatch only.
	got := hubDecisionsQuery(page, []string{"project-x 派活"}, decisionsQueryMaxBytes)
	if !strings.Contains(got, "[e-dispatch]") {
		t.Fatalf("expected e-dispatch in hubDecisionsQuery result, got %q", got)
	}
	if strings.Contains(got, "e-future") || strings.Contains(got, "e-sensitive") {
		t.Fatalf("leaked future or sensitive entry: %q", got)
	}
	// Missing second AND term must not match.
	if none := hubDecisionsQuery(page, []string{"project-x nonexistent-term"}, decisionsQueryMaxBytes); none != "" {
		t.Fatalf("expected empty result for unmatched AND term, got %q", none)
	}
	// Truncation at maxBytes.
	var big []memsrc.Entry
	for i := 0; i < 40; i++ {
		big = append(big, memsrc.Entry{
			ID:   fmt.Sprintf("e-%02d", i),
			Text: "project-x " + strings.Repeat("x", 120),
		})
	}
	clipped := hubDecisionsQuery(big, []string{"project-x"}, 500)
	if len(clipped) == 0 || len(clipped) > 500 {
		t.Fatalf("expected truncated output <= 500 bytes, got %d", len(clipped))
	}
}

func TestCollabTableSegments(t *testing.T) {
	r := collabResult{
		Model: "m", Judge: "j", N: 3, Repeats: 1,
		Score:  map[string]map[string]map[string]float64{"4096": {"resident": {"avoided": 0.5}}},
		Bytes:  map[string]map[string]int{"4096": {"resident": 4000}},
		PerInc: map[string]map[string]map[string]float64{"4096": {"resident": {"a": 1, "b": 0, "c": 0.5}}},
		SegmentN: map[string]int{
			"2026-11-01..2026-11-15": 2,
			"2026-11-25..2026-11-26": 1,
		},
		Segments: map[string]map[string]map[string]map[string]float64{
			"2026-11-01..2026-11-15": {"4096": {"resident": {"avoided": 0.5, "wrong_action": 0.25}}},
			"2026-11-25..2026-11-26": {"4096": {"resident": {"avoided": 1.0, "wrong_action": 0.0}}},
		},
	}
	md := collabTable(r, []string{"resident"})
	if !strings.Contains(md, "#### segment 2026-11-01..2026-11-15 (2 incidents)") || !strings.Contains(md, "#### segment 2026-11-25..2026-11-26 (1 incidents)") {
		t.Fatalf("segments missing from table:\n%s", md)
	}
}
