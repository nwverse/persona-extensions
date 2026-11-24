package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/nwverse/persona/sdk/go/ext"
	"github.com/nwverse/persona/sdk/go/wire"
)

func TestSpaceLifecycleUsesCoreRecords(t *testing.T) {
	created := false
	var saved map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Error("the extension's hub credential was not used")
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/assistant/tracks":
			if r.Method == http.MethodPost {
				created = true
				_ = json.NewDecoder(r.Body).Decode(&saved)
				_, _ = w.Write([]byte(`{"slug":"reading","kind":"space","title":"阅读","revision":1,"config":{"sidebar":{"group":"spaces"}}}`))
				return
			}
			_, _ = w.Write([]byte(`{"tracks":[{"slug":"reading","kind":"space","title":"阅读","revision":1,"config":{"sidebar":{"group":"spaces"}}}]}`))
		case "/api/v1/assistant":
			_, _ = w.Write([]byte(`{"machine_id":"computer-example","conversations":[{"session_id":"session-example","title":"第一本书","space":"reading"}]}`))
		case "/api/v1/assistant/tracks/reading/space-md":
			_, _ = w.Write([]byte(`{"content":"# 阅读\n","digest":"digest-example"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	a := &App{client: NewClient(srv.URL, "test-token")}
	home, err := a.render(context.Background(), &ext.UIRequest{UIParams: wire.UIParams{Contribution: "home"}})
	if err != nil || len(home.Sections[0].Items) != 2 {
		t.Fatalf("home = %#v, %v", home, err)
	}
	createdResult, err := a.action(context.Background(), &ext.UIRequest{UIParams: wire.UIParams{Action: "create", Values: map[string]string{"title": "阅读", "slug": "reading", "description": "想读的书"}}})
	if err != nil || !created || createdResult.Push.State != "reading" || saved["kind"] != "space" {
		t.Fatalf("create = %#v, %v, body %#v", createdResult, err, saved)
	}
	page, err := a.render(context.Background(), &ext.UIRequest{UIParams: wire.UIParams{Contribution: "space", State: "reading"}})
	if err != nil || len(page.Sections) != 3 {
		t.Fatalf("space = %#v, %v", page, err)
	}
	edit, err := a.render(context.Background(), &ext.UIRequest{UIParams: wire.UIParams{Contribution: "edit", State: "reading"}})
	if err != nil || edit.Sections[0].Items[1].Value != "# 阅读\n" {
		t.Fatalf("edit = %#v, %v", edit, err)
	}
}

func TestConversationLinkAndSlugValidation(t *testing.T) {
	for _, slug := range []string{"reading", "family-2"} {
		if !validSlug(slug) {
			t.Errorf("%q should be valid", slug)
		}
	}
	for _, slug := range []string{"", "new", "health", "2bad", "../secret", " spaces"} {
		if validSlug(slug) {
			t.Errorf("%q should be refused", slug)
		}
	}
	if got := conversationURL("computer-example", "reading", "session/example"); got != "persona://tracks/computer-example/reading/session%2Fexample" {
		t.Fatal(got)
	}
}
