package curate

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/entry"
	"github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/export"
)

// Synthetic seeds only (work-type placeholders): what an import keeps, merges and leaves out, and that a seed starts
// a candidate and goes out only once the user's own records bear it out (design v2 §11.2).
func TestSeedImport(t *testing.T) {
	m := &funcModel{answer: func(step string, in map[string]any) any {
		var out []any
		for _, it := range in["items"].([]any) {
			x := it.(map[string]any)
			cat := ""
			if strings.Contains(x["text"].(string), "占位私事") {
				cat = "health" // the model's reading: the third filter
			}
			out = append(out, map[string]any{"i": x["i"], "category": cat})
		}
		return map[string]any{"items": out}
	}}
	c, _ := newCurator(t, m, "review")
	c.St.Lock()
	own := c.create(&entry.Entry{Facet: entry.Interaction, Grain: entry.Class, Text: "汇报先写结论再写过程", Status: entry.Stable}, "t")
	c.St.Unlock()
	f := SeedFormat{Source: "notes", Items: []SeedItem{
		{ID: "r1", Kind: "rule", Text: "执行指令时直接做，不反复确认", Hard: true, Confirmed: "2026-03-01"},
		{ID: "r2", Kind: "rule", Text: "汇报先写结论再写过程", Confirmed: "2026-03-01"},                       // the curator has it: merged
		{ID: "f1", Kind: "fact", Text: "在推进占位项目的发布流程", Confirmed: "2026-03-01", Expiring: "recent"}, // new
		{ID: "f2", Kind: "fact", Text: "打算明年买房", Confirmed: "2026-03-01"},                           // keyword: sensitive
		{ID: "f3", Kind: "fact", Text: "每周固定运动", Category: "健康", Confirmed: "2026-03-01"},           // category: sensitive
		{ID: "f4", Kind: "fact", Text: "占位私事的安排", Confirmed: "2026-03-01"},                          // the model: sensitive
		{ID: "f5", Kind: "fact", Text: "没有确认过的一条", Confirmed: ""},                                   // unconfirmed
		{ID: "r1", Kind: "rule", Text: "同一个编号又来一次", Confirmed: "2026-03-01"},                        // duplicate id
	}}
	ctx := context.Background()
	r, err := c.ImportSeeds(ctx, f, false, 0)
	if err != nil {
		t.Fatal(err)
	}
	if r.New != 2 || r.Merged != 1 || r.Skipped[SeedSensitive] != 3 || r.Skipped[SeedUnconfirmed] != 1 || r.Skipped[SeedDuplicate] != 1 {
		t.Fatalf("preview: %+v", r)
	}
	if len(c.St.S.Entries) != 1 {
		t.Fatalf("a preview wrote: %d entries", len(c.St.S.Entries))
	}
	if r, err = c.ImportSeeds(ctx, f, true, 0); err != nil || !r.Applied || r.New != 2 {
		t.Fatalf("apply: %+v %v", r, err)
	}
	if !slices.Contains(own.Flags, seedMark("notes", "r2")) {
		t.Fatalf("merged entry not marked: %v", own.Flags)
	}
	var seed *entry.Entry
	for _, e := range c.St.S.Entries {
		if e.Origin == entry.Seed && e.Seed.ID == "r1" {
			seed = e
		}
		if e.Origin == entry.Seed && (e.Sensitive != "" || strings.Contains(e.Text, "买房") || strings.Contains(e.Text, "占位私事")) {
			t.Fatalf("a sensitive item came in: %+v", e)
		}
	}
	if seed == nil || seed.Status != entry.Candidate || export.Usable(c.St.S, seed) {
		t.Fatalf("a seed starts a candidate, out of the exits: %+v", seed)
	}
	// Borne out by one active day of the user's own records: stable, in the exits; with K21 off, out again.
	c.St.Lock()
	a := c.Observe(act(entry.Interaction, "action-seed", "chats/s1", "2026-03-15", "quote-s1"), "t")
	if err := c.File(a, seed.ID, "t"); err != nil {
		t.Fatal(err)
	}
	d, _ := Settle(c.St.S, seed, tally(c.St.S, seed), "2026-03-15")
	seed.Status = d.Status
	if d.Status != entry.Stable || !export.Usable(c.St.S, seed) {
		t.Fatalf("borne out: %+v", d)
	}
	c.St.S.Switches = map[string]entry.SwitchSetting{entry.K21Seeds: {Value: "off", By: "user"}}
	if export.Usable(c.St.S, seed) {
		t.Fatal("K21 off: a seed still goes out")
	}
	c.St.S.Switches = nil
	c.St.Unlock()
	// The same source again: the same items are the same, a changed text is updated.
	f.Items[0].Text = "执行指令时直接做，做完再汇报"
	if r, err = c.ImportSeeds(ctx, f, true, 0); err != nil || r.Updated != 1 || r.Same != 1 || r.New != 0 {
		t.Fatalf("again: %+v %v", r, err)
	}
	if seed.Text != "执行指令时直接做，做完再汇报" {
		t.Fatalf("not updated: %q", seed.Text)
	}
	st := c.Seeds(export.Usable)
	if !st.On || st.BySource["notes"]["stable"] != 1 || st.Merged != 1 || len(st.Imports) != 2 {
		t.Fatalf("status: %+v", st)
	}
}

// Without a model the sensitive check cannot run: nothing is imported, nothing written.
func TestSeedImportNeedsModel(t *testing.T) {
	c, _ := newCurator(t, nil, "review")
	c.Model = nil
	_, err := c.ImportSeeds(context.Background(), SeedFormat{Source: "notes", Items: []SeedItem{{ID: "a", Kind: "rule", Text: "占位规则", Confirmed: "2026-03-01"}}}, true, 0)
	if err == nil || len(c.St.S.Entries) != 0 {
		t.Fatalf("imported unchecked: %v", err)
	}
}

// The general format is strict: unknown fields, no source or too many items are refused.
func TestParseSeeds(t *testing.T) {
	if _, err := ParseSeeds([]byte(`{"source":"notes","items":[{"id":"a","kind":"rule","text":"x","confirmed":"2026-03-01"}]}`)); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{`{"source":"","items":[{"id":"a"}]}`, `{"source":"a b","items":[{"id":"a"}]}`, `{"source":"n","items":[],"x":1}`, `{"source":"n","items":[]}`} {
		if _, err := ParseSeeds([]byte(bad)); err == nil {
			t.Errorf("accepted %s", bad)
		}
	}
}
