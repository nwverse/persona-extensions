package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/curate"
	"github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/entry"
	"github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/store"
	"github.com/nwverse/persona/sdk/go/wire"
)

// Synthetic fixtures only: verify every subpage stays within the hub's View schema and 300-item / 20,000-char limits.

func TestReviewTabOver300ProposalsSchemaAndPaging(t *testing.T) {
	a := v4App(t)
	a.c.St.Lock()
	for i := 0; i < 450; i++ {
		a.c.St.S.Proposals = append(a.c.St.S.Proposals, &store.Proposal{
			ID:     fmt.Sprintf("K-%04d", i),
			Kind:   "pin_fact",
			Title:  fmt.Sprintf("Synthetic proposal %d for project-x", i),
			Risk:   "low",
			Status: "pending",
		})
	}
	a.c.St.Unlock()

	v0, _ := render(t, a, page{Tab: tabReview}, "zh-Hans")
	var nextState page
	foundMore := false
	for _, act := range wire.Actions(v0) {
		if act.ID == "open:review:more" && act.Push != nil {
			foundMore = true
			if err := json.Unmarshal([]byte(act.Push.State), &nextState); err != nil {
				t.Fatalf("decode review next state: %v", err)
			}
		}
	}
	if !foundMore {
		t.Fatalf("expected pagination row on review tab with 450 pending proposals")
	}
	if nextState.Sub != "100" {
		t.Fatalf("expected next review offset 100, got %q", nextState.Sub)
	}
	render(t, a, nextState, "zh-Hans")
	render(t, a, page{Tab: tabReview, Sub: "400"}, "en")
}

func TestFacetJudgementAndWorkflowManyTypesSchemaAndPaging(t *testing.T) {
	a := v4App(t)
	a.c.St.Lock()
	for _, f := range []entry.Facet{entry.Judgement, entry.Workflow} {
		for i := 0; i < 420; i++ {
			id := fmt.Sprintf("e-%s-%04d", f, i)
			a.c.St.S.Entries[id] = &entry.Entry{
				ID:     id,
				Facet:  f,
				Grain:  entry.Action,
				Type:   fmt.Sprintf("type_%04d", i),
				Text:   fmt.Sprintf("Synthetic %s entry %d", f, i),
				Status: entry.Candidate,
				Count:  1,
			}
		}
	}
	a.c.St.Unlock()

	for _, f := range []entry.Facet{entry.Judgement, entry.Workflow} {
		for _, locale := range []string{"zh-Hans", "en"} {
			v0, _ := render(t, a, page{Tab: tabFacets, Facet: string(f)}, locale)
			var nextState page
			foundMore := false
			for _, act := range wire.Actions(v0) {
				if act.ID == "open:facet:more" && act.Push != nil {
					foundMore = true
					if err := json.Unmarshal([]byte(act.Push.State), &nextState); err != nil {
						t.Fatalf("decode facet next state: %v", err)
					}
				}
			}
			if !foundMore {
				t.Fatalf("expected pagination row on facet %s (%s) with 420 types", f, locale)
			}
			if nextState.Sub == "" {
				t.Fatalf("expected non-empty Sub offset for facet %s (%s)", f, locale)
			}
			render(t, a, nextState, locale)
		}
	}
}

func TestRunPageLongEmptyAndDisallowedMarkdownSchema(t *testing.T) {
	a := v4App(t)
	longLine := strings.Repeat("Synthetic daily summary line for project-x. ", 10) + "\n"
	longReport := strings.Repeat(longLine, 65) // > 28,000 chars
	a.c.St.Lock()
	a.c.St.S.Runs = []*store.Run{
		{ID: "run-long", Kind: "daily", Period: "2026-11-20", Report: longReport},
		{ID: "run-empty", Kind: "daily", Period: "2026-11-21", Report: ""},
		{ID: "run-unsafe-md", Kind: "daily", Period: "2026-11-22", Report: "Summary ![img](https://example.com/a.png) and [bad](javascript:alert(1))"},
	}
	a.c.St.Unlock()

	for _, id := range []string{"run-long", "run-empty", "run-unsafe-md"} {
		for _, locale := range []string{"zh-Hans", "en"} {
			render(t, a, page{Tab: tabReports, Open: id}, locale)
		}
	}
}

func TestCardPageOver300LinesSchema(t *testing.T) {
	a := v4App(t)
	a.c.St.Lock()
	who := "person:synthetic-u1"
	a.c.St.S.Contacts[who] = &entry.Contact{Person: who, Name: "user-a", Ties: map[string]int{"dm": 10}, Score: 20, First: "2026-11-01", Last: "2026-11-24"}
	for i := 0; i < 340; i++ {
		id := fmt.Sprintf("e-card-%04d", i)
		a.c.St.S.Entries[id] = &entry.Entry{
			ID:     id,
			Facet:  entry.People,
			Person: who,
			Part:   entry.PartFact,
			Text:   fmt.Sprintf("Synthetic card line %d for user-a", i),
			Status: entry.Stable,
			Count:  2,
		}
	}
	a.c.St.Unlock()

	render(t, a, page{Tab: tabPeople, Open: "card:" + who}, "zh-Hans")
	render(t, a, page{Tab: tabPeople, Open: "card:" + who, Show: "reported"}, "en")
}

func TestEntryNodeProposalAndHealthHardeningSchema(t *testing.T) {
	a := v4App(t)
	longText := strings.Repeat("synthetic-word ", 220) // > 3,000 chars
	a.c.St.Lock()
	var counterIDs []string
	for i := 0; i < 45; i++ {
		cid := fmt.Sprintf("e-cnt-%02d", i)
		counterIDs = append(counterIDs, cid)
		a.c.St.S.Entries[cid] = &entry.Entry{ID: cid, Facet: entry.Interaction, Text: fmt.Sprintf("Counter %d", i), Status: entry.Candidate, Count: 1}
	}
	a.c.St.S.Entries["e-long"] = &entry.Entry{
		ID:       "e-long",
		Facet:    entry.Interaction,
		Text:     longText,
		Status:   entry.Stable,
		Count:    3,
		Counters: counterIDs,
		Export:   &entry.ExportText{Text: longText},
	}
	a.c.St.S.Proposals = append(a.c.St.S.Proposals, &store.Proposal{
		ID:       "K-long",
		Kind:     "pin_fact",
		Title:    longText,
		Detail:   longText,
		Risk:     "low",
		Status:   "pending",
		Evidence: []store.Evidence{{Speaker: "user-a", Chat: "chat-1", Day: "2026-11-20", N: 1, Quote: longText}},
	})
	a.c.St.S.Graph.Nodes["n-hub"] = &store.Node{ID: "n-hub", Name: "project-x", Type: "project", Status: "active"}
	for i := 0; i < 320; i++ {
		oid := fmt.Sprintf("n-o-%03d", i)
		a.c.St.S.Graph.Nodes[oid] = &store.Node{ID: oid, Name: fmt.Sprintf("peer-%03d", i), Type: "person", Status: "active"}
		aid := fmt.Sprintf("a-%03d", i)
		a.c.St.S.Graph.Assertions[aid] = &store.Assertion{ID: aid, S: "n-hub", P: "relates_to", O: oid, Status: "active", ValidFrom: "2026-11-01", Confidence: "high"}
	}
	a.c.St.Unlock()

	render(t, a, page{Tab: tabFacets, Open: "e:e-long"}, "zh-Hans")
	render(t, a, page{Tab: tabReview, Open: "K-long"}, "zh-Hans")
	render(t, a, page{Tab: "graph", Open: "n-hub"}, "zh-Hans")

	// Health refusal table with > 100 refusals (TableComponent.rows maxItems=100).
	refusals := make([]store.Refusal, 120)
	for i := range refusals {
		refusals[i] = store.Refusal{Name: fmt.Sprintf("src-%03d", i), Step: "daily", From: 1, To: 10, Lines: 10, Reason: "blocked", Outcome: store.RefusalSkipped}
	}
	vHealth := &wire.View{Title: "记忆体检", Sections: []wire.Section{refusalSection(&curate.RunBrief{Period: "2026-11-23", Refusals: refusals, Skipped: 120})}}
	if err := wire.CheckView(vHealth); err != nil {
		t.Fatalf("health refusal section fails CheckView: %v", err)
	}
}
