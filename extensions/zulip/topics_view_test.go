package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/nwverse/persona/sdk/go/ext"
	"github.com/nwverse/persona/sdk/go/wire"
)

// Zulip answers every topic of a stream at once, the most recent first, and a busy stream has thousands of them (a
// topic for each thread). The view is refused above wire.ItemsMost components.
func TestStreamWithManyTopicsFitsTheView(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/users/me/7/topics" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var topics []topic
		for i := 1; i <= 1200; i++ {
			topics = append(topics, topic{Name: fmt.Sprintf("thread %d", i), MaxID: int64(5000 - i)})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"result": "success", "topics": topics})
	}))
	defer srv.Close()
	a := &app{client: newClient(srv.URL, testEmail, testToken)}
	v, err := a.page(t.Context(), &ext.UIRequest{UIParams: wire.UIParams{Locale: "en", UIVersion: 3}}, pageState{Mode: "topics", StreamID: 7})
	if err != nil {
		t.Fatal(err)
	}
	if err := wire.CheckView(v); err != nil {
		t.Fatal(err)
	}
	if n, first := len(v.Sections[0].Items), v.Sections[0].Items[0].Title; n < 200 || first != "thread 1" {
		t.Fatalf("the most recent topics are not kept: %d rows from %q", n, first)
	}
}
