package main

import (
	"fmt"
	"testing"

	"github.com/nwverse/persona/sdk/go/ext"
	"github.com/nwverse/persona/sdk/go/wire"
)

func TestUIStatusUpdateFlowAndDetailBounds(t *testing.T) {
	t.Run("create item flow control", func(t *testing.T) {
		f := newFakeMonday(t)
		a := f.app()
		res, err := a.action(t.Context(), &ext.UIRequest{UIParams: wire.UIParams{
			Locale:    "en",
			UIVersion: 6,
			Action:    "review-create",
			State:     ext.EncodeState(pageState{Mode: "create", BoardID: 7, BoardName: "Reading", DraftID: "ctl-1"}),
			Values:    map[string]string{"name": "Control item"},
		}})
		if err != nil || res.View == nil || wire.CheckView(res.View) != nil {
			t.Fatalf("control review-create failed: %v %+v", err, res)
		}
	})

	t.Run("update status from detail view without column_id form field", func(t *testing.T) {
		f := newFakeMonday(t)
		a := f.app()

		detail, err := a.page(t.Context(), &ext.UIRequest{UIParams: wire.UIParams{Locale: "zh-Hans", UIVersion: 6}}, pageState{
			Mode:      "detail",
			BoardID:   7,
			BoardName: "Reading",
			ItemID:    42,
		})
		if err != nil {
			t.Fatalf("detail page failed: %v", err)
		}
		if err := wire.CheckView(detail); err != nil {
			t.Fatalf("detail CheckView failed: %v", err)
		}

		var pushState string
		for _, comp := range detail.Sections[0].Items {
			if comp.Action != nil && comp.Action.ID == "edit" && comp.Action.Push != nil {
				pushState = comp.Action.Push.State
				break
			}
		}
		if pushState == "" {
			t.Fatalf("expected edit push button in detail view: %+v", detail.Sections[0].Items)
		}

		editView, err := a.render(t.Context(), &ext.UIRequest{UIParams: wire.UIParams{
			Locale:    "zh-Hans",
			UIVersion: 6,
			State:     pushState,
		}})
		if err != nil {
			t.Fatalf("edit render failed: %v", err)
		}
		if err := wire.CheckView(editView); err != nil {
			t.Fatalf("edit CheckView failed: %v", err)
		}

		reviewRes, err := a.action(t.Context(), &ext.UIRequest{UIParams: wire.UIParams{
			Locale:    "zh-Hans",
			UIVersion: 6,
			Action:    "review-edit",
			State:     editView.State,
			Values:    map[string]string{"label": "Done"},
		}})
		if err != nil || reviewRes.View == nil {
			t.Fatalf("review-edit from UI editor failed: %v %+v", err, reviewRes)
		}
		if err := wire.CheckView(reviewRes.View); err != nil {
			t.Fatalf("review-edit CheckView failed: %v", err)
		}

		confirmRes, err := a.action(t.Context(), &ext.UIRequest{UIParams: wire.UIParams{
			Locale:    "zh-Hans",
			UIVersion: 6,
			Action:    "confirm-edit",
			State:     reviewRes.View.State,
		}})
		if err != nil || !confirmRes.Pop || f.updates != 1 {
			t.Fatalf("confirm-edit failed: %v %+v updates=%d", err, confirmRes, f.updates)
		}
	})

	t.Run("detailView caps many column values within wire.ItemsMost", func(t *testing.T) {
		it := sampleItem()
		for i := range 320 {
			it.ColumnValues = append(it.ColumnValues, struct {
				ID    string `json:"id"`
				Text  string `json:"text"`
				Value string `json:"value"`
			}{
				ID:   fmt.Sprintf("col_%d", i),
				Text: fmt.Sprintf("value %d", i),
			})
		}
		v := detailView(&ext.UIRequest{UIParams: wire.UIParams{Locale: "en", UIVersion: 6}}, pageState{
			Mode:    "detail",
			BoardID: 7,
			ItemID:  42,
			DraftID: "draft-cap",
		}, sampleBoard(), it)
		if err := wire.CheckView(v); err != nil {
			t.Fatalf("detailView with 321 column values failed CheckView: %v", err)
		}
	})
}
