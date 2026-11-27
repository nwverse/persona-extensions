package main

import (
	"strings"
	"testing"

	"github.com/nwverse/persona/sdk/go/ext"
	"github.com/nwverse/persona/sdk/go/wire"
)

// The page state holds the reviewed draft's task title and the selected workspace's name (both up to 500 characters).
// Every entry check counts them in characters, so Han characters, three bytes each, have to survive workspace
// navigation and task confirmation as well.
func TestWebFlowCreatesA500CharacterTaskName(t *testing.T) {
	for _, tc := range []struct {
		name  string
		title string
	}{
		{"han", strings.Repeat("字", 500)},
		{"ascii", strings.Repeat("a", 500)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeAsana(t)
			a := f.app()
			review, err := a.action(t.Context(), &ext.UIRequest{UIParams: wire.UIParams{
				Locale:    "zh-Hans",
				UIVersion: 6,
				Action:    "review-create",
				State:     ext.EncodeState(pageState{Mode: "create", WorkspaceGID: "workspace-1", DraftID: "draft-1"}),
				Values:    map[string]string{"name": tc.title, "notes": "保留来源上下文。", "project_gid": "project-1", "due_on": "2026-11-30"},
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
			if len(f.creates) != 1 || f.creates[0]["name"] != tc.title {
				t.Fatalf("the server received %v", f.creates)
			}
		})
	}
}

func TestOpeningAWorkspaceWithA500CharacterName(t *testing.T) {
	for _, tc := range []struct {
		name          string
		workspaceName string
	}{
		{"han", strings.Repeat("字", 500)},
		{"ascii", strings.Repeat("a", 500)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeAsana(t)
			a := f.app()
			r := &ext.UIRequest{UIParams: wire.UIParams{
				Locale:    "zh-Hans",
				UIVersion: 6,
				Action:    "workspace",
				Values:    map[string]string{"gid": "workspace-1", "name": tc.workspaceName},
			}}
			result, err := a.action(t.Context(), r)
			if err != nil || result.View == nil || result.View.Sections[0].More == nil || wire.CheckView(result.View) != nil {
				t.Fatalf("workspace: %v %+v", err, result)
			}
			r.Action, r.Values, r.State = "more", result.View.Sections[0].More.Action.Values, result.View.State
			if result, err = a.action(t.Context(), r); err != nil || result.View == nil || wire.CheckView(result.View) != nil {
				t.Fatalf("more: %v %+v", err, result)
			}
		})
	}
}
