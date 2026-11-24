package memsrc

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/entry"
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
	// Standing first (stable, usable in the resident exits), then rank, then newest; dead entries and evaluations out.
	want := []string{"i-stable", "p-card", "w-shift", "j-dorm", "u-cand"}
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
	if n.Entries != 5 || n.Active != 3 || n.Standing != 2 || n.Backfilled != 0 {
		t.Errorf("counts %+v", n)
	}
	for _, e := range r.Entries {
		switch e.ID {
		case "u-cand":
			if e.OriginSpace != "health" || !slices.Equal(e.Categories, []string{"health"}) || e.Standing {
				t.Errorf("tagged entry %+v", e)
			}
		case "p-card":
			if !slices.Equal(e.Subjects, []string{"Alex Park"}) || !e.Standing {
				t.Errorf("card %+v", e)
			}
		case "w-shift":
			if !slices.Equal(e.Subjects, []string{"project-x"}) || e.Standing {
				t.Errorf("shifting %+v", e) // shifting: active, but not in the resident block
			}
		case "i-stable":
			// core facet (400) + class (100) + evidence min(150, 5*2*12=120) = 620
			if e.Rank != 620 || e.Facet != "interaction" {
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
	if n := len([]rune(r.Entries[0].Text)); n != MaxText {
		t.Errorf("text clipped to %d", n)
	}
}

// Low-value weak relation triples (depends_on, collaborates_with, about, member_of, same_circle, waiting_on, ...)
// stay out of memory.entries without deleting them from state, unless pinned, edited or confirmed right by the user.
// Substantive relations (owns, decides, reports_to, means) stay in.
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
	for _, drop := range []string{"rel-dep", "rel-collab", "rel-about", "rel-member"} {
		if slices.Contains(got, drop) {
			t.Errorf("weak relation %s leaked into memory.entries: %v", drop, got)
		}
		if st.Entries[drop] == nil {
			t.Errorf("weak relation %s was deleted from state", drop)
		}
	}
	for _, keep := range []string{"rel-pinned", "rel-edited", "rel-confirm", "rel-owns", "rel-means"} {
		if !slices.Contains(got, keep) {
			t.Errorf("expected %s to stay in memory.entries: %v", keep, got)
		}
	}
	if n.Entries != 5 {
		t.Errorf("pool count = %d, want 5", n.Entries)
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
