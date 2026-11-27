package main

import (
	"strings"
	"testing"

	"github.com/nwverse/persona/sdk/go/ext"
	"github.com/nwverse/persona/sdk/go/wire"
)

// A page title of up to 255 characters is accepted at review and kept in the page state, so a title of Han characters,
// three bytes each, has to survive the confirm action as well, for a new page and for an edit.
func TestWebFlowSavesA255CharacterTitle(t *testing.T) {
	for name, title := range map[string]string{
		"han":   strings.Repeat("字", 255),
		"ascii": strings.Repeat("a", 255),
	} {
		for _, flow := range []struct {
			review, confirm string
			state           pageState
		}{
			{"review-create", "confirm-create", pageState{Mode: "create", BookID: 7, BookName: "Reading", DraftID: "draft-1"}},
			{"review-edit", "confirm-edit", pageState{Mode: "edit", PageID: 42, DraftID: "draft-2"}},
		} {
			f := newFakeBookStack(t)
			a := f.app()
			review, err := a.action(t.Context(), &ext.UIRequest{UIParams: wire.UIParams{Locale: "zh-Hans", UIVersion: 6, Action: flow.review, State: ext.EncodeState(flow.state), Values: map[string]string{"name": title, "markdown": "保留来源上下文。"}}})
			if err != nil || review.View == nil || wire.CheckView(review.View) != nil {
				t.Fatalf("%s %s: %v %+v", name, flow.review, err, review)
			}
			confirm, err := a.action(t.Context(), &ext.UIRequest{UIParams: wire.UIParams{Locale: "zh-Hans", UIVersion: 6, Action: flow.confirm, State: review.View.State}})
			if err != nil || !confirm.Pop {
				t.Fatalf("%s %s: %v %+v", name, flow.confirm, err, confirm)
			}
			writes := append(f.creates, f.updates...)
			if len(writes) != 1 || writes[0]["name"] != title {
				t.Fatalf("%s %s: the server received %v", name, flow.confirm, writes)
			}
		}
	}
}
