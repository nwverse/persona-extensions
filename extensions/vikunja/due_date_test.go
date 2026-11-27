package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nwverse/persona/sdk/go/ext"
	"github.com/nwverse/persona/sdk/go/wire"
)

// Vikunja's due_date is a non-pointer time.Time, so a task with no due date is answered with the zero time
// 0001-01-01T00:00:00Z, not null (pkg/models/tasks.go; the web client's parseDateOrNull reads year 1 as "no date").
func undatedVikunja(t *testing.T) *app {
	t.Helper()
	undated := map[string]any{"id": 42, "identifier": "READ-42", "title": "Review reading queue", "description": "Keep the source context.",
		"done": false, "due_date": "0001-01-01T00:00:00Z", "project_id": 7, "assignees": []any{}, "comments": []any{}, "updated": "2026-11-10T10:00:00Z"}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/user":
			_ = json.NewEncoder(w).Encode(user{ID: 1, Username: "reader", Name: "Reader"})
		case "/api/v1/tasks":
			_ = json.NewEncoder(w).Encode([]any{undated})
		case "/api/v1/tasks/42":
			_ = json.NewEncoder(w).Encode(undated)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return &app{client: newClient(srv.URL, testToken), receipts: newWriteStore("")}
}

func TestTaskToolsSayNoneForUndatedTask(t *testing.T) {
	a := undatedVikunja(t)
	for name, args := range map[string]string{"vikunja_tasks": `{}`, "vikunja_task": `{"task_id":42}`} {
		text, err := a.callTool(t.Context(), name, json.RawMessage(args))
		if err != nil || !strings.Contains(text, "due: none |") || strings.Contains(text, "0001-01-01") {
			t.Fatalf("%s: %v\n%s", name, err, text)
		}
	}
}

func TestTaskViewsShowNoDueDateForUndatedTask(t *testing.T) {
	a := undatedVikunja(t)
	r := &ext.UIRequest{UIParams: wire.UIParams{Locale: "en", UIVersion: 6}}
	list, err := a.page(t.Context(), r, pageState{})
	if err != nil {
		t.Fatal(err)
	}
	detail, err := a.page(t.Context(), r, pageState{Mode: "detail", TaskID: 42})
	if err != nil {
		t.Fatal(err)
	}
	var shown []string
	for _, v := range []*wire.View{list, detail} {
		for _, section := range v.Sections {
			for _, item := range section.Items {
				shown = append(shown, item.Title, item.Subtitle, item.Text)
			}
		}
	}
	text := strings.Join(shown, "\n")
	if strings.Contains(text, "0001-01-01") || !strings.Contains(text, "READ-42") {
		t.Fatalf("the views show the zero date or lose the identifier:\n%s", text)
	}
	for _, line := range shown {
		if strings.Contains(line, "READ-42 ·") || strings.HasSuffix(strings.TrimSpace(line), "·") {
			t.Fatalf("a dangling separator is left in %q", line)
		}
	}
}
