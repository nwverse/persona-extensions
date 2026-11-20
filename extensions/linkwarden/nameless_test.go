package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/nwverse/persona/sdk/go/ext"
	"github.com/nwverse/persona/sdk/go/wire"
)

// serveNameless answers the list with the given links, as Linkwarden does for a link saved by its address alone and
// not named yet, or from a page without a title: name is an empty string (the default of the field).
func serveNameless(t *testing.T, links string) *app {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/search":
			fmt.Fprintf(w, `{"data":{"links":%s,"nextCursor":null},"success":true}`, links)
		case "/collections":
			fmt.Fprint(w, `{"response":[{"id":5,"name":"Research"},{"id":8,"name":"Later"}]}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return &app{client: newClient(srv.URL, testKey), receipts: newWriteStore("")}
}

// A row needs a title, and the view check refuses the whole list for one without: a link not named yet makes the list
// unreadable until its title is fetched, or for good when the page has none.
func TestListWithAnUnnamedLinkOpens(t *testing.T) {
	a := serveNameless(t, `[{"id":1,"name":"","url":"https://example.com/untitled","description":""},{"id":2,"name":"","url":"https://example.com/described","description":"A page about boilers"},{"id":3,"name":"Named","url":"https://example.com/named"}]`)
	v, err := a.page(t.Context(), &ext.UIRequest{UIParams: wire.UIParams{Locale: "en", UIVersion: 3}}, pageState{Mode: "list"})
	if err != nil {
		t.Fatal(err)
	}
	if err := wire.CheckView(v); err != nil {
		t.Fatal(err)
	}
	var titles []string
	for _, c := range v.Sections[0].Items {
		if c.Type == wire.ComponentRow {
			titles = append(titles, c.Title)
		}
	}
	want := []string{"https://example.com/untitled", "A page about boilers", "Named"}
	if fmt.Sprint(titles) != fmt.Sprint(want) {
		t.Fatalf("titles %q, want %q", titles, want)
	}
}
