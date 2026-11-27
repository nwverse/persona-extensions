package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/nwverse/persona/sdk/go/ext"
	"github.com/nwverse/persona/sdk/go/wire"
)

// A fake Readeck that answers one bookmark whose url is raw.
func readeckWithURL(t *testing.T, raw string) *app {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/bookmarks/bm_7":
			_, _ = fmt.Fprintf(w, `{"id":"bm_7","url":%q,"title":"An old article","site_name":"Old Site","has_article":false,"loaded":true,"reading_time":3}`, raw)
		case "/api/bookmarks/bm_7/annotations":
			_, _ = w.Write([]byte(`[]`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return &app{client: newClient(srv.URL, testToken), receipts: newWriteStore("")}
}

// The view check takes a link only when it is https with a host, and refuses the whole view otherwise; many saved
// articles are plain http, and the url may be empty.
func TestBookmarkDetailOpensWithoutAnHTTPSLink(t *testing.T) {
	for _, link := range []string{"http://articles.example.com/old", "", "ftp://articles.example.com/old"} {
		a := readeckWithURL(t, link)
		v, err := a.page(t.Context(), &ext.UIRequest{UIParams: wire.UIParams{Locale: "en", UIVersion: 3}}, pageState{Mode: "detail", ID: "bm_7"})
		if err != nil {
			t.Fatalf("link %q: %v", link, err)
		}
		if err := wire.CheckView(v); err != nil {
			t.Fatalf("link %q: %v", link, err)
		}
		for _, c := range v.Sections[0].Items {
			if c.Type == wire.ComponentLink {
				t.Fatalf("link %q: offered %q", link, c.URL)
			}
		}
	}
}

func TestBookmarkDetailKeepsAnHTTPSLink(t *testing.T) {
	a := readeckWithURL(t, "https://articles.example.com/new")
	v, err := a.page(t.Context(), &ext.UIRequest{UIParams: wire.UIParams{Locale: "en", UIVersion: 3}}, pageState{Mode: "detail", ID: "bm_7"})
	if err != nil {
		t.Fatal(err)
	}
	if err := wire.CheckView(v); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, c := range v.Sections[0].Items {
		found = found || c.Type == wire.ComponentLink && c.URL == "https://articles.example.com/new"
	}
	if !found {
		t.Fatal("the https link is gone")
	}
}
