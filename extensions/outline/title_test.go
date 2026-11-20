package main

import (
	"strings"
	"testing"

	"github.com/nwverse/persona/sdk/go/ext"
	"github.com/nwverse/persona/sdk/go/wire"
)

// Outline allows a title of up to 100 characters (documents.create, maxLength 100). The page keeps the reviewed draft in
// its state, so a title of Han characters, three bytes each, has to fit the state check as well.
func TestWebFlowCreatesA100CharacterTitle(t *testing.T) {
	for name, title := range map[string]string{
		"han":   strings.Repeat("字", 100),
		"ascii": strings.Repeat("a", 100),
	} {
		f := newFakeOutline(t)
		a := f.app()
		review, err := a.action(t.Context(), &ext.UIRequest{UIParams: wire.UIParams{Locale: "zh-Hans", UIVersion: 6, Action: "review-create", State: ext.EncodeState(pageState{Mode: "create", CollectionID: collectionID, DraftID: "draft-1"}), Values: map[string]string{"title": title, "text": "保留来源上下文。"}}})
		if err != nil || review.View == nil || wire.CheckView(review.View) != nil {
			t.Fatalf("%s review: %v %+v", name, err, review)
		}
		confirm, err := a.action(t.Context(), &ext.UIRequest{UIParams: wire.UIParams{Locale: "zh-Hans", UIVersion: 6, Action: "confirm-create", State: review.View.State}})
		if err != nil || !confirm.Pop {
			t.Fatalf("%s confirm: %v %+v", name, err, confirm)
		}
		if len(f.creates) != 1 || f.creates[0]["title"] != title {
			t.Fatalf("%s: the server received %v", name, f.creates)
		}
	}
}
