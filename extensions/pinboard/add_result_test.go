package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// resultCodePinboard answers posts/add with the JSON form clients read from Pinboard: a top-level `result_code`
// ("done", or a message such as "item already exists" when replace=no meets a URL that is already saved). Pinboard's
// documentation shows only the XML form <result code="done" /> and calls the JSON form its "equivalent".
func resultCodePinboard(t *testing.T) *httptest.Server {
	t.Helper()
	var mu sync.Mutex
	saved := map[string]bool{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if r.URL.Path != "/posts/add" || q.Get("auth_token") != testToken || q.Get("format") != "json" || q.Get("url") == "" || q.Get("description") == "" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		mu.Lock()
		code := "done"
		if saved[q.Get("url")] && q.Get("replace") == "no" {
			code = "item already exists"
		}
		saved[q.Get("url")] = true
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"result_code": code})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestAddReadsTheTopLevelResultCode(t *testing.T) {
	c := newClient(resultCodePinboard(t).URL, testToken)
	if err := c.add(t.Context(), "https://articles.example.com/context", "Reading context", "", ""); err != nil {
		t.Fatalf("a result_code of done was not accepted: %v", err)
	}
}

func TestAddReportsAResultCodeThatIsNotDone(t *testing.T) {
	c := newClient(resultCodePinboard(t).URL, testToken)
	if err := c.add(t.Context(), "https://articles.example.com/context", "Reading context", "", ""); err != nil {
		t.Fatalf("first add: %v", err)
	}
	if err := c.add(t.Context(), "https://articles.example.com/context", "Reading context", "", ""); err == nil {
		t.Fatal("an add answered with result_code item already exists was reported as saved")
	}
}
