package main

import (
	"fmt"
	"strings"
	"testing"

	"github.com/nwverse/persona/sdk/go/ext"
	"github.com/nwverse/persona/sdk/go/wire"
)

// A task's notes are written line by line (a checklist, a pasted log). The view is refused above wire.ItemsMost
// components, and the notes made one for each line.
func TestTaskWithLongNotesFitsTheView(t *testing.T) {
	var notes strings.Builder
	for i := 1; i <= 400; i++ {
		fmt.Fprintf(&notes, "- step %d\n", i)
	}
	item := task{GID: "1201", Name: "Launch checklist", Notes: notes.String(), DueOn: "2026-12-01", PermalinkURL: "https://app.asana.com/0/1/1201"}
	v := detailView(&ext.UIRequest{UIParams: wire.UIParams{Locale: "en", UIVersion: 3}}, pageState{Mode: "detail", TaskGID: "1201"}, item)
	if err := wire.CheckView(v); err != nil {
		t.Fatal(err)
	}
	var description []wire.Component
	for _, s := range v.Sections {
		if s.Title == textsFor("en").description {
			description = s.Items
		}
	}
	if len(description) == 0 || description[0].Text != "- step 1" {
		t.Fatalf("the notes do not start at their first line: %d components", len(description))
	}
	if last := description[len(description)-1].Text; !strings.HasSuffix(last, "[…]") {
		t.Fatalf("the cut is not shown: %q", last)
	}
}
