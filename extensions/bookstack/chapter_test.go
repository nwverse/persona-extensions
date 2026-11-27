package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// BookStack answers a page list item's chapter_id as the chapter's integer id, or null for a page that sits directly
// in its book (BookStack pages-list.json and pages-read.json). The tool output names the id or says there is none; it
// must never print the address of the decoded pointer.
func TestPagesToolNamesChapterID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/api/pages" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{
			{"id": 1, "book_id": 7, "chapter_id": 3, "name": "In a chapter", "slug": "in-a-chapter", "draft": false, "updated_at": "2026-11-10T10:00:00.000000Z"},
			{"id": 2, "book_id": 7, "chapter_id": nil, "name": "In the book", "slug": "in-the-book", "draft": false, "updated_at": "2026-11-10T10:00:00.000000Z"},
		}, "total": 2})
	}))
	t.Cleanup(srv.Close)
	a := &app{client: newClient(srv.URL, testTokenID, testTokenSecret), receipts: newWriteStore("")}
	text, err := a.callTool(t.Context(), "bookstack_pages", json.RawMessage(`{"book_id":7}`))
	if err != nil || !strings.Contains(text, "chapter ID: 3 |") || !strings.Contains(text, "chapter ID: none |") || strings.Contains(text, "0x") {
		t.Fatalf("%v\n%s", err, text)
	}
}
