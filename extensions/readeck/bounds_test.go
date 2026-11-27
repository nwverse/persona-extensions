package main

import (
	"strings"
	"testing"

	"github.com/nwverse/persona/sdk/go/ext"
	"github.com/nwverse/persona/sdk/go/wire"
)

func TestDetailViewCapsArticleNoteAndHighlightsTogether(t *testing.T) {
	t.Run("article_note_and_many_highlights", func(t *testing.T) {
		item := sampleBookmark()
		item.Note = strings.Repeat("note line\n", 120)
		var highlights []highlight
		for range 4 {
			highlights = append(highlights, highlight{Text: strings.Repeat("quote line\n", 80)})
		}
		v := detailView(
			&ext.UIRequest{UIParams: wire.UIParams{Locale: "en", UIVersion: 6}},
			pageState{Mode: "detail", ID: "bm_42", DraftID: "draft-1"},
			item,
			strings.Repeat("article line\n", 120),
			highlights,
		)
		if err := wire.CheckView(v); err != nil {
			t.Fatalf("CheckView: %v", err)
		}
	})

	t.Run("short_control", func(t *testing.T) {
		v := detailView(
			&ext.UIRequest{UIParams: wire.UIParams{Locale: "en", UIVersion: 6}},
			pageState{Mode: "detail", ID: "bm_42", DraftID: "draft-1"},
			sampleBookmark(),
			"Short article",
			[]highlight{{Text: "Short quote"}},
		)
		if err := wire.CheckView(v); err != nil {
			t.Fatalf("CheckView: %v", err)
		}
	})
}
