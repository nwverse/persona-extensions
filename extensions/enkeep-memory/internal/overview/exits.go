package overview

import (
	"slices"
	"strings"

	"github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/entry"
	"github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/memsrc"
)

// The exit (design 2.4): the used patterns go to the hub through the extension's own memory source, memory.entries,
// beside the curated entries; the connector and the memory source keep their ids.
//
//   - a pattern with no category and no restricted space: "ov:pattern:<id>", rank 950–985 by its spread, and a
//     candidate for the standing set with its Score (0.2.0-beta.52: memsrc.Resident picks the few that stand, beside
//     the curated entries, within the hub's room; before, up to 80 stood, far more than the hub ever took);
//   - one resting on any categorised evidence or said in a space: "ov:cat:<category>:<id>", never standing, rank 900,
//     with every category and the space, for the hub to grade by scene.
const (
	rankTop    = 985
	rankBottom = 950
	rankCat    = 900
)

// Score is how much a used pattern matters, on entry.Score's scale: its lifecycle (core 25, scoped 20), how many
// scenes (projects and source kinds) and independent sources it rests on, and over how many weeks; at most 70, so a
// pattern competes with the curated entries for the standing set and does not take it whole. (The hard evaluation of
// 2026-11-24: scored up to 99, patterns of 200–300 bytes filled the set and it read worse than the entries alone.)
func Score(p *Pattern) int {
	s := 0
	switch p.Lifecycle {
	case Core:
		s = 25
	case Scoped:
		s = 20
	default:
		return 0
	}
	s += min(20, 2*p.ScenesCount) + min(15, p.SourcesCount) + min(10, 2*len(p.ActiveWeeks))
	return min(70, s)
}

// facetLabel names a facet in the entry's text, so a pattern read alone says which side of the user it is about.
var facetLabel = map[string]string{
	string(entry.Interaction): "和 AI 打交道",
	string(entry.Tools):       "AI 和工具",
	string(entry.Judgement):   "默认判断",
	string(entry.Workflow):    "做事方式",
	string(entry.World):       "人、项目和事实",
	string(entry.Persona):     "关于用户",
}

// Text is a pattern as one line the assistant can act on. A pattern whose evidence goes both ways says both ends, each
// with when it holds (Poles), not "X unless W".
func Text(p *Pattern) string {
	t := "跨事模式（" + facetLabel[p.Facet] + "）：" + strings.TrimSpace(p.Text)
	if ends := poleText(p.Poles); ends != "" {
		return t + "（两端：" + ends + "）"
	}
	if x := strings.TrimSpace(p.Exception); x != "" && !strings.Contains(p.Text, x) {
		t += "（例外：" + x + "）"
	}
	return t
}

// poleText is both ends of a pattern, "在 A 时 X；在 B 时 Y"; "" unless there are two.
func poleText(ps []Pole) string {
	var parts []string
	for _, p := range ps {
		w, c := strings.TrimSpace(p.When), strings.TrimSpace(p.Choice)
		if w == "" || c == "" {
			continue
		}
		parts = append(parts, w+"时，"+c)
	}
	if len(parts) < 2 {
		return ""
	}
	return strings.Join(parts[:2], "；")
}

// Exits are the patterns as memory.entries entries, each with its last evidence day for the order among alike ones,
// and its score and side for the standing set.
func Exits(s *State) []memsrc.Extra {
	if s == nil {
		return nil
	}
	var out []memsrc.Extra
	for _, p := range s.Sorted(func(p *Pattern) bool { return p.Used() }) {
		hex := strings.TrimPrefix(p.ID, "ov:")
		x := memsrc.Entry{Text: clip(Text(p), memsrc.MaxText), Facet: p.Facet, Status: "active", Subjects: p.Subjects}
		kind := ""
		if len(p.Categories) > 0 || p.OriginSpace != "" {
			cat := "space"
			if len(p.Categories) > 0 {
				cat = p.Categories[0]
			}
			x.ID = "ov:cat:" + cat + ":" + hex
			x.Categories = p.Categories[:min(len(p.Categories), memsrc.MaxCats)]
			x.OriginSpace = p.OriginSpace
			x.Rank = rankCat
		} else {
			x.ID = "ov:pattern:" + hex
			x.Rank = min(rankTop, rankBottom+p.ScenesCount*p.SourcesCount/2)
			kind = entry.KindOfFacet(p.Facet)
		}
		x.Subjects = slices.Clone(x.Subjects)
		out = append(out, memsrc.Extra{Entry: x, Last: p.LastEvidence, Score: Score(p), Kind: kind, Evidence: slices.Clone(p.EvidenceIDs)})
	}
	return out
}
