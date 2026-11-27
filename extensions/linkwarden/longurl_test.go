package main

import (
	"fmt"
	"strings"
	"testing"

	"github.com/nwverse/persona/sdk/go/ext"
	"github.com/nwverse/persona/sdk/go/wire"
)

// A saved address can be long (a search result, a map, a signed download link, a page with tracking parameters), and
// the list shows it as the row's subtitle. The view check refuses a string of more than 2000 characters, and with it
// the whole list.
func TestListWithAVeryLongAddressOpens(t *testing.T) {
	long := "https://example.com/search?q=" + strings.Repeat("a", 2500)
	a := serveNameless(t, fmt.Sprintf(`[{"id":1,"name":"A search","url":%q},{"id":2,"name":"Short","url":"https://example.com/short"}]`, long))
	v, err := a.page(t.Context(), &ext.UIRequest{UIParams: wire.UIParams{Locale: "en", UIVersion: 3}}, pageState{Mode: "list"})
	if err != nil {
		t.Fatal(err)
	}
	if err := wire.CheckView(v); err != nil {
		t.Fatal(err)
	}
	for _, c := range v.Sections[0].Items {
		if c.Type == wire.ComponentRow && c.Title == "Short" && c.Subtitle != "https://example.com/short" {
			t.Fatalf("a short address is changed: %q", c.Subtitle)
		}
	}
}

// The address the user is about to save is shown for review as it is, up to the 8192 characters the extension accepts.
func TestReviewOfAVeryLongAddressOpens(t *testing.T) {
	long := "https://example.com/search?q=" + strings.Repeat("a", 2500)
	v := reviewSaveView(&ext.UIRequest{UIParams: wire.UIParams{Locale: "en", UIVersion: 3}}, pageState{Mode: "review-save", SaveURL: long})
	if err := wire.CheckView(v); err != nil {
		t.Fatal(err)
	}
}
