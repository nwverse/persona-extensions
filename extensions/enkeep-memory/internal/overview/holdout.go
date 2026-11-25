package overview

import (
	"context"
	"crypto/sha1"
	_ "embed"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/entry"
	"github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/export"
	"github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/memsrc"
	"github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/model"
	"github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/store"
)

//go:embed predict.md
var predictPrompt string

//go:embed predict.schema.json
var predictSchema []byte

// PredictPrompt and PredictSchema are the forward check's question, for the hard evaluation (hardeval) to put its
// questions the same way.
var (
	PredictPrompt = predictPrompt
	PredictSchema = predictSchema
)

// The forward check (design 2.5, line 3): the user's real choices first seen in a week (a situation, what they chose
// and what they turned down) are put as two-option questions to a model three times: with the patterns kept before
// the week, with the scattered entries in the same number of characters, and with no memory. A question is scored by
// code (the option the user took), never by a model. Patterns resting on a question's own entry or its class are left
// out of the patterns' memory, and the same entries out of the scattered memory, so neither arm sees the answer.
// Neither arm carries anything categorised or from a restricted space (what memory.entries never makes standing).
const (
	HoldoutMost     = 30
	holdoutBatch    = 15
	holdoutMaxTok   = 1500
	memoryCharsMost = 6000
)

// Arms of the check.
const (
	ArmOverview = "overview"
	ArmScatter  = "scatter"
	ArmNone     = "none"
)

// question is one held-out choice.
type question struct {
	e      *entry.Entry
	a, b   string
	answer string // "A" or "B": where the user's choice is
}

// questions are the week's real choices: actions first seen in it with a situation, a choice and what was turned
// down, of the four facets where a choice is made; at most HoldoutMost, picked by a hash of their ids (no cherry
// picking), the options' order by the same hash.
func questions(st *store.State, days []string) []question {
	var out []question
	for _, e := range st.Entries {
		if !usable(st, e) || e.Grain != entry.Action || e.First < days[0] || e.First > days[6] {
			continue
		}
		switch e.Facet {
		case entry.Judgement, entry.Workflow, entry.Interaction, entry.Tools:
		default:
			continue
		}
		if strings.TrimSpace(e.Situation) == "" || strings.TrimSpace(e.Choice) == "" || strings.TrimSpace(e.Rejected) == "" {
			continue
		}
		out = append(out, question{e: e})
	}
	h := func(s string) string { x := sha1.Sum([]byte(s)); return fmt.Sprintf("%x", x) }
	sort.Slice(out, func(i, j int) bool { return h(out[i].e.ID) < h(out[j].e.ID) })
	out = out[:min(len(out), HoldoutMost)]
	for i := range out {
		q := &out[i]
		if h(q.e.ID + "/order")[0] < '8' {
			q.a, q.b, q.answer = q.e.Choice, q.e.Rejected, "A"
		} else {
			q.a, q.b, q.answer = q.e.Rejected, q.e.Choice, "B"
		}
	}
	return out
}

type (
	predictQuestion struct {
		ID        string `json:"id"`
		Situation string `json:"situation"`
		A         string `json:"A"`
		B         string `json:"B"`
	}
	predictInput struct {
		Memory    string            `json:"memory"`
		Questions []predictQuestion `json:"questions"`
	}
	predictOutput struct {
		Answers []struct {
			ID   string `json:"id"`
			Pick string `json:"pick"`
		} `json:"answers"`
	}
)

// Holdout checks the patterns of s (kept before the week) against the week's real choices in st. backtest marks a
// check whose patterns were built with a cutoff at the week's start.
func (r *Runner) Holdout(ctx context.Context, st *store.State, s *State, week string, backtest bool) (*Holdout, error) {
	days, err := weekDays(week)
	if err != nil {
		return nil, err
	}
	qs := questions(st, days)
	h := &Holdout{Week: week, At: stamp(r.now()), Backtest: backtest, N: len(qs), Hits: map[string]int{}, Rate: map[string]float64{},
		Chars: map[string]int{}}
	if len(qs) == 0 {
		h.Notes = append(h.Notes, "no real choice with a turned-down option was first seen in the week")
		return h, nil
	}
	// What a question's answer is: its entry and its class.
	leak := map[string]bool{}
	for _, q := range qs {
		leak[q.e.ID] = true
		if q.e.Parent != "" {
			leak[q.e.Parent] = true
		}
	}
	// The patterns' memory.
	var lines []string
	for _, p := range s.Sorted(func(p *Pattern) bool { return p.Used() && !private(p) }) {
		if slices.ContainsFunc(p.EvidenceIDs, func(id string) bool { return leak[id] }) {
			h.Excluded++
			continue
		}
		lines = append(lines, "- "+Text(p))
	}
	ovMem := fit(lines, memoryCharsMost)
	h.Patterns = len(strings.Split(strings.TrimSpace(ovMem), "\n"))
	if strings.TrimSpace(ovMem) == "" {
		h.Patterns = 0
	}
	// The scattered memory: the curated entries in memory.entries' order (standing, rank, newest), first seen before
	// the week, in as many characters as the patterns took (at least a little, so an empty overview still has a
	// baseline to compare with).
	budget := max(len([]rune(ovMem)), 1500)
	var pool []*entry.Entry
	for _, e := range st.Entries {
		if leak[e.ID] || !memsrc.Pool(st, e) || e.First >= days[0] || len(e.Cats()) > 0 || e.OriginSpace != "" {
			continue
		}
		pool = append(pool, e)
	}
	sort.Slice(pool, func(i, j int) bool {
		a, b := pool[i], pool[j]
		sa, sb := export.Usable(st, a), export.Usable(st, b)
		if sa != sb {
			return sa
		}
		if ra, rb := memsrc.Rank(a), memsrc.Rank(b); ra != rb {
			return ra > rb
		}
		if a.Last != b.Last {
			return a.Last > b.Last
		}
		return a.ID < b.ID
	})
	lines = lines[:0]
	for _, e := range pool {
		lines = append(lines, "- "+clip(e.Shown(), 200))
		if len([]rune(strings.Join(lines, "\n"))) > budget {
			break
		}
	}
	scMem := fit(lines, budget)
	arms := []struct{ name, mem string }{{ArmOverview, ovMem}, {ArmScatter, scMem}, {ArmNone, ""}}
	run := &Run{}
	for _, arm := range arms {
		h.Chars[arm.name] = len([]rune(arm.mem))
		for b := 0; b < len(qs); b += holdoutBatch {
			batch := qs[b:min(len(qs), b+holdoutBatch)]
			in := predictInput{Memory: arm.mem}
			for i, q := range batch {
				in.Questions = append(in.Questions, predictQuestion{ID: fmt.Sprintf("q%d", b+i+1), Situation: clip(q.e.Situation, 160),
					A: clip(q.a, 120), B: clip(q.b, 120)})
			}
			var out predictOutput
			err := r.call(ctx, run, "predict", model.Small, predictPrompt, predictSchema, in, holdoutMaxTok, &out)
			if errors.Is(err, model.ErrRefused) {
				h.Notes = append(h.Notes, arm.name+": the model declined a batch (counted wrong)")
				continue
			}
			if err != nil {
				h.Calls = run.Calls
				return h, err
			}
			picks := map[string]string{}
			for _, a := range out.Answers {
				picks[a.ID] = a.Pick
			}
			for i, q := range batch {
				if picks[fmt.Sprintf("q%d", b+i+1)] == q.answer {
					h.Hits[arm.name]++
				}
			}
		}
		h.Rate[arm.name] = float64(h.Hits[arm.name]) / float64(len(qs))
	}
	h.Calls = run.Calls
	return h, nil
}

// fit joins lines up to n characters.
func fit(lines []string, n int) string {
	var b strings.Builder
	used := 0
	for _, l := range lines {
		k := len([]rune(l)) + 1
		if used+k > n {
			break
		}
		b.WriteString(l + "\n")
		used += k
	}
	return b.String()
}
