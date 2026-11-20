package main

import (
	"fmt"
	"strings"
	"testing"

	"github.com/nwverse/persona/sdk/go/ext"
	"github.com/nwverse/persona/sdk/go/wire"
)

// A popular story has hundreds of comments, and the view is refused above wire.ItemsMost components.
func TestStoryWithManyCommentsFitsTheView(t *testing.T) {
	item := story{ShortID: "story1", Title: "Busy story", DescriptionPlain: "A description", Submitter: "author", CommentsURL: "https://lobste.rs/s/story1/busy"}
	for n := 1; n <= 400; n++ {
		item.Comments = append(item.Comments, comment{ShortID: fmt.Sprint("c", n), User: "reader", Score: 1, Text: fmt.Sprintf("comment %d\nsecond paragraph", n)})
	}
	v := detailView(&ext.UIRequest{UIParams: wire.UIParams{Locale: "en", UIVersion: 3}}, pageState{Mode: "detail", StoryID: "story1"}, item)
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
func TestStoryWithLongDescriptionFitsTheView(t *testing.T) {
	item := story{ShortID: "story1", Title: "Long story", DescriptionPlain: strings.Repeat("line\n", 500), Submitter: "author"}
	item.Comments = []comment{{ShortID: "c1", User: "reader", Text: "a comment"}}
	v := detailView(&ext.UIRequest{UIParams: wire.UIParams{Locale: "en", UIVersion: 3}}, pageState{Mode: "detail", StoryID: "story1"}, item)
	if err := wire.CheckView(v); err != nil {
		t.Fatal(err)
	}
}
