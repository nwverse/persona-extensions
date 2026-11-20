package main

import (
	"testing"

	"github.com/nwverse/persona/sdk/go/ext"
	"github.com/nwverse/persona/sdk/go/wire"
)

// The fake's first items_page answer holds only item 42, so an item that is on a later page can be reached only by its ID.
func TestOpenItemThatIsNotOnTheFirstPage(t *testing.T) {
	a := newFakeMonday(t).app()
	r := &ext.UIRequest{UIParams: wire.UIParams{Locale: "en", UIVersion: 3}}
	v, err := a.page(t.Context(), r, pageState{Mode: "detail", BoardID: 7, BoardName: "Reading", ItemID: 60})
	if err != nil {
		t.Fatal(err)
	}
	if v.Title != "Item 60" {
		t.Fatalf("title %q", v.Title)
	}
	if err := wire.CheckView(v); err != nil {
		t.Fatal(err)
	}
}

// An ID that belongs to another board or to no item must not open in this board's view.
func TestOpenItemOfAnotherBoardOrUnknownFails(t *testing.T) {
	a := newFakeMonday(t).app()
	r := &ext.UIRequest{UIParams: wire.UIParams{Locale: "en", UIVersion: 3}}
	for _, id := range []int64{61, 99} {
		_, err := a.page(t.Context(), r, pageState{Mode: "detail", BoardID: 7, BoardName: "Reading", ItemID: id})
		if err == nil {
			t.Fatalf("item %d opened", id)
		}
	}
}
