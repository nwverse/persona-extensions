package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/nwverse/persona/sdk/go/ext"
	"github.com/nwverse/persona/sdk/go/wire"
)

func bigIssue() issue {
	var item issue
	item.Key = "ENG-1"
	item.Fields.Summary = "Busy issue"
	item.Fields.Project.Key = "ENG"
	return item
}

// A busy issue has hundreds of comments, and the view is refused above wire.ItemsMost components.
func TestIssueWithManyCommentsFitsTheView(t *testing.T) {
	item := bigIssue()
	for n := 1; n <= 400; n++ {
		body, _ := json.Marshal(fmt.Sprintf("comment %d", n))
		item.Fields.Comment.Comments = append(item.Fields.Comment.Comments, comment{ID: fmt.Sprint(n), Body: body, Author: user{DisplayName: "Reviewer"}, Created: "2026-11-09T11:00:00Z"})
	}
	v := detailView(&ext.UIRequest{UIParams: wire.UIParams{Locale: "en", UIVersion: 3}}, pageState{Mode: "detail", IssueKey: "ENG-1"}, item)
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
func TestIssueWithLongDescriptionFitsTheView(t *testing.T) {
	item := bigIssue()
	item.Fields.Description, _ = json.Marshal(strings.Repeat("line\n", 500))
	body, _ := json.Marshal("a comment")
	item.Fields.Comment.Comments = []comment{{ID: "1", Body: body, Author: user{DisplayName: "Reviewer"}}}
	v := detailView(&ext.UIRequest{UIParams: wire.UIParams{Locale: "en", UIVersion: 3}}, pageState{Mode: "detail", IssueKey: "ENG-1"}, item)
	if err := wire.CheckView(v); err != nil {
		t.Fatal(err)
	}
}
