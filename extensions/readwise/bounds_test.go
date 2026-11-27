package main

import (
	"strings"
	"testing"

	"github.com/nwverse/persona/sdk/go/ext"
	"github.com/nwverse/persona/sdk/go/wire"
)

func TestEmptyDetailSectionsUntitledRowAndQueryBounds(t *testing.T) {
	t.Run("normal detailView control", func(t *testing.T) {
		v, err := detailView(&ext.UIRequest{UIParams: wire.UIParams{Locale: "en", UIVersion: 6}}, pageState{Mode: "detail", DocumentID: "doc-1"}, document{
			ID:          "doc-1",
			Title:       "Control article",
			SourceURL:   "https://example.com/article",
			HTMLContent: "<article><p>Paragraph.</p></article>",
		}, nil)
		if err != nil || wire.CheckView(v) != nil {
			t.Fatalf("control detailView failed: %v", err)
		}
	})

	t.Run("detailView with blank notes, highlights, and untitled document passes CheckView", func(t *testing.T) {
		v, err := detailView(&ext.UIRequest{UIParams: wire.UIParams{Locale: "zh-Hans", UIVersion: 6}}, pageState{Mode: "detail", DocumentID: "doc-blank"}, document{
			ID:          "doc-blank",
			Title:       "",
			SourceURL:   "https://example.com/snippet",
			Notes:       "\n\n",
			HTMLContent: "<article><p>\n</p></article>",
		}, []document{{ID: "h1", Content: "\n", Notes: ""}})
		if err != nil {
			t.Fatalf("detailView failed: %v", err)
		}
		if err := wire.CheckView(v); err != nil {
			t.Fatalf("detailView failed CheckView: %v", err)
		}
	})

	t.Run("untitled queue row and long search query pass CheckView", func(t *testing.T) {
		row := documentRow(document{ID: "doc-untitled", SourceURL: "https://example.com/note"})
		if err := wire.CheckView(&wire.View{Sections: []wire.Section{{Items: []wire.Component{row}}}}); err != nil {
			t.Fatalf("untitled documentRow failed CheckView: %v", err)
		}

		f := newFakeReader(t)
		res, err := f.app().action(t.Context(), &ext.UIRequest{UIParams: wire.UIParams{
			Locale:    "en",
			UIVersion: 6,
			Action:    "search",
			Values:    map[string]string{"q": strings.Repeat("阅", 6000)},
		}})
		if err != nil || res.View == nil {
			t.Fatalf("search failed: %v %+v", err, res)
		}
		if err := wire.CheckView(res.View); err != nil {
			t.Fatalf("search with long query failed CheckView: %v", err)
		}
	})
}
