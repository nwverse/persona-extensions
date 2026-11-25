package overview

import (
	"context"
	crand "crypto/sha1"
	_ "embed"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/model"
	"github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/store"
)

//go:embed audit.md
var auditPrompt string

//go:embed audit.schema.json
var auditSchema []byte

// The spot check (design 4.1, line 1): a model, blind to how the patterns were made, judges each sampled pattern from
// the quotes it rests on alone; the list is kept for the user to go over (the model's verdict is not the user's).
const (
	AuditMost     = 30
	auditBatch    = 10
	auditQuotes   = 6
	auditMaxTok   = 3000
	auditQuoteLen = 160
)

// Verdicts of the spot check.
const (
	VerdictRight       = "right"
	VerdictMostlyRight = "mostly_right"
	VerdictOneOff      = "one_off"
	VerdictGeneric     = "generic"
	VerdictUnsupported = "unsupported"
)

type (
	auditIn struct {
		ID        string   `json:"id"`
		Statement string   `json:"statement"`
		Quotes    []string `json:"quotes"`
	}
	auditInput struct {
		Lang  string    `json:"language"`
		Items []auditIn `json:"items"`
	}
	auditOutput struct {
		Items []struct {
			ID      string `json:"id"`
			Verdict string `json:"verdict"`
			Reason  string `json:"reason"`
		} `json:"items"`
	}
)

// Sample is up to AuditMost used patterns, spread over the facets in turn, each facet's picked by a hash of the ids.
func Sample(s *State, n int) []*Pattern {
	by := map[string][]*Pattern{}
	h := func(id string) string { x := crand.Sum([]byte(id + "/audit")); return fmt.Sprintf("%x", x) }
	for _, p := range s.Patterns {
		if p.Used() {
			by[p.Facet] = append(by[p.Facet], p)
		}
	}
	for _, ps := range by {
		sort.Slice(ps, func(i, j int) bool { return h(ps[i].ID) < h(ps[j].ID) })
	}
	var out []*Pattern
	for len(out) < n {
		took := false
		for _, f := range Facets {
			if ps := by[string(f)]; len(ps) > 0 && len(out) < n {
				out, by[string(f)], took = append(out, ps[0]), ps[1:], true
			}
		}
		if !took {
			break
		}
	}
	return out
}

// quotes are the user's lines a pattern rests on, one per independent unit·day, at most n.
func quotes(st *store.State, p *Pattern, n int) []string {
	var out []string
	seen := map[string]bool{}
	for _, id := range p.EvidenceIDs {
		e := st.Entries[id]
		if e == nil {
			continue
		}
		for _, ev := range evidenceOf(st, e, "") {
			if !ev.quoted || seen[ev.key] || len(out) >= n {
				continue
			}
			x := st.Entries[ev.entry]
			for _, line := range x.Evidence {
				if line.Ref+"@"+line.Day == ev.key && strings.TrimSpace(line.Quote) != "" {
					out = append(out, clip(line.Quote, auditQuoteLen))
					seen[ev.key] = true
					break
				}
			}
		}
	}
	return out
}

// Audit judges a sample of s's used patterns from their quotes in st.
func (r *Runner) Audit(ctx context.Context, st *store.State, s *State) (*Audit, error) {
	a := &Audit{At: stamp(r.now()), Summary: map[string]int{}}
	sample := Sample(s, AuditMost)
	run := &Run{}
	for b := 0; b < len(sample); b += auditBatch {
		batch := sample[b:min(len(sample), b+auditBatch)]
		in := auditInput{Lang: r.lang()}
		for i, p := range batch {
			in.Items = append(in.Items, auditIn{ID: fmt.Sprintf("s%d", b+i+1), Statement: clip(p.Text+exceptionClause(p), 300),
				Quotes: quotes(st, p, auditQuotes)})
		}
		var out auditOutput
		err := r.call(ctx, run, "audit", model.Medium, auditPrompt, auditSchema, in, auditMaxTok, &out)
		if errors.Is(err, model.ErrRefused) {
			for _, p := range batch {
				a.Items = append(a.Items, AuditItem{ID: p.ID, Facet: p.Facet, Text: p.Text, Verdict: "declined"})
			}
			continue
		}
		if err != nil {
			a.Calls = run.Calls
			return a, err
		}
		got := map[string][2]string{}
		for _, x := range out.Items {
			got[x.ID] = [2]string{x.Verdict, x.Reason}
		}
		for i, p := range batch {
			v := got[fmt.Sprintf("s%d", b+i+1)]
			a.Items = append(a.Items, AuditItem{ID: p.ID, Facet: p.Facet, Text: p.Text, Verdict: v[0], Reason: v[1]})
		}
	}
	for _, it := range a.Items {
		a.Summary[it.Verdict]++
	}
	a.Calls = run.Calls
	return a, nil
}

func exceptionClause(p *Pattern) string {
	if x := strings.TrimSpace(p.Exception); x != "" {
		return "（例外：" + x + "）"
	}
	return ""
}
