package entry

import "testing"

// A line of "the user's" that recurs word for word in three agent sessions is a template; an entry resting only on
// it is template-only; the same words in a chat, or in two sessions, are the user's. Placeholders only.
func TestTemplates(t *testing.T) {
	brief := "run the heavy gate on the gate machine, never on the laptop"
	ev := func(src, ref, q string) Evidence {
		return Evidence{Source: src, Ref: ref, Day: "2026-11-20", Who: WhoSelf, Quote: q}
	}
	es := map[string]*Entry{
		"a": {ID: "a", Evidence: []Evidence{ev(SrcAgent, "agents/1", brief)}},
		"b": {ID: "b", Evidence: []Evidence{ev(SrcAgent, "agents/2", brief)}},
		"c": {ID: "c", Evidence: []Evidence{ev(SrcAgent, "agents/3", brief)}},
		"d": {ID: "d", Evidence: []Evidence{ev(SrcAgent, "agents/3", brief), ev("S1", "conversations/x", "I said: answer in Chinese, always")}},
		"e": {ID: "e", Evidence: []Evidence{ev(SrcAgent, "agents/1", "only twice: this sentence is mine"), ev(SrcAgent, "agents/2", "only twice: this sentence is mine")}},
	}
	tpl := TemplateQuotes(es)
	if !TemplateOnly(es["a"], tpl) || TemplateOnly(es["d"], tpl) || TemplateOnly(es["e"], tpl) {
		t.Errorf("template-only: a %v d %v e %v", TemplateOnly(es["a"], tpl), TemplateOnly(es["d"], tpl), TemplateOnly(es["e"], tpl))
	}
	if !TemplateBacked(es, []string{"a", "b", "d", "e"}, tpl) || TemplateBacked(es, []string{"a", "d", "e"}, tpl) {
		t.Error("backed: half or more")
	}
}

// A correction the user had to make on more days scores higher.
func TestScoreCorrectionDays(t *testing.T) {
	mk := func(days ...string) *Entry {
		e := &Entry{Facet: Interaction, Type: "correction", Status: Stable, Count: 1}
		for _, d := range days {
			e.Evidence = append(e.Evidence, Evidence{Who: WhoSelf, Day: d, Quote: "q"})
		}
		return e
	}
	if Score(mk("2026-11-20", "2026-11-21", "2026-11-22")) <= Score(mk("2026-11-20")) {
		t.Error("repeated correction not above a single one")
	}
}
