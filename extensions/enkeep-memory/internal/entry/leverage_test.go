package entry

import "testing"

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

// 0.2.5: hard rules rank above 90 by their own leverage, not in a tie at 90; and a rule distilled from dozens of
// observations outranks one seen seven times.
func TestHardRulesRankByLeverage(t *testing.T) {
	ev := func(day string) Evidence { return Evidence{Who: WhoSelf, Day: day, Quote: "以后别再这样"} }
	narrow := &Entry{Facet: Interaction, Type: "correction", Status: Stable, Grain: Action, Count: 1,
		Evidence: []Evidence{ev("2026-07-14")}, Leverage: &Leverage{Reach: ReachOneOff, Horizon: HorizonDays}}
	wide := &Entry{Facet: Interaction, Type: "correction", Status: Stable, Grain: Action, Count: 6,
		Evidence: []Evidence{ev("2026-11-01"), ev("2026-11-10"), ev("2026-11-20")}, Leverage: &Leverage{Reach: ReachCrossProject, Horizon: HorizonMonths}}
	if Score(narrow) < 90 || Score(wide) <= Score(narrow) {
		t.Errorf("hard scores: narrow %d, wide %d", Score(narrow), Score(wide))
	}
	seven := &Entry{Facet: Interaction, Grain: Abstract, Status: Stable, Count: 7}
	dozens := &Entry{Facet: Interaction, Grain: Abstract, Status: Stable, Count: 70}
	if Score(dozens) <= Score(seven) {
		t.Errorf("distilled from 70 %d <= seen 7 times %d", Score(dozens), Score(seven))
	}
}
