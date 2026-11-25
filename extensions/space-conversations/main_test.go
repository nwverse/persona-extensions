package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
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

func TestArchiveAndUnarchiveSendExplicitRequestedState(t *testing.T) {
	tests := []struct {
		name         string
		initialState bool
		action       string
		wantArchived bool
	}{
		{name: "archive active track", initialState: false, action: "archive", wantArchived: true},
		{name: "archive already archived track stays archived", initialState: true, action: "archive", wantArchived: true},
		{name: "unarchive archived track", initialState: true, action: "unarchive", wantArchived: false},
		{name: "unarchive already active track stays active", initialState: false, action: "unarchive", wantArchived: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var gotBody map[string]any
			archived := tc.initialState
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch {
				case r.URL.Path == "/api/v1/assistant/tracks" && r.Method == http.MethodGet:
					_ = json.NewEncoder(w).Encode(map[string]any{
						"tracks": []map[string]any{{
							"slug":     "reading",
							"kind":     "space",
							"title":    "阅读",
							"archived": archived,
							"revision": 3,
							"config":   map[string]any{"sidebar": map[string]any{"group": "spaces"}},
						}},
					})
				case r.URL.Path == "/api/v1/assistant/tracks/reading" && r.Method == http.MethodPut:
					if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
						t.Fatalf("decode PUT body: %v", err)
					}
					if v, ok := gotBody["archived"].(bool); ok {
						archived = v
					}
					w.WriteHeader(http.StatusOK)
				default:
					http.NotFound(w, r)
				}
			}))
			defer srv.Close()

			a := &App{client: NewClient(srv.URL, "test-token")}
			res, err := a.action(context.Background(), &ext.UIRequest{UIParams: wire.UIParams{
				Action: tc.action,
				Values: map[string]string{"slug": "reading"},
			}})
			if err != nil {
				t.Fatalf("action(%q) error: %v", tc.action, err)
			}
			got, ok := gotBody["archived"].(bool)
			if !ok || got != tc.wantArchived {
				t.Fatalf("PUT archived = %#v, want %v (full body: %#v)", gotBody["archived"], tc.wantArchived, gotBody)
			}
			if res.View == nil {
				t.Fatalf("expected refreshed home view, got nil")
			}
			if err := wire.CheckView(res.View); err != nil {
				t.Fatalf("home view invalid: %v", err)
			}
		})
	}
}

func TestArchivePropagatesHomeRefreshError(t *testing.T) {
	getCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/v1/assistant/tracks" && r.Method == http.MethodGet:
			getCount++
			if getCount == 1 {
				_, _ = w.Write([]byte(`{"tracks":[{"slug":"reading","kind":"space","title":"阅读","revision":1,"config":{"sidebar":{"group":"spaces"}}}]}`))
				return
			}
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":"database unavailable"}`))
		case r.URL.Path == "/api/v1/assistant/tracks/reading" && r.Method == http.MethodPut:
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	a := &App{client: NewClient(srv.URL, "test-token")}
	res, err := a.action(context.Background(), &ext.UIRequest{UIParams: wire.UIParams{
		Action: "archive",
		Values: map[string]string{"slug": "reading"},
	}})
	if err == nil {
		t.Fatalf("expected homeView refresh error to be returned, got res=%#v, err=nil", res)
	}
}

func TestRenderedViewsPassWireValidationAndPreserveState(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/assistant/tracks":
			tracks := []map[string]any{
				{
					"slug":        "no-desc",
					"kind":        "space",
					"title":       "无简介空间",
					"description": "",
					"revision":    1,
					"config":      map[string]any{"sidebar": map[string]any{"group": "spaces"}},
				},
				{
					"slug":        "reading",
					"kind":        "space",
					"title":       "",
					"description": "想读的书和笔记",
					"revision":    1,
					"config":      map[string]any{"sidebar": map[string]any{"group": "spaces"}},
				},
			}
			for i := 0; i < wire.ItemsMost+5; i++ {
				desc := ""
				if i == 0 {
					desc = strings.Repeat("长", wire.StringMost+50)
				}
				tracks = append(tracks, map[string]any{
					"slug":        fmt.Sprintf("extra-%d", i),
					"kind":        "space",
					"title":       fmt.Sprintf("空间 %d", i),
					"description": desc,
					"revision":    1,
					"config":      map[string]any{"sidebar": map[string]any{"group": "spaces"}},
				})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"tracks": tracks})
		case "/api/v1/assistant":
			convs := []map[string]any{
				{"session_id": "s-untitled", "title": "", "last_text": "第一条消息预览", "space": "no-desc"},
			}
			for i := 0; i < wire.ItemsMost+5; i++ {
				title := fmt.Sprintf("对话 %d", i)
				last := "短预览"
				if i == 0 {
					title = strings.Repeat("标", wire.StringMost+10)
					last = strings.Repeat("摘", wire.StringMost+10)
				}
				convs = append(convs, map[string]any{
					"session_id": fmt.Sprintf("s-%d", i),
					"title":      title,
					"last_text":  last,
					"space":      "no-desc",
				})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"machine_id":    "computer-example",
				"conversations": convs,
			})
		case "/api/v1/assistant/tracks/no-desc/space-md":
			_, _ = w.Write([]byte(`{"content":"# 无简介空间\n","digest":"sha256-digest-123"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	a := &App{client: NewClient(srv.URL, "test-token")}

	t.Run("home", func(t *testing.T) {
		home, err := a.render(context.Background(), &ext.UIRequest{UIParams: wire.UIParams{Contribution: "home"}})
		if err != nil {
			t.Fatalf("home render error: %v", err)
		}
		if err := wire.CheckView(home); err != nil {
			t.Fatalf("home view failed wire.CheckView: %v", err)
		}
		// Row for "reading" must use Subtitle (not Detail) for description and fallback title to slug.
		readingRow := home.Sections[0].Items[2]
		if readingRow.Title != "reading" {
			t.Fatalf("empty track title fallback = %q, want %q", readingRow.Title, "reading")
		}
		if readingRow.Subtitle != "想读的书和笔记" {
			t.Fatalf("home row Subtitle = %q (Detail=%q), want description in Subtitle", readingRow.Subtitle, readingRow.Detail)
		}
	})

	t.Run("space_empty_description_and_refresh_state", func(t *testing.T) {
		page, err := a.render(context.Background(), &ext.UIRequest{UIParams: wire.UIParams{Contribution: "space", State: "no-desc"}})
		if err != nil {
			t.Fatalf("space render error: %v", err)
		}
		if err := wire.CheckView(page); err != nil {
			t.Fatalf("space view with empty description failed wire.CheckView: %v", err)
		}
		if page.State != "no-desc" {
			t.Fatalf("space view State = %q, want %q for RefreshAfter re-renders", page.State, "no-desc")
		}
		firstChatRow := page.Sections[2].Items[0]
		if firstChatRow.Subtitle != "第一条消息预览" {
			t.Fatalf("conversation row Subtitle = %q (Detail=%q), want last_text in Subtitle", firstChatRow.Subtitle, firstChatRow.Detail)
		}
		if firstChatRow.Title == "" {
			t.Fatalf("untitled conversation row must have non-empty Title")
		}
		refreshed, err := a.render(context.Background(), &ext.UIRequest{UIParams: wire.UIParams{Contribution: "space", State: page.State}})
		if err != nil || refreshed.Title != "无简介空间" {
			t.Fatalf("re-render space with returned State failed: view=%#v err=%v", refreshed, err)
		}
	})

	t.Run("space_with_description", func(t *testing.T) {
		page, err := a.render(context.Background(), &ext.UIRequest{UIParams: wire.UIParams{Contribution: "space", State: "reading"}})
		if err != nil {
			t.Fatalf("space render error: %v", err)
		}
		if err := wire.CheckView(page); err != nil {
			t.Fatalf("space view with description failed wire.CheckView: %v", err)
		}
		if page.Sections[0].Items[0].Type != wire.ComponentText || page.Sections[0].Items[0].Text != "想读的书和笔记" {
			t.Fatalf("expected description text component first, got %#v", page.Sections[0].Items[0])
		}
	})

	t.Run("edit_preserves_slug_state", func(t *testing.T) {
		edit, err := a.render(context.Background(), &ext.UIRequest{UIParams: wire.UIParams{Contribution: "edit", State: "no-desc"}})
		if err != nil {
			t.Fatalf("edit render error: %v", err)
		}
		if err := wire.CheckView(edit); err != nil {
			t.Fatalf("edit view failed wire.CheckView: %v", err)
		}
		if edit.State != "no-desc" {
			t.Fatalf("edit view State = %q, want %q", edit.State, "no-desc")
		}
		if _, err := a.render(context.Background(), &ext.UIRequest{UIParams: wire.UIParams{Contribution: "edit", State: edit.State}}); err != nil {
			t.Fatalf("re-render edit with returned State failed: %v", err)
		}
	})

	t.Run("new", func(t *testing.T) {
		newV, err := a.render(context.Background(), &ext.UIRequest{UIParams: wire.UIParams{Contribution: "new"}})
		if err != nil {
			t.Fatalf("new render error: %v", err)
		}
		if err := wire.CheckView(newV); err != nil {
			t.Fatalf("new view failed wire.CheckView: %v", err)
		}
	})
}
