package main

import (
	"strings"
	"testing"

	"github.com/nwverse/persona/sdk/go/ext"
	"github.com/nwverse/persona/sdk/go/wire"
)

// The page state holds the reviewed draft's issue summary (up to 500 characters). Every entry check counts it in
// characters, so Han characters, three bytes each, have to survive the confirm action as well.
func TestWebFlowCreatesA500CharacterSummary(t *testing.T) {
	for _, tc := range []struct {
		name    string
		summary string
	}{
		{"han", strings.Repeat("字", 500)},
		{"ascii", strings.Repeat("a", 500)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeYouTrack(t)
			a := f.app()
			review, err := a.action(t.Context(), &ext.UIRequest{UIParams: wire.UIParams{
				Locale:    "zh-Hans",
				UIVersion: 6,
				Action:    "review-create",
				State:     ext.EncodeState(pageState{Mode: "create", ProjectID: "0-7", DraftID: "draft-1"}),
				Values:    map[string]string{"project_id": "0-7", "summary": tc.summary, "description": "保留来源上下文。"},
			}})
			if err != nil || review.View == nil || wire.CheckView(review.View) != nil {
				t.Fatalf("review: %v %+v", err, review)
			}
			confirm, err := a.action(t.Context(), &ext.UIRequest{UIParams: wire.UIParams{
				Locale:    "zh-Hans",
				UIVersion: 6,
				Action:    "confirm-create",
				State:     review.View.State,
			}})
			if err != nil || !confirm.Pop {
				t.Fatalf("confirm: %v %+v", err, confirm)
			}
			if len(f.creates) != 1 || f.creates[0]["summary"] != tc.summary {
				t.Fatalf("the server received %v", f.creates)
			}
		})
	}
}
