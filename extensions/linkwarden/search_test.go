package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// serveSearch answers GET /search as Linkwarden does: the text is the query parameter searchQueryString (q is not
// read, so a search without it gives the newest links) and the links are in data.links; data is an empty array when
// nothing matched.
func serveSearch(t *testing.T) *app {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/search" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		switch r.URL.Query().Get("searchQueryString") {
		case "":
			fmt.Fprint(w, `{"data":{"links":[{"id":1,"name":"Newest link","url":"https://example.com/newest"}],"nextCursor":null},"success":true,"message":"Success"}`)
		case "context":
			fmt.Fprint(w, `{"data":{"links":[{"id":101,"name":"Reading with context","url":"https://example.com/article","collection":{"id":5,"name":"Research"}}],"nextCursor":null},"success":true,"message":"Success"}`)
		default:
			fmt.Fprint(w, `{"data":[],"success":true,"message":"Nothing found."}`)
		}
	}))
	t.Cleanup(srv.Close)
	return &app{client: newClient(srv.URL, testKey), receipts: newWriteStore("")}
}

func TestSearchSendsSearchQueryStringAndReadsDataLinks(t *testing.T) {
	a := serveSearch(t)
	items, err := a.client.search(t.Context(), "context", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].ID != 101 {
		t.Fatalf("wrong links: %+v", items)
	}
}

func TestSearchWithNothingFoundIsEmpty(t *testing.T) {
	a := serveSearch(t)
	items, err := a.client.search(t.Context(), "nothing", 0)
	if err != nil || len(items) != 0 {
		t.Fatalf("%v %+v", err, items)
	}
}
