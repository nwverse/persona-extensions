package curate

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/entry"
	"github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/export"
	"github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/model"
	"github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/store"
)

// The rule merge (0.2.6): the rules that compete for the standing set (the assistant's rules, ① ②, settled or hard)
// are put to the model together, across types, and the ones that tell the assistant the same thing on the same kind
// of occasion are made one: the strongest of them stays, with one sentence for the default and every condition under
// which it does not hold that any of them or the user's lines state. The class merge (merge.go) sees one grain and
// one type at a time, and the consolidation compares a new entry with the ones alike in words, so a rule written
// down again in other words, under another type or by the assistant's own memory stood several times, and the
// standing set filled with one rule ("do not ask, just do it") while the copy that kept its condition ("only when it
// is live, public or deletes data") was not among them.

//go:embed rules_merge.md
var rulesMergePrompt string

//go:embed rules_merge.schema.json
var rulesMergeSchema []byte

const (
	rulesMergeMost      = 120 // rules per call, the highest scoring first
	rulesMergeScore     = 60  // the lowest score put to the merge (a hard rule always is)
	rulesMergeMaxTokens = 8000
)

type (
	rulesMergeRule struct {
		ID        string   `json:"id"`
		Type      string   `json:"type,omitempty"`
		Text      string   `json:"text"`
		Exception string   `json:"exception,omitempty"`
		Count     int      `json:"count"`
		Quotes    []string `json:"user_lines,omitempty"`
	}
	rulesMergeInput struct {
		Lang  string           `json:"language"`
		Rules []rulesMergeRule `json:"rules"`
	}
	rulesMergeGroup struct {
		Rules       []string `json:"rules"`
		Text        string   `json:"text"`
		Exception   string   `json:"exception"`
		Conflicting []string `json:"conflicting,omitempty"`
		Reason      string   `json:"reason"`
	}
	rulesMergeOutput struct {
		Groups []rulesMergeGroup `json:"groups"`
	}
)

// mergeCandidates are the rules put to the merge: live rules of ① ②, settled or hard, not covered and not kept apart
// by the user, scoring rulesMergeScore or more, the highest first, at most rulesMergeMost.
func mergeCandidates(s *store.State, before string) []*entry.Entry {
	var out []*entry.Entry
	for _, e := range s.Entries {
		if !e.Live() || (e.Facet != entry.Interaction && e.Facet != entry.Tools) || entry.KindOf(e) != entry.KindRules ||
			e.Grain == entry.Abstract || e.Predicate != "" || entry.CoveredBy(e) != "" || slices.Contains(e.Flags, flagNoAutoMerge) ||
			!export.Usable(s, e) {
			continue
		}
		if before != "" && e.First >= before {
			continue
		}
		if entry.Score(e) < rulesMergeScore && !entry.Hard(e) {
			continue
		}
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool {
		if a, b := entry.Score(out[i]), entry.Score(out[j]); a != b {
			return a > b
		}
		return out[i].ID < out[j].ID
	})
	if len(out) > rulesMergeMost {
		out = out[:rulesMergeMost]
	}
	return out
}

// rulesSignature names a set of rules as it stands (ids and when each changed): the merge runs again only when it
// changed.
func rulesSignature(es []*entry.Entry) string {
	h := sha256.New()
	for _, e := range es {
		fmt.Fprintf(h, "%s@%s\n", e.ID, e.Updated)
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// mergeRules asks which rules are one and merges them; once per changed set of rules. Call it with the store's lock
// held.
func (c *Curator) mergeRules(ctx context.Context, m model.Model, run *store.Run) error {
	return c.mergeRulesBefore(ctx, m, "", run)
}

// mergeRulesBefore is mergeRules over the rules first seen before a day ("" every rule).
func (c *Curator) mergeRulesBefore(ctx context.Context, m model.Model, before string, run *store.Run) error {
	s := c.St.S
	rules := mergeCandidates(s, before)
	if m == nil || len(rules) < 2 {
		return nil
	}
	sig := rulesSignature(rules)
	if s.RulesMerged == sig {
		return nil
	}
	in := rulesMergeInput{Lang: c.language()}
	local := map[string]*entry.Entry{}
	for i, e := range rules {
		id := fmt.Sprintf("r%d", i+1)
		local[id] = e
		var qs []string
		for _, ev := range e.Evidence {
			if q := strings.TrimSpace(ev.Quote); q != "" && ev.Who == entry.WhoSelf && len(qs) < 2 {
				qs = append(qs, clip(q, 120))
			}
		}
		in.Rules = append(in.Rules, rulesMergeRule{ID: id, Type: e.Type, Text: e.Shown(), Exception: e.Exception, Count: e.Count, Quotes: qs})
	}
	body, _ := json.Marshal(in)
	resp, err := m.Complete(ctx, model.Request{Step: "rules_merge", Tier: model.Medium, System: rulesMergePrompt, User: string(body),
		MaxTokens: rulesMergeMaxTokens, Schema: rulesMergeSchema})
	if errors.Is(err, model.ErrRefused) {
		run.Counts["rules_merge_refused"]++
		return nil
	}
	if err != nil {
		return err
	}
	run.Counts["rules_merge_calls"]++
	var out rulesMergeOutput
	if json.Unmarshal([]byte(extractJSON(resp.Text)), &out) != nil {
		run.Notes = append(run.Notes, "规矩合并：答案读不了")
		return nil
	}
	c.applyRuleMerges(local, out.Groups, run)
	s.RulesMerged = rulesSignature(mergeCandidates(s, before))
	return nil
}

// applyRuleMerges checks and applies the groups: ids it was shown, each rule in one group, one facet per group. The
// strongest rule of a group stays (the user's own first, then the highest score, then the most seen); the others are
// merged into it, or marked covered by it when they may not be merged away (the user's, or the assistant's own
// memory). The kept rule takes the group's sentence unless the user or the assistant's memory wrote it, and the
// group's exception when it carries none of its own.
func (c *Curator) applyRuleMerges(local map[string]*entry.Entry, groups []rulesMergeGroup, run *store.Run) {
	used := map[string]bool{}
	for _, g := range groups {
		for _, id := range g.Conflicting {
			used[id] = true
		}
	}
	for _, g := range groups {
		var members []*entry.Entry
		for _, id := range g.Rules {
			if e := local[id]; e != nil && !used[id] && e.Live() {
				if len(members) > 0 && e.Facet != members[0].Facet {
					continue
				}
				members = append(members, e)
				used[id] = true
			}
		}
		if len(members) < 2 {
			continue
		}
		keep := members[0]
		for _, e := range members[1:] {
			ke, kk := e.User != nil || e.Pinned, keep.User != nil || keep.Pinned
			se, sk := entry.Score(e), entry.Score(keep)
			if ke && !kk || ke == kk && (se > sk || se == sk && (e.Count > keep.Count || e.Count == keep.Count && e.ID < keep.ID)) {
				keep = e
			}
		}
		why := "same rule: " + clip(strings.TrimSpace(g.Reason), 100)
		done := 0
		for _, e := range members {
			if e == keep {
				continue
			}
			if !userTouched(e) && c.Merge(e.ID, keep.ID, why, run.ID) == nil {
				done++
				continue
			}
			e.Flags = appendFlag(e.Flags, entry.FlagCoveredBy+keep.ID)
			e.Updated = c.stamp()
			c.commit(entry.EvMerge, e, keep.ID, "covered: "+why, run.ID)
			done++
			run.Counts["rules_covered"]++
		}
		if done == 0 {
			continue
		}
		run.Counts["rule_groups"]++
		run.Counts["rules_merged"] += done
		changed := false
		if t := strings.TrimSpace(g.Text); t != "" && !userTouched(keep) && !tombstoned(c.St.S, keep.Facet, t, keep.Person) {
			keep.Text, changed = t, true
		}
		if x := strings.TrimSpace(g.Exception); x != "" && keep.User == nil && strings.TrimSpace(keep.Exception) == "" {
			keep.Exception, changed = x, true
		}
		if changed {
			keep.Updated = c.stamp()
			c.commit(entry.EvMerge, keep, "", "one rule for its group", run.ID)
		}
	}
}

// MergeRules runs the rule merge now, under the store's lock, without saving (the caller saves) or settling: for an offline copy
// of the state (hardeval merge-rules).
// before limits it to the rules first seen before that day (an evaluation's cutoff; "" every rule).
func (c *Curator) MergeRules(ctx context.Context, m model.Model, before string) (*store.Run, error) {
	c.St.Lock()
	defer c.St.Unlock()
	run := &store.Run{ID: fmt.Sprintf("rules-merge-%d", c.now().Unix()), Kind: "rules_merge", Counts: map[string]int{}}
	c.St.S.RulesMerged = ""
	// Not settled again here: the statuses stay as the copy had them, so a reading measures the merge alone.
	err := c.mergeRulesBefore(ctx, m, before, run)
	return run, err
}
