package profile

import (
	"context"
	_ "embed"
	"encoding/json"
	"strings"

	"github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/entry"
	"github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/model"
)

// synthMaxTokens bounds the reply: six facets of two or three sentences, the overview and the recent highlights in
// Chinese came to 2,500–3,200 tokens on a live history, and a reply cut off falls back to the strung-together summary.
const synthMaxTokens = 4000

//go:embed synth.md
var synthPrompt string

//go:embed synth.schema.json
var synthSchema json.RawMessage

type synthInput struct {
	Language string         `json:"language"`
	Recent   []string       `json:"recent,omitempty"`
	Sections []synthInFacet `json:"sections"`
}

type synthInFacet struct {
	Facet  string  `json:"facet"`
	Points []Point `json:"points"`
}

type synthOutput struct {
	Overview string            `json:"overview"`
	Sections map[string]string `json:"sections"`
	Recent   *[]string         `json:"recent"`
}

// synthesize polishes the general (non-sensitive) overview and six-facet summaries with a single model call.
// Sensitive sections are never sent to the model here.
func synthesize(ctx context.Context, m model.Model, tier model.Tier, base *Summary, lang string) (*Summary, error) {
	in := synthInput{
		Language: lang,
		Recent:   base.Recent,
	}
	if in.Language == "" {
		in.Language = "zh-Hans"
	}
	for _, sec := range base.Sections {
		if len(sec.Points) == 0 {
			continue
		}
		in.Sections = append(in.Sections, synthInFacet{
			Facet:  string(sec.Facet),
			Points: sec.Points,
		})
	}
	if len(in.Sections) == 0 {
		return base, nil
	}
	rawIn, err := json.Marshal(in)
	if err != nil {
		return nil, err
	}
	if tier == "" {
		tier = model.Small
	}
	resp, err := m.Complete(ctx, model.Request{
		Step:      "profile_summary",
		Tier:      tier,
		System:    synthPrompt,
		User:      string(rawIn),
		MaxTokens: synthMaxTokens,
		Schema:    synthSchema,
	})
	if err != nil {
		return nil, err
	}
	var parsed synthOutput
	if err := json.Unmarshal([]byte(strings.TrimSpace(resp.Text)), &parsed); err != nil {
		return nil, err
	}
	out := *base
	out.Sections = append([]Section(nil), base.Sections...)
	ov := cleanLine(parsed.Overview)
	if ov != "" {
		out.Overview = clipRunes(ov, 360)
	}
	applied := ov != ""
	for i := range out.Sections {
		if len(out.Sections[i].Points) == 0 {
			out.Sections[i].Summary = ""
			continue
		}
		if s := cleanLine(parsed.Sections[string(out.Sections[i].Facet)]); s != "" {
			out.Sections[i].Summary = clipRunes(s, 280)
			applied = true
		}
	}
	// The recent highlights as the model kept them: only the user's own (it drops what others did). Each kept one is
	// a rewrite of an input item, so it is grounded; an answer without the list keeps the deterministic ones.
	if parsed.Recent != nil {
		var rec []string
		for _, r := range *parsed.Recent {
			if r = cleanLine(r); r != "" && len(rec) < MaxRecent {
				rec = append(rec, clipRunes(r, 120))
			}
		}
		out.Recent = rec
		applied = true
	}
	if applied {
		out.Mode = ModeModel
	}
	return &out, nil
}

// SectionByFacet returns the section for f, if present.
func (s *Summary) SectionByFacet(f entry.Facet) (Section, bool) {
	if s == nil {
		return Section{}, false
	}
	for _, sec := range s.Sections {
		if sec.Facet == f {
			return sec, true
		}
	}
	return Section{}, false
}
