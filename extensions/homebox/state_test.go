package main

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/nwverse/persona/sdk/go/ext"
	"github.com/nwverse/persona/sdk/go/wire"
)

// The page state holds the search query (clipped to 200 characters) and the reviewed draft description (up to 1000
// characters). Both are counted in characters wherever they are limited, so Han characters, three bytes each, have
// to survive the next action as well. clipText keeps limit-1 characters and adds an ellipsis, so a longer query is
// held as exactly 200 characters.
func TestSearchQueryOf200CharactersLoadsMore(t *testing.T) {
	for name, query := range map[string]string{
		"han exact":   strings.Repeat("字", 200),
		"han clipped": strings.Repeat("字", 300),
		"ascii":       strings.Repeat("a", 200),
	} {
		f := newFakeHomebox(t)
		a := f.app()
		r := &ext.UIRequest{UIParams: wire.UIParams{Locale: "zh-Hans", UIVersion: 6, Action: "search", Values: map[string]string{"q": query}}}
		result, err := a.action(t.Context(), r)
		if err != nil || result.View == nil || result.View.Sections[0].More == nil {
			t.Fatalf("%s search: %+v, %v", name, result, err)
		}
		var st pageState
		if err := (&ext.UIRequest{UIParams: wire.UIParams{State: result.View.State}}).DecodeState(&st); err != nil || utf8.RuneCountInString(st.Query) > 200 {
			t.Fatalf("%s: the state holds %d characters: %v", name, utf8.RuneCountInString(st.Query), err)
		}
		r.Action, r.Values, r.State = "more", result.View.Sections[0].More.Action.Values, result.View.State
		if result, err = a.action(t.Context(), r); err != nil || result.View == nil {
			t.Fatalf("%s more: %+v, %v", name, result, err)
		}
	}
}

func TestWebFlowCreatesA1000CharacterDescription(t *testing.T) {
	for name, description := range map[string]string{
		"han":   strings.Repeat("字", 1000),
		"ascii": strings.Repeat("a", 1000),
	} {
		f := newFakeHomebox(t)
		a := f.app()
		review, err := a.action(t.Context(), &ext.UIRequest{UIParams: wire.UIParams{Locale: "zh-Hans", UIVersion: 6, Action: "review-create", State: ext.EncodeState(pageState{Mode: "create", EntityTypeID: typeID, LocationID: locationID, DraftID: "draft-1"}), Values: map[string]string{"name": "备用阅读器", "description": description, "quantity": "1"}}})
		if err != nil || review.View == nil || wire.CheckView(review.View) != nil {
			t.Fatalf("%s review: %v %+v", name, err, review)
		}
		confirm, err := a.action(t.Context(), &ext.UIRequest{UIParams: wire.UIParams{Locale: "zh-Hans", UIVersion: 6, Action: "confirm-create", State: review.View.State}})
		if err != nil || !confirm.Pop {
			t.Fatalf("%s confirm: %v %+v", name, err, confirm)
		}
		if len(f.creates) != 1 || f.creates[0]["description"] != description {
			t.Fatalf("%s: the server received %v", name, f.creates)
		}
	}
}
