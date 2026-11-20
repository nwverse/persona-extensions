package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/nwverse/persona/sdk/go/ext"
	"github.com/nwverse/persona/sdk/go/wire"
)

// officialPageRead answers GET /api/pages/42 with the keys of BookStack's own example (dev/api/responses/pages-read.json):
// no url and no book.
func officialPageRead(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/api/pages/42" {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"id": 42, "book_id": 7, "chapter_id": nil, "name": "Reading context", "slug": "reading-context",
		"html": "<h1>Reading context</h1>", "raw_html": "<h1>Reading context</h1>", "priority": 13,
		"created_at": "2026-11-10T09:00:00.000000Z", "updated_at": "2026-11-10T10:00:00.000000Z",
		"draft": false, "markdown": "# Reading context\n\nKeep source context beside the next step.",
		"revision_count": 5, "template": false, "editor": "markdown", "tags": []any{},
	})
}

func pageViewLinks(t *testing.T, a *app) []string {
	t.Helper()
	r := &ext.UIRequest{UIParams: wire.UIParams{Locale: "en", UIVersion: 6}}
	v, err := a.page(t.Context(), r, pageState{Mode: "detail", PageID: 42})
	if err != nil {
		t.Fatal(err)
	}
	var links []string
	for _, section := range v.Sections {
		for _, item := range section.Items {
			if item.Type == wire.ComponentLink {
				links = append(links, item.URL)
			}
		}
	}
	return links
}

// A page read from the API has no address of its own, so the page view opens the instance's /link/{id} permalink,
// which BookStack redirects to the page.
func TestPageViewLinksToThePermalink(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(officialPageRead))
	t.Cleanup(srv.Close)
	a := &app{client: newClient(srv.URL, testTokenID, testTokenSecret), receipts: newWriteStore("")}
	a.client.http = srv.Client()
	links := pageViewLinks(t, a)
	if len(links) != 1 || links[0] != srv.URL+"/link/42" {
		t.Fatalf("links %v, want [%s/link/42]", links, srv.URL)
	}
}

// The link is shown only for an HTTPS address.
func TestPageViewHasNoLinkOnPlainHTTP(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(officialPageRead))
	t.Cleanup(srv.Close)
	a := &app{client: newClient(srv.URL, testTokenID, testTokenSecret), receipts: newWriteStore("")}
	if links := pageViewLinks(t, a); len(links) != 0 {
		t.Fatalf("links %v", links)
	}
}
