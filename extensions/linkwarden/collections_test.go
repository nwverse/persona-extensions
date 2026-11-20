package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/nwverse/persona/sdk/go/ext"
	"github.com/nwverse/persona/sdk/go/wire"
)

// serveCollections answers /collections with n collections (IDs 1 to n) and an empty search.
func serveCollections(t *testing.T, n int) *app {
	t.Helper()
	var list []string
	for i := 1; i <= n; i++ {
		list = append(list, fmt.Sprintf(`{"id":%d,"name":"Collection %d"}`, i, i))
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/search":
			fmt.Fprint(w, `{"data":[],"success":true}`)
		case "/collections":
			fmt.Fprintf(w, `{"response":[%s]}`, strings.Join(list, ","))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return &app{client: newClient(srv.URL, testKey), receipts: newWriteStore("")}
}

// The collection filter is a drop-down of at most 100 choices, and a Linkwarden account with nested collections can
// have more than 99.
func TestListOfAnAccountWithManyCollectionsOpens(t *testing.T) {
	a := serveCollections(t, 150)
	v, err := a.page(t.Context(), &ext.UIRequest{UIParams: wire.UIParams{Locale: "en", UIVersion: 3}}, pageState{Mode: "list"})
	if err != nil {
		t.Fatal(err)
	}
	if err := wire.CheckView(v); err != nil {
		t.Fatal(err)
	}
	if v.Filter == nil || v.Filter.Choices[1].Title != "Collection 1" {
		t.Fatalf("the filter is lost or does not start at the first collection: %+v", v.Filter)
	}
}

// The collection being looked at stays in the drop-down, wherever it is among the collections.
func TestSelectedCollectionBeyondTheFilterStaysChosen(t *testing.T) {
	a := serveCollections(t, 150)
	v, err := a.page(t.Context(), &ext.UIRequest{UIParams: wire.UIParams{Locale: "en", UIVersion: 3}}, pageState{Mode: "list", CollectionID: 140})
	if err != nil {
		t.Fatal(err)
	}
	if err := wire.CheckView(v); err != nil {
		t.Fatal(err)
	}
	if v.Filter == nil || v.Filter.Value != strconv.Itoa(140) {
		t.Fatalf("the selected collection is not the filter's value: %+v", v.Filter)
	}
}
