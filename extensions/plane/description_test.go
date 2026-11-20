package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// serverWorkItems serves one project's work items the way Plane's work item serializer does: the description is the
// `description_html` field, in and out. `description_stripped` is derived by the server from the HTML and is not
// part of the API: it is ignored in a request and absent from an answer.
func serverWorkItems(t *testing.T) *app {
	t.Helper()
	var mu sync.Mutex
	descriptionHTML := "<p></p>"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-API-Key") != testToken {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		item := func() map[string]any {
			return map[string]any{"id": "item-1", "name": "Keep reading context", "description_html": descriptionHTML, "sequence_id": 42, "priority": "none", "state": "state-backlog", "project": "project-1", "updated_at": "2026-11-10T10:00:00Z"}
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/workspaces/personal/projects/project-1/work-items/" && r.Method == http.MethodPost:
			var in map[string]any
			if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			if html, ok := in["description_html"].(string); ok {
				descriptionHTML = html
			}
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(item())
		case r.URL.Path == "/workspaces/personal/projects/project-1/work-items/item-1/":
			_ = json.NewEncoder(w).Encode(item())
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return &app{client: newClient(srv.URL, testToken), workspace: "personal", receipts: newWriteStore("")}
}

func TestCreatedDescriptionIsStoredAndRead(t *testing.T) {
	a := serverWorkItems(t)
	create := []byte(`{"project_id":"project-1","name":"Keep reading context","description":"Keep the source & the notes.\nSecond line."}`)
	if _, err := a.doAction(t.Context(), "create-1", "plane_create_work_item", create); err != nil {
		t.Fatal(err)
	}
	text, err := a.callTool(t.Context(), "plane_work_item", json.RawMessage(`{"project_id":"project-1","work_item_id":"item-1"}`))
	if err != nil || !strings.Contains(text, "Description:\nKeep the source & the notes.\nSecond line.\n") {
		t.Fatalf("%v, %s", err, text)
	}
}

func TestDescriptionIsReadFromTheHTMLField(t *testing.T) {
	a := serverWorkItems(t)
	if _, err := a.client.create(t.Context(), "personal", "project-1", map[string]any{"name": "x", "description_html": "<p>First &amp; second</p><p>Third<br>line</p>"}); err != nil {
		t.Fatal(err)
	}
	item, err := a.client.workItem(t.Context(), "personal", "project-1", "item-1")
	if err != nil {
		t.Fatal(err)
	}
	if got := itemDescription(item); got != "First & second\nThird\nline" {
		t.Fatalf("description %q", got)
	}
}
