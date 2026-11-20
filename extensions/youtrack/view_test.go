package main

import (
	"fmt"
	"strings"
	"testing"

	"github.com/nwverse/persona/sdk/go/ext"
	"github.com/nwverse/persona/sdk/go/wire"
)

func issueView(t *testing.T, item issue, comments []comment) *wire.View {
	t.Helper()
	v := detailView(&ext.UIRequest{UIParams: wire.UIParams{Locale: "en", UIVersion: 3}}, pageState{Mode: "detail", IssueID: "2-1"}, item, comments)
	if err := wire.CheckView(v); err != nil {
		t.Fatal(err)
	}
	return v
}

func commentsOf(v *wire.View) []wire.Component {
	for _, s := range v.Sections {
		if s.Title == textsFor("en").comments {
			return s.Items
		}
	}
	return nil
}

func discussion(n int, lines string) []comment {
	var out []comment
	for i := 1; i <= n; i++ {
		out = append(out, comment{ID: fmt.Sprint(i), Text: fmt.Sprintf("comment %d", i) + lines, Author: &user{Name: "Ann"}})
	}
	return out
}

// An issue that has run for a while has a hundred comments (the most the extension reads), each one a header and a
// component per line. The view is refused above wire.ItemsMost components.
func TestIssueWithManyCommentsFitsTheView(t *testing.T) {
	v := issueView(t, issue{IDReadable: "APP-7", Summary: "Crash on start", Description: "It crashes."}, discussion(100, "\nsteps to reproduce"))
	items := commentsOf(v)
	if len(items) < 100 || items[1].Text != "comment 1" {
		t.Fatalf("the comments are not kept from the first: %d components", len(items))
	}
	if last := items[len(items)-1].Text; !strings.HasSuffix(last, "[…]") {
		t.Fatalf("the cut is not shown: %q", last)
	}
}

// The description (up to 240 components) and the comments together are what the view is refused for: a long
// description leaves the comments their room.
func TestIssueWithLongDescriptionFitsTheView(t *testing.T) {
	var body strings.Builder
	for i := 1; i <= 400; i++ {
		fmt.Fprintf(&body, "- step %d\n", i)
	}
	v := issueView(t, issue{IDReadable: "APP-7", Summary: "Release", Description: body.String()}, discussion(30, ""))
	if items := commentsOf(v); len(items) < 40 || items[1].Text != "comment 1" {
		t.Fatalf("the comments are lost: %d components", len(items))
	}
}
