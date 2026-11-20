package curate

import (
	"strings"
	"testing"

	"github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/store"
	"github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/stub"
)

// The status names the days whose sessions the hub's history could not list in full: which machine, which source,
// what reason; and the days of a hub without history in one line.
func TestRerunStatusNamesHistoryGaps(t *testing.T) {
	c, _ := newCurator(t, &stub.Model{}, "shadow")
	c.St.S.SessionHistory = map[string]*store.HistoryRead{
		"2026-11-01": {Why: "the hub's answer has no history"},
		"2026-11-02": {Why: "the hub's answer has no history"},
		"2026-11-03": {Offered: true, Gaps: []store.HistoryGap{
			{Machine: "m1", Source: "runtime", Runtime: "rt-a", Reason: "unsupported"},
			{Machine: "m2", Reason: "offline"},
			{Machine: "m1", Source: "claude", Honored: true},
		}},
	}
	lines := c.RerunStatus().History
	if len(lines) != 2 {
		t.Fatalf("lines %q", lines)
	}
	if !strings.Contains(lines[0], "2 day(s)") || !strings.Contains(lines[0], "2026-11-01, 2026-11-02") || !strings.Contains(lines[0], "no history") {
		t.Errorf("no-history line %q", lines[0])
	}
	for _, want := range []string{"2026-11-03", "machine m1, source runtime rt-a: not honored (unsupported)", "machine m2, source ?: not honored (offline)",
		"machine m1, source claude: not complete"} {
		if !strings.Contains(lines[1], want) {
			t.Errorf("gap line lacks %q: %q", want, lines[1])
		}
	}
}
