package main

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/nwverse/persona/sdk/go/ext"
	"github.com/nwverse/persona/sdk/go/wire"
)

// The page state holds the search query (clipped to 100 characters). Every entry check counts it in characters, so
// Han characters, three bytes each, have to survive opening a file from search results as well. clipText keeps
// limit-1 characters and adds an ellipsis, so a longer query is held as at most 100 characters.
func TestSearchQueryOf100CharactersOpensFile(t *testing.T) {
	f := newFakeNextcloud(t)

	for _, tc := range []struct {
		name  string
		query string
	}{
		{"han exact", strings.Repeat("字", 100)},
		{"han clipped", strings.Repeat("字", 150)},
		{"ascii", strings.Repeat("a", 100)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := f.app()
			r := &ext.UIRequest{UIParams: wire.UIParams{Locale: "zh-Hans", UIVersion: 6, Action: "search", Values: map[string]string{"q": tc.query}}}
			result, err := a.action(t.Context(), r)
			if err != nil || result.View == nil || result.View.Sections[0].Items[0].Action == nil || wire.CheckView(result.View) != nil {
				t.Fatalf("search: %+v, %v", result, err)
			}
			var st pageState
			if err := (&ext.UIRequest{UIParams: wire.UIParams{State: result.View.State}}).DecodeState(&st); err != nil || utf8.RuneCountInString(st.Query) > 100 {
				t.Fatalf("the state holds %d characters: %v", utf8.RuneCountInString(st.Query), err)
			}
			r.Action, r.Values, r.State = "detail", result.View.Sections[0].Items[0].Action.Values, result.View.State
			if result, err = a.action(t.Context(), r); err != nil || result.View == nil || result.View.Title != "reading.md" || wire.CheckView(result.View) != nil {
				t.Fatalf("detail: %+v, %v", result, err)
			}
		})
	}
}
