package curate

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/entry"
	"github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/export"
	"github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/hubapi"
	"github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/model"
	"github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/stub"
)

// The daily pass ends with the ledger, the signals and the exits; without a hub every exit waits for its change and
// nothing fails. A dry run writes no exit.
func TestDailyEndsWithSignalsAndExits(t *testing.T) {
	down := stub.Scripted(func(model.Request) (string, error) { return "", model.ErrUnavailable })
	c, _ := newCurator(t, down, "shadow")
	c.Hub = hubapi.None{}
	c.St.S.Entries = map[string]*entry.Entry{"i-0000000001": {ID: "i-0000000001", Facet: entry.Interaction, Grain: entry.Class,
		Text: "汇报时先给结论再给细节（占位）", Status: entry.Stable, Origin: entry.FromOnboarding, Count: 2,
		Impact: entry.Impact{Moment: "report", Action: "先给结论"}}}
	if _, err := c.RunDaily(context.Background(), "2026-11-03", true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(c.St.Dir, export.ManifestPath)); err == nil {
		t.Fatal("a dry run wrote the exits")
	}
	run, err := c.RunDaily(context.Background(), "2026-11-03", false)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(c.St.Dir, export.InstructionsPath))
	if err != nil || !strings.Contains(string(b), "汇报时先给结论") {
		t.Fatalf("instructions: %v %q", err, b)
	}
	if c.St.S.Exits.Waiting["A"] != "H1" || c.St.S.Entries["i-0000000001"].Effective == "" || run.Counts["exit_files_written"] == 0 {
		t.Fatalf("exits %+v, counts %v", c.St.S.Exits, run.Counts)
	}
	if _, err := os.Stat(filepath.Join(c.St.Dir, "signals.json")); err != nil {
		t.Fatalf("signals did not run: %v", err)
	}
}
