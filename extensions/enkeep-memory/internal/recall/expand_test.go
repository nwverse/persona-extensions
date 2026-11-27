package recall

import (
	"strings"
	"testing"

	"github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/entry"
	"github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/store"
)

func TestSearchOneHopEntityExpansion(t *testing.T) {
	st := &store.State{
		Entries: map[string]*entry.Entry{
			"i-proj": {
				ID: "i-proj", Facet: entry.Interaction, Status: entry.Stable, Count: 3, Last: "2026-11-20",
				Text:   "在星云网关里改动前必须先跑契约测试",
				Scopes: []entry.Scope{{Kind: entry.ScopeProject, Name: "星云网关"}},
				Impact: entry.Impact{Moment: "dispatch", Action: "先跑契约测试", Object: "星云网关"},
				Evidence: []entry.Evidence{
					{Source: "S1", Day: "2026-11-18", Who: entry.WhoSelf, Quote: "星云网关改动前一定要先跑契约测试"},
					{Source: "S1", Day: "2026-11-19", Who: entry.WhoSelf, Quote: "星云网关改动前一定要先跑契约测试"}, // duplicate
					{Source: "S1", Day: "2026-11-20", Who: entry.WhoSelf, Quote: "不跑契约测试不许合进主分支"},
				},
			},
			"t-dep": {
				ID: "t-dep", Facet: entry.Tools, Status: entry.Stable, Count: 2, Last: "2026-11-19",
				Text:   "极光鉴权库发版后要同步更新依赖锁文件",
				Object: "极光鉴权库",
				Impact: entry.Impact{Moment: "use_object", Action: "更新锁文件", Object: "极光鉴权库"},
				Evidence: []entry.Evidence{
					{Source: "S1", Day: "2026-11-19", Who: entry.WhoSelf, Quote: "极光鉴权库发版记得更锁文件"},
				},
			},
		},
		Graph: &store.Graph{
			Nodes: map[string]*store.Node{
				"n-proj-1": {ID: "n-proj-1", Type: "project", Name: "星云网关", Status: "active"},
				"n-proj-2": {ID: "n-proj-2", Type: "repo", Name: "极光鉴权库", Status: "active"},
				"n-pers-1": {ID: "n-pers-1", Type: "person", Name: "甲乙丙负责人", Status: "active"},
				"n-top-1":  {ID: "n-top-1", Type: "topic", Name: "泛泛话题", Status: "active"},
			},
			Assertions: map[string]*store.Assertion{
				"a-1": {ID: "a-1", S: "n-pers-1", P: "owns", O: "n-proj-1", Status: "active", ValidFrom: "2026-11-15"},
				"a-2": {ID: "a-2", S: "n-proj-1", P: "depends_on", O: "n-proj-2", Status: "active", ValidFrom: "2026-11-16"},
				"a-3": {ID: "a-3", S: "n-proj-1", P: "about", O: "n-top-1", Status: "active", ValidFrom: "2026-11-17"}, // noise edge
			},
		},
	}

	res := Search(st, Query{Text: "星云网关"})
	if len(res.Entities) != 1 {
		t.Fatalf("Entities = %d, want 1", len(res.Entities))
	}
	ec := res.Entities[0]
	if ec.Name != "星云网关" {
		t.Fatalf("Entity name = %q, want 星云网关", ec.Name)
	}
	if len(ec.Skeleton) != 2 {
		t.Fatalf("Skeleton edges = %+v, want 2 (owns + depends_on, excluding noise 'about')", ec.Skeleton)
	}
	if len(ec.Rules) != 1 || ec.Rules[0].ID != "i-proj" {
		t.Fatalf("Anchored rules = %+v, want [i-proj]", ec.Rules)
	}
	if len(ec.Quotes) != 2 {
		t.Fatalf("Deduplicated quotes = %+v, want 2", ec.Quotes)
	}

	// 1-hop neighbor entry (t-dep on 极光鉴权库) should appear in res.Hits with Hop=1.
	var foundHop bool
	for _, h := range res.Hits {
		if h.ID == "t-dep" {
			foundHop = true
			if h.Hop != 1 || !strings.Contains(h.Via, "极光鉴权库") {
				t.Errorf("1-hop hit metadata wrong: %+v", h)
			}
			if !strings.Contains(Format(h), "1跳") {
				t.Errorf("Format missing 1跳 marker: %q", Format(h))
			}
		}
	}
	if !foundHop {
		t.Fatalf("expected 1-hop neighbor hit t-dep in %+v", res.Hits)
	}

	// SkeletonNeighbors helper returns structural neighbors only.
	nbs := SkeletonNeighbors(st, []string{"星云网关"}, 4)
	if len(nbs) != 2 {
		t.Fatalf("SkeletonNeighbors = %v, want 2", nbs)
	}
}
