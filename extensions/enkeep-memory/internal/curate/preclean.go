package curate

import (
	"maps"
	"strings"

	"github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/entry"
	"github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/norm"
	"github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/relay"
	srcs "github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/sources"
	"github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/store"
)

// PrecleanStats reports what deterministic pre-model cleaning removed before triage and L0 extraction.
type PrecleanStats struct {
	LinesBefore      int
	LinesAfter       int
	BytesBefore      int
	BytesAfter       int
	BoilerplateLines int
	TemplateLines    int
	S3UnitsDropped   int
}

// PrecleanUnits deterministically strips Persona platform boilerplate and multi-session repeated S3 dispatch
// templates from units before they reach triage or L0 extraction models (unified design §1.3, §5.1 block 2).
// Historical S3 templates from st.Entries (when st is non-nil) and same-day S3 verbatim repeats (>=3 sessions)
// are both stripped; exit-side TemplateOnly filtering remains as a secondary backstop.
func PrecleanUnits(units []Unit, st *store.State) ([]Unit, PrecleanStats) {
	var stats PrecleanStats
	tpl := map[string]bool{}
	if st != nil && len(st.Entries) > 0 {
		maps.Copy(tpl, entry.TemplateQuotes(st.Entries))
	}
	s3Batch := map[string][]string{}
	for _, u := range units {
		if u.Entity != srcs.KindAgents {
			continue
		}
		for _, l := range u.Lines {
			if l.Speaker == srcs.SpeakerPersona || l.Speaker == norm.Me || l.Speaker == srcs.SpeakerTest {
				body := strings.TrimPrefix(l.Body, relay.RelayedMark)
				if clean := relay.StripBoilerplate(body); clean != "" {
					s3Batch[u.Ref] = append(s3Batch[u.Ref], clean)
				}
			}
		}
	}
	maps.Copy(tpl, entry.BatchTemplateLines(s3Batch))

	out := make([]Unit, 0, len(units))
	for _, u := range units {
		isS3 := u.Entity == srcs.KindAgents
		kept := make([]norm.Line, 0, len(u.Lines))
		for _, l := range u.Lines {
			stats.LinesBefore++
			stats.BytesBefore += len(l.Body)

			relayed := strings.HasPrefix(l.Body, relay.RelayedMark)
			rawBody := strings.TrimPrefix(l.Body, relay.RelayedMark)
			clean := relay.StripBoilerplate(rawBody)
			if clean == "" {
				stats.BoilerplateLines++
				continue
			}
			if isS3 && (l.Speaker == srcs.SpeakerPersona || l.Speaker == norm.Me || l.Speaker == srcs.SpeakerTest) {
				if entry.IsTemplateLine(clean, tpl) {
					stats.TemplateLines++
					continue
				}
			}
			if relayed {
				l.Body = relay.RelayedMark + clean
			} else {
				l.Body = clean
			}
			kept = append(kept, l)
			stats.LinesAfter++
			stats.BytesAfter += len(l.Body)
		}
		if len(kept) == 0 && isS3 {
			stats.S3UnitsDropped++
			continue
		}
		u.Lines = kept
		out = append(out, u)
	}
	return out, stats
}
