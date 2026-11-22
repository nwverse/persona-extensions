package curate

import (
	"context"
	"testing"
	"time"

	"github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/entry"
	"github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/hubapi"
	"github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/norm"
	"github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/relay"
	srcs "github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/sources"
	"github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/speaker"
)

// fakeLedger is a hub with the ledger (design v2 I1) and nothing else.
type fakeLedger struct {
	hubapi.None
	es []hubapi.LedgerEntry
}

func (f fakeLedger) Ledger(context.Context, hubapi.LedgerQuery) ([]hubapi.LedgerEntry, error) {
	return f.es, nil
}

// A relayed claim is the user's line only when the day's own lines (or the ledger) bear it out; the rest is dropped.
func TestVerifyRelays(t *testing.T) {
	units := []Unit{
		{Ref: "chats/assistant", Lines: []norm.Line{{N: 1, Speaker: norm.Me, Body: "以后汇报先写结论，再写过程"}, {N: 2, Speaker: norm.Me, Body: "默认开"}}},
		{Ref: "agents/m/s1", Lines: []norm.Line{
			{N: 1, Speaker: srcs.SpeakerPersona, Body: "## 用户原话 …"},
			{N: 2, Speaker: srcs.SpeakerRelayCand, Body: "以后汇报先写结论"}, // part of a line of the user's: kept
			{N: 3, Speaker: srcs.SpeakerRelayCand, Body: "默认开"},      // short, a whole line of the user's: kept
			{N: 4, Speaker: srcs.SpeakerRelayCand, Body: "开"},        // short, only part of a line: dropped
			{N: 5, Speaker: srcs.SpeakerRelayCand, Body: "直接全补占位月份"}, // nowhere in the user's words: dropped
			{N: 6, Speaker: srcs.SpeakerRelayCand, Body: "按账本里的原话办"}, // in the ledger by the user: kept
		}},
	}
	c := &Curator{Hub: fakeLedger{es: []hubapi.LedgerEntry{{ID: "l1", By: "user", Quote: "按账本里的原话办", At: time.Now()}}}}
	c.verifyRelays(context.Background(), units)
	got := units[1].Lines
	if len(got) != 4 {
		t.Fatalf("lines %+v", got)
	}
	for _, l := range got[1:] {
		if l.Speaker != norm.Me || relay.Three(l.Speaker, l.Body, norm.Me) != relay.WhoRelayed {
			t.Fatalf("not relayed: %+v", l)
		}
		if units[1].Who[l.N] != entry.WhoSelf || units[1].WhoWhy[l.N] != speaker.WhyRelayed {
			t.Fatalf("who of %d: %q %q", l.N, units[1].Who[l.N], units[1].WhoWhy[l.N])
		}
	}
	if relay.Three(got[0].Speaker, got[0].Body, norm.Me) != relay.WhoAgent {
		t.Fatal("the relay itself is not the user's")
	}
	// Without the ledger (a hub that has none) the ledger's claim is dropped, without an error.
	units[1].Lines = []norm.Line{{N: 6, Speaker: srcs.SpeakerRelayCand, Body: "按账本里的原话办"}}
	(&Curator{Hub: hubapi.None{}}).verifyRelays(context.Background(), units)
	if len(units[1].Lines) != 0 {
		t.Fatalf("kept without evidence: %+v", units[1].Lines)
	}
}

// A ledger entry a later one supersedes, a probe and an automation entry are left out.
func TestLedgerClean(t *testing.T) {
	es := hubapi.LedgerCleanForTest([]hubapi.LedgerEntry{{ID: "a", By: "user", Quote: "旧的"}, {ID: "b", By: "user", Quote: "新的", Supersedes: "a"},
		{ID: "p", By: "user", Kind: "probe"}, {ID: "x", By: "user", Origin: "automation"}})
	if len(es) != 2 || !es[0].Superseded() || es[1].Superseded() {
		t.Fatalf("%+v", es)
	}
}
