package main

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/nwverse/persona/sdk/go/ext"
	"github.com/nwverse/persona/sdk/go/wire"
)

// The page state holds the search query (clipped to 200 characters) and the name of the book that was opened (up to
// 255 characters, as BookStack names are). Both are counted in characters wherever they are limited, so Han
// characters, three bytes each, have to be accepted and survive the next action. clipText keeps limit-1 characters
// and adds an ellipsis, so a longer query is held as exactly 200 characters.
func TestSearchQueryOf200CharactersLoadsMore(t *testing.T) {
	for name, query := range map[string]string{
		"han exact":   strings.Repeat("字", 200),
		"han clipped": strings.Repeat("字", 300),
		"ascii":       strings.Repeat("a", 200),
	} {
		f := newMoreBookStack(t, false)
		a := f.app()
		r := &ext.UIRequest{UIParams: wire.UIParams{Locale: "zh-Hans", UIVersion: 6, Action: "search", Values: map[string]string{"q": query}}}
		result, err := a.action(t.Context(), r)
		if err != nil || result.View == nil {
			t.Fatalf("%s search: %+v, %v", name, result, err)
		}
		var st pageState
		if err := (&ext.UIRequest{UIParams: wire.UIParams{State: result.View.State}}).DecodeState(&st); err != nil || utf8.RuneCountInString(st.Search) > 200 {
			t.Fatalf("%s: the state holds %d characters: %v", name, utf8.RuneCountInString(st.Search), err)
		}
		r.Action, r.Values, r.State = "more-search", nil, result.View.State
		if result, err = a.action(t.Context(), r); err != nil || result.View == nil {
			t.Fatalf("%s more-search: %+v, %v", name, result, err)
		}
	}
}

func TestOpeningABookWithA255CharacterName(t *testing.T) {
	for name, bookName := range map[string]string{
		"han":   strings.Repeat("字", 255),
		"ascii": strings.Repeat("a", 255),
	} {
		f := newMoreBookStack(t, false)
		a := f.app()
		r := &ext.UIRequest{UIParams: wire.UIParams{Locale: "zh-Hans", UIVersion: 6, Action: "book", Values: map[string]string{"id": "7", "name": bookName}}}
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
