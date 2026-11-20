package main

import (
	"fmt"
	"strings"
	"testing"

	"github.com/nwverse/persona/sdk/go/ext"
	"github.com/nwverse/persona/sdk/go/wire"
)

func bigStory() story {
	return story{ID: 42, Name: "Busy story", StoryType: "feature"}
}

// A busy story has hundreds of comments, and the view is refused above wire.ItemsMost components.
func TestStoryWithManyCommentsFitsTheView(t *testing.T) {
	item := bigStory()
	for n := 1; n <= 400; n++ {
		item.Comments = append(item.Comments, comment{ID: int64(n), Text: fmt.Sprintf("comment %d", n)})
	}
	v := detailView(&ext.UIRequest{UIParams: wire.UIParams{Locale: "en", UIVersion: 3}}, pageState{StoryID: 42, Mode: "detail"}, item, nil)
	if err := wire.CheckView(v); err != nil {
		t.Fatal(err)
	}
	comments := v.Sections[len(v.Sections)-1].Items
	if len(comments) < 100 || comments[0].Text != "comment 1" {
		t.Fatalf("the comments are not kept from the first: %d", len(comments))
	}
	if last := comments[len(comments)-1].Text; !strings.HasSuffix(last, "[…]") {
		t.Fatalf("the cut is not shown: %q", last)
	}
}

// A description of hundreds of lines leaves room for the comments and fits too.
func TestStoryWithLongDescriptionFitsTheView(t *testing.T) {
	item := bigStory()
	item.Description = strings.Repeat("- [ ] step\n", 500)
	item.Comments = []comment{{ID: 1, Text: "a comment"}}
	v := detailView(&ext.UIRequest{UIParams: wire.UIParams{Locale: "en", UIVersion: 3}}, pageState{StoryID: 42, Mode: "detail"}, item, nil)
	if err := wire.CheckView(v); err != nil {
		t.Fatal(err)
	}
}
