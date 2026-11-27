package main

import (
	"fmt"
	"strings"
	"testing"

	"github.com/nwverse/persona/sdk/go/ext"
	"github.com/nwverse/persona/sdk/go/wire"
)

func manyParagraphs(n int) string {
	var b strings.Builder
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&b, "<p>Paragraph %d of the article.</p>", i)
	}
	return b.String()
}

func highlightsOf(n int) []document {
	var out []document
	for i := 1; i <= n; i++ {
		out = append(out, document{ID: fmt.Sprint("h", i), Content: fmt.Sprintf("highlight %d", i), Notes: fmt.Sprintf("note %d", i)})
	}
	return out
}

// A heavily highlighted long article has hundreds of highlights beside its text, and the view is refused above
// wire.ItemsMost components.
func TestDocumentWithManyHighlightsFitsTheView(t *testing.T) {
	item := document{ID: "doc-1", Title: "Long read", SourceURL: "https://example.com/article", HTMLContent: manyParagraphs(140)}
	v, err := detailView(&ext.UIRequest{UIParams: wire.UIParams{Locale: "en", UIVersion: 3}}, pageState{Mode: "detail", DocumentID: "doc-1"}, item, highlightsOf(150))
	if err != nil {
		t.Fatal(err)
	}
	if err := wire.CheckView(v); err != nil {
		t.Fatal(err)
	}
	var highlights []wire.Component
	for _, s := range v.Sections {
		if s.Title == textsFor("en").highlights {
			highlights = s.Items
		}
	}
	if len(highlights) < 100 || highlights[0].Text != "highlight 1" {
		t.Fatalf("the highlights are not kept from the first: %d", len(highlights))
	}
	if last := highlights[len(highlights)-1].Text; !strings.HasSuffix(last, "[…]") {
		t.Fatalf("the cut is not shown: %q", last)
	}
}

// Notes of hundreds of lines leave room for the highlights and the text and fit too.
func TestDocumentWithLongNotesFitsTheView(t *testing.T) {
	item := document{ID: "doc-1", Title: "Long read", SourceURL: "https://example.com/article", HTMLContent: manyParagraphs(140), Notes: strings.Repeat("note\n", 500)}
	v, err := detailView(&ext.UIRequest{UIParams: wire.UIParams{Locale: "en", UIVersion: 3}}, pageState{Mode: "detail", DocumentID: "doc-1"}, item, highlightsOf(10))
	if err != nil {
		t.Fatal(err)
	}
	if err := wire.CheckView(v); err != nil {
		t.Fatal(err)
	}
}
