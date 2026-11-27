package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/curate"
	"github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/store"
)

// hardevalMergeRules runs the rule merge (0.2.6) on a copy of the state, as the next daily pass would, and writes the
// merged copy: for measuring the standing set after it, offline.
func hardevalMergeRules(args []string) error {
	fs := flag.NewFlagSet("hardeval merge-rules", flag.ContinueOnError)
	statePath := fs.String("state", "", "a copy of state.json (read only)")
	out := fs.String("out", "", "a new directory for the merged copy (state.json)")
	cmd := fs.String("model-cmd", "claude", "the model command line")
	mdl := fs.String("model", "claude-sonnet-5-5", "the model that merges")
	cache := fs.String("cache", "", "the model answers' cache directory")
	before := fs.String("before", "", "merge only the rules first seen before this day (the evaluation's cutoff)")
	today := fs.String("today", "", "the day the copy is settled as of (YYYY-MM-DD; default the state's last day)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *statePath == "" || *out == "" {
		return errors.New("-state and -out are required")
	}
	raw, err := os.ReadFile(*statePath)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(*out, 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(*out, "state.json"), raw, 0o600); err != nil {
		return err
	}
	st, err := store.Open(*out)
	if err != nil {
		return err
	}
	now := time.Now()
	if *today != "" {
		if now, err = time.Parse("2006-01-02", *today); err != nil {
			return err
		}
		now = now.Add(12 * time.Hour)
	}
	c := &curate.Curator{St: st, Cfg: curate.Defaults(), Loc: time.UTC, Now: func() time.Time { return now }}
	run, err := c.MergeRules(context.Background(), heCLI(*cmd, *mdl, *cache), *before)
	if err != nil {
		return err
	}
	if err := st.Save(); err != nil {
		return err
	}
	fmt.Printf("hardeval merge-rules: %v\n", run.Counts)
	for _, n := range run.Notes {
		fmt.Println("  " + n)
	}
	return nil
}
