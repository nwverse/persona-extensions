package main

import (
	"testing"

	"github.com/nwverse/persona/sdk/go/ext"
	"github.com/nwverse/persona/sdk/go/wire"
)

// A task in My Tasks has no project, and one without a due date has nothing to summarize: the summary line is
// empty, and a text component needs a text.
func TestTaskWithoutDueDateOrProjectOpens(t *testing.T) {
	item := task{GID: "1201", Name: "Call the plumber", Notes: "Ask about the boiler", PermalinkURL: "https://app.asana.com/0/1/1201"}
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
	if len(description) == 0 || description[0].Text != "Ask about the boiler" {
		t.Fatalf("the notes are lost: %+v", description)
	}
}
