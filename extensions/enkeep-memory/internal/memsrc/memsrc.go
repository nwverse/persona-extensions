// Package memsrc answers the hub's memory_sources call memory.entries (Persona #13532, schema/extension/memory.schema.json,
// docs/extensions/extensions.md "Memory sources"): the curated entries one by one, read-only, for the hub to grade by
// scene. It reads a snapshot of the state and never changes it. Nothing is filtered by scene here: every entry the
// text outlets (the resident block, the project files, the Inbox context, enkeep_recall) could carry is answered,
// with what the hub needs to grade it.
package memsrc

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"regexp"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/curate"
	"github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/entry"
	"github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/export"
	"github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/recall"
	"github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/store"
)

// The protocol's bounds (memory.schema.json).
const (
	MaxLimit    = 500
	MaxText     = 4096
	MaxCats     = 8
	MaxSubjects = 16
	MaxSubject  = 80
	MaxAfter    = 512
	// MaxBytes: a page ends before its answer passes this (the hub's limit is 1 MB; room for the envelope).
	MaxBytes = 900 << 10
)

// Params is memory.entries' parameters (wire.MemoryEntriesParams; local until the SDK copy carries the types).
type Params struct {
	Contribution string `json:"contribution"`
	Limit        int    `json:"limit"`
	After        string `json:"after,omitempty"`
}

// Result is one page (wire.MemoryEntriesResult).
type Result struct {
	Entries []Entry `json:"entries"`
	Tagged  bool    `json:"tagged"`
	HasMore bool    `json:"has_more"`
	After   string  `json:"after,omitempty"`
}

// Entry is one entry (wire.MemoryEntry).
type Entry struct {
	ID          string   `json:"id"`
	Text        string   `json:"text"`
	Facet       string   `json:"facet,omitempty"`
	Status      string   `json:"status"`
	OriginSpace string   `json:"origin_space,omitempty"`
	Categories  []string `json:"categories,omitempty"`
	Standing    bool     `json:"standing,omitempty"`
	Rank        int      `json:"rank,omitempty"`
	Subjects    []string `json:"subjects,omitempty"`
}

// ErrBadAfter is an after this source did not give.
var ErrBadAfter = errors.New("after is not a cursor this source gave")

var (
	idRe    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
	facetRe = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)
	spaceRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)
)

// item is a pooled entry with its order key.
type item struct {
	e        *entry.Entry
	standing bool
	rank     int
	last     string
}

// key is the cursor: the last answered entry's place in the order.
type key struct {
	S bool   `json:"s"`
	R int    `json:"r"`
	L string `json:"l"`
	I string `json:"i"`
}

func (it item) key() key { return key{it.standing, it.rank, it.last, it.e.ID} }

// before says whether a comes before b: standing first, then rank, then newest, then id.
func before(a, b key) bool {
	if a.S != b.S {
		return a.S
	}
	if a.R != b.R {
		return a.R > b.R
	}
	if a.L != b.L {
		return a.L > b.L
	}
	return a.I < b.I
}

// weakPredicates are low-value relation triples (e.g. "A depends on B", "A works with B") that crowd memory.entries
// without actionable guidance. They stay in state and are only filtered at the memory.entries outlet unless the user
// explicitly pinned, edited or confirmed them.
var weakPredicates = map[string]bool{
	"about":             true,
	"allocated":         true,
	"assigned":          true,
	"blocked_by":        true,
	"collaborates_with": true,
	"depends_on":        true,
	"member_of":         true,
	"same_circle":       true,
	"serves":            true,
	"waiting_on":        true,
}

// substantiveWorldTypes are world-facet types that carry durable project/ownership/decision value over transient status.
var substantiveWorldTypes = map[string]bool{
	"committed":  true,
	"decided":    true,
	"decides":    true,
	"decision":   true,
	"means":      true,
	"mentors":    true,
	"owns":       true,
	"reports_to": true,
	"role":       true,
	"term":       true,
}

func userConfirmedRight(e *entry.Entry) bool {
	for i := len(e.Verdicts) - 1; i >= 0; i-- {
		if e.Verdicts[i].By == "user" {
			return e.Verdicts[i].Mark == "right"
		}
	}
	return false
}

// IsWeakRelation says whether e is a low-value relation triple excluded from memory.entries. Nothing is deleted from
// state; user-pinned, user-edited or user-confirmed entries are always kept.
func IsWeakRelation(e *entry.Entry) bool {
	if e == nil || e.Facet != entry.World || e.Predicate == "" {
		return false
	}
	if !weakPredicates[strings.ToLower(strings.TrimSpace(e.Predicate))] {
		return false
	}
	if e.Pinned || (e.User != nil && strings.TrimSpace(e.User.Text) != "") || userConfirmedRight(e) {
		return false
	}
	return true
}

// Pool says whether an entry is answered: live, settled or on its way (not one the user deleted, merged, retired or
// marked wrong), never a card's evaluation or inference about a person, never a low-value weak relation triple, and
// not life content when the user chose "work only". The same gate as enkeep_recall's, without its category look-up:
// grading by category is the hub's.
func Pool(st *store.State, e *entry.Entry) bool {
	if !e.Live() {
		return false
	}
	switch e.Status {
	case entry.Stable, entry.Shifting, entry.Candidate, entry.Dormant:
	default:
		if !e.Pinned {
			return false
		}
	}
	if e.Facet == entry.People && (e.Part == entry.PartUserEval || e.Part == entry.PartInferred || e.Inferred) {
		return false
	}
	if IsWeakRelation(e) {
		return false
	}
	if e.Domain == entry.Life && st.Onboarding != nil && st.Onboarding.Use == entry.Work {
		return false
	}
	return true
}

// Status maps the plugin's states to the protocol's: active is what the exits use today (stable, shifting, pinned,
// written by the user); candidate, dormant and retired as they are.
func Status(e *entry.Entry) string {
	switch {
	case !e.Live() || e.Status == entry.Retired:
		return "retired"
	case e.Status == entry.Stable || e.Status == entry.Shifting || export.Stable(e):
		return "active"
	case e.Status == entry.Dormant:
		return "dormant"
	}
	return "candidate"
}

// Rank orders entries by usefulness (0 to 1000, pinned 1000) so the hub's bounded memory.entries fetch (at most 1000
// entries, re-sorted by standing then rank) keeps the highest-value rules, judgements, workflows, tools, persona and
// substantive people/world entries first.
func Rank(e *entry.Entry) int {
	if e.Pinned {
		return 1000
	}
	var score int
	switch e.Facet {
	case entry.Interaction, entry.Judgement, entry.Workflow, entry.Tools, entry.Persona:
		score = 400
	case entry.People:
		score = 200
	default: // World and any other facet
		score = 180
		typ := strings.ToLower(strings.TrimSpace(e.Type))
		pred := strings.ToLower(strings.TrimSpace(e.Predicate))
		if substantiveWorldTypes[typ] || substantiveWorldTypes[pred] {
			score += 40
		} else if typ == "status" {
			score -= 40
		}
	}
	switch e.Grain {
	case entry.Abstract:
		score += 160
	case entry.Class:
		score += 100
	default:
		if slices.Contains(e.Flags, "durable") {
			score += 60
		}
	}
	if (e.Origin != entry.Behavior && e.Origin != "" && e.Origin != entry.Seed) ||
		(e.User != nil && strings.TrimSpace(e.User.Text) != "") ||
		userConfirmedRight(e) {
		score += 120
	}
	score += min(150, max(0, e.Count)*max(1, len(e.Kinds))*12)
	switch Status(e) {
	case "dormant":
		score /= 2
	case "candidate":
		score = score * 3 / 4
	}
	return max(0, min(999, score))
}

// Tagged says whether the entry's categories and space were decided by the tagging rules: its own mark, or, for a
// class or an abstract entry, every member's.
func Tagged(st *store.State, e *entry.Entry) bool { return tagged(st, e, 0) }

func tagged(st *store.State, e *entry.Entry, depth int) bool {
	if len(e.Members) == 0 || depth > 2 {
		return e.Tagged
	}
	n := 0
	for _, id := range e.Members {
		m := st.Entries[id]
		if m == nil {
			continue
		}
		if !tagged(st, m, depth+1) {
			return false
		}
		n++
	}
	return n > 0 || e.Tagged
}

// Tags are the categories and the source space an entry is answered with, and whether they were decided now rather
// than when it was read. An entry tagged by the 0.2.0-beta.40 rules (Tagged) keeps its own. One read before them is
// tagged here by the same rules on what it carries (curate.CategoriesOf): its old model label, the money words of its
// text and quotes when it has no label, the identity-paper and legal words; a class or an abstract entry also takes
// every member's categories and, without a space of its own, the first member's space. The state is not changed
// (memory.entries is read-only). Space: before 0.2.0-beta.40 no unit carried its space, so such an entry is answered
// from the main space unless it or a member says otherwise; what it is about still tags it (a health matter is
// health wherever it was said), and the hub never carries a sensitive entry outside its track.
func Tags(st *store.State, e *entry.Entry) (cats []string, space string, backfilled bool) {
	if Tagged(st, e) {
		return e.Cats(), e.OriginSpace, false
	}
	cats, space = backfill(st, e, 0)
	return cats, space, true
}

func backfill(st *store.State, e *entry.Entry, depth int) ([]string, string) {
	cats, space := e.Cats(), e.OriginSpace
	add := func(list []string) {
		for _, c := range list {
			if !slices.Contains(cats, c) && !slices.Contains(e.CatsRemoved, c) {
				cats = append(cats, c)
			}
		}
	}
	if !e.Tagged {
		texts := []string{e.Text, e.Object}
		for _, ev := range e.Evidence {
			texts = append(texts, ev.Quote)
		}
		words, _ := curate.CategoriesOf("", texts...)
		if e.Sensitive != "" || len(e.Categories) > 0 { // a model's label: the money words add nothing on their own
			words = slices.DeleteFunc(words, func(c string) bool { return c == entry.CatFinance })
		}
		add(words)
	}
	if depth < 2 {
		for _, id := range e.Members {
			m := st.Entries[id]
			if m == nil {
				continue
			}
			var mc []string
			var ms string
			if Tagged(st, m) {
				mc, ms = m.Cats(), m.OriginSpace
			} else {
				mc, ms = backfill(st, m, depth+1)
			}
			add(mc)
			if space == "" {
				space = ms
			}
		}
	}
	for _, c := range entry.SpaceDefaults(space) {
		add([]string{c})
	}
	return cats, space
}

// Subjects are the names whose mention brings the entry in: the person of a card, its object, its scopes' names.
func Subjects(st *store.State, e *entry.Entry) []string {
	var out []string
	add := func(s string) {
		s = strings.TrimSpace(s)
		if s == "" || len(out) >= MaxSubjects || slices.Contains(out, s) {
			return
		}
		out = append(out, clip(s, MaxSubject))
	}
	if k := st.Contacts[e.Person]; e.Person != "" && k != nil && k.Manual != "removed" {
		add(k.Name)
	}
	add(e.Object)
	for _, sc := range e.Scopes {
		if sc.Kind != entry.ScopeGlobal {
			add(sc.Name)
		}
	}
	return out
}

// Counts summarises a source's pool: entries, active, standing, and Backfilled: read before the 0.2.0-beta.40 tagging
// rules and tagged by them now (Tags).
type Counts struct{ Entries, Active, Standing, Backfilled int }

// Page answers one memory.entries page of st. The order is standing first, then rank, then newest; after is the
// last answered entry's place, so pages stay consistent while entries come and go between them.
func Page(st *store.State, p Params) (Result, Counts, error) {
	var from *key
	if p.After != "" {
		b, err := base64.RawURLEncoding.DecodeString(p.After)
		var k key
		if err != nil || json.Unmarshal(b, &k) != nil || k.I == "" {
			return Result{}, Counts{}, ErrBadAfter
		}
		from = &k
	}
	limit := min(max(p.Limit, 1), MaxLimit)
	var all []item
	var n Counts
	for _, e := range st.Entries {
		if !Pool(st, e) || !idRe.MatchString(e.ID) || strings.TrimSpace(recall.Line(e)) == "" {
			continue
		}
		it := item{e: e, standing: export.Usable(st, e), rank: Rank(e), last: e.Last}
		all = append(all, it)
		n.Entries++
		if Status(e) == "active" {
			n.Active++
		}
		if it.standing {
			n.Standing++
		}
		if !Tagged(st, e) {
			n.Backfilled++
		}
	}
	sort.Slice(all, func(i, j int) bool { return before(all[i].key(), all[j].key()) })
	start := 0
	if from != nil {
		start = sort.Search(len(all), func(i int) bool { return before(*from, all[i].key()) })
	}
	// Every entry's categories and space are decided (Tags), so the page keeps the protocol's promise: tagged.
	res := Result{Entries: []Entry{}, Tagged: true}
	size := 64
	for i := start; i < len(all); i++ {
		if len(res.Entries) == limit {
			res.HasMore = true
			break
		}
		x := entryOf(st, all[i])
		b, _ := json.Marshal(x)
		if size+len(b)+1 > MaxBytes && len(res.Entries) > 0 {
			res.HasMore = true
			break
		}
		size += len(b) + 1
		res.Entries = append(res.Entries, x)
		k, _ := json.Marshal(all[i].key())
		if a := base64.RawURLEncoding.EncodeToString(k); len(a) <= MaxAfter {
			res.After = a
		}
	}
	if !res.HasMore {
		res.After = ""
	}
	return res, n, nil
}

// entryOf is what the protocol carries of an entry, with its categories and space (Tags). A space slug the protocol
// would refuse is left out; the entry's categories still grade it.
func entryOf(st *store.State, it item) Entry {
	e := it.e
	x := Entry{ID: e.ID, Text: clip(strings.TrimSpace(recall.Line(e)), MaxText), Status: Status(e), Standing: it.standing,
		Rank: it.rank, Subjects: Subjects(st, e)}
	if f := string(e.Facet); facetRe.MatchString(f) {
		x.Facet = f
	}
	cats, space, _ := Tags(st, e)
	if spaceRe.MatchString(space) {
		x.OriginSpace = space
	}
	x.Categories = cats[:min(len(cats), MaxCats)]
	return x
}

func clip(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n])
}
