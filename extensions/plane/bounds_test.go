package main

import (
	"strings"
	"testing"

	"github.com/nwverse/persona/sdk/go/ext"
	"github.com/nwverse/persona/sdk/go/wire"
)

func TestHanProjectAndLongDescriptionFlows(t *testing.T) {
	f := newFakePlane(t)
	a := f.app()

	t.Run("han_project_255", func(t *testing.T) {
		projectName := strings.Repeat("项", 255)
		res, err := a.action(t.Context(), &ext.UIRequest{
			UIParams: wire.UIParams{
				Locale:    "zh-Hans",
				UIVersion: 6,
				Action:    "project",
				Values: map[string]string{
					"id":         "project-1",
					"name":       projectName,
					"identifier": "READ",
				},
			},
		})
		if err != nil {
			t.Fatalf("project: %v", err)
		}
		if err := wire.CheckView(res.View); err != nil {
			t.Fatalf("project CheckView: %v", err)
		}
		more, err := a.action(t.Context(), &ext.UIRequest{
			UIParams: wire.UIParams{
				Locale:    "zh-Hans",
				UIVersion: 6,
				State:     res.View.State,
				Action:    "more",
				Values:    res.View.Sections[0].More.Action.Values,
			},
		})
		if err != nil {
			t.Fatalf("more: %v", err)
		}
		if err := wire.CheckView(more.View); err != nil {
			t.Fatalf("more CheckView: %v", err)
		}
	})

	t.Run("han_description_5000_create", func(t *testing.T) {
		review, err := a.action(t.Context(), &ext.UIRequest{
			UIParams: wire.UIParams{
				Locale:    "zh-Hans",
				UIVersion: 6,
				State:     ext.EncodeState(pageState{Mode: "create", ProjectID: "project-1", ProjectName: "Reading", Identifier: "READ", DraftID: "draft-han"}),
				Action:    "review-create",
				Values: map[string]string{
					"name":        strings.Repeat("题", 255),
					"description": strings.Repeat("述", 5000),
					"priority":    "high",
				},
			},
		})
		if err != nil {
			t.Fatalf("review-create: %v", err)
		}
		if err := wire.CheckView(review.View); err != nil {
			t.Fatalf("review-create CheckView: %v", err)
		}
		confirm, err := a.action(t.Context(), &ext.UIRequest{
			UIParams: wire.UIParams{
				Locale:    "zh-Hans",
				UIVersion: 6,
				State:     review.View.State,
				Action:    "confirm-create",
			},
		})
		if err != nil || !confirm.Pop {
			t.Fatalf("confirm-create: %v %+v", err, confirm)
		}
	})

	t.Run("long_multiline_description_views", func(t *testing.T) {
		item := sampleItem()
		item.DescriptionStripped = strings.Repeat("line\n", 350)
		v := detailView(
			&ext.UIRequest{UIParams: wire.UIParams{Locale: "en", UIVersion: 6}},
			pageState{Mode: "detail", ProjectID: "project-1", WorkItemID: "item-1"},
			item,
			"https://app.plane.so/personal/projects/project-1/work-items/item-1",
		)
		if err := wire.CheckView(v); err != nil {
			t.Fatalf("detailView CheckView: %v", err)
		}
		review, err := a.action(t.Context(), &ext.UIRequest{
			UIParams: wire.UIParams{
				Locale:    "en",
				UIVersion: 6,
				State:     ext.EncodeState(pageState{Mode: "create", ProjectID: "project-1", ProjectName: "Reading", Identifier: "READ", DraftID: "draft-lines"}),
				Action:    "review-create",
				Values: map[string]string{
					"name":        "Multiline item",
					"description": strings.Repeat("line\n", 350),
					"priority":    "none",
				},
			},
		})
		if err != nil {
			t.Fatalf("review-create: %v", err)
		}
		if err := wire.CheckView(review.View); err != nil {
			t.Fatalf("review-create CheckView: %v", err)
		}
	})

	t.Run("ascii_short_control", func(t *testing.T) {
		res, err := a.action(t.Context(), &ext.UIRequest{
			UIParams: wire.UIParams{
				Locale:    "en",
				UIVersion: 6,
				Action:    "project",
				Values: map[string]string{
					"id":         "project-1",
					"name":       "Reading",
					"identifier": "READ",
				},
			},
		})
		if err != nil {
			t.Fatalf("project: %v", err)
		}
		if err := wire.CheckView(res.View); err != nil {
			t.Fatalf("CheckView: %v", err)
		}
	})
}
