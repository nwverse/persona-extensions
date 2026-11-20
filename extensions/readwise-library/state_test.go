package main

import (
	"strings"
	"testing"

	"github.com/nwverse/persona/sdk/go/ext"
	"github.com/nwverse/persona/sdk/go/wire"
)

// The page state holds the reviewed draft's title (up to 511 characters), author (up to 1024 characters) and tag
// name (up to 127 characters), and the name of the book that was opened (a title, up to 511 characters). Each is
// counted in characters wherever it is limited, so Han characters, three bytes each, have to be accepted and survive
// the next action.
func TestWebFlowCreatesAHighlightWithTheLongestTitleAndAuthor(t *testing.T) {
	for name, text := range map[string]string{
		"han":   "字",
		"ascii": "a",
	} {
		f := newFakeLibrary(t)
		a := f.app()
		title, author := strings.Repeat(text, 511), strings.Repeat(text, 1024)
		review, err := a.action(t.Context(), &ext.UIRequest{UIParams: wire.UIParams{Locale: "zh-Hans", UIVersion: 6, Action: "review-create", State: ext.EncodeState(pageState{Mode: "create", BookID: 7, DraftID: "draft-1"}), Values: map[string]string{"text": "保留来源上下文。", "title": title, "author": author}}})
		if err != nil || review.View == nil || wire.CheckView(review.View) != nil {
			t.Fatalf("%s review: %v %+v", name, err, review)
		}
		confirm, err := a.action(t.Context(), &ext.UIRequest{UIParams: wire.UIParams{Locale: "zh-Hans", UIVersion: 6, Action: "confirm-create", State: review.View.State}})
		if err != nil || !confirm.Pop {
			t.Fatalf("%s confirm: %v %+v", name, err, confirm)
		}
		items, _ := f.creates[0]["highlights"].([]any)
		first, _ := items[0].(map[string]any)
		if len(f.creates) != 1 || len(items) != 1 || first["title"] != title || first["author"] != author {
			t.Fatalf("%s: the server received %v", name, f.creates)
		}
	}
}

func TestWebFlowAddsA127CharacterTag(t *testing.T) {
	for name, tag := range map[string]string{
		"han":   strings.Repeat("字", 127),
		"ascii": strings.Repeat("a", 127),
	} {
		f := newFakeLibrary(t)
		a := f.app()
		review, err := a.action(t.Context(), &ext.UIRequest{UIParams: wire.UIParams{Locale: "zh-Hans", UIVersion: 6, Action: "review-tag", State: ext.EncodeState(pageState{Mode: "add-tag", BookID: 7, DraftID: "draft-1"}), Values: map[string]string{"tag_name": tag}}})
		if err != nil || review.View == nil || wire.CheckView(review.View) != nil {
			t.Fatalf("%s review: %v %+v", name, err, review)
		}
		confirm, err := a.action(t.Context(), &ext.UIRequest{UIParams: wire.UIParams{Locale: "zh-Hans", UIVersion: 6, Action: "confirm-tag", State: review.View.State}})
		if err != nil || !confirm.Pop {
			t.Fatalf("%s confirm: %v %+v", name, err, confirm)
		}
		if len(f.tags) != 1 || f.tags[0]["name"] != tag {
			t.Fatalf("%s: the server received %v", name, f.tags)
		}
	}
}

func TestOpeningABookWithA511CharacterTitle(t *testing.T) {
	for name, title := range map[string]string{
		"han":   strings.Repeat("字", 511),
		"ascii": strings.Repeat("a", 511),
	} {
		f := newFakeLibrary(t)
		a := f.app()
		r := &ext.UIRequest{UIParams: wire.UIParams{Locale: "zh-Hans", UIVersion: 6, Action: "book", Values: map[string]string{"id": "7", "name": title}}}
		result, err := a.action(t.Context(), r)
		if err != nil || result.View == nil || wire.CheckView(result.View) != nil {
			t.Fatalf("%s book: %+v, %v", name, result, err)
		}
		r.Action, r.Values, r.State = "more", nil, result.View.State
		if result, err = a.action(t.Context(), r); err != nil || result.View == nil {
			t.Fatalf("%s more: %+v, %v", name, result, err)
		}
	}
}
