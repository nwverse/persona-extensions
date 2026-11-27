package main

import (
	"testing"

	"github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/entry"
	"github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/store"
)

// The start card counts each facet's settled and watched entries; a class's concrete actions and dormant entries
// are not counted (they made a facet read 56 against 1411), and it says how many watched ones were seen more than
// once instead of the same "1 more" on every row.
func TestStartCardCounts(t *testing.T) {
	ent := func(id string, g entry.Grain, s entry.Status, count int, parent string) *entry.Entry {
		return &entry.Entry{ID: id, Facet: entry.Judgement, Grain: g, Status: s, Origin: entry.Behavior, Count: count, Parent: parent}
	}
	st := &store.State{Entries: map[string]*entry.Entry{}}
	for _, e := range []*entry.Entry{
		ent("j-1", entry.Class, entry.Stable, 5, ""),
		ent("j-2", entry.Class, entry.Candidate, 1, ""),
		ent("j-3", entry.Class, entry.Candidate, 2, ""),
		ent("j-4", entry.Action, entry.Candidate, 1, ""),
		ent("j-5", entry.Action, entry.Candidate, 1, "j-1"), // the class's evidence
		ent("j-6", entry.Class, entry.Dormant, 1, ""),
		ent("j-7", entry.Class, entry.Shifting, 4, ""),
	} {
		st.Entries[e.ID] = e
	}
	c := facetCounts(st)[entry.Judgement]
	if c != (facetCount{Settled: 1, Watching: 3, SeenTwice: 1}) {
		t.Fatalf("%+v", c)
	}
	if _, ok := facetCounts(st)[entry.Workflow]; ok {
		t.Fatal("a facet without entries has counts")
	}
}

// The tab bar fits one row on a 390-point iPhone at the default text size, so the app does not fall back to the
// sideways-scrolling bar it draws under the first cards. The model is the app's: label 12 pt (a CJK character 12 pt,
// a Latin one about 6.5), 12 pt padding on each side of a tab, 8 pt between tabs, 16 pt page margins, and a 4-digit
// badge in 13 pt digits.
func TestTabBarFitsAPhone(t *testing.T) {
	for _, lang := range []tr{trOf("zh-Hans", ""), trOf("en", "")} {
		tabs := barTabs(lang)
		w := 0.0
		for i, x := range tabs {
			for _, r := range x.Title {
				if r < 0x2E80 {
					w += 6.5
				} else {
					w += 12
				}
			}
			w += 24
			if i > 0 {
				w += 8
			}
		}
		w += 4 + 4*7.8 // the 待确认 badge
		if w > 390-2*16 {
			t.Errorf("%v: the tab bar is %.0f pt wide", tabs, w)
		}
	}
	more := moreSection(trOf("zh-Hans", ""))
	if len(more.Items) != 3 {
		t.Fatalf("the tabs left out of the bar are not on the start page: %+v", more)
	}
}
