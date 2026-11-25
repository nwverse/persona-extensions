package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/entry"
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
