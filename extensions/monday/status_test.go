package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"sync"
	"testing"
)

var changeMultiple = regexp.MustCompile(`change_multiple_column_values\(board_id: (\d+), item_id: (\d+), column_values: ("(?:[^"\\]|\\.)*")\)`)

// documentedStatusBoard accepts change_multiple_column_values the way monday's status-column reference documents it:
// column_values is a JSON string whose outer keys are column IDs, e.g. "{\"status\": {\"label\": \"Done\"}}". A key that
// is not a column of the board is an application error.
type documentedStatusBoard struct {
	srv     *httptest.Server
	mu      sync.Mutex
	applied map[string]any
}

func newDocumentedStatusBoard(t *testing.T, columns ...string) *documentedStatusBoard {
	t.Helper()
	f := &documentedStatusBoard{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Query string `json:"query"`
		}
		var m []string
		if json.NewDecoder(r.Body).Decode(&in) == nil {
			m = changeMultiple.FindStringSubmatch(in.Query)
		}
		if m == nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var text string
		var values map[string]any
		if json.Unmarshal([]byte(m[3]), &text) != nil || json.Unmarshal([]byte(text), &values) != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		for column := range values {
			if !slices.Contains(columns, column) {
				_ = json.NewEncoder(w).Encode(map[string]any{"errors": []map[string]string{{"message": "Column not found: " + column}}})
				return
			}
		}
		f.mu.Lock()
		f.applied = values
		f.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"change_multiple_column_values": map[string]any{"id": m[2], "name": "Review reading notes", "url": "https://example.monday.com/items/" + m[2], "column_values": []any{}}}})
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func TestStatusChangeKeysTheLabelByColumnID(t *testing.T) {
	for _, tc := range []struct{ column, label string }{{"status", "Done"}, {"color_mksreyj6", "Working on it"}} {
		f := newDocumentedStatusBoard(t, "status", "color_mksreyj6")
		c := newClient(f.srv.URL, testToken)
		if _, err := c.updateStatusColumn(t.Context(), 7, 42, tc.column, tc.label); err != nil {
			t.Fatalf("%s: %v", tc.column, err)
		}
		want := map[string]map[string]string{tc.column: {"label": tc.label}}
		f.mu.Lock()
		gotJSON, _ := json.Marshal(f.applied)
		f.mu.Unlock()
		wantJSON, _ := json.Marshal(want)
		if string(gotJSON) != string(wantJSON) {
			t.Fatalf("column_values %s, want %s", gotJSON, wantJSON)
		}
	}
}
