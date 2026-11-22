// Package recall is the look-up exit (0.2.0-beta.37, docs/exits.md "enkeep_recall"): the assistant or an agent asks
// for what the curator knows about a person, a project, a system or a kind of fork, and gets the best matching entry
// lines, each with its facet, status, last day and source kinds. It is what keeps the per-turn block short: facts,
// people and the ways of working that are not in the block stay here, asked for when needed (also after the
// conversation was compacted).
//
// The pool is every live entry outside the sensitive categories, candidates and dormant ones too (ranked lower); a
// people card's evaluation and inferred parts are never returned, whatever K15 says: the hub's privacy filter does
// not see an extension's own tools, so the curator keeps them back itself. Nothing calls a model; the line is the
// entry's exit wording when it has one, else its own text. The query is not kept anywhere.
package recall

import (
	"fmt"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/entry"
	"github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/norm"
	"github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/store"
)

const (
	DefaultLimit = 12
	MostLimit    = 30
	// MostBytes bounds the answer's lines together.
	MostBytes = 4000
)

// Query is one look-up.
type Query struct {
	Text   string
	Facets []entry.Facet // empty: every facet
	Limit  int
}

// Hit is one entry line of an answer.
type Hit struct {
	ID     string
	Facet  entry.Facet
	Status entry.Status
	Last   string
	Kinds  []string
	Text   string
	Score  float64
}

// Result is an answer, with what it counted (for the curator's per-day counts; the query itself is not kept).
type Result struct {
	Hits []Hit
	Pool int // entries searched
}

var statusWeight = map[entry.Status]float64{entry.Stable: 1, entry.Shifting: 0.8, entry.Candidate: 0.7, entry.Dormant: 0.5}

// usable is the pool's gate: live, not sensitive, not a card's evaluation or inferred part, not life content when the
// user chose "work only".
func usable(st *store.State, e *entry.Entry) bool {
	if !e.Live() || e.Sensitive != "" {
		return false
	}
	if _, ok := statusWeight[e.Status]; !ok && !e.Pinned {
		return false
	}
	if e.Facet == entry.People && (e.Part == entry.PartUserEval || e.Part == entry.PartInferred || e.Inferred) {
		return false
	}
	if e.Domain == entry.Life && st.Onboarding != nil && st.Onboarding.Use == entry.Work {
		return false
	}
	return true
}

// Line is what an entry says in an answer: the user's own wording, else its exit wording, else its text.
func Line(e *entry.Entry) string {
	if e.Export != nil {
		if e.Export.User != "" {
			return e.Export.User
		}
		if e.Export.Text != "" && !e.Export.Withheld && e.User == nil {
			return e.Export.Text
		}
	}
	return e.Shown()
}

// names are the names an entry is about: its object, subject, target, scopes and card's person.
func names(st *store.State, e *entry.Entry) []string {
	var out []string
	add := func(s string) {
		s = strings.TrimSpace(s)
		for _, p := range []string{"project:", "person:", "system:", "object:"} {
			s = strings.TrimPrefix(s, p)
		}
		if s != "" {
			out = append(out, s)
		}
	}
	add(e.Object)
	add(e.Subject)
	add(e.Target)
	for _, s := range e.Scopes {
		add(s.Name)
	}
	if e.Person != "" {
		if k := st.Contacts[e.Person]; k != nil && k.Manual != "removed" && k.Name != "" {
			out = append(out, k.Name)
		}
	}
	return out
}

// bigrams are a normalized text's character pairs (a single character counts as one).
func bigrams(q string) map[string]int {
	r := []rune(q)
	out := map[string]int{}
	if len(r) == 1 {
		out[q]++
	}
	for i := 0; i+1 < len(r); i++ {
		out[string(r[i:i+2])]++
	}
	return out
}

// cover is the share of the query's bigrams the text has: how much of the query the text
// covers (a long entry is not penalised for saying more).
func cover(q, t map[string]int) float64 {
	n, hit := 0, 0
	for k, c := range q {
		n += c
		hit += min(c, t[k])
	}
	if n == 0 {
		return 0
	}
	return float64(hit) / float64(n)
}

// terms are the query's words of at least two characters (split at spaces and punctuation), normalized.
func terms(text string) []string {
	var out []string
	for _, f := range strings.FieldsFunc(text, func(r rune) bool {
		return r == ' ' || r == '\t' || r == '\n' || r == ',' || r == '，' || r == '、' || r == ';' || r == '；' || r == '/' || r == '|'
	}) {
		if q := norm.Q(f); utf8.RuneCountInString(q) >= 2 && !slices.Contains(out, q) {
			out = append(out, q)
		}
	}
	return out
}

// Search ranks the pool for a query: how much of the query the entry's text and names cover, doubled when a query
// word is one of the names the entry is about, times the status weight; ties by count × source kinds, then last
// seen. Hits below a fifth of the query covered are left out.
func Search(st *store.State, q Query) Result {
	var res Result
	qq := norm.Q(q.Text)
	if qq == "" {
		return res
	}
	qb := bigrams(qq)
	words := terms(q.Text)
	limit := q.Limit
	if limit <= 0 {
		limit = DefaultLimit
	}
	limit = min(limit, MostLimit)
	type cand struct {
		e     *entry.Entry
		score float64
	}
	var cs []cand
	for _, e := range st.Entries {
		if len(q.Facets) > 0 && !slices.Contains(q.Facets, e.Facet) {
			continue
		}
		if !usable(st, e) {
			continue
		}
		res.Pool++
		ns := names(st, e)
		body := norm.Q(Line(e) + " " + e.Situation + " " + e.Choice + " " + strings.Join(ns, " "))
		s := cover(qb, bigrams(body))
		if strings.Contains(body, qq) {
			s += 0.5
		}
		for _, n := range ns {
			nq := norm.Q(n)
			if nq == "" {
				continue
			}
			if nq == qq || slices.Contains(words, nq) || (utf8.RuneCountInString(nq) >= 2 && strings.Contains(qq, nq)) {
				s *= 2
				break
			}
		}
		if s < 0.2 {
			continue
		}
		w := statusWeight[e.Status]
		if e.Pinned {
			w = 1
		}
		cs = append(cs, cand{e, s * w})
	}
	sort.SliceStable(cs, func(i, j int) bool {
		if cs[i].score != cs[j].score {
			return cs[i].score > cs[j].score
		}
		wi, wj := cs[i].e.Count*max(1, len(cs[i].e.Kinds)), cs[j].e.Count*max(1, len(cs[j].e.Kinds))
		if wi != wj {
			return wi > wj
		}
		if cs[i].e.Last != cs[j].e.Last {
			return cs[i].e.Last > cs[j].e.Last
		}
		return cs[i].e.ID < cs[j].e.ID
	})
	seen := map[string]bool{}
	size := 0
	for _, c := range cs {
		if len(res.Hits) == limit {
			break
		}
		t := strings.Join(strings.Fields(Line(c.e)), " ")
		if seen[t] {
			continue
		}
		h := Hit{ID: c.e.ID, Facet: c.e.Facet, Status: c.e.Status, Last: c.e.Last, Kinds: c.e.Kinds, Text: t, Score: c.score}
		if size+len(Format(h)) > MostBytes {
			continue
		}
		seen[t] = true
		size += len(Format(h))
		res.Hits = append(res.Hits, h)
	}
	return res
}

var facetName = map[entry.Facet]string{entry.Interaction: "和AI打交道", entry.Tools: "机器和工具", entry.Judgement: "默认判断",
	entry.Workflow: "做事方式", entry.World: "人项目和事实", entry.People: "有来往的人", entry.Persona: "关于用户"}

var statusName = map[entry.Status]string{entry.Stable: "稳定", entry.Shifting: "在变", entry.Candidate: "候选", entry.Dormant: "较早"}

// Format is one hit as an answer line: facet and status, the line, the last day and source kinds, and the entry id
// (for enkeep_mark_wrong).
func Format(h Hit) string {
	st := statusName[h.Status]
	if st == "" {
		st = string(h.Status)
	}
	kinds := ""
	if len(h.Kinds) > 0 {
		kinds = "；来源 " + strings.Join(h.Kinds, "/")
	}
	return fmt.Sprintf("- [%s·%s] %s（最后 %s%s）id:%s\n", facetName[h.Facet], st, h.Text, h.Last, kinds, h.ID)
}

// FacetOf parses a facet name as the tool takes it (the internal names).
func FacetOf(s string) (entry.Facet, bool) {
	f := entry.Facet(strings.TrimSpace(strings.ToLower(s)))
	return f, slices.Contains(entry.Facets, f)
}
