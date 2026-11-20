package main

import (
	"fmt"
	"strings"
	"testing"

	"github.com/nwverse/persona/sdk/go/ext"
	"github.com/nwverse/persona/sdk/go/wire"
)

func issueView(t *testing.T, item issue) *wire.View {
	t.Helper()
	v := detailView(&ext.UIRequest{UIParams: wire.UIParams{Locale: "en", UIVersion: 3}}, pageState{Mode: "detail", IssueID: 7}, item)
	if err := wire.CheckView(v); err != nil {
		t.Fatal(err)
	}
	return v
}

func journalsOf(v *wire.View) []wire.Component {
	for _, s := range v.Sections {
		if s.Title == textsFor("en").journals {
			return s.Items
		}
	}
	return nil
}

func notes(n int, lines string) []journal {
	var out []journal
	for i := 1; i <= n; i++ {
		out = append(out, journal{ID: int64(i), Notes: fmt.Sprintf("note %d", i) + lines, User: namedResource{Name: "Ann"}, CreatedOn: "2026-11-01T10:00:00Z"})
	}
	return out
}

// Redmine answers every journal of an issue at once, and each note is a header and a component per line. The view is
// refused above wire.ItemsMost components.
func TestIssueWithManyNotesFitsTheView(t *testing.T) {
	v := issueView(t, issue{ID: 7, Subject: "Crash on start", Description: "It crashes.", Journals: notes(120, "\nsteps to reproduce")})
	items := journalsOf(v)
	if len(items) < 100 || items[1].Text != "note 1" {
		t.Fatalf("the notes are not kept from the first: %d components", len(items))
	}
	if last := items[len(items)-1].Text; !strings.HasSuffix(last, "[…]") {
		t.Fatalf("the cut is not shown: %q", last)
	}
}

// The description (up to 240 components) and the notes together are what the view is refused for: a long description
// leaves the notes their room.
func TestIssueWithLongDescriptionFitsTheView(t *testing.T) {
	var body strings.Builder
	for i := 1; i <= 400; i++ {
		fmt.Fprintf(&body, "- step %d\n", i)
	}
	v := issueView(t, issue{ID: 7, Subject: "Release", Description: body.String(), Journals: notes(30, "")})
	if items := journalsOf(v); len(items) < 40 || items[1].Text != "note 1" {
		t.Fatalf("the notes are lost: %d components", len(items))
	}
}
