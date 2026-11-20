package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/nwverse/persona/sdk/go/ext"
	"github.com/nwverse/persona/sdk/go/wire"
)

// A fake Reader that answers one document whose source_url is raw.
func readerWithSource(t *testing.T, raw string) *app {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/list/" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.URL.Query().Get("category") == "highlight" {
			_, _ = w.Write([]byte(`{"count":0,"nextPageCursor":null,"results":[]}`))
			return
		}
		_, _ = fmt.Fprintf(w, `{"count":1,"nextPageCursor":null,"results":[{"id":"doc-1","url":"https://read.readwise.io/read/doc-1","source_url":%q,"title":"An old article","author":"Example Author","site_name":"Example","reading_progress":0.1,"html_content":"<article><p>First paragraph.</p></article>"}]}`, raw)
	}))
	t.Cleanup(srv.Close)
	return &app{client: newClient(srv.URL, testToken), receipts: newWriteStore("")}
}

// The view check takes a link only when it is https with a host, and refuses the whole view otherwise; many saved pages
// are plain http, and a document without a page has no source_url.
func TestDocumentDetailOpensWithoutAnHTTPSLink(t *testing.T) {
	for _, link := range []string{"http://example.com/old", "", "ftp://example.com/old", "https://"} {
		a := readerWithSource(t, link)
		v, err := a.page(t.Context(), &ext.UIRequest{UIParams: wire.UIParams{Locale: "en", UIVersion: 3}}, pageState{Mode: "detail", DocumentID: "doc-1"})
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

func TestDocumentDetailKeepsAnHTTPSLink(t *testing.T) {
	a := readerWithSource(t, "https://example.com/new")
	v, err := a.page(t.Context(), &ext.UIRequest{UIParams: wire.UIParams{Locale: "en", UIVersion: 3}}, pageState{Mode: "detail", DocumentID: "doc-1"})
	if err != nil {
		t.Fatal(err)
	}
	if err := wire.CheckView(v); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, c := range v.Sections[0].Items {
		found = found || c.Type == wire.ComponentLink && c.URL == "https://example.com/new"
	}
	if !found {
		t.Fatal("the https link is gone")
	}
}
