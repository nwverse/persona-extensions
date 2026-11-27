package main

// Offline exit compile and look-up on a copy of the state (0.2.0-beta.37): what the per-turn block, the skills and the
// project blocks would hold, and what enkeep_recall answers, without a hub or a model. Lines already rewritten are
// reused; an entry without one goes out with its own text when it passes the check. Nothing is written to a hub.

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/entry"
	exits "github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/export"
	"github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/recall"
	"github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/store"
)

func readState(path string) (*store.State, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	st := &store.State{}
	return st, json.Unmarshal(raw, st)
}

// exitsCmd: enkeep-memory exits -state <state.json> -out <dir> [-projects <json: [{"id","name","aliases"}]>] [-lang zh]
func exitsCmd(args []string) error {
	fs := flag.NewFlagSet("exits", flag.ContinueOnError)
	statePath := fs.String("state", "", "a copy of state.json (read only)")
	out := fs.String("out", "", "an empty directory for the exit files")
	projects := fs.String("projects", "", "the knowledge base's projects as JSON [{\"id\",\"name\",\"aliases\"}]; empty: the list kept in the state")
	lang := fs.String("lang", "zh-Hans", "language of the fixed wording")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *statePath == "" || *out == "" {
		return fmt.Errorf("-state and -out are required")
	}
	st, err := readState(*statePath)
	if err != nil {
		return err
	}
	var ps []entry.KBProject
	if *projects != "" {
		raw, err := os.ReadFile(*projects)
		if err != nil {
			return err
		}
		if err := json.Unmarshal(raw, &ps); err != nil {
			return err
		}
	} else if st.Exits != nil {
		ps = st.Exits.Projects
	}
	st.Exits = nil // a fresh manifest in out: every file is written
	if err := os.MkdirAll(*out, 0o700); err != nil {
		return err
	}
	rep, err := exits.Compile(context.Background(), st, exits.Options{Dir: *out, Lang: *lang, Loc: time.Local, Projects: ps})
	if err != nil {
		return err
	}
	paths := make([]string, 0, len(rep.Files))
	for p := range rep.Files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		f := rep.Files[p]
		fmt.Printf("%s\t%s\t%d bytes\t%d entries\n", f.Exit, p, f.Bytes, f.Entries)
	}
	var secs []string
	for _, s := range exits.SectionOrder {
		secs = append(secs, fmt.Sprintf("%s %dB/%d lines", s, rep.Sections[s], rep.SecLines[s]))
	}
	fmt.Println("exit A sections: " + strings.Join(secs, "; "))
	skills, _ := filepath.Glob(filepath.Join(*out, exits.SkillsDir, "*"))
	fmt.Printf("skills: %d\n", len(skills))
	return nil
}

// recallCmd: enkeep-memory recall -state <state.json> -q <query> [-facets a,b] [-limit n]
func recallCmd(args []string) error {
	fs := flag.NewFlagSet("recall", flag.ContinueOnError)
	statePath := fs.String("state", "", "a copy of state.json (read only)")
	q := fs.String("q", "", "the query")
	facets := fs.String("facets", "", "comma-separated facets")
	limit := fs.Int("limit", recall.DefaultLimit, "most lines")
	if err := fs.Parse(args); err != nil {
		return err
	}
	st, err := readState(*statePath)
	if err != nil {
		return err
	}
	query := recall.Query{Text: *q, Limit: *limit}
	for _, f := range strings.Split(*facets, ",") {
		if strings.TrimSpace(f) == "" {
			continue
		}
		x, ok := recall.FacetOf(f)
		if !ok {
			return fmt.Errorf("unknown facet %q", f)
		}
		query.Facets = append(query.Facets, x)
	}
	res := recall.Search(st, query)
	fmt.Printf("%d hit(s) of %d entries (%d entity skeleton(s))\n", len(res.Hits), res.Pool, len(res.Entities))
	for _, ec := range res.Entities {
		fmt.Print(recall.FormatEntity(ec))
	}
	for _, h := range res.Hits {
		fmt.Print(recall.Format(h))
	}
	return nil
}
