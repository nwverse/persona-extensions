package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/nwverse/persona/sdk/go/ext"
	"github.com/nwverse/persona/sdk/go/wire"
)

func TestDetailViewHandlesHTTPBookmarkAndUnmatchedHighlights(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/bookmarks/http-bm":
			fmt.Fprint(w, `{"id":"http-bm","title":"HTTP article","type":"link","url":"http://example.com/article","content":"Saved body"}`)
		case "/bookmarks/plain-bm":
			fmt.Fprint(w, `{"id":"plain-bm","title":"Plain article","type":"link","url":"https://example.com/plain","content":"Saved body"}`)
		case "/bookmarks/https-bm":
			fmt.Fprint(w, `{"id":"https-bm","title":"HTTPS article","type":"link","url":"https://example.com/https","content":"Saved body"}`)
		case "/highlights":
			fmt.Fprint(w, `{"bookmarks":[{"id":"h1","bookmarkId":"http-bm","text":"Highlighted HTTP"},{"id":"h2","bookmarkId":"https-bm","text":"Highlighted HTTPS"}],"nextCursor":null}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	a := &app{client: newClient(srv.URL, testKey), receipts: newWriteStore("")}

	for _, tc := range []struct {
		name          string
		id            string
		wantLink      bool
		wantHighlight bool
	}{
		{"http bookmark", "http-bm", false, true},
		{"unmatched highlights", "plain-bm", true, false},
		{"https bookmark with highlight", "https-bm", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &ext.UIRequest{UIParams: wire.UIParams{Locale: "zh-Hans", UIVersion: 6, Action: "detail", Values: map[string]string{"id": tc.id}}}
			res, err := a.action(t.Context(), r)
			if err != nil || res.View == nil {
				t.Fatalf("detail: %+v, %v", res, err)
			}
			if err := wire.CheckView(res.View); err != nil {
				t.Fatalf("CheckView: %v", err)
			}
			hasLink := slices.ContainsFunc(res.View.Sections[0].Items, func(c wire.Component) bool {
				return c.Type == wire.ComponentLink
			})
			if hasLink != tc.wantLink {
				t.Fatalf("hasLink=%v, want %v", hasLink, tc.wantLink)
			}
			if got := len(res.View.Sections) == 3; got != tc.wantHighlight {
				t.Fatalf("hasHighlightSection=%v, want %v (sections=%d)", got, tc.wantHighlight, len(res.View.Sections))
			}
		})
	}
}
