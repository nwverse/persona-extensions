package curate

import (
	"context"
	"testing"

	"github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/entry"
)

// Rules saying one thing in other words are made one (0.2.6): the strongest stays with the group's exception; one
// the assistant's own memory wrote is marked covered rather than merged away; one saying the opposite is left apart;
// the merge runs again only when the rules changed.
func TestMergeRules(t *testing.T) {
	c := lockedCurator(t)
	ev := func(day, q string) []entry.Evidence {
		return []entry.Evidence{{Who: entry.WhoSelf, Day: day, Quote: q}}
	}
	rule := func(text, origin string, count int, q string) *entry.Entry {
		e := c.create(&entry.Entry{Facet: entry.Interaction, Grain: entry.Action, Type: "correction", Text: text, Status: entry.Stable,
			Origin: origin, Count: count, Evidence: ev("2026-11-20", q)}, "t")
		return e
	}
	a := rule("rule-a: do not stop to ask", entry.Behavior, 6, "以后别再停下来问我")
	b := rule("rule-b: no plan card, just start", entry.Behavior, 2, "别再弹计划卡")
	h := rule("rule-h: act on what was already allowed", entry.Handwritten, 3, "不要再让我确认")
	d := rule("rule-d: always ask before acting", entry.Behavior, 2, "以后别再自己动手")
	calls := 0
	m := &funcModel{answer: func(step string, in map[string]any) any {
		calls++
		ids := map[string]string{}
		for _, x := range in["rules"].([]any) {
			r := x.(map[string]any)
			ids[r["text"].(string)] = r["id"].(string)
		}
		return map[string]any{"groups": []any{map[string]any{
			"rules":       []any{ids[a.Text], ids[b.Text], ids[h.Text], ids[d.Text], "r99"},
			"conflicting": []any{ids[d.Text]},
			"text":        "在已放行的事上，助手直接做，不停下来问", "exception": "只有涉及线上、对外可见或删数据时才停下问", "reason": "same"}}}
	}}
	run := newRunT()
	if err := c.mergeRules(context.Background(), m, run); err != nil {
		t.Fatal(err)
	}
	if a.MergedInto != "" || !a.Live() {
		t.Fatalf("the strongest rule did not stay: %+v", a)
	}
	if b.MergedInto != a.ID || entry.CoveredBy(h) != a.ID || h.MergedInto != "" || !h.Live() {
		t.Fatalf("merged %q, covered %q (merged %q)", b.MergedInto, entry.CoveredBy(h), h.MergedInto)
	}
	if a.Text != "在已放行的事上，助手直接做，不停下来问" || a.Exception != "只有涉及线上、对外可见或删数据时才停下问" {
		t.Fatalf("kept rule: %q / %q", a.Text, a.Exception)
	}
	if d.MergedInto != "" || entry.CoveredBy(d) != "" {
		t.Errorf("the opposite rule was merged")
	}
	if run.Counts["rule_groups"] != 1 || run.Counts["rules_merged"] != 2 || run.Counts["rules_covered"] != 1 {
		t.Errorf("counts %v", run.Counts)
	}
	if err := c.mergeRules(context.Background(), m, newRunT()); err != nil || calls != 1 {
		t.Errorf("asked again with nothing changed: %d calls, %v", calls, err)
	}
}
