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
	if n.Entries != 5 || n.Active != 3 || n.Standing != 2 || n.Untagged != 0 {
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
			if e.Rank != 10 || e.Facet != "interaction" {
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

// One entry without the tagging mark (from before 0.2.0-beta.40) makes the page untagged, and then no entry carries
// origin_space or categories; a class is tagged when every member is.
func TestUntagged(t *testing.T) {
	st := state()
	st.Entries["i-stable"].Members = []string{"a1", "a2"}
	st.Entries["a1"] = &entry.Entry{ID: "a1", Facet: entry.Interaction, Grain: entry.Action, Text: "a1", Status: entry.Retired, Tagged: true}
	st.Entries["a2"] = &entry.Entry{ID: "a2", Facet: entry.Interaction, Grain: entry.Action, Text: "a2", Status: entry.Retired}
	r, n, _ := Page(st, Params{Contribution: "memory", Limit: 200})
	if r.Tagged || n.Untagged != 1 {
		t.Fatalf("tagged %v untagged %d", r.Tagged, n.Untagged)
	}
	for _, e := range r.Entries {
		if e.OriginSpace != "" || e.Categories != nil {
			t.Errorf("untagged page carries tags: %+v", e)
		}
	}
	st.Entries["a2"].Tagged = true
	if r, _, _ := Page(st, Params{Contribution: "memory", Limit: 200}); !r.Tagged {
		t.Error("every member tagged: the class is")
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

func itoa(i int) string { b, _ := json.Marshal(i); return string(b) }
