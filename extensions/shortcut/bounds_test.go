package main

import (
	"strings"
	"testing"

	"github.com/nwverse/persona/sdk/go/ext"
	"github.com/nwverse/persona/sdk/go/wire"
)

func TestCreateReviewBoundsAndHanDescriptionState(t *testing.T) {
	t.Run("short ASCII story description control", func(t *testing.T) {
		f := newFakeShortcut(t)
		a := f.app()
		res, err := a.action(t.Context(), &ext.UIRequest{UIParams: wire.UIParams{
			Locale:    "en",
			UIVersion: 6,
			Action:    "review-create",
			State:     ext.EncodeState(pageState{Mode: "create", DraftID: "ctl-1"}),
			Values: map[string]string{
				"name":        "Control story",
				"description": "Short context.",
				"state_id":    "10",
				"story_type":  "feature",
			},
		}})
		if err != nil || res.View == nil || wire.CheckView(res.View) != nil {
			t.Fatalf("control review-create failed: %v %+v", err, res)
		}
	})

	t.Run("review-create caps >300-line story description within wire.ItemsMost", func(t *testing.T) {
		f := newFakeShortcut(t)
		a := f.app()
		res, err := a.action(t.Context(), &ext.UIRequest{UIParams: wire.UIParams{
			Locale:    "en",
			UIVersion: 6,
			Action:    "review-create",
			State:     ext.EncodeState(pageState{Mode: "create", DraftID: "lines-1"}),
			Values: map[string]string{
				"name":        "Long multiline story",
				"description": strings.Repeat("line context\n", 340),
				"state_id":    "10",
				"story_type":  "feature",
			},
		}})
		if err != nil || res.View == nil {
			t.Fatalf("review-create failed: %v %+v", err, res)
		}
		if err := wire.CheckView(res.View); err != nil {
			t.Fatalf("review-create with 340-line description failed CheckView: %v", err)
		}
	})

	t.Run("4000-Han-character story description round-trips through confirm-create", func(t *testing.T) {
		f := newFakeShortcut(t)
		a := f.app()
		hanDesc := strings.Repeat("故", 4000)
		reviewRes, err := a.action(t.Context(), &ext.UIRequest{UIParams: wire.UIParams{
			Locale:    "zh-Hans",
			UIVersion: 6,
			Action:    "review-create",
			State:     ext.EncodeState(pageState{Mode: "create", DraftID: "han-1"}),
			Values: map[string]string{
				"name":        "整理故事上下文",
				"description": hanDesc,
				"state_id":    "10",
				"story_type":  "feature",
			},
		}})
		if err != nil || reviewRes.View == nil {
			t.Fatalf("review-create with 4000 Han chars failed: %v %+v", err, reviewRes)
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
		if err != nil || !confirmRes.Pop || len(f.creates) != 1 || f.creates[0]["description"] != hanDesc {
			t.Fatalf("confirm-create with 4000 Han chars failed: %v %+v %v", err, confirmRes, f.creates)
		}
	})
}
