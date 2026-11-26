package memsrc

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/entry"
	"github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/store"
)

// All texts are placeholders.
func cand(id, kind string, score, n int) Candidate {
	// Each id its own run of characters (n bytes, three a character), so no two lines are alike.
	base := rune(0x4e00 + 300*int(id[0]%7) + 40*int(id[1]-'0'))
	var b strings.Builder
	for i := 0; b.Len()+3 <= n; i++ {
		b.WriteRune(base + rune(i%40))
	}
	return Candidate{ID: id, Kind: kind, Score: score, Text: b.String()}
}

// The set fits the hub's room; rules never take the understanding's share; what one side leaves goes to the other;
// alike lines count once; the two sides interleave, each by score.
func TestResidentQuotas(t *testing.T) {
	var cs []Candidate
	for i, id := range []string{"r1", "r2", "r3", "r4", "r5", "r6", "r7"} {
		cs = append(cs, cand(id, entry.KindRules, 90-i, 150)) // rules score higher than any understanding
	}
	cs = append(cs, cand("u1", entry.KindUnderstanding, 50, 150), cand("u2", entry.KindUnderstanding, 40, 150))
	got := Resident(cs)
	size, rules, under := 0, 0, 0
	for _, c := range got {
		size += len(c.Text) + residentOverhead
		if c.Kind == entry.KindRules {
			rules++
		} else {
			under++
		}
	}
	if size > ResidentBytes || under != 2 || rules == 0 {
		t.Fatalf("%d bytes, %d rules, %d understanding: %v", size, rules, under, got)
	}
	if got[0].ID != "u1" || got[1].ID != "r1" || got[2].ID != "u2" || got[3].ID != "r2" {
		t.Errorf("interleaved %v", got)
	}
	// Alike lines: one.
	dup := Resident([]Candidate{{ID: "a", Kind: entry.KindRules, Score: 9, Text: "report the conclusion first, then the details"},
		{ID: "b", Kind: entry.KindRules, Score: 8, Text: "report the conclusion first, then details"}})
	if len(dup) != 1 || dup[0].ID != "a" {
		t.Errorf("dedupe %v", dup)
	}
}

// Only the standing set stands; it starts at rankStand; the rest carry a line of the user's own words for recall.
func TestPageStandingSetAndQuote(t *testing.T) {
	st := &store.State{Entries: map[string]*entry.Entry{
		"j-wide": {ID: "j-wide", Facet: entry.Judgement, Text: "pilot before any rollout", Situation: "a new rollout", Choice: "pilot first",
			Status: entry.Stable, Count: 4, Tagged: true, Origin: entry.Behavior,
			Leverage: &entry.Leverage{Reach: entry.ReachCrossProject, Horizon: entry.HorizonLasting, Principle: "learn cheaply before betting"}},
		"w-cand": {ID: "w-cand", Facet: entry.Workflow, Text: "write the notes after the call", Status: entry.Candidate, Count: 1, Tagged: true,
			Origin: entry.Behavior, Evidence: []entry.Evidence{{Ref: "chats/a", Day: "2026-11-02", Who: entry.WhoSelf, Quote: "call notes right after"}}},
	}}
	r, n, err := Page(st, Params{Contribution: "memory", Limit: 10})
	if err != nil || n.Standing != 1 {
		t.Fatalf("%v %+v", err, n)
	}
	w := r.Entries[0]
	if w.ID != "j-wide" || !w.Standing || w.Rank != rankStand || !strings.HasPrefix(w.Text, "原则：learn cheaply before betting") {
		t.Errorf("standing %+v", w)
	}
	c := r.Entries[1]
	if c.Standing || !strings.Contains(c.Text, "原话：「call notes right after」（2026-11-02）") {
		t.Errorf("recalled %+v", c)
	}
}

// Alike scores are ordered by days of evidence, then the latest, not by id.
func TestResidentTieBreak(t *testing.T) {
	got := Resident([]Candidate{
		{ID: "a", Kind: entry.KindRules, Score: 50, Days: 1, Last: "2026-11-23", Text: "一二三四五"},
		{ID: "b", Kind: entry.KindRules, Score: 50, Days: 5, Last: "2026-11-01", Text: "六七八九十"},
		{ID: "c", Kind: entry.KindRules, Score: 50, Days: 1, Last: "2026-11-24", Text: "甲乙丙丁戊"},
	})
	if len(got) != 3 || got[0].ID != "b" || got[1].ID != "c" || got[2].ID != "a" {
		t.Errorf("order %v", got)
	}
}

// A wider room holds more lines.
func TestResidentRoom(t *testing.T) {
	var cs []Candidate
	for i := 0; i < 40; i++ {
		var b strings.Builder
		for k := 0; k < 30; k++ {
			b.WriteRune(rune(0x4e00 + 40*i + k))
		}
		cs = append(cs, Candidate{ID: fmt.Sprint("r", i), Kind: entry.KindRules, Score: 90 - i, Text: b.String()})
	}
	small, wide := len(ResidentWithin(cs, ResidentBytes, ResidentMostFor(ResidentBytes))), len(ResidentWithin(cs, 4096, ResidentMostFor(4096)))
	if wide <= small {
		t.Errorf("room 1400: %d lines, 4096: %d", small, wide)
	}
}

// The overview's patterns are offered PatternShare of the room first, on their side's quota, although single entries
// score higher; the leftover room takes no line under spillFloor.
func TestResidentPatternShare(t *testing.T) {
	var cs []Candidate
	for i := 0; i < 10; i++ { // rules only, more than their 60% quota (16 of these fit): the rest goes to the other side's room
		cs = append(cs, cand(fmt.Sprint("r", i), entry.KindRules, 90, 150), cand(fmt.Sprint("s", i), entry.KindRules, 90, 150))
	}
	p := cand("ov:pattern:1", entry.KindUnderstanding, 60, 200)
	p.Pattern = true
	low := cand("w-low", entry.KindRules, 20, 125) // larger than what the rules quota leaves
	cs = append(cs, p, low)
	got := ResidentWithin(cs, 4096, ResidentMostFor(4096))
	var ids []string
	for _, c := range got {
		ids = append(ids, c.ID)
	}
	if !slices.Contains(ids, "ov:pattern:1") || !slices.Contains(ids, "s9") {
		t.Errorf("the pattern or the spill was kept out: %v", ids)
	}
	if slices.Contains(ids, "w-low") {
		t.Errorf("a fragment under the floor filled the room: %v", ids)
	}
}
