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

// documentedNext is what the Shortcut OpenAPI document describes for the `next` field of a search response: "The URL
// path and query string for the next page of search results". The page token is the `next` parameter inside it.
const documentedNext = "/api/v3/search/stories?next=token-2&query=owner%3Areader+is%3Astory+%21is%3Adone&page_size=50&detail=slim&entity_types=story"

// documentedShortcut serves the API below /api/v3 and answers the story search the way the document describes: the
// first page carries the path and query string of the second page, which only a request with `next=token-2` reads.
func documentedShortcut(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v3/member":
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "member-1", "mention_name": "reader", "name": "Example User"})
		case "/api/v3/search/stories":
			if r.URL.Query().Get("query") != "owner:reader is:story !is:done" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			switch r.URL.Query().Get("next") {
			case "":
				_ = json.NewEncoder(w).Encode(map[string]any{"total": 2, "data": []map[string]any{{"id": 41, "name": "First story", "story_type": "feature"}}, "next": documentedNext})
			case "token-2":
				_ = json.NewEncoder(w).Encode(map[string]any{"total": 2, "data": []map[string]any{{"id": 42, "name": "Second story", "story_type": "bug"}}, "next": nil})
			default:
				w.WriteHeader(http.StatusBadRequest)
			}
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func documentedApp(t *testing.T) *app {
	return &app{client: newClient(documentedShortcut(t).URL+"/api/v3", testToken), receipts: newWriteStore("")}
}

func TestStoryListFollowsTheDocumentedNextLink(t *testing.T) {
	a := documentedApp(t)
	r := &ext.UIRequest{UIParams: wire.UIParams{Locale: "en", UIVersion: 3}}
	v, err := a.page(t.Context(), r, pageState{})
	if err != nil || len(v.Sections[0].Items) != 1 || v.Sections[0].More == nil {
		t.Fatalf("first page: %+v, %v", v, err)
	}
	r.Action, r.Values, r.State = "more", v.Sections[0].More.Action.Values, v.State
	result, err := a.action(t.Context(), r)
	if err != nil || result.View == nil {
		t.Fatalf("more action: %+v, %v", result, err)
	}
	items := result.View.Sections[0].Items
	if len(items) != 1 || items[0].Key != "story-42" || result.View.Sections[0].More != nil {
		t.Fatalf("second page: %+v", items)
	}
}

func TestStoriesToolFollowsTheDocumentedNextLink(t *testing.T) {
	a := documentedApp(t)
	text, err := a.callTool(t.Context(), "shortcut_stories", json.RawMessage(`{}`))
	if err != nil || !strings.Contains(text, documentedNext) {
		t.Fatalf("first page: %v, next value missing (%d bytes)", err, len(text))
	}
	raw, _ := json.Marshal(map[string]string{"next": documentedNext})
	text, err = a.callTool(t.Context(), "shortcut_stories", raw)
	if err != nil || !strings.Contains(text, "Second story") {
		t.Fatalf("second page: %v, %s", err, text)
	}
	if _, err := a.callTool(t.Context(), "shortcut_stories", json.RawMessage(`{"next":"https://example.com/api/v3/search/stories?next=token-2"}`)); err == nil {
		t.Fatal("accepted a next link with a host")
	}
}
