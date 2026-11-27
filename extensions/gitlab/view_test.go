package main

import (
	"fmt"
	"strings"
	"testing"

	"github.com/nwverse/persona/sdk/go/ext"
	"github.com/nwverse/persona/sdk/go/wire"
)

func changelog(lines int) string {
	var b strings.Builder
	for i := 1; i <= lines; i++ {
		fmt.Fprintf(&b, "- change %d\n", i)
	}
	return b.String()
}

// A release merge request lists its changes line by line in the description. The view is refused above
// wire.ItemsMost components, and the description made one for each line.
func TestMergeRequestWithLongDescriptionFitsTheView(t *testing.T) {
	item := mergeRequest{IID: 7, Title: "Release 4.0", Description: changelog(400), WebURL: "https://gitlab.example.com/acme/app/-/merge_requests/7"}
	notes := []note{{ID: 1, Body: "Looks good", Author: user{Username: "ann"}}}
	v := detailView(&ext.UIRequest{UIParams: wire.UIParams{Locale: "en", UIVersion: 3}}, pageState{Project: "42", IID: 7, Mode: "detail"}, item, notes)
	if err := wire.CheckView(v); err != nil {
		t.Fatal(err)
	}
	var description []wire.Component
	for _, s := range v.Sections {
		if s.Title == textsFor("en").description {
			description = s.Items
		}
	}
	if len(description) == 0 || description[0].Text != "- change 1" {
		t.Fatalf("the description does not start at its first line: %d components", len(description))
	}
	if last := description[len(description)-1].Text; !strings.HasSuffix(last, "[…]") {
		t.Fatalf("the cut is not shown: %q", last)
	}
}

// The reviewers and the discussion keep their room beside a long description.
func TestMergeRequestWithLongDescriptionKeepsTheDiscussion(t *testing.T) {
	item := mergeRequest{IID: 7, Title: "Release 4.0", Description: changelog(400)}
	for i := 0; i < 5; i++ {
		item.Reviewers = append(item.Reviewers, user{Name: fmt.Sprintf("Reviewer %d", i), Username: fmt.Sprintf("r%d", i)})
	}
	notes := []note{{ID: 1, Body: "Looks good", Author: user{Username: "ann"}}}
	v := detailView(&ext.UIRequest{UIParams: wire.UIParams{Locale: "en", UIVersion: 3}}, pageState{Project: "42", IID: 7, Mode: "detail"}, item, notes)
	if err := wire.CheckView(v); err != nil {
		t.Fatal(err)
	}
	var comments []wire.Component
	for _, s := range v.Sections {
		if s.Title == textsFor("en").comments {
			comments = s.Items
		}
	}
	if len(comments) < 2 || comments[1].Text != "Looks good" {
		t.Fatalf("the discussion is lost: %+v", comments)
	}
}
