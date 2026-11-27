package entry

import (
	"testing"
	"time"
)

// All texts are placeholders.
func TestLeverageNormalizeAndMerge(t *testing.T) {
	if (&Leverage{Reach: "galaxy", Horizon: "forever"}).Normalize() != nil {
		t.Error("unknown values only: nothing kept")
	}
	e := &Entry{}
	e.MergeLeverage(&Leverage{Reach: ReachProject, Horizon: HorizonWeeks, Principle: "keep a way back", PrincipleInferred: true})
	e.MergeLeverage(&Leverage{Reach: ReachTaskKind, Horizon: HorizonLasting, Principle: "always keep a rollback"})
	l := e.Leverage
	if l.Reach != ReachProject || l.Horizon != HorizonLasting || l.Principle != "always keep a rollback" || l.PrincipleInferred {
		t.Errorf("merged %+v", l)
	}
}

// What reaches further and holds longer scores higher than a one-off detail seen as often; a candidate less than the
// same stable entry; pinned is 100.
func TestScoreOrders(t *testing.T) {
	wide := &Entry{Facet: Judgement, Status: Stable, Count: 3, Leverage: &Leverage{Reach: ReachCrossProject, Horizon: HorizonLasting, Principle: "p"}}
	detail := &Entry{Facet: Tools, Status: Stable, Count: 3, Leverage: &Leverage{Reach: ReachOneOff, Horizon: HorizonDays}}
	if Score(wide) <= Score(detail) {
		t.Errorf("wide %d <= detail %d", Score(wide), Score(detail))
	}
	cand := *wide
	cand.Status = Candidate
	if Score(&cand) >= Score(wide) {
		t.Error("a candidate scores as much as the stable one")
	}
	if Score(&Entry{Pinned: true}) != 100 {
		t.Error("pinned")
	}
	// Without labels (read before daily/17) a class scores above one action of the same count.
	if Score(&Entry{Facet: Judgement, Grain: Class, Status: Stable, Count: 2}) <= Score(&Entry{Facet: Judgement, Grain: Action, Status: Stable, Count: 2}) {
		t.Error("fallback: class not above action")
	}
}

func TestKindOf(t *testing.T) {
	for _, c := range []struct {
		e    *Entry
		want string
	}{
		{&Entry{Facet: Interaction}, KindRules}, {&Entry{Facet: Tools}, KindRules},
		{&Entry{Facet: Judgement}, KindUnderstanding}, {&Entry{Facet: Persona}, KindUnderstanding},
		{&Entry{Facet: World, Type: "decision"}, KindUnderstanding}, {&Entry{Facet: World, Type: "status"}, ""},
		{&Entry{Facet: World, Type: "decision", Predicate: "owns"}, ""}, {&Entry{Facet: People}, ""},
	} {
		if got := KindOf(c.e); got != c.want {
			t.Errorf("%+v: %q, want %q", c.e, got, c.want)
		}
	}
}

// Rooted: four ISO weeks of evidence, or a lasting cross-project principle on two days.
func TestRooted(t *testing.T) {
	e := &Entry{Facet: Judgement}
	if !Rooted(e, []string{"2026-10-05", "2026-10-12", "2026-10-19", "2026-10-26"}) {
		t.Error("four weeks")
	}
	if Rooted(e, []string{"2026-10-05", "2026-10-06", "2026-10-07"}) {
		t.Error("one week")
	}
	e.Leverage = &Leverage{Reach: ReachCrossProject, Horizon: HorizonLasting, Principle: "p"}
	if !Rooted(e, []string{"2026-10-05", "2026-10-06"}) || Rooted(e, []string{"2026-10-05"}) {
		t.Error("principle on two days")
	}
}

// Unified design §4.2: the legacy L0 90-99 hard-rule floor is removed; hard rules still score by their own leverage
// (wider/longer outranks narrow one-off) without being artificially bumped to 90+.
func TestHardRulesScoreByLeverageWithoutFloor(t *testing.T) {
	ev := func(day string) Evidence { return Evidence{Who: WhoSelf, Day: day, Quote: "以后别再这样"} }
	narrow := &Entry{Facet: Interaction, Type: "correction", Status: Stable, Grain: Action, Count: 1,
		Evidence: []Evidence{ev("2026-07-14")}, Leverage: &Leverage{Reach: ReachOneOff, Horizon: HorizonDays}}
	wide := &Entry{Facet: Interaction, Type: "correction", Status: Stable, Grain: Action, Count: 6,
		Evidence: []Evidence{ev("2026-11-01"), ev("2026-11-10"), ev("2026-11-20")}, Leverage: &Leverage{Reach: ReachCrossProject, Horizon: HorizonMonths}}
	if !Hard(narrow) || !Hard(wide) {
		t.Fatal("expected both to be Hard")
	}
	if Score(narrow) >= 90 {
		t.Errorf("narrow hard rule should not be bumped to 90+: got %d", Score(narrow))
	}
	if Score(wide) <= Score(narrow) {
		t.Errorf("hard scores: narrow %d, wide %d", Score(narrow), Score(wide))
	}
}

func TestFastHotAndColdStart(t *testing.T) {
	now := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	e := &Entry{Facet: Interaction, Type: "correction", Status: Candidate}
	SetFastHot(e, "2026-11-20")
	SetFastHot(e, "2026-11-24")
	if got := FastHotDay(e); got != "2026-11-24" {
		t.Fatalf("FastHotDay = %q, want 2026-11-24", got)
	}
	if !IsFastHot(e, now, 7) {
		t.Fatal("expected active within 7 days")
	}
	if IsFastHot(e, now.AddDate(0, 0, 10), 7) {
		t.Fatal("expected expired after 7 days")
	}
	// ColdStartEligible: cross_project + lasting + principle (understanding), or hard/wideLasting (rules).
	pOK := &Entry{Facet: Judgement, Leverage: &Leverage{Reach: ReachCrossProject, Horizon: HorizonLasting, Principle: "verify first"}}
	pNoPrin := &Entry{Facet: Judgement, Leverage: &Leverage{Reach: ReachCrossProject, Horizon: HorizonLasting}}
	if !ColdStartEligible(pOK) || ColdStartEligible(pNoPrin) {
		t.Fatalf("cold start understanding: ok=%v noPrin=%v", ColdStartEligible(pOK), ColdStartEligible(pNoPrin))
	}
	rHard := &Entry{Facet: Interaction, Type: "correction", Evidence: []Evidence{{Who: WhoSelf, Day: "2026-11-20", Quote: "以后一律先检查"}}}
	rPlain := &Entry{Facet: Interaction, Type: "correction"}
	if !ColdStartEligible(rHard) || ColdStartEligible(rPlain) {
		t.Fatalf("cold start rules: hard=%v plain=%v", ColdStartEligible(rHard), ColdStartEligible(rPlain))
	}
}
