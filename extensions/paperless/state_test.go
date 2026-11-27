package main

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/nwverse/persona/sdk/go/ext"
	"github.com/nwverse/persona/sdk/go/wire"
)

// The page state holds the search query (clipped to 500 characters). Every entry check counts it in characters, so
// Han characters, three bytes each, have to survive Load more as well. clipText keeps limit-1 characters and adds an
// ellipsis, so a longer query is held as at most 500 characters.
func TestSearchQueryOf500CharactersLoadsMore(t *testing.T) {
	f := newMorePaperless(t, 26)

	for _, tc := range []struct {
		name  string
		query string
	}{
		{"han exact", strings.Repeat("字", 500)},
		{"han clipped", strings.Repeat("字", 600)},
		{"ascii", strings.Repeat("a", 500)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := f.app()
			r := &ext.UIRequest{UIParams: wire.UIParams{Locale: "zh-Hans", UIVersion: 6, Action: "search", Values: map[string]string{"q": tc.query}}}
			result, err := a.action(t.Context(), r)
			if err != nil || result.View == nil || result.View.Sections[0].More == nil || wire.CheckView(result.View) != nil {
				t.Fatalf("search: %+v, %v", result, err)
			}
			var st pageState
			if err := (&ext.UIRequest{UIParams: wire.UIParams{State: result.View.State}}).DecodeState(&st); err != nil || utf8.RuneCountInString(st.Query) > 500 {
				t.Fatalf("the state holds %d characters: %v", utf8.RuneCountInString(st.Query), err)
			}
			r.Action, r.Values, r.State = "more", result.View.Sections[0].More.Action.Values, result.View.State
			if result, err = a.action(t.Context(), r); err != nil || result.View == nil || len(rowKeys(result.View.Sections[0])) != 26 || wire.CheckView(result.View) != nil {
				t.Fatalf("more: %+v, %v", result, err)
			}
		})
	}
}
