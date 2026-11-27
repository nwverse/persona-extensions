package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sync"
	"testing"
)

// TestCreateSendsTheUsersLocalTime uses a server that takes only what Kimai's REST API documents for the begin and end
// of a timesheet: the HTML5 local date and time (2006-01-02T15:04:05, no offset), read as the time in the timezone of
// the Kimai user.
func TestCreateSendsTheUsersLocalTime(t *testing.T) {
	html5 := regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}$`)
	var mu sync.Mutex
	var created []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/users/me":
			_ = json.NewEncoder(w).Encode(map[string]any{"id": 1, "username": "reader", "timezone": "Europe/Berlin"})
		case r.Method == http.MethodPost && r.URL.Path == "/api/timesheets":
			var in map[string]any
			if json.NewDecoder(r.Body).Decode(&in) != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			for _, key := range []string{"begin", "end"} {
				if text, _ := in[key].(string); !html5.MatchString(text) {
					w.WriteHeader(http.StatusBadRequest)
					return
				}
			}
			mu.Lock()
			created = append(created, in)
			mu.Unlock()
			item := sampleTimesheet()
			item.ID = 43
			_ = json.NewEncoder(w).Encode(item)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	a := &app{client: newClient(srv.URL, testToken), receipts: newWriteStore("")}
	args := []byte(`{"project_id":7,"activity_id":3,"begin":"2026-11-10T10:00:00+08:00","end":"2026-11-10T11:00:00+08:00","description":"Review context"}`)
	if _, err := a.doAction(t.Context(), "create-1", "kimai_create_timesheet", args); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(created) != 1 || created[0]["begin"] != "2026-11-10T03:00:00" || created[0]["end"] != "2026-11-10T04:00:00" {
		t.Fatalf("wrong times: %v", created)
	}
}
