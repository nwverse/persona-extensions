package main

import (
	"strings"
	"testing"

	"github.com/nwverse/persona/sdk/go/ext"
	"github.com/nwverse/persona/sdk/go/wire"
)

// The page state holds the reviewed draft's item name (up to 500 characters) and status label (up to 100
// characters). Both are counted in characters wherever they are limited, so Han characters, three bytes each, have to
// survive the confirm action as well.
func TestWebFlowCreatesA500CharacterItemName(t *testing.T) {
	for name, title := range map[string]string{
		"han":   strings.Repeat("字", 500),
		"ascii": strings.Repeat("a", 500),
	} {
		f := newFakeMonday(t)
		a := f.app()
		review, err := a.action(t.Context(), &ext.UIRequest{UIParams: wire.UIParams{Locale: "zh-Hans", UIVersion: 6, Action: "review-create", State: ext.EncodeState(pageState{Mode: "create", BoardID: 7, DraftID: "draft-1"}), Values: map[string]string{"name": title}}})
		if err != nil || review.View == nil || wire.CheckView(review.View) != nil {
			t.Fatalf("%s review: %v %+v", name, err, review)
		}
		confirm, err := a.action(t.Context(), &ext.UIRequest{UIParams: wire.UIParams{Locale: "zh-Hans", UIVersion: 6, Action: "confirm-create", State: review.View.State}})
		if err != nil || !confirm.Pop || f.creates != 1 {
			t.Fatalf("%s confirm: %v %+v, %d items created", name, err, confirm, f.creates)
		}
	}
}

func TestWebFlowSetsA100CharacterStatusLabel(t *testing.T) {
	for name, label := range map[string]string{
		"han":   strings.Repeat("字", 100),
		"ascii": strings.Repeat("a", 100),
	} {
		f := newFakeMonday(t)
		f.extraLabel = label
		a := f.app()
		review, err := a.action(t.Context(), &ext.UIRequest{UIParams: wire.UIParams{Locale: "zh-Hans", UIVersion: 6, Action: "review-edit", State: ext.EncodeState(pageState{Mode: "edit", BoardID: 7, ItemID: 42, DraftID: "draft-1"}), Values: map[string]string{"column_id": "status", "label": label}}})
		if err != nil || review.View == nil || wire.CheckView(review.View) != nil {
			t.Fatalf("%s review: %v %+v", name, err, review)
		}
		confirm, err := a.action(t.Context(), &ext.UIRequest{UIParams: wire.UIParams{Locale: "zh-Hans", UIVersion: 6, Action: "confirm-edit", State: review.View.State}})
		if err != nil || !confirm.Pop || f.updates != 1 {
			t.Fatalf("%s confirm: %v %+v, %d updates", name, err, confirm, f.updates)
		}
	}
}
