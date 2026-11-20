package main

import (
	"strings"
	"testing"

	"github.com/nwverse/persona/sdk/go/ext"
	"github.com/nwverse/persona/sdk/go/wire"
)

// A story name of up to 512 characters is accepted at review and kept in the page state, so a name of Han characters,
// three bytes each, has to survive the confirm action as well.
func TestWebFlowCreatesA512CharacterName(t *testing.T) {
	for name, title := range map[string]string{
		"han":   strings.Repeat("字", 512),
		"ascii": strings.Repeat("a", 512),
	} {
		f := newFakeShortcut(t)
		a := f.app()
		review, err := a.action(t.Context(), &ext.UIRequest{UIParams: wire.UIParams{Locale: "zh-Hans", UIVersion: 6, Action: "review-create", State: ext.EncodeState(pageState{Mode: "create", DraftID: "draft-1"}), Values: map[string]string{"name": title, "description": "保留来源上下文。", "state_id": "10", "story_type": "feature"}}})
		if err != nil || review.View == nil || wire.CheckView(review.View) != nil {
			t.Fatalf("%s review: %v %+v", name, err, review)
		}
		confirm, err := a.action(t.Context(), &ext.UIRequest{UIParams: wire.UIParams{Locale: "zh-Hans", UIVersion: 6, Action: "confirm-create", State: review.View.State}})
		if err != nil || !confirm.Pop {
			t.Fatalf("%s confirm: %v %+v", name, err, confirm)
		}
		if len(f.creates) != 1 || f.creates[0]["name"] != title {
			t.Fatalf("%s: the server received %v", name, f.creates)
		}
	}
}
