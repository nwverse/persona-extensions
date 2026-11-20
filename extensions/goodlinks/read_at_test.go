package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// GoodLinks answers readAt as an ISO-8601 string, or null for a link that was not read. The tool output gives the
// timestamp or says there is none; it must never print the address of the decoded pointer.
func TestLinksToolNamesReadAt(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/api/v1/lists/unread" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{
			{"id": "read1", "url": "https://example.com/a", "title": "Read once", "tags": []string{}, "readAt": "2026-11-10T10:00:00Z"},
			{"id": "new1", "url": "https://example.com/b", "title": "Never read", "tags": []string{}, "readAt": nil},
		}, "hasMore": false})
	}))
	t.Cleanup(srv.Close)
	a := &app{client: newClient(srv.URL, testToken), receipts: newWriteStore("")}
	text, err := a.callTool(t.Context(), "goodlinks_links", json.RawMessage(`{"view":"unread"}`))
	if err != nil || !strings.Contains(text, "read at: 2026-11-10T10:00:00Z |") || !strings.Contains(text, "read at: none |") || strings.Contains(text, "0x") {
		t.Fatalf("%v\n%s", err, text)
	}
}
