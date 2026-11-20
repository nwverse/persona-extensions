package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// TestArchiveSendsPUT uses a server that handles /links/{id}/archive for PUT only, as Linkwarden does.
func TestArchiveSendsPUT(t *testing.T) {
	var mu sync.Mutex
	archived := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/links/101/archive" || r.Method != http.MethodPut {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		mu.Lock()
		archived++
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"response":"Link is being archived."}`)
	}))
	t.Cleanup(srv.Close)
	if err := newClient(srv.URL, testKey).archive(t.Context(), 101); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if archived != 1 {
		t.Fatalf("archived %d times", archived)
	}
}
