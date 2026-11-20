package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// TestApproveSetsTheReviewEvent uses a server that makes a review an approval only when the request's event is
// APPROVED, as Gitea does (CreatePullReviewOptions has event, not state); any other review is a pending draft.
func TestApproveSetsTheReviewEvent(t *testing.T) {
	var mu sync.Mutex
	approved, pending := 0, 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/repos/reading/notes/pulls/42/reviews" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var in struct {
			Event string `json:"event"`
		}
		if json.NewDecoder(r.Body).Decode(&in) != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		state := "PENDING"
		mu.Lock()
		if in.Event == "APPROVED" {
			state = "APPROVED"
			approved++
		} else {
			pending++
		}
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"id": 1, "state": state})
	}))
	t.Cleanup(srv.Close)
	a := &app{client: newClient(srv.URL, testToken), receipts: newWriteStore("")}
	if _, err := a.doAction(t.Context(), "approve-1", "gitea_approve", []byte(`{"owner":"reading","repo":"notes","number":42}`)); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if approved != 1 || pending != 0 {
		t.Fatalf("approved %d, pending %d", approved, pending)
	}
}
