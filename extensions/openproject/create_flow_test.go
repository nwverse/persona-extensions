package main

import (
	"strings"
	"testing"

	"github.com/nwverse/persona/sdk/go/ext"
	"github.com/nwverse/persona/sdk/go/wire"
)

func TestProjectsRowSelectionAndStateBounds(t *testing.T) {
	t.Run("preset state create flow", func(t *testing.T) {
		f := newFakeOpenProject(t)
		a := f.app()
		req := &ext.UIRequest{UIParams: wire.UIParams{
			Locale:    "en",
			UIVersion: 6,
			Action:    "review-create",
			State: ext.EncodeState(pageState{
				Mode:        "create",
				ProjectHref: "/api/v3/projects/7",
				ProjectName: "Reading",
				TypeHref:    "/api/v3/types/1",
				TypeName:    "Task",
				DraftID:     "draft-control",
			}),
			Values: map[string]string{"subject": "Control task", "description": "Short note"},
		}}
		res, err := a.action(t.Context(), req)
		if err != nil || res.View == nil {
			t.Fatalf("control review-create failed: %v %+v", err, res)
		}
		if err := wire.CheckView(res.View); err != nil {
			t.Fatalf("control CheckView failed: %v", err)
		}
	})

	t.Run("select project row from projects view and create work package", func(t *testing.T) {
		f := newFakeOpenProject(t)
		a := f.app()

		projectsView, err := a.page(t.Context(), &ext.UIRequest{UIParams: wire.UIParams{Locale: "zh-Hans", UIVersion: 6}}, pageState{Mode: "projects"})
		if err != nil {
			t.Fatalf("projects view failed: %v", err)
		}
		if err := wire.CheckView(projectsView); err != nil {
			t.Fatalf("projects CheckView failed: %v", err)
		}
		row := projectsView.Sections[0].Items[0]
		if row.Action == nil || row.Action.ID != "create" {
			t.Fatalf("expected project row create action, got %+v", row.Action)
		}

		createRes, err := a.action(t.Context(), &ext.UIRequest{UIParams: wire.UIParams{
			Locale:    "zh-Hans",
			UIVersion: 6,
			Action:    row.Action.ID,
			State:     projectsView.State,
			Values:    row.Action.Values,
		}})
		if err != nil || createRes.View == nil {
			t.Fatalf("create action from project row failed: %v %+v", err, createRes)
		}
		if err := wire.CheckView(createRes.View); err != nil {
			t.Fatalf("create view CheckView failed: %v", err)
		}

		hanDesc := strings.Repeat("阅", 3500)
		reviewRes, err := a.action(t.Context(), &ext.UIRequest{UIParams: wire.UIParams{
			Locale:    "zh-Hans",
			UIVersion: 6,
			Action:    "review-create",
			State:     createRes.View.State,
			Values: map[string]string{
				"subject":     "整理读书笔记",
				"description": hanDesc,
				"start_date":  "2026-11-10",
				"due_date":    "2026-11-30",
			},
		}})
		if err != nil || reviewRes.View == nil {
			t.Fatalf("review-create after selecting project row failed: %v %+v", err, reviewRes)
		}
		if err := wire.CheckView(reviewRes.View); err != nil {
			t.Fatalf("review-create CheckView failed: %v", err)
		}

		confirmRes, err := a.action(t.Context(), &ext.UIRequest{UIParams: wire.UIParams{
			Locale:    "zh-Hans",
			UIVersion: 6,
			Action:    "confirm-create",
			State:     reviewRes.View.State,
		}})
		if err != nil || !confirmRes.Pop || len(f.creates) != 1 {
			t.Fatalf("confirm-create failed: %v %+v %v", err, confirmRes, f.creates)
		}

		if _, err := a.action(t.Context(), &ext.UIRequest{UIParams: wire.UIParams{
			Locale:    "zh-Hans",
			UIVersion: 6,
			Action:    "review-create",
			State:     createRes.View.State,
			Values: map[string]string{
				"subject":     "超长草稿",
				"description": strings.Repeat("超", 10500),
			},
		}}); err == nil {
			t.Fatal("expected oversized multibyte draft exceeding wire.StateMost to be rejected")
		}
	})
}
