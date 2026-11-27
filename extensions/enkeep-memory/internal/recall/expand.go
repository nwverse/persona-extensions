package recall

import (
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/entry"
	"github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/norm"
	"github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/store"
)

const (
	quoteSliceMost  = 80 // max runes per L0 user original quote slice
	quotesPerEntity = 2  // max L0 user original quote slices per entity/hit
	maxMatchedEnts  = 3  // cap matched entities per recall query
	maxSkeletonOut  = 12 // cap 1-hop skeleton edges per entity
	maxRulesPerEnt  = 8  // cap local rules/decisions attached to an EntityContext
)

// SkeletonEdge is one 1-hop structural relation edge around a matched entity.
type SkeletonEdge struct {
	From      string
	Predicate string
	To        string
	ValidFrom string
}

// EntityContext is the 1-hop expansion of a matched entity (person, project, repo, machine, system):
// its structural skeleton edges, local rules and decisions anchored on it, and up to 2 deduplicated
// L0 user original quote slices (<=80 runes, with date).
type EntityContext struct {
	Name     string
	Type     string
	Skeleton []SkeletonEdge
	Rules    []Hit
	Quotes   []string
}

// skeletonIdx caches the 1-hop structural skeleton adjacency of a store.Graph snapshot for O(1) neighbor lookup.
type skeletonIdx struct {
	g          *store.Graph
	nodes      int
	assertions int
	// byNorm maps norm.Q(entity name or alias) to its resolved node ids.
	byNorm map[string][]string
	// adj maps resolved node id to its sorted 1-hop skeleton edges (from, predicate, to, validFrom, otherID, otherName).
	adj map[string][]adjEdge
}

type adjEdge struct {
	edge      SkeletonEdge
	otherID   string
	otherName string
}

var skelCache struct {
	mu  sync.Mutex
	idx *skeletonIdx
}

func getSkeletonIdx(st *store.State) *skeletonIdx {
	if st == nil || st.Graph == nil {
		return nil
	}
	g := st.Graph
	nNodes, nAsserts := len(g.Nodes), len(g.Assertions)
	skelCache.mu.Lock()
	if skelCache.idx != nil && skelCache.idx.g == g && skelCache.idx.nodes == nNodes && skelCache.idx.assertions == nAsserts {
		idx := skelCache.idx
		skelCache.mu.Unlock()
		return idx
	}
	skelCache.mu.Unlock()

	idx := buildSkeletonIdx(g)
	skelCache.mu.Lock()
	skelCache.idx = idx
	skelCache.mu.Unlock()
	return idx
}

func buildSkeletonIdx(g *store.Graph) *skeletonIdx {
	idx := &skeletonIdx{
		g:          g,
		nodes:      len(g.Nodes),
		assertions: len(g.Assertions),
		byNorm:     map[string][]string{},
		adj:        map[string][]adjEdge{},
	}
	addName := func(name, id string) {
		q := norm.Q(name)
		if utf8.RuneCountInString(q) < 2 {
			return
		}
		if !slices.Contains(idx.byNorm[q], id) {
			idx.byNorm[q] = append(idx.byNorm[q], id)
		}
	}
	for id, n := range g.Nodes {
		if n == nil || n.MergedInto != "" || n.Status == "not_entity" || id == "me" {
			continue
		}
		addName(n.Name, id)
		for _, a := range n.Aliases {
			addName(a.Name, id)
		}
	}
	for _, ids := range idx.byNorm {
		sort.Strings(ids)
	}
	for _, a := range g.Assertions {
		if a == nil || (a.Status != "active" && a.Status != "candidate") || !entry.IsSkeletonPredicate(a.P) {
			continue
		}
		sid, oid := g.Resolve(a.S), g.Resolve(a.O)
		if sid == "" || oid == "" || sid == oid {
			continue
		}
		sn, on := graphNodeName(g, sid), graphNodeName(g, oid)
		if sn == "" || on == "" {
			continue
		}
		se := SkeletonEdge{From: sn, Predicate: strings.ToLower(strings.TrimSpace(a.P)), To: on, ValidFrom: a.ValidFrom}
		if sid != "me" {
			idx.adj[sid] = append(idx.adj[sid], adjEdge{edge: se, otherID: oid, otherName: on})
		}
		if oid != "me" {
			idx.adj[oid] = append(idx.adj[oid], adjEdge{edge: se, otherID: sid, otherName: sn})
		}
	}
	for id := range idx.adj {
		sort.Slice(idx.adj[id], func(i, j int) bool {
			ai, aj := idx.adj[id][i], idx.adj[id][j]
			if ai.edge.ValidFrom != aj.edge.ValidFrom {
				return ai.edge.ValidFrom > aj.edge.ValidFrom
			}
			if ai.edge.Predicate != aj.edge.Predicate {
				return ai.edge.Predicate < aj.edge.Predicate
			}
			return ai.otherName < aj.otherName
		})
	}
	return idx
}

func graphNodeName(g *store.Graph, id string) string {
	if id == "me" {
		return "用户"
	}
	if n := g.Nodes[id]; n != nil && strings.TrimSpace(n.Name) != "" {
		return strings.TrimSpace(n.Name)
	}
	return ""
}

// SkeletonNeighbors returns up to limit distinct 1-hop structural skeleton neighbor display names
// for the given entity names using st.Graph. Used by memsrc.Subjects to index entity-anchored rules.
func SkeletonNeighbors(st *store.State, entityNames []string, limit int) []string {
	if limit <= 0 || len(entityNames) == 0 {
		return nil
	}
	idx := getSkeletonIdx(st)
	if idx == nil {
		return nil
	}
	selfNorm := map[string]bool{}
	for _, name := range entityNames {
		if q := norm.Q(name); q != "" {
			selfNorm[q] = true
		}
	}
	var out []string
	seen := map[string]bool{}
	for _, name := range entityNames {
		q := norm.Q(name)
		for _, id := range idx.byNorm[q] {
			for _, ae := range idx.adj[id] {
				if ae.otherID == "me" || ae.otherName == "" || ae.otherName == "用户" {
					continue
				}
				oq := norm.Q(ae.otherName)
				if oq == "" || selfNorm[oq] || seen[oq] {
					continue
				}
				seen[oq] = true
				out = append(out, ae.otherName)
				if len(out) >= limit {
					return out
				}
			}
		}
	}
	return out
}

// matchedEntity is one entity matched by a recall query.
type matchedEntity struct {
	name   string
	typ    string
	nodeID string
	norms  map[string]bool // norm.Q of canonical name & aliases
	exact  bool
}

func matchEntities(st *store.State, qq string, words []string) []matchedEntity {
	var cands []matchedEntity
	seen := map[string]bool{}
	matches := func(nq string) (ok, exact bool) {
		if utf8.RuneCountInString(nq) < 2 {
			return false, false
		}
		if nq == qq || slices.Contains(words, nq) {
			return true, true
		}
		if strings.Contains(qq, nq) {
			return true, false
		}
		return false, false
	}

	if st.Graph != nil {
		var ids []string
		for id, n := range st.Graph.Nodes {
			if n == nil || n.MergedInto != "" || n.Status == "not_entity" || id == "me" {
				continue
			}
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			n := st.Graph.Nodes[id]
			ok, exact := matches(norm.Q(n.Name))
			if !ok {
				for _, a := range n.Aliases {
					if aok, aex := matches(norm.Q(a.Name)); aok {
						ok, exact = true, exact || aex
						break
					}
				}
			}
			if !ok {
				continue
			}
			key := norm.Q(n.Name)
			if key == "" || seen[key] {
				continue
			}
			seen[key] = true
			ns := map[string]bool{key: true}
			for _, a := range n.Aliases {
				if aq := norm.Q(a.Name); utf8.RuneCountInString(aq) >= 2 {
					ns[aq] = true
				}
			}
			cands = append(cands, matchedEntity{name: strings.TrimSpace(n.Name), typ: n.Type, nodeID: id, norms: ns, exact: exact})
		}
	}

	var contactKeys []string
	for k := range st.Contacts {
		contactKeys = append(contactKeys, k)
	}
	sort.Strings(contactKeys)
	for _, k := range contactKeys {
		ct := st.Contacts[k]
		if ct == nil || ct.Manual == "removed" || strings.TrimSpace(ct.Name) == "" {
			continue
		}
		nq := norm.Q(ct.Name)
		if seen[nq] {
			continue
		}
		if ok, exact := matches(nq); ok {
			seen[nq] = true
			cands = append(cands, matchedEntity{name: strings.TrimSpace(ct.Name), typ: "person", norms: map[string]bool{nq: true}, exact: exact})
		}
	}

	// Fallback: match entity names declared on usable entries (e.g. when Graph has no node yet).
	if len(cands) == 0 {
		var eids []string
		for id := range st.Entries {
			eids = append(eids, id)
		}
		sort.Strings(eids)
		for _, id := range eids {
			e := st.Entries[id]
			for _, raw := range names(st, e) {
				nq := norm.Q(raw)
				if seen[nq] {
					continue
				}
				if ok, exact := matches(nq); ok {
					seen[nq] = true
					cands = append(cands, matchedEntity{name: strings.TrimSpace(raw), norms: map[string]bool{nq: true}, exact: exact})
				}
			}
		}
	}

	sort.SliceStable(cands, func(i, j int) bool {
		if cands[i].exact != cands[j].exact {
			return cands[i].exact
		}
		li, lj := utf8.RuneCountInString(cands[i].name), utf8.RuneCountInString(cands[j].name)
		if li != lj {
			return li > lj
		}
		if (cands[i].nodeID != "") != (cands[j].nodeID != "") {
			return cands[i].nodeID != ""
		}
		return cands[i].name < cands[j].name
	})
	if len(cands) > maxMatchedEnts {
		cands = cands[:maxMatchedEnts]
	}
	return cands
}

// expandEntities builds 1-hop EntityContexts and returns 1-hop neighbor candidate entries for Search.
func expandEntities(st *store.State, q Query, qq string, words []string) ([]EntityContext, map[string]hopCand) {
	matched := matchEntities(st, qq, words)
	if len(matched) == 0 {
		return nil, nil
	}
	idx := getSkeletonIdx(st)
	var contexts []EntityContext
	hopCands := map[string]hopCand{}

	for _, ent := range matched {
		ec := EntityContext{Name: ent.name, Type: ent.typ}
		edgeSeen := map[string]bool{}
		neighbors := map[string]string{} // norm.Q(neighbor) -> display name

		addEdge := func(se SkeletonEdge, otherName string) {
			key := norm.Q(se.From) + "|" + se.Predicate + "|" + norm.Q(se.To)
			if edgeSeen[key] || len(ec.Skeleton) >= maxSkeletonOut {
				return
			}
			edgeSeen[key] = true
			ec.Skeleton = append(ec.Skeleton, se)
			if otherName != "" && otherName != "用户" {
				if oq := norm.Q(otherName); utf8.RuneCountInString(oq) >= 2 && !ent.norms[oq] {
					neighbors[oq] = otherName
				}
			}
		}

		// 1. Structural skeleton edges from st.Graph.
		if idx != nil && ent.nodeID != "" {
			for _, ae := range idx.adj[ent.nodeID] {
				addEdge(ae.edge, ae.otherName)
			}
		}

		// 2. Structural skeleton edges & local rules anchored on ent from st.Entries.
		var ruleEntries []*entry.Entry
		for _, e := range st.Entries {
			if !usable(st, e, q.Categories) {
				continue
			}
			// World skeleton edges mentioning ent.
			if e.Facet == entry.World && entry.IsSkeletonPredicate(e.Predicate) && entryMatchesEntity(st, e, ent.norms) {
				sn := cleanEntityRef(st, e.Subject)
				tn := cleanEntityRef(st, e.Target)
				if sn != "" && tn != "" {
					other := tn
					if ent.norms[norm.Q(tn)] {
						other = sn
					}
					addEdge(SkeletonEdge{
						From:      sn,
						Predicate: strings.ToLower(strings.TrimSpace(e.Predicate)),
						To:        tn,
						ValidFrom: firstNonEmptyStr(e.ValidFrom, e.Last),
					}, other)
				}
			}
			if len(q.Facets) > 0 && !slices.Contains(q.Facets, e.Facet) {
				continue
			}
			if isAnchoredRuleOrDecision(e) && entryMatchesEntity(st, e, ent.norms) {
				ruleEntries = append(ruleEntries, e)
			}
		}

		sort.SliceStable(ruleEntries, func(i, j int) bool {
			wi, wj := entryRankWeight(ruleEntries[i]), entryRankWeight(ruleEntries[j])
			if wi != wj {
				return wi > wj
			}
			if ruleEntries[i].Last != ruleEntries[j].Last {
				return ruleEntries[i].Last > ruleEntries[j].Last
			}
			return ruleEntries[i].ID < ruleEntries[j].ID
		})

		quoteSeen := map[string]bool{}
		for _, e := range ruleEntries {
			qs := UserQuotes(e, quotesPerEntity)
			for _, qsItem := range qs {
				if len(ec.Quotes) >= quotesPerEntity {
					break
				}
				qk := norm.Q(qsItem)
				if !quoteSeen[qk] {
					quoteSeen[qk] = true
					ec.Quotes = append(ec.Quotes, qsItem)
				}
			}
			if len(ec.Rules) < maxRulesPerEnt {
				ec.Rules = append(ec.Rules, Hit{
					ID:     e.ID,
					Facet:  e.Facet,
					Status: e.Status,
					Last:   e.Last,
					Kinds:  e.Kinds,
					Text:   strings.Join(strings.Fields(Line(e)), " "),
					Cats:   e.Cats(),
					Quotes: qs,
				})
			}
		}

		// 3. 1-hop neighbor rules/decisions: entries anchored on a 1-hop structural neighbor of ent.
		if len(neighbors) > 0 {
			for _, e := range st.Entries {
				if !usable(st, e, q.Categories) {
					continue
				}
				if len(q.Facets) > 0 && !slices.Contains(q.Facets, e.Facet) {
					continue
				}
				if !isAnchoredRuleOrDecision(e) && !(e.Facet == entry.World && entry.IsSkeletonPredicate(e.Predicate)) {
					continue
				}
				if nbName := entryMatchesNeighbor(st, e, neighbors); nbName != "" {
					w := statusWeight[e.Status]
					if e.Pinned {
						w = 1
					}
					sc := 0.55 * w
					if old, ok := hopCands[e.ID]; !ok || sc > old.score {
						hopCands[e.ID] = hopCand{e: e, score: sc, via: ent.name + "→" + nbName}
					}
				}
			}
		}

		if len(ec.Skeleton) > 0 || len(ec.Rules) > 0 {
			contexts = append(contexts, ec)
		}
	}
	return contexts, hopCands
}

type hopCand struct {
	e     *entry.Entry
	score float64
	via   string
}

func isAnchoredRuleOrDecision(e *entry.Entry) bool {
	switch e.Facet {
	case entry.Interaction, entry.Tools, entry.Judgement, entry.Workflow:
		return true
	case entry.World:
		t := strings.ToLower(strings.TrimSpace(e.Type))
		p := strings.ToLower(strings.TrimSpace(e.Predicate))
		return t == "decision" || t == "decided" || t == "committed" || p == "decided" || p == "decides" || p == "committed" || e.Commitment != nil
	}
	return false
}

func entryRankWeight(e *entry.Entry) int {
	w := entry.Score(e)
	if e.Pinned {
		w += 100
	}
	if e.Status == entry.Stable {
		w += 20
	}
	return w
}

func entryMatchesEntity(st *store.State, e *entry.Entry, norms map[string]bool) bool {
	for _, n := range entryEntityNames(st, e) {
		if norms[norm.Q(n)] {
			return true
		}
	}
	return false
}

func entryMatchesNeighbor(st *store.State, e *entry.Entry, neighbors map[string]string) string {
	for _, n := range entryEntityNames(st, e) {
		if nb, ok := neighbors[norm.Q(n)]; ok {
			return nb
		}
	}
	return ""
}

func entryEntityNames(st *store.State, e *entry.Entry) []string {
	ns := names(st, e)
	if o := strings.TrimSpace(e.Impact.Object); o != "" {
		ns = append(ns, o)
	}
	return ns
}

func cleanEntityRef(st *store.State, raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if strings.HasPrefix(raw, "person:") && st != nil {
		if k := st.Contacts[raw]; k != nil && k.Manual != "removed" && k.Name != "" {
			return strings.TrimSpace(k.Name)
		}
	}
	for _, p := range []string{"project:", "person:", "system:", "object:"} {
		raw = strings.TrimPrefix(raw, p)
	}
	return strings.TrimSpace(raw)
}

func firstNonEmptyStr(xs ...string) string {
	for _, x := range xs {
		if strings.TrimSpace(x) != "" {
			return x
		}
	}
	return ""
}

// UserQuotes extracts up to limit deduplicated L0 user-authored original quote slices (<=80 runes, with date).
func UserQuotes(e *entry.Entry, limit int) []string {
	if e == nil || limit <= 0 {
		return nil
	}
	var out []string
	seen := map[string]bool{}
	for i := len(e.Evidence) - 1; i >= 0 && len(out) < limit; i-- {
		ev := e.Evidence[i]
		if ev.Who != entry.WhoSelf || ev.Reported {
			continue
		}
		q := strings.Join(strings.Fields(ev.Quote), " ")
		if q == "" {
			continue
		}
		k := norm.Q(q)
		if k == "" || seen[k] {
			continue
		}
		seen[k] = true
		if r := []rune(q); len(r) > quoteSliceMost {
			q = string(r[:quoteSliceMost]) + "…"
		}
		if ev.Day != "" {
			out = append(out, "原话：「"+q+"」（"+ev.Day+"）")
		} else {
			out = append(out, "原话：「"+q+"」")
		}
	}
	return out
}

// FormatEntity renders one EntityContext (1-hop skeleton edges, anchored rules, and L0 quote slices).
func FormatEntity(ec EntityContext) string {
	var b strings.Builder
	title := ec.Name
	if ec.Type != "" {
		title += "（" + ec.Type + "）"
	}
	fmt.Fprintf(&b, "## 实体骨架 1 跳展开：%s\n", title)
	if len(ec.Skeleton) > 0 {
		b.WriteString("骨架关系：\n")
		for _, se := range ec.Skeleton {
			if se.ValidFrom != "" {
				fmt.Fprintf(&b, "- %s -[%s]-> %s（%s）\n", se.From, se.Predicate, se.To, se.ValidFrom)
			} else {
				fmt.Fprintf(&b, "- %s -[%s]-> %s\n", se.From, se.Predicate, se.To)
			}
		}
	}
	if len(ec.Rules) > 0 {
		b.WriteString("挂靠规矩与决定：\n")
		for _, r := range ec.Rules {
			b.WriteString(Format(r))
		}
	}
	if len(ec.Quotes) > 0 {
		b.WriteString("相关原话：\n")
		for _, q := range ec.Quotes {
			fmt.Fprintf(&b, "- %s\n", q)
		}
	}
	return b.String()
}
