package memsrc

import (
	"slices"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/entry"
	"github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/export"
	"github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/norm"
	"github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/recall"
	"github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/store"
)

// The standing set (0.2.0-beta.52, design l0-leverage §4): the few entries the hub puts in every main-space turn. The
// hub gives the extension's standing entries at most 1536 bytes, after its own ledger, taking the highest rank first
// and skipping whatever does not fit; so the set is chosen here, by score, to fit: ResidentBytes in all, the
// understanding of the user (③ ④ ⑥, ⑤'s decisions and evaluations) UnderstandingShare of it and the assistant's rules
// (① ②) the rest, so rules never take the understanding's room. What one side leaves goes to the other. A rule (or an
// understanding) alike in words to one already in is the same one: the higher score stays. Ranks interleave the two
// sides, so when the ledger leaves less room both shrink together.
const (
	ResidentBytes      = 1400
	UnderstandingShare = 0.4
	// GroupShare is the most of the room one class of lines takes (a facet and its type, so "autonomy" rules or
	// "dispatch" rules, whatever the type is called; 0.2.6): a run of rules saying one thing in other words, or one
	// kind of occasion, does not crowd out the others.
	GroupShare   = 0.2
	ResidentMost = 12
	// residentTextMost is the longest line of the set, in bytes.
	residentTextMost = 240
	// sameRule is how alike two lines' words are to be one.
	sameRule  = 0.45
	rankStand = 989 // the first standing entry's rank; the set counts down from it
	// residentOverhead is what the hub adds to each line ("- " and a line end).
	residentOverhead = 3
	// PatternShare is the part of the room the overview's cross-event patterns are offered first (0.2.0): a
	// pattern scores at most 70, so without it the single entries above 70 (hard rules at 90) and the short lines that
	// fill what is left kept every pattern out (none standing at 1400 bytes, none of the rules side at 4096).
	PatternShare = 0.25
	// spillFloor is the least score a line takes the room the other side left with (0.2.0): below it the
	// leftover bytes went to short fragments (a story's character, a one-line self-description).
	spillFloor = 50
)

// Candidate is one entry that may stand: a curated entry or an overview pattern, with its score and side.
type Candidate struct {
	ID    string
	Text  string
	Kind  string // entry.KindRules or entry.KindUnderstanding
	Score int
	// Days and Last break a tie in score: more days of evidence first, then the latest; the id only when those tie
	// too (before, alike scores were ordered by id, a draw by hash).
	Days int
	Last string
	// Pattern marks an overview pattern (ov:pattern:…): offered PatternShare of the room first.
	Pattern bool
	// Group is the class GroupShare caps ("<facet>:<type>"); "" is capped by nothing.
	Group string
}

// Resident picks the standing set from cands: the ids in rank order (highest first) with the text each stands with.
func Resident(cands []Candidate) []Candidate {
	return ResidentWithin(cands, ResidentBytes, ResidentMost)
}

// ResidentWithin is Resident in total bytes, at most most entries (the hard evaluation's wider budget).
func ResidentWithin(cands []Candidate, total, most int) []Candidate {
	if total <= 0 {
		total = ResidentBytes
	}
	cands = slices.Clone(cands)
	sort.SliceStable(cands, func(i, j int) bool {
		a, b := cands[i], cands[j]
		if a.Score != b.Score {
			return a.Score > b.Score
		}
		if a.Days != b.Days {
			return a.Days > b.Days
		}
		if a.Last != b.Last {
			return a.Last > b.Last
		}
		return a.ID < b.ID
	})
	quota := map[string]int{
		entry.KindUnderstanding: int(float64(total) * UnderstandingShare),
		entry.KindRules:         total - int(float64(total)*UnderstandingShare),
	}
	var chosen []Candidate
	taken := map[string]bool{}
	alike := func(c Candidate) bool {
		for _, x := range chosen {
			if norm.Jaccard(x.Text, c.Text) >= sameRule {
				return true
			}
		}
		return false
	}
	used := 0
	patterns := int(float64(total) * PatternShare)
	groups, groupMost := map[string]int{}, int(float64(total)*GroupShare)
	pass := func(spill, onlyPatterns bool) {
		for _, c := range cands {
			if len(chosen) >= most {
				return
			}
			if taken[c.ID] || c.Text == "" || (onlyPatterns && !c.Pattern) || (spill && c.Score < spillFloor) || alike(c) {
				continue
			}
			size := len(c.Text) + residentOverhead
			if used+size > total || (onlyPatterns && size > patterns) {
				continue
			}
			if c.Group != "" && groups[c.Group]+size > groupMost {
				continue
			}
			if onlyPatterns {
				patterns -= size
			}
			if !spill && size > quota[c.Kind] {
				continue
			}
			if !spill {
				quota[c.Kind] -= size
			}
			used += size
			groups[c.Group] += size
			taken[c.ID] = true
			chosen = append(chosen, c)
		}
	}
	pass(false, true) // the patterns' share, each side within its quota
	pass(false, false)
	pass(true, false) // what one side left
	// Interleave: understanding, rules, understanding… each side by score.
	var u, r []Candidate
	for _, c := range chosen {
		if c.Kind == entry.KindRules {
			r = append(r, c)
		} else {
			u = append(u, c)
		}
	}
	out := make([]Candidate, 0, len(chosen))
	for i := 0; i < len(u) || i < len(r); i++ {
		if i < len(u) {
			out = append(out, u[i])
		}
		if i < len(r) {
			out = append(out, r[i])
		}
	}
	return out
}

// ResidentText is the line an entry stands with: for the understanding, its principle first, then the choice it
// shows; for a rule, what the assistant does. At most residentTextMost bytes.
func ResidentText(e *entry.Entry) string {
	line := strings.TrimSpace(recall.Head(e))
	if p := e.Principle(); p != "" && entry.KindOf(e) == entry.KindUnderstanding && e.User == nil {
		ex := strings.TrimSpace(e.Choice)
		if s := strings.TrimSpace(e.Situation); s != "" && ex != "" {
			ex = s + "：" + ex
		}
		if ex == "" {
			ex = line
		}
		line = "原则：" + strings.TrimRight(p, "。.") + "（如 " + strings.TrimRight(ex, "。.") + "）"
	}
	// The exception stays whole: what is cut is the rest of the line (0.2.6).
	x := recall.Exception(e)
	if len(x) >= residentTextMost/2 {
		x = ""
	}
	return clipBytes(line, residentTextMost-len(x)) + x
}

// clipBytes cuts s to at most n bytes at a rune boundary.
func clipBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[:n]
	for len(s) > 0 && !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s + "…"
}

// CuratedCandidate is e as a standing candidate, or false when it may not stand: not usable by the exits, no side
// (a people card, a relation, a passing fact), tagged with a category or said in a space (the hub grades those by
// scene; they never stand), or resting only on template lines (entry.TemplateQuotes tpl: a dispatch prompt's rules,
// not the user's).
func CuratedCandidate(st *store.State, e *entry.Entry, tpl map[string]bool) (Candidate, bool) {
	k := entry.KindOf(e)
	if k == "" && e.Pinned { // what the user pinned stands whatever it is
		if k = entry.KindOfFacet(string(e.Facet)); k == "" {
			k = entry.KindUnderstanding
		}
	}
	if k == "" || !export.Usable(st, e) || !Pool(st, e) || (!e.Pinned && entry.TemplateOnly(e, tpl)) ||
		(!e.Pinned && entry.CoveredBy(e) != "" && coverLive(st, e)) {
		return Candidate{}, false
	}
	if cats, space, _ := Tags(st, e); len(cats) > 0 || space != "" {
		return Candidate{}, false
	}
	days := map[string]bool{}
	for _, ev := range e.Evidence {
		days[ev.Day] = true
	}
	return Candidate{ID: e.ID, Text: ResidentText(e), Kind: k, Score: entry.Score(e), Days: len(days), Last: e.Last,
		Group: GroupOf(st, e)}, true
}

// coverLive: the rule that covers e is still there to stand in its place (when it went, e stands again).
func coverLive(st *store.State, e *entry.Entry) bool {
	x := st.Entries[entry.CoveredBy(e)]
	return x != nil && x.Live()
}

// GroupOf is the class an entry's line counts towards in the standing set: its facet and its type as the type table
// names it now (a renamed type under its new name); "" without a type.
func GroupOf(st *store.State, e *entry.Entry) string {
	name := strings.TrimSpace(e.Type)
	if name == "" {
		return ""
	}
	for i := 0; i < 8 && st != nil; i++ {
		j := slices.IndexFunc(st.Types[e.Facet], func(t entry.TypeDef) bool { return norm.Q(t.Name) == norm.Q(name) })
		if j < 0 || st.Types[e.Facet][j].Renamed == "" {
			break
		}
		name = st.Types[e.Facet][j].Renamed
	}
	return string(e.Facet) + ":" + norm.Q(name)
}

// Quote is a line of the user's own words behind e, for recall on demand ("原话：「…」(day)"); "" when it rests on
// no line of the user's.
func Quote(e *entry.Entry) string {
	for i := len(e.Evidence) - 1; i >= 0; i-- {
		ev := e.Evidence[i]
		if ev.Who != entry.WhoSelf || ev.Reported || strings.TrimSpace(ev.Quote) == "" {
			continue
		}
		q := clip(strings.Join(strings.Fields(ev.Quote), " "), quoteMost)
		return "原话：「" + q + "」（" + ev.Day + "）"
	}
	return ""
}

// quoteMost is the longest quote a recalled line carries, in characters.
const quoteMost = 80
