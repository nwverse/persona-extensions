package memsrc

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/entry"
	"github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/profile"
	"github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/store"
)

// All texts are placeholders (project-x, user-a).
func state() *store.State {
	es := map[string]*entry.Entry{
		"i-stable":  {ID: "i-stable", Facet: entry.Interaction, Grain: entry.Class, Text: "report the conclusion first", Status: entry.Stable, Count: 5, Kinds: []string{"S1", "S3"}, Last: "2026-11-02", Tagged: true, Origin: entry.Behavior},
		"w-shift":   {ID: "w-shift", Facet: entry.Workflow, Text: "run the smoke test on project-x first", Status: entry.Shifting, Count: 3, Last: "2026-11-05", Tagged: true, Object: "project-x", Origin: entry.Behavior},
		"u-cand":    {ID: "u-cand", Facet: entry.Persona, Text: "user-a sleeps late", Status: entry.Candidate, Count: 1, Last: "2026-11-06", Tagged: true, Origin: entry.Behavior, Categories: []entry.Category{{Name: "health", By: entry.ByModel}}, OriginSpace: "health"},
		"j-dorm":    {ID: "j-dorm", Facet: entry.Judgement, Text: "prefer the smaller change", Status: entry.Dormant, Count: 2, Last: "2026-09-01", Tagged: true, Origin: entry.Behavior},
		"j-retired": {ID: "j-retired", Facet: entry.Judgement, Text: "old", Status: entry.Retired, Tagged: true},
		"j-deleted": {ID: "j-deleted", Facet: entry.Judgement, Text: "deleted", Status: entry.Stable, Deleted: true, Tagged: true},
		"p-eval":    {ID: "p-eval", Facet: entry.People, Text: "an evaluation", Status: entry.Stable, Part: entry.PartUserEval, Tagged: true},
		"p-card":    {ID: "p-card", Facet: entry.People, Text: "prefers mail", Status: entry.Stable, Count: 1, Person: "person:pa", Last: "2026-11-01", Tagged: true, Origin: entry.Behavior},
	}
	return &store.State{Entries: es, Contacts: map[string]*entry.Contact{"person:pa": {Person: "person:pa", Name: "Alex Park"}}}
}

func ids(r Result) []string {
	var out []string
	for _, e := range r.Entries {
		out = append(out, e.ID)
	}
	return out
}

func TestPage(t *testing.T) {
	st := state()
	r, n, err := Page(st, Params{Contribution: "memory", Limit: 200})
	if err != nil {
		t.Fatal(err)
	}
	// The standing set first (here the one stable rule), then by score (entry.Score, not by how new); dead entries and
	// evaluations out. A people card never stands.
	want := []string{"i-stable", "w-shift", "p-card", "u-cand", "j-dorm"}
	if !slices.Equal(ids(r), want) || r.HasMore || r.After != "" || !r.Tagged {
		t.Fatalf("page %v (want %v) more %v after %q tagged %v", ids(r), want, r.HasMore, r.After, r.Tagged)
	}
	st2 := map[string]string{}
	for _, e := range r.Entries {
		st2[e.ID] = e.Status
	}
	if st2["i-stable"] != "active" || st2["w-shift"] != "active" || st2["u-cand"] != "candidate" || st2["j-dorm"] != "dormant" {
		t.Errorf("statuses %v", st2)
	}
	if n.Entries != 5 || n.Active != 3 || n.Standing != 1 || n.Backfilled != 0 {
		t.Errorf("counts %+v", n)
	}
	for _, e := range r.Entries {
		switch e.ID {
		case "u-cand":
			if e.OriginSpace != "health" || !slices.Equal(e.Categories, []string{"health"}) || e.Standing {
				t.Errorf("tagged entry %+v", e)
			}
		case "p-card":
			if !slices.Equal(e.Subjects, []string{"Alex Park"}) || e.Standing {
				t.Errorf("card %+v", e)
			}
		case "w-shift":
			if !slices.Equal(e.Subjects, []string{"project-x"}) || e.Standing {
				t.Errorf("shifting %+v", e) // shifting: active, but not in the resident block
			}
		case "i-stable":
			// the first of the standing set
			if e.Rank != rankStand || !e.Standing || e.Facet != "interaction" {
				t.Errorf("rank or facet %+v", e)
			}
		}
	}
	// Pages of two: the same order, after on every page with more.
	var got []string
	p := Params{Contribution: "memory", Limit: 2}
	for i := 0; i < 5; i++ {
		r, _, err := Page(st, p)
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, ids(r)...)
		if !r.HasMore {
			break
		}
		if r.After == "" || len(r.After) > MaxAfter {
			t.Fatalf("has_more without a good after: %q", r.After)
		}
		p.After = r.After
	}
	if !slices.Equal(got, want) {
		t.Fatalf("paged %v", got)
	}
	if _, _, err := Page(st, Params{Contribution: "memory", Limit: 2, After: "nope!"}); err != ErrBadAfter {
		t.Errorf("bad after: %v", err)
	}
}

// Entries from before the 0.2.0-beta.40 tagging rules are tagged here by the same rules, so the page stays tagged (an
// untagged page would carry none of the entries anywhere): the old model label, the money and legal words when there
// is no label, a class's members' categories and space. The state is not changed.
func TestBackfilledTags(t *testing.T) {
	st := state()
	st.Entries["i-stable"].Members = []string{"a1", "a2"}
	st.Entries["a1"] = &entry.Entry{ID: "a1", Facet: entry.Interaction, Grain: entry.Action, Text: "a1", Status: entry.Retired, Tagged: true}
	st.Entries["a2"] = &entry.Entry{ID: "a2", Facet: entry.Interaction, Grain: entry.Action, Text: "a2", Status: entry.Retired, OriginSpace: "health"}
	st.Entries["w-old"] = &entry.Entry{ID: "w-old", Facet: entry.Workflow, Text: "renew the passport before the trip", Status: entry.Stable,
		Count: 2, Last: "2026-10-01", Origin: entry.Behavior}
	st.Entries["w-label"] = &entry.Entry{ID: "w-label", Facet: entry.Workflow, Text: "keeps the receipts", Status: entry.Stable,
		Count: 2, Last: "2026-10-01", Origin: entry.Behavior, Sensitive: "emotion",
		Evidence: []entry.Evidence{{Quote: "报销的发票都留着"}}}
	st.Entries["w-money"] = &entry.Entry{ID: "w-money", Facet: entry.Workflow, Text: "pays the rent", Status: entry.Stable,
		Count: 2, Last: "2026-10-01", Origin: entry.Behavior, Evidence: []entry.Evidence{{Quote: "每月转账交房租"}}}
	st.Entries["w-plain"] = &entry.Entry{ID: "w-plain", Facet: entry.Workflow, Text: "runs the gate before merging", Status: entry.Stable,
		Count: 2, Last: "2026-10-01", Origin: entry.Behavior}
	r, n, _ := Page(st, Params{Contribution: "memory", Limit: 200})
	if !r.Tagged || n.Backfilled != 5 {
		t.Fatalf("tagged %v backfilled %d", r.Tagged, n.Backfilled)
	}
	got := map[string]Entry{}
	for _, e := range r.Entries {
		got[e.ID] = e
	}
	if e := got["i-stable"]; e.OriginSpace != "health" || !slices.Equal(e.Categories, []string{"health"}) {
		t.Errorf("class from its members: %+v", e)
	}
	if e := got["w-old"]; !slices.Equal(e.Categories, []string{"legal_id"}) || e.OriginSpace != "" {
		t.Errorf("legal words: %+v", e)
	}
	if e := got["w-label"]; !slices.Equal(e.Categories, []string{"emotion"}) {
		t.Errorf("a label: the money words add nothing: %+v", e)
	}
	if e := got["w-money"]; !slices.Contains(e.Categories, "finance") {
		t.Errorf("money words without a label: %+v", e)
	}
	if e := got["w-plain"]; e.Categories != nil || e.OriginSpace != "" {
		t.Errorf("general: %+v", e)
	}
	if st.Entries["w-old"].Tagged || len(st.Entries["w-old"].Categories) != 0 {
		t.Error("the state was changed")
	}
	st.Entries["a2"].Tagged = true
	if _, n, _ := Page(st, Params{Contribution: "memory", Limit: 200}); n.Backfilled != 4 {
		t.Errorf("every member tagged: the class is; backfilled %d", n.Backfilled)
	}
}

// A page stays under the size bound: it ends early with has_more and after.
func TestPageSize(t *testing.T) {
	st := &store.State{Entries: map[string]*entry.Entry{}}
	long := strings.Repeat("长", 5000)
	for i := 0; i < 300; i++ {
		id := "w-" + strings.Repeat("0", 3-len(itoa(i))) + itoa(i)
		st.Entries[id] = &entry.Entry{ID: id, Facet: entry.Workflow, Text: long, Status: entry.Stable, Count: 1, Tagged: true, Origin: entry.Behavior}
	}
	r, _, err := Page(st, Params{Contribution: "memory", Limit: 500})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(r)
	if len(b) > 1<<20 || !r.HasMore || r.After == "" || len(r.Entries) == 0 || len(r.Entries) >= 300 {
		t.Fatalf("%d bytes, %d entries, more %v", len(b), len(r.Entries), r.HasMore)
	}
	if n := len([]rune(r.Entries[len(r.Entries)-1].Text)); n != MaxText { // one not standing: its whole text
		t.Errorf("text clipped to %d", n)
	}
}

// Low-value weak relation triples (depends_on, collaborates_with, about, member_of, same_circle, waiting_on, ...)
// are answered last (0.2.0-beta.52: the hub reads every page, so they no longer crowd out the rest; the record is kept
// for recall), never standing, and stay out of the pool the overview and the exits read. Pinned, edited or
// confirmed ones count as any other.
func TestWeakRelationsFiltered(t *testing.T) {
	st := &store.State{Entries: map[string]*entry.Entry{
		"rel-dep":     {ID: "rel-dep", Facet: entry.World, Predicate: "depends_on", Text: "service-a depends on service-b", Status: entry.Stable, Count: 5, Last: "2026-11-10", Tagged: true, Origin: entry.Behavior},
		"rel-collab":  {ID: "rel-collab", Facet: entry.World, Predicate: "collaborates_with", Text: "user-a works with user-b", Status: entry.Stable, Count: 8, Last: "2026-11-10", Tagged: true, Origin: entry.Behavior},
		"rel-about":   {ID: "rel-about", Facet: entry.World, Predicate: "about", Text: "doc-a is about project-x", Status: entry.Stable, Count: 3, Last: "2026-11-10", Tagged: true, Origin: entry.Behavior},
		"rel-member":  {ID: "rel-member", Facet: entry.World, Predicate: "member_of", Text: "user-b belongs to team-a", Status: entry.Stable, Count: 3, Last: "2026-11-10", Tagged: true, Origin: entry.Behavior},
		"rel-pinned":  {ID: "rel-pinned", Facet: entry.World, Predicate: "depends_on", Text: "core-a depends on core-b", Status: entry.Stable, Pinned: true, Count: 1, Last: "2026-11-10", Tagged: true, Origin: entry.Behavior},
		"rel-edited":  {ID: "rel-edited", Facet: entry.World, Predicate: "collaborates_with", Text: "raw", User: &entry.UserEdit{Text: "key partner"}, Status: entry.Stable, Count: 1, Last: "2026-11-10", Tagged: true, Origin: entry.Behavior},
		"rel-confirm": {ID: "rel-confirm", Facet: entry.World, Predicate: "depends_on", Text: "confirmed dep", Verdicts: []entry.Verdict{{Mark: "right", By: "user"}}, Status: entry.Stable, Count: 1, Last: "2026-11-10", Tagged: true, Origin: entry.Behavior},
		"rel-owns":    {ID: "rel-owns", Facet: entry.World, Predicate: "owns", Text: "user-b is responsible for project-x", Status: entry.Stable, Count: 2, Last: "2026-11-10", Tagged: true, Origin: entry.Behavior},
		"rel-means":   {ID: "rel-means", Facet: entry.World, Predicate: "means", Text: "abbr-x means service-x", Status: entry.Stable, Count: 2, Last: "2026-11-10", Tagged: true, Origin: entry.Behavior},
	}}
	r, n, err := Page(st, Params{Contribution: "memory", Limit: 200})
	if err != nil {
		t.Fatal(err)
	}
	got := ids(r)
	first := len(got)
	for _, weak := range []string{"rel-dep", "rel-collab", "rel-about", "rel-member"} {
		i := slices.Index(got, weak)
		if i < 0 {
			t.Errorf("weak relation %s not answered: %v", weak, got)
		}
		first = min(first, i)
		if Pool(st, st.Entries[weak]) {
			t.Errorf("weak relation %s in the pool", weak)
		}
	}
	for _, e := range r.Entries[first:] {
		if !IsWeakRelation(st.Entries[e.ID]) || e.Standing {
			t.Errorf("weak relations are answered last, not standing: %v", got)
		}
	}
	for _, keep := range []string{"rel-pinned", "rel-edited", "rel-confirm", "rel-owns", "rel-means"} {
		if !slices.Contains(got, keep) {
			t.Errorf("expected %s to stay in memory.entries: %v", keep, got)
		}
	}
	if n.Entries != 9 {
		t.Errorf("answered = %d, want 9", n.Entries)
	}
}

// Rank puts pinned first, then core behavioral/persona rules (abstract > class > durable > action), then substantive
// people/world entries ahead of transient world status facts, so a 1000-entry hub fetch keeps the most useful entries.
func TestRankPrioritizesUseful(t *testing.T) {
	st := &store.State{Entries: map[string]*entry.Entry{
		"w-status":   {ID: "w-status", Facet: entry.World, Type: "status", Grain: entry.Action, Text: "build-x passed today", Status: entry.Stable, Count: 20, Kinds: []string{"S1", "S3"}, Last: "2026-11-10", Tagged: true, Origin: entry.Behavior},
		"w-decision": {ID: "w-decision", Facet: entry.World, Type: "decision", Grain: entry.Action, Text: "project-x keeps plugins decoupled", Status: entry.Stable, Count: 2, Last: "2026-11-10", Tagged: true, Origin: entry.Behavior},
		"j-class":    {ID: "j-class", Facet: entry.Judgement, Grain: entry.Class, Text: "validate on a small sample first", Status: entry.Stable, Count: 2, Last: "2026-11-01", Tagged: true, Origin: entry.Behavior},
		"j-abstract": {ID: "j-abstract", Facet: entry.Judgement, Grain: entry.Abstract, Text: "prefer incremental rollout over big-bang", Status: entry.Stable, Count: 2, Last: "2026-11-01", Tagged: true, Origin: entry.Behavior},
		"pinned":     {ID: "pinned", Facet: entry.World, Type: "status", Grain: entry.Action, Text: "pinned note", Status: entry.Stable, Pinned: true, Count: 1, Last: "2026-10-01", Tagged: true, Origin: entry.Behavior},
	}}
	r, _, err := Page(st, Params{Contribution: "memory", Limit: 200})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"pinned", "j-abstract", "j-class", "w-decision", "w-status"}
	if !slices.Equal(ids(r), want) {
		t.Fatalf("order = %v, want %v", ids(r), want)
	}
}

func itoa(i int) string { b, _ := json.Marshal(i); return string(b) }

func TestPageWithProfile(t *testing.T) {
	st := state()
	st.Entries["u-health"] = &entry.Entry{
		ID: "u-health", Facet: entry.Persona, Text: "user-a runs three times a week", Status: entry.Stable,
		Count: 3, Last: "2026-11-06", Tagged: true, Origin: entry.Behavior,
		Categories: []entry.Category{{Name: entry.CatHealth, By: entry.ByModel}}, OriginSpace: "health",
	}
	sum := profile.Build(st, timeNowFixture(), "zh-Hans")
	r, _, err := PageWithProfile(st, sum, Params{Contribution: "memory", Limit: 200})
	if err != nil {
		t.Fatal(err)
	}
	if !r.Tagged {
		t.Fatal("expected tagged=true")
	}
	byID := map[string]Entry{}
	for _, e := range r.Entries {
		byID[e.ID] = e
	}
	ov, ok := byID["profile:overview"]
	if !ok {
		t.Fatalf("missing profile:overview in %v", ids(r))
	}
	if ov.Standing || ov.Rank != 995 || len(ov.Categories) != 0 || ov.Facet != "persona" { // too long to stand: recalled
		t.Errorf("unexpected profile:overview: %+v", ov)
	}
	if strings.Contains(ov.Text, "runs three times") {
		t.Errorf("profile:overview leaked sensitive text: %s", ov.Text)
	}
	// Each facet is an entry of its own, recalled by its subjects (not standing: the overview is), its text the
	// section's description rather than the points strung after it.
	if pi, ok := byID["profile:interaction"]; !ok || pi.Standing || strings.Contains(pi.Text, " - ") || len(pi.Subjects) == 0 {
		t.Errorf("profile:interaction %+v in %v", pi, ids(r))
	}
	hp, ok := byID["profile:cat:health"]
	if !ok {
		t.Fatalf("missing profile:cat:health in %v", ids(r))
	}
	if hp.Standing || !slices.Equal(hp.Categories, []string{"health"}) || hp.OriginSpace != "health" {
		t.Errorf("sensitive profile entry must not be standing and must carry category+space: %+v", hp)
	}
	// Pagination with limit=2 across profile + regular entries stays consistent.
	var paged []string
	p := Params{Contribution: "memory", Limit: 2}
	for i := 0; i < 20; i++ {
		pr, _, err := PageWithProfile(st, sum, p)
		if err != nil {
			t.Fatal(err)
		}
		paged = append(paged, ids(pr)...)
		if !pr.HasMore {
			break
		}
		p.After = pr.After
	}
	if !slices.Equal(paged, ids(r)) {
		t.Fatalf("paged %v != full %v", paged, ids(r))
	}
}

func timeNowFixture() time.Time {
	return time.Date(2026, 11, 24, 0, 0, 0, 0, time.UTC)
}

// Extras (the overview's patterns) are answered among the curated entries in the same order; one the protocol would
// refuse is left out, and one clashing with a curated id never replaces it.
func TestPageWithExtras(t *testing.T) {
	st := &store.State{Entries: map[string]*entry.Entry{}}
	extras := []Extra{
		{Entry: Entry{ID: "ov:pattern:aa", Text: "a pattern", Facet: "judgement", Rank: 960}, Last: "2026-11-20", Score: 80, Kind: entry.KindUnderstanding},
		{Entry: Entry{ID: "ov:cat:health:bb", Text: "a health pattern", Categories: []string{"health"}, Rank: 900, OriginSpace: "Bad Space"}},
		{Entry: Entry{ID: "bad id with spaces", Text: "x"}},
		{Entry: Entry{ID: "ov:pattern:empty", Text: "  "}},
	}
	res, n, err := PageWith(st, nil, extras, Params{Contribution: "memory", Limit: 10})
	if err != nil || n.Entries != 2 || n.Standing != 1 || len(res.Entries) != 2 {
		t.Fatalf("%v %+v %+v", err, n, res)
	}
	if res.Entries[0].ID != "ov:pattern:aa" || res.Entries[0].Status != "active" || res.Entries[1].OriginSpace != "" {
		t.Fatalf("%+v", res.Entries)
	}
	// Paged one at a time, the cursor walks over extras too.
	res, _, _ = PageWith(st, nil, extras, Params{Contribution: "memory", Limit: 1})
	if !res.HasMore || res.After == "" {
		t.Fatalf("%+v", res)
	}
	res2, _, err := PageWith(st, nil, extras, Params{Contribution: "memory", Limit: 1, After: res.After})
	if err != nil || len(res2.Entries) != 1 || res2.Entries[0].ID != "ov:cat:health:bb" {
		t.Fatalf("%v %+v", err, res2)
	}
}
