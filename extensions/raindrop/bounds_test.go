package main

import (
	"strings"
	"testing"

	"github.com/nwverse/persona/sdk/go/ext"
	"github.com/nwverse/persona/sdk/go/wire"
)

func TestDetailAndReviewSaveViewBounds(t *testing.T) {
	f := newFakeRaindrop(t)
	a := f.app()

	t.Run("long_note_and_highlights", func(t *testing.T) {
		v := a.detailView(
			&ext.UIRequest{UIParams: wire.UIParams{Locale: "en", UIVersion: 6}},
			pageState{Mode: "detail", Bookmark: 1},
			raindrop{
				ID:     1,
				Title:  "Long bookmark",
				Domain: "example.com",
				Link:   "https://example.com/long",
				Note:   strings.Repeat("note line\n", 180),
				Highlights: []highlight{{
					Text: strings.Repeat("quote line\n", 180),
					Note: strings.Repeat("comment line\n", 60),
				}},
			},
		)
		if err := wire.CheckView(v); err != nil {
			t.Fatalf("CheckView: %v", err)
		}
	})

	t.Run("empty_highlight_and_newline_note", func(t *testing.T) {
		v := a.detailView(
			&ext.UIRequest{UIParams: wire.UIParams{Locale: "en", UIVersion: 6}},
			pageState{Mode: "detail", Bookmark: 2},
			raindrop{
				ID:         2,
				Title:      "Empty highlight",
				Domain:     "example.com",
				Link:       "https://example.com/empty",
				Note:       "\n\n",
				Highlights: []highlight{{Text: "", Note: ""}},
			},
		)
		if err := wire.CheckView(v); err != nil {
			t.Fatalf("CheckView: %v", err)
		}
	})

	t.Run("long_review_save_note", func(t *testing.T) {
		res, err := a.action(t.Context(), &ext.UIRequest{
			UIParams: wire.UIParams{
				Locale:    "en",
				UIVersion: 6,
				State:     ext.EncodeState(pageState{Mode: "form", DraftID: "draft-1"}),
				Action:    "review-save",
				Values: map[string]string{
					"url":        "https://example.com/article",
					"collection": "-1",
					"note":       strings.Repeat("note line\n", 350),
				},
			},
		})
		if err != nil {
			t.Fatalf("review-save: %v", err)
		}
		if err := wire.CheckView(res.View); err != nil {
			t.Fatalf("CheckView: %v", err)
		}
	})

	t.Run("short_control", func(t *testing.T) {
		v := a.detailView(
			&ext.UIRequest{UIParams: wire.UIParams{Locale: "en", UIVersion: 6}},
			pageState{Mode: "detail", Bookmark: 3},
			raindrop{
				ID:         3,
				Title:      "Short bookmark",
				Domain:     "example.com",
				Link:       "https://example.com/short",
				Note:       "Short note",
				Highlights: []highlight{{Text: "Short quote"}},
			},
		)
		if err := wire.CheckView(v); err != nil {
			t.Fatalf("CheckView: %v", err)
		}
	})
}
