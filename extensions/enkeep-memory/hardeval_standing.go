package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/entry"
	"github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/memsrc"
	"github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/overview"
)

// hardevalStanding prints the standing set memory.entries answers for a copy of the state and the overview, and why
// each overview pattern is in it or not (its score against the set's lowest, or the template filter): a diagnosis,
// read only.
func hardevalStanding(args []string) error {
	fs := flag.NewFlagSet("hardeval standing", flag.ContinueOnError)
	statePath := fs.String("state", "", "a copy of state.json (read only)")
	ovPath := fs.String("overview", "", "a copy of overview-state.json (read only)")
	room := fs.Int("room", 4096, "the standing room in bytes (resident_bytes)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *statePath == "" {
		return errors.New("-state is required")
	}
	st, err := readState(*statePath)
	if err != nil {
		return err
	}
	var extras []memsrc.Extra
	if *ovPath != "" {
		b, err := os.ReadFile(*ovPath)
		if err != nil {
			return err
		}
		ov := &overview.State{}
		if err := json.Unmarshal(b, ov); err != nil {
			return err
		}
		extras = overview.Exits(ov)
	}
	var standing []memsrc.Entry
	after := ""
	for {
		r, _, err := memsrc.PageWith(st, nil, extras, memsrc.Params{Contribution: "memory", Limit: memsrc.MaxLimit, After: after, ResidentBytes: *room})
		if err != nil {
			return err
		}
		for _, e := range r.Entries {
			if e.Standing {
				standing = append(standing, e)
			}
		}
		if !r.HasMore || len(r.Entries) == 0 || !r.Entries[len(r.Entries)-1].Standing {
			break
		}
		after = r.After
	}
	in := map[string]bool{}
	low := 101
	bytes := 0
	fmt.Printf("standing set, room %d bytes:\n", *room)
	for _, e := range standing {
		in[e.ID] = true
		sc := -1
		if x := st.Entries[e.ID]; x != nil {
			sc = entry.Score(x)
		} else {
			for _, x := range extras {
				if x.Entry.ID == e.ID {
					sc = x.Score
				}
			}
		}
		low = min(low, sc)
		bytes += len(e.Text) + 3
		fmt.Printf("  %4d  score %3d  %-28s %s\n", e.Rank, sc, e.ID, clipRunes(strings.ReplaceAll(e.Text, "\n", " "), 60))
	}
	fmt.Printf("  %d lines, %d bytes, lowest score %d\n\npatterns:\n", len(standing), bytes, low)
	tpl := entry.TemplateQuotes(st.Entries)
	sort.Slice(extras, func(i, j int) bool { return extras[i].Score > extras[j].Score })
	for _, x := range extras {
		why := "in"
		switch {
		case in[x.Entry.ID]:
		case x.Kind == "":
			why = "not standing by kind (a category or space pattern, or a facet with no side)"
		case entry.TemplateBacked(st.Entries, x.Evidence, tpl):
			why = "template-backed"
		case x.Score < low:
			why = "score below the set's lowest"
		default:
			why = "alike to a chosen line, or no room"
		}
		fmt.Printf("  score %3d  %-10s %-40s %s | %s\n", x.Score, x.Kind, x.Entry.ID, why, clipRunes(strings.ReplaceAll(x.Entry.Text, "\n", " "), 50))
	}
	return nil
}
