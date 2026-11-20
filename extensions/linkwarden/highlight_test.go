package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestHighlightNoteIsReadFromComment uses a server that answers a link's highlights with the fields Linkwarden stores
// for one: the note is comment, and the offsets are startOffset and endOffset.
func TestHighlightNoteIsReadFromComment(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/links/101":
			fmt.Fprint(w, `{"response":{"id":101,"name":"Reading with context","url":"https://example.com/article"}}`)
		case "/links/101/highlights":
			fmt.Fprint(w, `{"response":[{"id":11,"color":"yellow","comment":"A note on the passage","linkId":101,"userId":1,"startOffset":3,"endOffset":24,"text":"A highlighted passage"}]}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	items, err := newClient(srv.URL, testKey).highlights(t.Context(), 101)
	if err != nil || len(items) != 1 || items[0].Note != "A note on the passage" {
		t.Fatalf("%v %+v", err, items)
	}
	a := &app{client: newClient(srv.URL, testKey), receipts: newWriteStore("")}
	text, err := a.callTool(t.Context(), "linkwarden_read", []byte(`{"id":101}`))
	if err != nil || !strings.Contains(text, "A highlighted passage") || !strings.Contains(text, "A note on the passage") {
		t.Fatalf("%v %s", err, text)
	}
}
