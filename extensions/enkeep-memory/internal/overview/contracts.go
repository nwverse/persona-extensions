package overview

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/entry"
	"github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/model"
	"github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/store"
)

// The L1 collaboration contract channel (unified design §1.3 step C): folds the 0.2.6 rule merge (curate/rules_merge)
// into the unified L1 overview state for facets ① interaction and ② tools. It merges synonymous rules, preserves every
// stated exception, assigns a polarity (act/check/communicate/route) so downstream standing selection can balance
// action vs verification, computes hard and violation_count deterministically from evidence, and preserves covered_by
// protection for handwritten or user-edited entries.

//go:embed contracts.md
var contractsPrompt string

//go:embed contracts.schema.json
var contractsSchema []byte

const (
	contractCandMost       = 120 // total rules per call across interaction and tools
	contractInteractionCap = 80  // nominal cap for ① interaction (unused tools slots spill over)
	contractToolsCap       = 40  // guaranteed floor/cap for ② tools so interaction never starves it
	contractNonHardPct     = 40  // at least 40% of each facet's quota reserved for non-hard high-score rules
	contractCandScore      = 50  // lowest score for ① interaction non-hard rules
	contractToolsScore     = 40  // lowest score for ② tools stable non-hard rules (tools lacks interaction correction bonus)
	contractMaxTokens      = 8000
	flagNoAutoMerge        = "no_auto_merge"
)

type (
	contractRuleInput struct {
		ID        string   `json:"id"`
		Facet     string   `json:"facet"`
		Type      string   `json:"type,omitempty"`
		Text      string   `json:"text"`
		Exception string   `json:"exception,omitempty"`
		Count     int      `json:"count"`
		Hard      bool     `json:"hard,omitempty"`
		UserOwned bool     `json:"user_owned,omitempty"`
		Quotes    []string `json:"user_lines,omitempty"`
	}
	contractExistingInput struct {
		ID        string `json:"id"`
		Facet     string `json:"facet"`
		Polarity  string `json:"polarity,omitempty"`
		Text      string `json:"text"`
		Exception string `json:"exception,omitempty"`
	}
	contractInput struct {
		Lang     string                  `json:"language"`
		Rules    []contractRuleInput     `json:"rules"`
		Existing []contractExistingInput `json:"existing,omitempty"`
	}
	contractProposal struct {
		Extends     string   `json:"extends"`
		Rules       []string `json:"rules"`
		Polarity    string   `json:"polarity"`
		Text        string   `json:"text"`
		Situation   string   `json:"situation"`
		Choice      string   `json:"choice"`
		Exception   string   `json:"exception"`
		Conflicting []string `json:"conflicting,omitempty"`
		Reason      string   `json:"reason"`
	}
	contractOutput struct {
		Contracts []contractProposal `json:"contracts"`
	}
)

// isUserOwned says whether e was written, edited or pinned by the user, or came from the assistant's handwritten
// memory: such entries are never silently merged away; a covering contract records them in CoveredIDs.
func isUserOwned(e *entry.Entry) bool {
	if e == nil {
		return false
	}
	return e.User != nil || e.Pinned || e.Origin == entry.Handwritten
}

// contractCandidates returns the live interaction/tools rules eligible for L1 contract aggregation, before cutoff
// ("" means all). Live non-sensitive user-owned rules (handwritten/edited/pinned) are admitted first so high-precision
// curated specs are never starved; remaining slots reserve a dedicated quota for ② tools (up to contractToolsCap) and
// at least contractNonHardPct% per facet for non-hard high-scoring rules (including cross-month Abstract entries).
func contractCandidates(st *store.State, cutoff string) []*entry.Entry {
	var ownedInter, ownedTools, inter, tools []*entry.Entry
	for _, e := range st.Entries {
		if e == nil || !e.Live() || (e.Facet != entry.Interaction && e.Facet != entry.Tools) {
			continue
		}
		if entry.KindOf(e) != entry.KindRules || e.Predicate != "" {
			continue
		}
		if entry.CoveredBy(e) != "" || slices.Contains(e.Flags, flagNoAutoMerge) {
			continue
		}
		fastHot := entry.FastHotDay(e) != ""
		owned := isUserOwned(e)
		if !usable(st, e) && !owned && !fastHot {
			continue
		}
		if cutoff != "" && e.First >= cutoff {
			continue
		}
		minScore := contractCandScore
		if e.Facet == entry.Tools && e.Status == entry.Stable {
			minScore = contractToolsScore
		}
		if entry.Score(e) < minScore && !entry.Hard(e) && !owned && !fastHot && e.Grain != entry.Abstract {
			continue
		}
		if owned {
			if e.Facet == entry.Tools {
				ownedTools = append(ownedTools, e)
			} else {
				ownedInter = append(ownedInter, e)
			}
			continue
		}
		if e.Facet == entry.Tools {
			tools = append(tools, e)
		} else {
			inter = append(inter, e)
		}
	}
	sortByScore := func(s []*entry.Entry) {
		sort.Slice(s, func(i, j int) bool {
			if a, b := entry.Score(s[i]), entry.Score(s[j]); a != b {
				return a > b
			}
			if s[i].Count != s[j].Count {
				return s[i].Count > s[j].Count
			}
			return s[i].ID < s[j].ID
		})
	}
	sortByScore(ownedInter)
	sortByScore(ownedTools)

	toolsRem := max(0, contractToolsCap-len(ownedTools))
	toolsPicked := append(ownedTools, pickFacetRules(tools, toolsRem)...)
	if len(toolsPicked) > contractToolsCap {
		toolsPicked = toolsPicked[:contractToolsCap]
	}
	interCap := contractCandMost - len(toolsPicked)
	if interCap < 0 {
		interCap = 0
	}
	interOwnedCap := min(len(ownedInter), interCap)
	interRem := max(0, interCap-interOwnedCap)
	interPicked := append(ownedInter[:interOwnedCap], pickFacetRules(inter, interRem)...)
	out := append(interPicked, toolsPicked...)
	if len(out) > contractCandMost {
		out = out[:contractCandMost]
	}
	return out
}

// pickFacetRules picks up to cap entries from a single facet, reserving at least contractNonHardPct% for non-hard
// high-scoring entries (unused slots in either pool spill over to the other).
func pickFacetRules(es []*entry.Entry, capN int) []*entry.Entry {
	if capN <= 0 || len(es) == 0 {
		return nil
	}
	var hardPool, regPool []*entry.Entry
	for _, e := range es {
		if entry.Hard(e) {
			hardPool = append(hardPool, e)
		} else {
			regPool = append(regPool, e)
		}
	}
	sortByScore := func(s []*entry.Entry) {
		sort.Slice(s, func(i, j int) bool {
			if a, b := entry.Score(s[i]), entry.Score(s[j]); a != b {
				return a > b
			}
			if s[i].Count != s[j].Count {
				return s[i].Count > s[j].Count
			}
			return s[i].ID < s[j].ID
		})
	}
	sortByScore(hardPool)
	sortByScore(regPool)

	regFloor := capN * contractNonHardPct / 100
	hardCap := capN - regFloor
	takeHard := min(len(hardPool), hardCap)
	takeReg := min(len(regPool), regFloor)
	rem := capN - takeHard - takeReg
	if rem > 0 && takeReg < len(regPool) {
		add := min(rem, len(regPool)-takeReg)
		takeReg += add
		rem -= add
	}
	if rem > 0 && takeHard < len(hardPool) {
		add := min(rem, len(hardPool)-takeHard)
		takeHard += add
	}
	out := make([]*entry.Entry, 0, takeHard+takeReg)
	out = append(out, hardPool[:takeHard]...)
	out = append(out, regPool[:takeReg]...)
	return out
}

// contractsSignature hashes the eligible rule set so SyncContracts skips redundant model calls when nothing changed.
func contractsSignature(rules []*entry.Entry) string {
	h := sha256.New()
	for _, e := range rules {
		fmt.Fprintf(h, "%s@%s\n", e.ID, e.Updated)
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// normalizePolarity returns a valid polarity or "" if unrecognised.
func normalizePolarity(p string) string {
	p = strings.ToLower(strings.TrimSpace(p))
	if slices.Contains(Polarities, p) {
		return p
	}
	return ""
}

// SyncContracts aggregates interaction and tools rules into L1 collaboration contracts in s. When cutoff is "" and the
// rule set signature has not changed since the last run (and s already holds contracts), it skips the model call.
// Returns the ids of created or updated contracts.
func (r *Runner) SyncContracts(ctx context.Context, st *store.State, s *State, run *Run, cutoff, now string) ([]string, error) {
	rules := contractCandidates(st, cutoff)
	run.Counts["contract_candidates"] = len(rules)
	if len(rules) == 0 {
		return nil, nil
	}
	sig := contractsSignature(rules)
	existing := s.Sorted(func(p *Pattern) bool { return p.IsContract() && p.Lifecycle != Retired })
	if cutoff == "" && s.ContractsSig == sig && len(existing) > 0 {
		run.Counts["contracts_skipped_unchanged"]++
		return nil, nil
	}

	in := contractInput{Lang: r.lang()}
	local := map[string]*entry.Entry{}
	for i, e := range rules {
		id := fmt.Sprintf("r%d", i+1)
		local[id] = e
		in.Rules = append(in.Rules, contractRuleInput{
			ID:        id,
			Facet:     string(e.Facet),
			Type:      e.Type,
			Text:      clip(e.Shown(), 180),
			Exception: clip(e.Exception, 140),
			Count:     e.Count,
			Hard:      entry.Hard(e),
			UserOwned: isUserOwned(e),
			Quotes:    itemQuotes(st, e, cutoff, 2),
		})
	}
	localP := map[string]*Pattern{}
	for i, p := range existing {
		if i >= synthExisting {
			break
		}
		pid := fmt.Sprintf("p%d", i+1)
		localP[pid] = p
		in.Existing = append(in.Existing, contractExistingInput{
			ID:        pid,
			Facet:     p.Facet,
			Polarity:  p.Polarity,
			Text:      clip(p.Text, 200),
			Exception: clip(p.Exception, 120),
		})
	}

	var out contractOutput
	err := r.call(ctx, run, "contracts", model.Medium, contractsPrompt, contractsSchema, in, contractMaxTokens, &out)
	if errors.Is(err, model.ErrRefused) {
		run.Counts["contracts_refused"]++
		run.Notes = append(run.Notes, "contracts: the model declined")
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	touched := applyContractProposals(st, s, local, localP, out.Contracts, cutoff, now, run)
	if cutoff == "" {
		s.ContractsSig = sig
	}
	return touched, nil
}

// applyContractProposals validates and writes the model's contract proposals into s.
func applyContractProposals(st *store.State, s *State, local map[string]*entry.Entry, localP map[string]*Pattern,
	proposals []contractProposal, cutoff, now string, run *Run) []string {
	used := map[string]bool{}
	for _, prop := range proposals {
		for _, cid := range prop.Conflicting {
			used[cid] = true
		}
	}
	var touched []string
	for _, prop := range proposals {
		var members []*entry.Entry
		for _, rid := range prop.Rules {
			e := local[rid]
			if e == nil || used[rid] || !e.Live() {
				continue
			}
			if len(members) > 0 && e.Facet != members[0].Facet {
				continue
			}
			members = append(members, e)
			used[rid] = true
		}
		text := strings.TrimSpace(prop.Text)
		if text == "" || len(members) == 0 {
			run.Counts["contract_dropped"]++
			continue
		}
		facet := string(members[0].Facet)
		var p *Pattern
		if ext := localP[prop.Extends]; ext != nil && ext.Facet == facet {
			p = ext
		}
		if p == nil {
			id := patternID(facet, prop.Situation, prop.Choice, text)
			p = s.Patterns[id]
			if p == nil {
				p = &Pattern{ID: id, Facet: facet, Kind: KindContract, Lifecycle: Candidate, Created: now}
				s.Patterns[id] = p
				run.Counts["contracts_new"]++
			}
		}
		if p.Lifecycle == Retired || p.Lifecycle == EraClosed {
			p.Lifecycle = Candidate
		}
		p.Kind = KindContract
		p.Text = text
		if sit := strings.TrimSpace(prop.Situation); sit != "" {
			p.Situation = sit
		}
		if ch := strings.TrimSpace(prop.Choice); ch != "" {
			p.Choice = ch
		}
		if pol := normalizePolarity(prop.Polarity); pol != "" {
			p.Polarity = pol
		}
		// Merge exceptions: never lose a member's or existing contract's stated exception.
		p.Exception = mergeExceptions(p.Exception, prop.Exception, members)
		// Pick the strongest member's Type as RuleType when unset.
		if p.RuleType == "" {
			p.RuleType = primaryRuleType(members)
		}
		var memberIDs, coveredIDs []string
		for _, e := range members {
			memberIDs = append(memberIDs, e.ID)
			if isUserOwned(e) {
				coveredIDs = append(coveredIDs, e.ID)
			}
		}
		p.EvidenceIDs = union(p.EvidenceIDs, memberIDs...)
		if len(coveredIDs) > 0 {
			p.CoveredIDs = union(p.CoveredIDs, coveredIDs...)
			sort.Strings(p.CoveredIDs)
		}
		p.Reason = strings.TrimSpace(prop.Reason)
		p.UpdatedAt = now
		recount(st, p, cutoff)
		if p.Gate == "" {
			if p.Exception != "" {
				p.Lifecycle = Scoped
			} else {
				p.Lifecycle = Core
			}
		} else {
			p.Lifecycle = Candidate
		}
		touched = union(touched, p.ID)
		run.Counts["contracts_synced"]++
	}
	return touched
}

// mergeExceptions keeps the combined exception without silently dropping conditions stated on the existing contract
// or any member rule.
func mergeExceptions(existing, proposed string, members []*entry.Entry) string {
	proposed = strings.TrimSpace(proposed)
	existing = strings.TrimSpace(existing)
	if proposed != "" {
		return proposed
	}
	if existing != "" {
		return existing
	}
	var parts []string
	for _, e := range members {
		if x := strings.TrimSpace(e.Exception); x != "" && !slices.Contains(parts, x) {
			parts = append(parts, x)
		}
	}
	return strings.Join(parts, "；")
}

// primaryRuleType picks the Type of the highest-scoring member with a non-empty Type.
func primaryRuleType(members []*entry.Entry) string {
	best := ""
	bestScore := -1
	for _, e := range members {
		t := strings.TrimSpace(e.Type)
		if t == "" {
			continue
		}
		if s := entry.Score(e); s > bestScore {
			best, bestScore = t, s
		}
	}
	return best
}
