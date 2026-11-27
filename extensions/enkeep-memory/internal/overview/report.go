package overview

import (
	"fmt"
	"slices"
	"strings"
)

// tr picks the Chinese or the English text.
type tr bool

func (z tr) s(zh, en string) string {
	if z {
		return zh
	}
	return en
}

func (z tr) f(zh, en string, a ...any) string { return fmt.Sprintf(z.s(zh, en), a...) }

// Chinese says whether a language tag is Chinese (the reports' two languages: Chinese, else English).
func Chinese(lang string) bool { return lang == "" || strings.HasPrefix(strings.ToLower(lang), "zh") }

var facetLabelEN = map[string]string{
	"interaction": "working with the AI", "tools": "the AI and tools", "judgement": "default judgements",
	"workflow": "how things are done", "world": "people, projects and facts", "persona": "about the user",
}

func (z tr) facet(f string) string { return z.s(facetLabel[f], facetLabelEN[f]) }

// SummaryMost bounds the pushed summary (design 2.2: the notification is a short summary, the full text a page). The
// hub refuses a body over 1000 characters ("body is too long"); the summary stays well under it.
const SummaryMost = 300

// Coverage is how many of the six facets have at least one used pattern, and per facet the count by lifecycle.
func Coverage(s *State) (covered int, by map[string]map[string]int) {
	by = map[string]map[string]int{}
	for _, p := range s.Patterns {
		if by[p.Facet] == nil {
			by[p.Facet] = map[string]int{}
		}
		by[p.Facet][p.Lifecycle]++
	}
	for _, f := range Facets {
		if m := by[string(f)]; m[Core]+m[Scoped] > 0 {
			covered++
		}
	}
	return covered, by
}

// private says whether a pattern's words stay out of a notification: any category or restricted space.
func private(p *Pattern) bool { return len(p.Categories) > 0 || p.OriginSpace != "" }

// changedBy are the patterns a run made or moved, of the lifecycles given, in the state's order.
func changedBy(s *State, run *Run, lifecycles ...string) []*Pattern {
	if run == nil {
		return nil
	}
	return s.Sorted(func(p *Pattern) bool {
		return slices.Contains(run.Changed, p.ID) && slices.Contains(lifecycles, p.Lifecycle)
	})
}

// Section is the overview's part of a weekly or monthly report, in Markdown: coverage per facet, the patterns used
// now that the run made or moved, the drifting ones to look at, and the last forward check. Categorised patterns are
// counted, never quoted.
func Section(s *State, run *Run, lang string) string {
	z := tr(Chinese(lang))
	var b strings.Builder
	line := func(t string) { b.WriteString(t + "\n") }
	covered, by := Coverage(s)
	line(z.s("## 跨事总览", "## Cross-event overview"))
	line(z.f("六个方面里 %d 个已有确认的跨事模式。", "%d of six facets have a confirmed cross-event pattern.", covered))
	for _, f := range Facets {
		m := by[string(f)]
		line(z.f("- %s：在用 %d（稳定 %d、有边界 %d），观察中 %d，在变 %d，阶段已结束 %d",
			"- %s: used %d (stable %d, scoped %d), watching %d, drifting %d, era closed %d",
			z.facet(string(f)), m[Core]+m[Scoped], m[Core], m[Scoped], m[Candidate], m[Drifting], m[EraClosed]))
	}
	if run != nil {
		line("")
		line(z.f("本次调用模型 %d 次。", "%d model call(s) this time.", run.Calls))
		if run.Error != "" {
			line(z.s("这次没跑完：", "Not finished this time: ") + run.Error)
		}
	}
	used := changedBy(s, run, Core, Scoped)
	if len(used) > 0 {
		line("")
		line(z.f("## 这次确认或更新的模式（%d）", "## Patterns confirmed or updated (%d)", len(used)))
		hidden := 0
		for _, p := range used {
			if private(p) {
				hidden++
				continue
			}
			line(z.f("- %s%s（%d 个场景、%d 处独立证据、%d 周）", "- %s%s (%d scenes, %d independent sources, %d weeks)",
				p.Text, exceptionClause(p), p.ScenesCount, p.SourcesCount, len(p.ActiveWeeks)))
		}
		if hidden > 0 {
			line(z.f("- 另有 %d 条带受控分类，只在插件页上看", "- %d more in a protected category, on the page only", hidden))
		}
	}
	drift := changedBy(s, run, Drifting)
	if len(drift) > 0 {
		line("")
		line(z.f("## 在变：最近常有反例，先不用（%d）", "## Drifting: recent counter-examples, not used for now (%d)", len(drift)))
		for _, p := range drift {
			if private(p) {
				continue
			}
			line(z.f("- %s（反例占 %.0f%%）", "- %s (%.0f%% counter-examples)", p.Text, p.CounterRatio*100))
		}
		line(z.s("还成立的话，在插件页上把它的证据点「对」；不成立就点「不对」。",
			"If it still holds, mark its evidence right on the page; if not, mark it wrong."))
	}
	if n := len(s.Holdouts); n > 0 {
		h := s.Holdouts[n-1]
		line("")
		line(z.f("## 预测检验（%s，%d 题）", "## Forward check (%s, %d questions)", h.Week, h.N))
		line(z.f("用之前的模式预测这周的真实取舍：跨事模式 %.0f%%，同样字数的散点条目 %.0f%%，不带记忆 %.0f%%。",
			"The patterns kept before predicting the week's real choices: patterns %.0f%%, scattered entries in as many characters %.0f%%, no memory %.0f%%.",
			h.Rate[ArmOverview]*100, h.Rate[ArmScatter]*100, h.Rate[ArmNone]*100))
	}
	return strings.TrimSpace(b.String())
}

// Summary is the pushed text of a week: what the overview found, the operations' counts the old report carried, and
// where the full text is. At most SummaryMost characters; categorised patterns are never quoted.
func Summary(s *State, run *Run, ops map[string]int, lang string) string {
	z := tr(Chinese(lang))
	covered, _ := Coverage(s)
	used := changedBy(s, run, Core, Scoped)
	drift := changedBy(s, run, Drifting)
	head := z.f("跨事总览：这周确认或更新 %d 条模式，%d 条在变待你看；六个方面 %d 个已有模式。",
		"Overview: %d pattern(s) confirmed or updated this week, %d drifting for you to look at; %d of six facets have patterns.",
		len(used), len(drift), covered)
	if run == nil {
		head = z.f("跨事总览这周没有跑出结果；六个方面 %d 个已有模式。", "No overview this week; %d of six facets have patterns.", covered)
	}
	var tail []string
	if n := ops["effective"]; n > 0 {
		tail = append(tail, z.f("新生效 %d 条", "%d new in effect", n))
	}
	if n := ops["commitments_overdue"]; n > 0 {
		tail = append(tail, z.f("可能逾期的承诺 %d 条", "%d commitment(s) possibly overdue", n))
	}
	if n := ops["asks"]; n > 0 {
		tail = append(tail, z.f("好久没见的 %d 条待确认", "%d quiet entr(ies) to confirm", n))
	}
	foot := z.s("全文在 enkeep 记忆「开始」页的「定期总览」。", "The full report is under Periodic overview on the enkeep memory start page.")
	if len(tail) > 0 {
		foot = z.s("另：", "Also: ") + strings.Join(tail, z.s("，", ", ")) + z.s("。", ". ") + foot
	}
	var mid []string
	for _, p := range used {
		if !private(p) {
			mid = append(mid, "· "+clip(p.Text, 60))
		}
	}
	for {
		text := strings.Join(append(append([]string{head}, mid...), foot), "\n")
		if len([]rune(text)) <= SummaryMost || len(mid) == 0 {
			return clip(text, SummaryMost)
		}
		mid = mid[:len(mid)-1]
	}
}
