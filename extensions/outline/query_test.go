package main

import (
	"strings"
	"testing"

	"github.com/nwverse/persona/sdk/go/ext"
	"github.com/nwverse/persona/sdk/go/wire"
)

// The search box takes a query of up to 200 characters and keeps it in the page state, so a query of Han characters,
// three bytes each, has to survive the next action (Load more) as well.
func TestSearchWithAQueryOf200CharactersLoadsMore(t *testing.T) {
	for name, query := range map[string]string{
		"han":   strings.Repeat("字", 200),
		"ascii": strings.Repeat("a", 200),
	} {
		f := newMoreOutline(t, 26)
		a := f.app()
		r := &ext.UIRequest{UIParams: wire.UIParams{Locale: "en", UIVersion: 3, Action: "search", Values: map[string]string{"q": query}}}
		result, err := a.action(t.Context(), r)
		if err != nil || result.View == nil || result.View.Sections[0].More == nil {
			t.Fatalf("%s search: %+v, %v", name, result, err)
		}
		r.Action, r.Values, r.State = "more", result.View.Sections[0].More.Action.Values, result.View.State
		result, err = a.action(t.Context(), r)
		if err != nil || result.View == nil || len(resultKeys(result.View.Sections[0])) != 26 {
			t.Fatalf("%s more: %+v, %v", name, result, err)
		}
	}
}
