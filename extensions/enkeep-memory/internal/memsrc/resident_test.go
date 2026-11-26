package memsrc

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/entry"
	"github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/profile"
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
	bucket := BucketPrinciple
	if kind == entry.KindRules {
		bucket = BucketContract
	}
	return Candidate{ID: id, Kind: kind, Bucket: bucket, L1: true, Score: score, Text: b.String()}
}

// Three-bucket quotas: contracts never starve principles; unused principle L1 room spills to L1 contracts; alike lines
// count once; contracts and principles strictly interleave.
func TestResidentQuotas(t *testing.T) {
	var cs []Candidate
	for i, id := range []string{"r1", "r2", "r3", "r4", "r5", "r6", "r7"} {
		cs = append(cs, cand(id, entry.KindRules, 90-i, 150))
	}
	cs = append(cs, cand("u1", entry.KindUnderstanding, 50, 150), cand("u2", entry.KindUnderstanding, 40, 150))
	got := Resident(cs)
	size, rules, under := 0, 0, 0
	for _, c := range got {
		size += len(c.Text) + residentOverhead
		if c.Bucket == BucketContract {
			rules++
		} else {
			under++
		}
	}
	if size > ResidentBytes || under != 2 || rules == 0 {
		t.Fatalf("%d bytes, %d contracts, %d principles: %v", size, rules, under, got)
	}
	// Bucket 1 (contracts) and Bucket 2 (principles) strictly interleave: r1, u1, r2, u2, ...
	if got[0].ID != "r1" || got[1].ID != "u1" || got[2].ID != "r2" || got[3].ID != "u2" {
		t.Errorf("interleaved %v", got)
	}
	// Alike lines: one.
	dup := Resident([]Candidate{
		{ID: "a", Kind: entry.KindRules, Bucket: BucketContract, L1: true, Score: 9, Text: "report the conclusion first, then the details"},
		{ID: "b", Kind: entry.KindRules, Bucket: BucketContract, L1: true, Score: 8, Text: "report the conclusion first, then details"},
	})
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

// Alike scores break ties by latest verification day first, then independent quotes (Days), then id.
func TestResidentTieBreak(t *testing.T) {
	got := Resident([]Candidate{
		{ID: "a", Kind: entry.KindRules, Bucket: BucketContract, L1: true, Score: 50, Days: 5, Last: "2026-11-23", Text: "一二三四五"},
		{ID: "b", Kind: entry.KindRules, Bucket: BucketContract, L1: true, Score: 50, Days: 2, Last: "2026-11-24", Text: "六七八九十"},
		{ID: "c", Kind: entry.KindRules, Bucket: BucketContract, L1: true, Score: 50, Days: 6, Last: "2026-11-24", Text: "甲乙丙丁戊"},
	})
	if len(got) != 3 || got[0].ID != "c" || got[1].ID != "b" || got[2].ID != "a" {
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
		cs = append(cs, Candidate{ID: fmt.Sprint("r", i), Kind: entry.KindRules, Bucket: BucketContract, L1: true, Score: 90 - i, Text: b.String()})
	}
	small, wide := len(ResidentWithin(cs, ResidentBytes, ResidentMostFor(ResidentBytes))), len(ResidentWithin(cs, 4096, ResidentMostFor(4096)))
	if wide <= small {
		t.Errorf("room 1400: %d lines, 4096: %d", small, wide)
	}
}

// Bucket 0 (Hot Slot): capped at min(15%, 600 B) and at most 3 entries; Pinned ranks ahead of Fast-Path; unused hot-slot
// bytes return to Buckets 1 and 2; ranks start at 995 for hot slot and 989 for interleaved contracts/principles.
func TestResidentHotSlotAndRanks(t *testing.T) {
	now := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	prevNow := nowFn
	nowFn = func() time.Time { return now }
	defer func() { nowFn = prevNow }()

	st := &store.State{Entries: map[string]*entry.Entry{
		"pin-1":  {ID: "pin-1", Facet: entry.Judgement, Text: "用户置顶的核心原则", Status: entry.Stable, Pinned: true, Last: "2026-11-20", Tagged: true, Origin: entry.SelfReport},
		"fh-1":   {ID: "fh-1", Facet: entry.Interaction, Type: "correction", Text: "热槽当日纠正甲，遇到线上变更先确认", Exception: "回滚操作除外", Status: entry.Candidate, Last: "2026-11-26", Tagged: true, Origin: entry.Behavior, Flags: []string{entry.FlagFastHot + "2026-11-26"}},
		"fh-2":   {ID: "fh-2", Facet: entry.Interaction, Type: "correction", Text: "热槽当日纠正乙，汇报结论先给证据链接", Status: entry.Candidate, Last: "2026-11-25", Tagged: true, Origin: entry.Behavior, Flags: []string{entry.FlagFastHot + "2026-11-25"}},
		"fh-3":   {ID: "fh-3", Facet: entry.Interaction, Type: "correction", Text: "热槽超限第四条不应入选", Status: entry.Candidate, Last: "2026-11-24", Tagged: true, Origin: entry.Behavior, Flags: []string{entry.FlagFastHot + "2026-11-24"}},
		"fh-old": {ID: "fh-old", Facet: entry.Interaction, Type: "correction", Text: "超过七天的旧热槽应过期退出", Status: entry.Candidate, Last: "2026-11-10", Tagged: true, Origin: entry.Behavior, Flags: []string{entry.FlagFastHot + "2026-11-10"}},
		"fh-abs": {ID: "fh-abs", Facet: entry.Interaction, Type: "correction", Text: "已被契约吸收的纠正应退出热槽", Status: entry.Candidate, Last: "2026-11-26", Tagged: true, Origin: entry.Behavior, Flags: []string{entry.FlagFastHot + "2026-11-26"}},
	}}
	extras := []Extra{
		{
			Entry:    Entry{ID: "ov:pattern:c1", Facet: "interaction", Text: "正式契约一"},
			Kind:     entry.KindRules,
			L1Kind:   BucketContract,
			Score:    120,
			Hard:     true,
			Polarity: "check",
			Body:     "正式契约一：变更前先核对环境",
			Evidence: []string{"fh-abs"},
		},
		{
			Entry:     Entry{ID: "ov:pattern:p1", Facet: "judgement", Text: "正式原理一"},
			Kind:      entry.KindUnderstanding,
			L1Kind:    BucketPrinciple,
			Score:     90,
			Dimension: "decision",
			Body:      "正式原理一：小步验证再推全量",
		},
	}
	res, _, err := PageWith(st, nil, extras, Params{Contribution: "memory", Limit: 50, ResidentBytes: 4096})
	if err != nil {
		t.Fatal(err)
	}
	standingByID := map[string]Entry{}
	var hotIDs []string
	for _, e := range res.Entries {
		if e.Standing {
			standingByID[e.ID] = e
			if e.Rank >= 990 {
				hotIDs = append(hotIDs, e.ID)
			}
		}
	}
	// Hot slot holds at most 3: pin-1 (995), fh-1 (994), fh-2 (993). fh-3 (4th), fh-old (expired), fh-abs (absorbed) excluded.
	wantHot := []string{"pin-1", "fh-1", "fh-2"}
	if !slices.Equal(hotIDs, wantHot) {
		t.Fatalf("hot slot = %v, want %v", hotIDs, wantHot)
	}
	if standingByID["pin-1"].Rank != rankHot || standingByID["fh-1"].Rank != rankHot-1 || standingByID["fh-2"].Rank != rankHot-2 {
		t.Errorf("hot ranks: pin=%d fh1=%d fh2=%d", standingByID["pin-1"].Rank, standingByID["fh-1"].Rank, standingByID["fh-2"].Rank)
	}
	if !strings.Contains(standingByID["fh-1"].Text, "（例外：回滚操作除外）") {
		t.Errorf("hot slot lost exception: %q", standingByID["fh-1"].Text)
	}
	if _, ok := standingByID["fh-old"]; ok {
		t.Error("expired fast_hot should not stand")
	}
	if _, ok := standingByID["fh-abs"]; ok {
		t.Error("L1-absorbed fast_hot should exit hot slot")
	}
	if standingByID["ov:pattern:c1"].Rank != rankStand || standingByID["ov:pattern:p1"].Rank != rankStand-1 {
		t.Errorf("interleaved ranks: c1=%d p1=%d", standingByID["ov:pattern:c1"].Rank, standingByID["ov:pattern:p1"].Rank)
	}
}

// Contract bucket: single polarity <= 35% of effective bucket budget; non-generic group <= 20%.
func TestResidentContractPolarityAndGroupCaps(t *testing.T) {
	var cs []Candidate
	for i := 0; i < 12; i++ {
		c := cand(fmt.Sprint("a", i), entry.KindRules, 130-i, 150)
		c.Hard = true
		c.Polarity = "act"
		c.Group = "interaction:autonomy"
		cs = append(cs, c)
	}
	for i := 0; i < 6; i++ {
		c := cand(fmt.Sprint("b", i), entry.KindRules, 110-i, 150)
		c.Hard = true
		c.Polarity = "check"
		c.Group = "interaction:verification"
		cs = append(cs, c)
	}
	for i := 0; i < 6; i++ {
		c := cand(fmt.Sprint("c", i), entry.KindRules, 90-i, 150)
		c.Polarity = "communicate"
		cs = append(cs, c)
	}
	for i := 0; i < 6; i++ {
		c := cand(fmt.Sprint("d", i), entry.KindRules, 80-i, 150)
		c.Polarity = "route"
		cs = append(cs, c)
	}
	// Add 2 principles so principles don't use full 50% and leftover spills to L1 contracts.
	cs = append(cs,
		cand("p1", entry.KindUnderstanding, 85, 120),
		cand("p2", entry.KindUnderstanding, 80, 120),
	)
	room := 4096
	got := ResidentWithin(cs, room, ResidentMostFor(room))
	pBytes := 0
	polBytes := map[string]int{}
	grpBytes := map[string]int{}
	for _, c := range got {
		sz := len(c.Text) + residentOverhead
		if c.Bucket == BucketPrinciple {
			pBytes += sz
		} else if c.Bucket == BucketContract {
			polBytes[c.Polarity] += sz
			if c.Group != "" {
				grpBytes[c.Group] += sz
			}
		}
	}
	cEff := room - pBytes
	if polBytes["act"] > int(float64(cEff)*PolarityShare) {
		t.Errorf("act polarity bytes %d exceeded 35%% of effective contract budget %d", polBytes["act"], cEff)
	}
	if grpBytes["interaction:autonomy"] > int(float64(cEff)*GroupShare) {
		t.Errorf("group autonomy bytes %d exceeded 20%% of effective contract budget %d", grpBytes["interaction:autonomy"], cEff)
	}
	if polBytes["check"] == 0 || polBytes["communicate"] == 0 || polBytes["route"] == 0 {
		t.Errorf("expected all four polarities represented, got %v", polBytes)
	}
}

// Principle bucket: pass 1 caps each dimension at 35% to ensure diversity; pass 2 fills remaining room with qualified
// L1 principles when other dimensions are exhausted; L0 fragments are never used to fill leftover L1 room.
func TestResidentPrincipleDimensionAndSpillover(t *testing.T) {
	var cs []Candidate
	// 5 decision L1 principles + 1 thinking L1 principle; no contracts -> principle bucket gets 100% of room.
	for i := 0; i < 5; i++ {
		c := cand(fmt.Sprint("d", i), entry.KindUnderstanding, 90-i, 150)
		c.Dimension = "decision"
		cs = append(cs, c)
	}
	t1 := cand("t1", entry.KindUnderstanding, 70, 150)
	t1.Dimension = "thinking"
	cs = append(cs, t1)
	// Also add an L0 fragment in principle bucket: must be ignored once L1 principles exist.
	l0 := cand("l0", entry.KindUnderstanding, 99, 100)
	l0.L1 = false
	cs = append(cs, l0)

	got := ResidentWithin(cs, 1400, ResidentMostFor(1400))
	var ids []string
	for _, c := range got {
		ids = append(ids, c.ID)
	}
	if !slices.Contains(ids, "t1") {
		t.Fatalf("pass 1 dimension cap should bring thinking principle in: %v", ids)
	}
	if len(ids) < 4 {
		t.Fatalf("pass 2 should fill remaining room with qualified L1 principles: %v", ids)
	}
	if slices.Contains(ids, "l0") {
		t.Fatalf("L0 fragment must never enter once L1 principles exist: %v", ids)
	}
}

// Profile summaries never stand, even when profile-summary is passed to PageWith.
func TestProfileSummaryNeverStands(t *testing.T) {
	st := &store.State{Entries: map[string]*entry.Entry{
		"i-1": {ID: "i-1", Facet: entry.Interaction, Text: "concise replies", Status: entry.Stable, Count: 4, Last: "2026-11-20", Tagged: true, Origin: entry.Behavior},
	}}
	sum := profile.Build(st, time.Date(2026, 11, 26, 0, 0, 0, 0, time.UTC), "zh-Hans")
	res, _, err := PageWith(st, sum, nil, Params{Contribution: "memory", Limit: 50, ResidentBytes: 4096})
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range res.Entries {
		if strings.HasPrefix(e.ID, "profile:") && e.Standing {
			t.Errorf("profile entry %s must never stand", e.ID)
		}
	}
}

// A rule's exception or principle's poles stay whole when the line is clipped (unified design §2.1).
func TestResidentTextKeepsExceptionAndPoles(t *testing.T) {
	e := &entry.Entry{Facet: entry.Interaction, Text: strings.Repeat("做完一轮不要停下等批准，", 12), Exception: "只有涉及线上、对外可见、删数据时才停"}
	line := ResidentText(e)
	if !strings.HasSuffix(line, "（例外：只有涉及线上、对外可见、删数据时才停）") || len(line) > HotTextMost {
		t.Errorf("line %q (%d bytes, max %d)", line, len(line), HotTextMost)
	}
	e.Text = "做完一轮不要停下等批准，只有涉及线上、对外可见、删数据时才停。"
	if strings.Contains(ResidentText(e), "例外") {
		t.Error("an exception already in the text is not said twice")
	}
	// FormatResidentLine with poles on a long principle keeps poles intact within PrincipleTextMost.
	poles := "（两端：探索阶段时，放开验证；上线阶段时，严格收窄范围）"
	pl := FormatResidentLine(strings.Repeat("规划和执行任务时持续权衡收益与成本，", 10), poles, PrincipleTextMost)
	if !strings.HasSuffix(pl, poles) || len(pl) > PrincipleTextMost {
		t.Errorf("poles line %q (%d bytes, max %d)", pl, len(pl), PrincipleTextMost)
	}
}
