package main

import (
	"strings"
	"testing"

	"github.com/nwverse/persona/sdk/go/ext"
	"github.com/nwverse/persona/sdk/go/wire"
)

// The page state holds the reviewed draft's task name (up to 500 characters) and status (up to 100 characters). Both
// are counted in characters wherever they are limited, so Han characters, three bytes each, have to survive the
// confirm action as well.
func TestWebFlowCreatesA500CharacterTaskName(t *testing.T) {
	for name, title := range map[string]string{
		"han":   strings.Repeat("字", 500),
		"ascii": strings.Repeat("a", 500),
	} {
		f := newFakeClickUp(t)
		a := f.app()
		review, err := a.action(t.Context(), &ext.UIRequest{UIParams: wire.UIParams{Locale: "zh-Hans", UIVersion: 6, Action: "review-create", State: ext.EncodeState(pageState{Mode: "create", TeamID: "team-1", SpaceID: "space-1", ListID: "list-1", DraftID: "draft-1"}), Values: map[string]string{"name": title, "description": "保留来源上下文。"}}})
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

func TestWebFlowSetsA100CharacterStatus(t *testing.T) {
	for name, status := range map[string]string{
		"han":   strings.Repeat("字", 100),
		"ascii": strings.Repeat("a", 100),
	} {
		f := newFakeClickUp(t)
		a := f.app()
		review, err := a.action(t.Context(), &ext.UIRequest{UIParams: wire.UIParams{Locale: "zh-Hans", UIVersion: 6, Action: "review-edit", State: ext.EncodeState(pageState{Mode: "edit", TaskID: "task-1", DraftID: "draft-1"}), Values: map[string]string{"status": status}}})
		if err != nil || review.View == nil || wire.CheckView(review.View) != nil {
			t.Fatalf("%s review: %v %+v", name, err, review)
		}
		confirm, err := a.action(t.Context(), &ext.UIRequest{UIParams: wire.UIParams{Locale: "zh-Hans", UIVersion: 6, Action: "confirm-edit", State: review.View.State}})
		if err != nil || !confirm.Pop {
			t.Fatalf("%s confirm: %v %+v", name, err, confirm)
		}
		if len(f.updates) != 1 || f.updates[0]["status"] != status {
			t.Fatalf("%s: the server received %v", name, f.updates)
		}
	}
}
