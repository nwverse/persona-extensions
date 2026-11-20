package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Zulip lists a stream's topics at GET /users/me/{stream_id}/topics (get-stream-topics); there is no such thing under
// /users/me/subscriptions/{stream_id}, which only has PATCH.
func TestTopicsAreReadFromZulipsTopicsEndpoint(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/users/me/7/topics" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"result": "success", "topics": []topic{{Name: "source context", MaxID: 42}}})
	}))
	defer srv.Close()
	topics, err := newClient(srv.URL, testEmail, testToken).topics(t.Context(), 7)
	if err != nil {
		t.Fatal(err)
	}
	if len(topics) != 1 || topics[0].Name != "source context" {
		t.Fatalf("topics %+v", topics)
	}
}
