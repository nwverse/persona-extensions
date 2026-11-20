package main

import (
	"fmt"
	"strings"
	"testing"

	"github.com/nwverse/persona/sdk/go/ext"
	"github.com/nwverse/persona/sdk/go/wire"
)

func bigPullRequest() pullRequest {
	var item pullRequest
	item.ID, item.Title = 7, "Busy pull request"
	return item
}

// A busy pull request has up to 100 activities, and the view is refused above wire.ItemsMost components.
func TestPullRequestWithManyCommentsFitsTheView(t *testing.T) {
	item := bigPullRequest()
	var activities []activity
	for n := 1; n <= 100; n++ {
		var a activity
		a.ID, a.Comment.Text, a.Comment.Author.DisplayName = int64(n), fmt.Sprintf("comment %d\nsecond line\nthird line", n), "Reviewer"
		activities = append(activities, a)
	}
	v := detailView(&ext.UIRequest{UIParams: wire.UIParams{Locale: "en", UIVersion: 3}}, pageState{}, item, activities)
	if err := wire.CheckView(v); err != nil {
		t.Fatal(err)
	}
	comments := v.Sections[len(v.Sections)-1].Items
	if len(comments) < 100 || !strings.Contains(comments[1].Text, "comment 1") {
		t.Fatalf("the comments are not kept from the first: %d", len(comments))
	}
	if last := comments[len(comments)-1].Text; !strings.HasSuffix(last, "[…]") {
		t.Fatalf("the cut is not shown: %q", last)
	}
}

// A description of hundreds of lines leaves room for the comments and fits too.
func TestPullRequestWithLongDescriptionFitsTheView(t *testing.T) {
	item := bigPullRequest()
	item.Description = strings.Repeat("- change\n", 500)
	var a activity
	a.Comment.Text, a.Comment.Author.DisplayName = "a comment", "Reviewer"
	v := detailView(&ext.UIRequest{UIParams: wire.UIParams{Locale: "en", UIVersion: 3}}, pageState{}, item, []activity{a})
	if err := wire.CheckView(v); err != nil {
		t.Fatal(err)
	}
}
