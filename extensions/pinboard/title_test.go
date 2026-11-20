package main

import (
	"strings"
	"testing"

	"github.com/nwverse/persona/sdk/go/ext"
	"github.com/nwverse/persona/sdk/go/wire"
)

// A bookmark title of up to 255 characters is accepted at review and kept in the page state, so a title of Han
// characters, three bytes each, has to survive the confirm action as well.
func TestWebFlowAddsA255CharacterTitle(t *testing.T) {
	for name, title := range map[string]string{
		"han":   strings.Repeat("字", 255),
		"ascii": strings.Repeat("a", 255),
	} {
		f := newCountingPinboard(t)
		a := f.app()
		review, err := a.action(t.Context(), &ext.UIRequest{UIParams: wire.UIParams{Locale: "zh-Hans", UIVersion: 6, Action: "review-create", State: ext.EncodeState(pageState{Mode: "create", DraftID: "draft-1"}), Values: map[string]string{"url": "https://articles.example.com/context", "title": title, "notes": "保留来源上下文。", "tags": "阅读 上下文"}}})
		if err != nil || review.View == nil || wire.CheckView(review.View) != nil {
			t.Fatalf("%s review: %v %+v", name, err, review)
		}
		confirm, err := a.action(t.Context(), &ext.UIRequest{UIParams: wire.UIParams{Locale: "zh-Hans", UIVersion: 6, Action: "confirm-create", State: review.View.State}})
		if err != nil || !confirm.Pop {
			t.Fatalf("%s confirm: %v %+v", name, err, confirm)
		}
		if f.requests != 1 || f.titles[0] != title {
			t.Fatalf("%s: the server received %d requests with titles %q", name, f.requests, f.titles)
		}
	}
}
