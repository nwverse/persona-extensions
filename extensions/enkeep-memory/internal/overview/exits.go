package overview

import (
	"slices"
	"strings"

	"github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/entry"
	"github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/memsrc"
	"github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/norm"
)

// The exit (unified design §2.1): the used L1 principles and collaboration contracts go to the hub through the
// extension's own memory source, memory.entries, beside the curated entries; the connector and the memory source keep
// their ids.
//
//   - a pattern with no category and no restricted space: "ov:pattern:<id>", rank 950–985 by its spread when recalled,
//     and a candidate for the three-bucket standing set with its Score and L1 metadata (memsrc decides whether it
//     stands, never the maker);
//   - one resting on any categorised evidence or said in a space: "ov:cat:<category>:<id>", never standing, rank 900,
//     with every category and the space, for the hub to grade by scene.
const (
	rankTop    = 985
	rankBottom = 950
	rankCat    = 900
)

// genericRuleTypes name no specific occasion and do not trigger the per-group 20% cap in the contract bucket.
var genericRuleTypes = map[string]bool{"": true, "correction": true, "preference": true, "纠正": true}

// independentQuotes returns the pattern's distinct normalised user quote count, falling back to SourcesCount for
// older states written before UniqueQuotes was recorded.
func independentQuotes(p *Pattern) int {
	if p.UniqueQuotes > 0 {
		return p.UniqueQuotes
	}
	return p.SourcesCount
}

// Score ranks a used L1 pattern within its standing bucket by multi-dimensional leverage (unified design §2.1):
//   - Hard contract:     80 + min(20, viol*4) + min(24, scenes*6) + min(16, quotes*2) + owned(12+min(18,cov*3)) + check(6);
//     capped at 70 when confined to a single scene and not user-owned (keeps narrow incidents in entity recall).
//   - Regular contract:  lifecycle(core:30 / scoped:24) + min(24, scenes*6) + min(20, quotes*2) + min(16, weeks*4)
//   - owned(15+min(15,cov*3)); single-scene non-owned capped at 70.
//   - Principle:         lifecycle(core:30 / scoped:26) + min(25, scenes*5) + min(20, weeks*4) + min(15, quotes*2)
//   - hasPolesOrException(10) + owned(18) + decisionOrAttention(8) + judgementOrWorkflow(6)
func Score(p *Pattern) int {
	if p == nil || !p.Used() {
		return 0
	}
	quotes := independentQuotes(p)
	weeks := len(p.ActiveWeeks)
	cov := len(p.CoveredIDs)
	owned := cov > 0
	if p.IsContract() {
		if p.Hard {
			s := 80 + min(20, p.ViolationCount*4) + min(24, p.ScenesCount*6) + min(16, quotes*2)
			if owned {
				s += 12 + min(18, cov*3)
			}
			if p.Polarity == PolarityCheck {
				s += 6
			}
			if p.ScenesCount <= 1 && !owned {
				s = min(s, 70)
			}
			return s
		}
		base := 24
		if p.Lifecycle == Core {
			base = 30
		}
		s := base + min(24, p.ScenesCount*6) + min(20, quotes*2) + min(16, weeks*4)
		if owned {
			s += 15 + min(15, cov*3)
		}
		if p.ScenesCount <= 1 && !owned {
			s = min(s, 70)
		}
		return s
	}
	base := 26
	if p.Lifecycle == Core {
		base = 30
	}
	bonus := 0
	if poleText(p.Poles) != "" || strings.TrimSpace(p.Exception) != "" {
		bonus += 10
	}
	if owned {
		bonus += 18
	}
	if p.Dimension == "decision" || p.Dimension == "attention" {
		bonus += 8
	}
	if p.Facet == string(entry.Judgement) || p.Facet == string(entry.Workflow) {
		bonus += 6
	}
	return base + min(25, p.ScenesCount*5) + min(20, weeks*4) + min(15, quotes*2) + bonus
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

// suffixOf returns the boundary suffix ("（两端：…）" or "（例外：…）") for p, or "" if none applies.
func suffixOf(p *Pattern) string {
	if ends := poleText(p.Poles); ends != "" {
		return "（两端：" + ends + "）"
	}
	if x := strings.TrimSpace(p.Exception); x != "" && !strings.Contains(p.Text, x) {
		return "（例外：" + x + "）"
	}
	return ""
}

// Text is a pattern as one line the assistant can act on (for recall and UI). A pattern whose evidence goes both ways
// says both ends, each with when it holds (Poles), not "X unless W".
func Text(p *Pattern) string {
	return "跨事模式（" + facetLabel[p.Facet] + "）：" + strings.TrimSpace(p.Text) + suffixOf(p)
}

// poleText is both ends of a pattern, "在 A 时 X；在 B 时 Y"; "" unless there are two.
func poleText(ps []Pole) string {
	var parts []string
	for _, p := range ps {
		w, c := strings.TrimSpace(p.When), strings.TrimSpace(p.Choice)
		if w == "" || c == "" {
			continue
		}
		parts = append(parts, strings.TrimSuffix(w, "时")+"时，"+c)
	}
	if len(parts) < 2 {
		return ""
	}
	return strings.Join(parts[:2], "；")
}

// contractGroup returns "<facet>:<normalized rule_type>" for a contract's non-generic occasion/type, or "".
func contractGroup(p *Pattern) string {
	if !p.IsContract() {
		return ""
	}
	rt := strings.ToLower(strings.TrimSpace(p.RuleType))
	if genericRuleTypes[rt] {
		return ""
	}
	q := norm.Q(p.RuleType)
	if q == "" {
		return ""
	}
	return p.Facet + ":" + q
}

// Exits are the patterns as memory.entries entries, each with its last evidence day, score, and L1 metadata for the
// three-bucket standing set.
func Exits(s *State) []memsrc.Extra {
	if s == nil {
		return nil
	}
	var out []memsrc.Extra
	for _, p := range s.Sorted(func(p *Pattern) bool { return p.Used() }) {
		hex := strings.TrimPrefix(p.ID, "ov:")
		x := memsrc.Entry{Text: clip(Text(p), memsrc.MaxText), Facet: p.Facet, Status: "active", Subjects: p.Subjects}
		kind := ""
		l1Kind := ""
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
			if p.IsContract() {
				l1Kind = memsrc.BucketContract
			} else {
				l1Kind = memsrc.BucketPrinciple
			}
		}
		x.Subjects = slices.Clone(x.Subjects)
		out = append(out, memsrc.Extra{
			Entry:      x,
			Last:       p.LastEvidence,
			Score:      Score(p),
			Kind:       kind,
			Evidence:   slices.Clone(p.EvidenceIDs),
			L1Kind:     l1Kind,
			Hard:       p.Hard,
			Polarity:   p.Polarity,
			Dimension:  p.Dimension,
			Group:      contractGroup(p),
			Quotes:     independentQuotes(p),
			Body:       strings.TrimSpace(p.Text),
			Suffix:     suffixOf(p),
			CoveredIDs: slices.Clone(p.CoveredIDs),
		})
	}
	return out
}
