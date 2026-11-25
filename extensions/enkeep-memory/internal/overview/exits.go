package overview

import (
	"slices"
	"strings"

	"github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/entry"
	"github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/memsrc"
)

// The exit (design 2.4): the used patterns go to the hub through the extension's own memory source, memory.entries,
// beside the curated entries; the connector and the memory source keep their ids. Ranks put them just under the
// profile summary (995, 990) and above every scattered entry but a pinned one:
//
//   - a pattern with no category and no restricted space: "ov:pattern:<id>", standing, rank 950–985 by its spread;
//   - one resting on any categorised evidence or said in a space: "ov:cat:<category>:<id>", never standing, rank 900,
//     with every category and the space, for the hub to grade by scene.
//
// At most StandingMost standing patterns are answered, the most spread first; the rest wait for the next pass.
const (
	StandingMost = 80
	rankTop      = 985
	rankBottom   = 950
	rankCat      = 900
)

// facetLabel names a facet in the entry's text, so a pattern read alone says which side of the user it is about.
var facetLabel = map[string]string{
	string(entry.Interaction): "和 AI 打交道",
	string(entry.Tools):       "AI 和工具",
	string(entry.Judgement):   "默认判断",
	string(entry.Workflow):    "做事方式",
	string(entry.World):       "人、项目和事实",
	string(entry.Persona):     "关于用户",
}

// Text is a pattern as one line the assistant can act on.
func Text(p *Pattern) string {
	t := "跨事模式（" + facetLabel[p.Facet] + "）：" + strings.TrimSpace(p.Text)
	if x := strings.TrimSpace(p.Exception); x != "" && !strings.Contains(p.Text, x) {
		t += "（例外：" + x + "）"
	}
	return t
}

// Exits are the patterns as memory.entries entries, each with its last evidence day for the order among alike ones.
func Exits(s *State) []memsrc.Extra {
	if s == nil {
		return nil
	}
	var out []memsrc.Extra
	standing := 0
	for _, p := range s.Sorted(func(p *Pattern) bool { return p.Used() }) {
		hex := strings.TrimPrefix(p.ID, "ov:")
		x := memsrc.Entry{Text: clip(Text(p), memsrc.MaxText), Facet: p.Facet, Status: "active", Subjects: p.Subjects}
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
			if standing >= StandingMost {
				continue
			}
			standing++
			x.ID = "ov:pattern:" + hex
			x.Standing = true
			x.Rank = min(rankTop, rankBottom+p.ScenesCount*p.SourcesCount/2)
		}
		x.Subjects = slices.Clone(x.Subjects)
		out = append(out, memsrc.Extra{Entry: x, Last: p.LastEvidence})
	}
	return out
}
