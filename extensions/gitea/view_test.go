package main

import (
	"fmt"
	"strings"
	"testing"

	"github.com/nwverse/persona/sdk/go/ext"
	"github.com/nwverse/persona/sdk/go/wire"
)

func detail(t *testing.T, item pullRequest, comments []comment) *wire.View {
	t.Helper()
	item.Number, item.HTMLURL = 7, "https://gitea.example.com/acme/app/pulls/7"
	v := detailView(&ext.UIRequest{UIParams: wire.UIParams{Locale: "en", UIVersion: 3}}, pageState{Owner: "acme", Repo: "app", PRID: 7, Mode: "detail"}, item, comments)
	if err := wire.CheckView(v); err != nil {
		t.Fatal(err)
	}
	return v
}

func section(v *wire.View, title string) []wire.Component {
	for _, s := range v.Sections {
		if s.Title == title {
			return s.Items
		}
	}
	return nil
}

// Gitea answers every comment of a pull request at once, and each one is two components or more. The view is refused
// above wire.ItemsMost components.
func TestPullRequestWithManyCommentsFitsTheView(t *testing.T) {
	var comments []comment
	for i := 1; i <= 160; i++ {
		comments = append(comments, comment{ID: int64(i), Body: fmt.Sprintf("comment %d", i), User: user{Login: "ann", FullName: "Ann"}})
	}
	v := detail(t, pullRequest{Title: "Add retries", Body: "Retry the upload."}, comments)
	items := section(v, textsFor("en").comments)
	if len(items) < 100 || items[1].Text != "comment 1" {
		t.Fatalf("the comments are not kept from the first: %d components", len(items))
	}
	if last := items[len(items)-1].Text; !strings.HasSuffix(last, "[…]") {
		t.Fatalf("the cut is not shown: %q", last)
	}
}

// The description (up to 240 components) and the comments together are what the view is refused for: a long
// description leaves the comments their room.
func TestPullRequestWithLongDescriptionFitsTheView(t *testing.T) {
	var body strings.Builder
	for i := 1; i <= 400; i++ {
		fmt.Fprintf(&body, "- change %d\n", i)
	}
	var comments []comment
	for i := 1; i <= 30; i++ {
		comments = append(comments, comment{ID: int64(i), Body: fmt.Sprintf("comment %d", i), User: user{Login: "ann", FullName: "Ann"}})
	}
	v := detail(t, pullRequest{Title: "Release", Body: body.String()}, comments)
	if items := section(v, textsFor("en").comments); len(items) < 40 || items[1].Text != "comment 1" {
		t.Fatalf("the comments are lost: %d components", len(items))
	}
}
