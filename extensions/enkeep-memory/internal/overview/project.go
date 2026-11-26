package overview

import (
	"crypto/sha1"
	"encoding/hex"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/entry"
	"github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/memsrc"
	"github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/norm"
	"github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/store"
)

// The four-perspective projection: every curated entry the overview may use, with its evidence spread over time
// (ISO weeks), projects, people and source kinds, so a pattern's scenes, independent sources and weeks are counted by
// code, never taken from the model.

// The gate's thresholds (design 2.5, line 1). They do not relax with little data: a pattern that misses one stays a
// candidate.
const (
	MinScenes  = 2 // distinct projects or source kinds
	MinSources = 3 // distinct quoted unit·day pieces of evidence
	MinWeeks   = 2 // distinct ISO weeks
)

// Gate failures.
const (
	GateScenes  = "scenes"
	GateSources = "sources"
	GateWeeks   = "weeks"
	GateDefault = "no_default"
	GateGeneric = "holds_for_anyone"
	GateQuotes  = "quotes"
	GateEmpty   = "no_evidence"
)

// evid is one piece of evidence under an entry.
type evid struct {
	entry  string // the curated entry it is under (an action, or the class itself)
	key    string // ref@day: one unit on one day, the independent unit of evidence
	day    string
	group  string // source kind (sourceGroup)
	quoted bool
}

// sourceGroup is the kind of source a code belongs to: the user's chats with the assistant, cards and to-dos, agent
// sessions, connected message sources, the user's own notes, the knowledge base, recordings.
func sourceGroup(code string) string {
	switch code {
	case entry.SrcAssistant:
		return "chat"
	case entry.SrcAsk, entry.SrcTodo, entry.SrcLedger:
		return "card"
	case entry.SrcAgent, entry.SrcTools:
		return "agent"
	case entry.SrcChat, entry.SrcSent:
		return "messages"
	case entry.SrcMemory, entry.SrcAbout:
		return "self"
	case entry.SrcKB:
		return "kb"
	case entry.SrcRecording:
		return "recording"
	}
	return strings.ToLower(code)
}

// facetOf is the overview facet an entry feeds: its own, or World for a card's fact or commitment; "" for none.
func facetOf(e *entry.Entry) entry.Facet {
	if e.Facet == entry.People {
		if e.Part == entry.PartFact || e.Part == entry.PartCommitment {
			return entry.World
		}
		return ""
	}
	if slices.Contains(Facets, e.Facet) {
		return e.Facet
	}
	return ""
}

// usable says whether an entry may feed the overview: what memory.entries may answer (memsrc.Pool: live, never a
// card's evaluation or inference, no life content for a "work only" user), and of a facet the overview covers.
func usable(st *store.State, e *entry.Entry) bool {
	return e != nil && memsrc.Pool(st, e) && !e.Inferred && facetOf(e) != ""
}

// projects are the project names an entry is scoped to.
func projects(e *entry.Entry) []string {
	var out []string
	for _, sc := range e.Scopes {
		if sc.Kind == entry.ScopeProject && strings.TrimSpace(sc.Name) != "" && !slices.Contains(out, sc.Name) {
			out = append(out, sc.Name)
		}
	}
	return out
}

// evidenceOf is the evidence under e: its own lines and its live members' (two levels: an abstract's classes' actions),
// only days before cutoff when one is set.
func evidenceOf(st *store.State, e *entry.Entry, cutoff string) []evid {
	var out []evid
	seen := map[string]bool{}
	var walk func(x *entry.Entry, depth int)
	walk = func(x *entry.Entry, depth int) {
		if seen[x.ID] {
			return
		}
		seen[x.ID] = true
		for _, ev := range x.Evidence {
			if ev.Day == "" || cutoff != "" && ev.Day >= cutoff {
				continue
			}
			out = append(out, evid{entry: x.ID, key: ev.Ref + "@" + ev.Day, day: ev.Day, group: sourceGroup(ev.Source),
				quoted: strings.TrimSpace(ev.Quote) != ""})
		}
		if depth >= 2 {
			return
		}
		for _, id := range x.Members {
			if m := st.Entries[id]; m != nil && m.Live() {
				walk(m, depth+1)
			}
		}
	}
	walk(e, 0)
	return out
}

// tally is a set of evidence counted for the gate.
type tally struct {
	scenes   []string // "p:<project>" and "s:<source kind>"
	sources  []string // quoted ref@day
	weeks    []string
	groups   []string
	projects []string
	people   []string
	last     string
	unquoted int // entries among the evidence without a quoted line
}

// count tallies the evidence of the entries ids, before cutoff.
func count(st *store.State, ids []string, cutoff string) tally {
	var t tally
	add := func(list *[]string, s string) {
		if s != "" && !slices.Contains(*list, s) {
			*list = append(*list, s)
		}
	}
	for _, id := range ids {
		e := st.Entries[id]
		if e == nil || !e.Live() {
			continue
		}
		evs := evidenceOf(st, e, cutoff)
		quoted := false
		for _, ev := range evs {
			x := st.Entries[ev.entry]
			if x != nil {
				for _, p := range projects(x) {
					add(&t.scenes, "p:"+p)
					add(&t.projects, p)
				}
				if x.Person != "" {
					add(&t.people, x.Person)
				}
			}
			if !ev.quoted {
				continue
			}
			quoted = true
			add(&t.scenes, "s:"+ev.group)
			add(&t.groups, ev.group)
			add(&t.sources, ev.key)
			add(&t.weeks, isoWeek(ev.day))
			if ev.day > t.last {
				t.last = ev.day
			}
		}
		for _, p := range projects(e) {
			add(&t.scenes, "p:"+p)
			add(&t.projects, p)
		}
		if e.Person != "" {
			add(&t.people, e.Person)
		}
		if !quoted {
			t.unquoted++
		}
	}
	sort.Strings(t.weeks)
	return t
}

// perspective is where the evidence differs most: sources first (what was said in one place and done in another),
// then projects, people, and time.
func (t tally) perspective() string {
	switch {
	case len(t.groups) >= 2:
		return PerspCrossSource
	case len(t.projects) >= 2:
		return PerspProject
	case len(t.people) >= 2:
		return PerspPeople
	}
	return PerspTemporal
}

// gate checks a pattern against the code gate (design 2.5, line 1) from its tally; "" when it passes.
func gate(p *Pattern, t tally) string {
	switch {
	case len(t.sources) == 0:
		return GateEmpty
	case strings.TrimSpace(p.DefaultContrast) == "" || norm.Q(p.DefaultContrast) == norm.Q(p.Text):
		return GateDefault
	case p.HoldsForAnyone:
		return GateGeneric
	case t.unquoted > 0:
		return GateQuotes
	case len(t.scenes) < MinScenes:
		return GateScenes
	case len(t.sources) < MinSources:
		return GateSources
	case len(t.weeks) < MinWeeks:
		return GateWeeks
	}
	return ""
}

// apply writes a tally onto a pattern: the counts, the weeks, the perspective and the gate.
func apply(p *Pattern, t tally) {
	p.ScenesCount, p.SourcesCount, p.ActiveWeeks, p.LastEvidence = len(t.scenes), len(t.sources), t.weeks, t.last
	p.Perspective = t.perspective()
	p.Gate = gate(p, t)
}

// tags are the categories and the source space a pattern carries: the union of its evidence's (privacy taint,
// design 2.8), decided as memory.entries decides them for each entry (memsrc.Tags).
// A class tagged with no category still taints its pattern with its members' categories: every entry under an id (two
// levels, as evidenceOf walks) counts.
func tags(st *store.State, ids []string) (cats []string, space string) {
	var all []string
	var walk func(id string, depth int)
	walk = func(id string, depth int) {
		e := st.Entries[id]
		if e == nil || slices.Contains(all, id) {
			return
		}
		all = append(all, id)
		if depth < 2 {
			for _, m := range e.Members {
				if x := st.Entries[m]; x != nil && x.Live() {
					walk(m, depth+1)
				}
			}
		}
	}
	for _, id := range ids {
		walk(id, 0)
	}
	for _, id := range all {
		e := st.Entries[id]
		c, s, _ := memsrc.Tags(st, e)
		for _, x := range c {
			if !slices.Contains(cats, x) {
				cats = append(cats, x)
			}
		}
		if space == "" {
			space = s
		}
	}
	sort.Strings(cats)
	return cats, space
}

// subjects are the names that bring a pattern in: its evidence's (memsrc.Subjects), at most MaxSubjects.
func subjects(st *store.State, ids []string) []string {
	var out []string
	for _, id := range ids {
		e := st.Entries[id]
		if e == nil {
			continue
		}
		for _, s := range memsrc.Subjects(st, e) {
			if len(out) < memsrc.MaxSubjects && !slices.Contains(out, s) {
				out = append(out, s)
			}
		}
	}
	return out
}

func isoWeek(day string) string {
	t, err := time.Parse("2006-01-02", day)
	if err != nil {
		return ""
	}
	y, w := t.ISOWeek()
	return weekName(y, w)
}

func weekName(y, w int) string {
	b := []byte("0000-W00")
	for i := 3; i >= 0; i-- {
		b[i] = byte('0' + y%10)
		y /= 10
	}
	b[6], b[7] = byte('0'+w/10), byte('0'+w%10)
	return string(b)
}

// patternID is a pattern's id, from its facet and what it is about, so the same pattern found again keeps its id.
func patternID(facet, situation, choice, text string) string {
	key := norm.Q(situation) + "|" + norm.Q(choice)
	if strings.Trim(key, "|") == "" {
		key = norm.Q(text)
	}
	h := sha1.Sum([]byte(facet + "|" + key))
	return "ov:" + hex.EncodeToString(h[:])[:10]
}

func clip(s string, n int) string {
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

func union(a []string, b ...string) []string {
	out := slices.Clone(a)
	for _, x := range b {
		if x != "" && !slices.Contains(out, x) {
			out = append(out, x)
		}
	}
	return out
}
