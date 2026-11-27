package main

import (
	"strings"
	"testing"

	"github.com/nwverse/persona/sdk/go/ext"
	"github.com/nwverse/persona/sdk/go/wire"
)

func TestLongHanHighlightAndDetailViewBounds(t *testing.T) {
	t.Run("han_3500_create_and_update_note", func(t *testing.T) {
		f := newFakeLibrary(t)
		a := f.app()
		passage := strings.Repeat("文", 3500)
		note := strings.Repeat("注", 3500)

		review, err := a.action(t.Context(), &ext.UIRequest{
			UIParams: wire.UIParams{
				Locale:    "zh-Hans",
				UIVersion: 6,
				State:     ext.EncodeState(pageState{Mode: "create", DraftID: "draft-create"}),
				Action:    "review-create",
				Values: map[string]string{
					"text":  passage,
					"title": "Reading context",
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

		reviewNote, err := a.action(t.Context(), &ext.UIRequest{
			UIParams: wire.UIParams{
				Locale:    "zh-Hans",
				UIVersion: 6,
				State:     ext.EncodeState(pageState{Mode: "edit-note", HighlightID: 42, DraftID: "draft-note"}),
				Action:    "review-note",
				Values:    map[string]string{"note": note},
			},
		})
		if err != nil {
			t.Fatalf("review-note: %v", err)
		}
		if err := wire.CheckView(reviewNote.View); err != nil {
			t.Fatalf("review-note CheckView: %v", err)
		}
		confirmNote, err := a.action(t.Context(), &ext.UIRequest{
			UIParams: wire.UIParams{
				Locale:    "zh-Hans",
				UIVersion: 6,
				State:     reviewNote.View.State,
				Action:    "confirm-note",
			},
		})
		if err != nil || !confirmNote.Pop {
			t.Fatalf("confirm-note: %v %+v", err, confirmNote)
		}
	})

	t.Run("han_8191_note_detail_push_state", func(t *testing.T) {
		item := sampleHighlight()
		item.Note = strings.Repeat("注", 8191)
		v := detailView(
			&ext.UIRequest{UIParams: wire.UIParams{Locale: "zh-Hans", UIVersion: 6}},
			pageState{Mode: "detail", HighlightID: 42, DraftID: "draft-1"},
			item,
		)
		if err := wire.CheckView(v); err != nil {
			t.Fatalf("CheckView: %v", err)
		}
	})

	t.Run("multiline_text_and_note_detail", func(t *testing.T) {
		item := sampleHighlight()
		item.Text = strings.Repeat("passage line\n", 200)
		item.Note = strings.Repeat("note line\n", 200)
		v := detailView(
			&ext.UIRequest{UIParams: wire.UIParams{Locale: "en", UIVersion: 6}},
			pageState{Mode: "detail", HighlightID: 42, DraftID: "draft-1"},
			item,
		)
		if err := wire.CheckView(v); err != nil {
			t.Fatalf("CheckView: %v", err)
		}
	})

	t.Run("ascii_short_control", func(t *testing.T) {
		v := detailView(
			&ext.UIRequest{UIParams: wire.UIParams{Locale: "en", UIVersion: 6}},
			pageState{Mode: "detail", HighlightID: 42, DraftID: "draft-1"},
			sampleHighlight(),
		)
		if err := wire.CheckView(v); err != nil {
			t.Fatalf("CheckView: %v", err)
		}
	})
}
