package main

import (
	"strings"
	"testing"

	"github.com/nwverse/persona/sdk/go/ext"
	"github.com/nwverse/persona/sdk/go/wire"
)

// A bookmark title of up to 1000 characters is accepted at review and kept in the page state, so a title of Han
// characters, three bytes each, has to survive the confirm action as well.
func TestWebFlowSavesA1000CharacterTitle(t *testing.T) {
	for name, title := range map[string]string{
		"han":   strings.Repeat("字", 1000),
		"ascii": strings.Repeat("a", 1000),
	} {
		f := newFakeReadeck(t)
		a := f.app()
		review, err := a.action(t.Context(), &ext.UIRequest{UIParams: wire.UIParams{Locale: "zh-Hans", UIVersion: 6, Action: "review-save", State: ext.EncodeState(pageState{Mode: "save", DraftID: "draft-1"}), Values: map[string]string{"url": "https://articles.example.com/context", "title": title, "labels": "阅读, 上下文"}}})
		if err != nil || review.View == nil || wire.CheckView(review.View) != nil {
			t.Fatalf("%s review: %v %+v", name, err, review)
		}
		confirm, err := a.action(t.Context(), &ext.UIRequest{UIParams: wire.UIParams{Locale: "zh-Hans", UIVersion: 6, Action: "confirm-save", State: review.View.State}})
		if err != nil || !confirm.Pop {
			t.Fatalf("%s confirm: %v %+v", name, err, confirm)
		}
		if len(f.creates) != 1 || f.creates[0]["title"] != title {
			t.Fatalf("%s: the server received %v", name, f.creates)
		}
	}
}
