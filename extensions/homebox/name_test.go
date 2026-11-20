package main

import (
	"strings"
	"testing"

	"github.com/nwverse/persona/sdk/go/ext"
	"github.com/nwverse/persona/sdk/go/wire"
)

// An item name of up to 255 characters is accepted at review and kept in the page state, so a name of Han characters,
// three bytes each, has to survive the confirm action as well.
func TestWebFlowCreatesA255CharacterName(t *testing.T) {
	for name, title := range map[string]string{
		"han":   strings.Repeat("字", 255),
		"ascii": strings.Repeat("a", 255),
	} {
		f := newFakeHomebox(t)
		a := f.app()
		review, err := a.action(t.Context(), &ext.UIRequest{UIParams: wire.UIParams{Locale: "zh-Hans", UIVersion: 6, Action: "review-create", State: ext.EncodeState(pageState{Mode: "create", EntityTypeID: typeID, LocationID: locationID, DraftID: "draft-1"}), Values: map[string]string{"name": title, "description": "备用阅读器", "quantity": "1"}}})
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
