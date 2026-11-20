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

// documentedSubscriptions answers GET /users/me/subscriptions the way Zulip's get-subscriptions page documents it:
// the list is the `subscriptions` field of the response (the `streams` field belongs to GET /streams), and each item
// carries `stream_id`, `name` and `description`.
func documentedSubscriptions(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/users/me/subscriptions" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"result": "success", "msg": "", "subscriptions": []map[string]any{
			{"stream_id": 7, "name": "reading", "description": "Reading and research", "rendered_description": "<p>Reading and research</p>"},
		}})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestStreamsAreReadFromTheSubscriptionsField(t *testing.T) {
	streams, err := newClient(documentedSubscriptions(t).URL, testEmail, testToken).streams(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(streams) != 1 || streams[0].ID != 7 || streams[0].Name != "reading" || streams[0].Description != "Reading and research" {
		t.Fatalf("streams %+v", streams)
	}
}

func TestSubscribedStreamsShowInTheViewAndTheTool(t *testing.T) {
	a := &app{client: newClient(documentedSubscriptions(t).URL, testEmail, testToken)}
	v, err := a.page(t.Context(), &ext.UIRequest{UIParams: wire.UIParams{Locale: "en", UIVersion: 3}}, pageState{})
	if err != nil || len(v.Sections[0].Items) != 1 || v.Sections[0].Items[0].Key != "stream-7" {
		t.Fatalf("view: %d rows (first %q), %v", len(v.Sections[0].Items), v.Sections[0].Items[0].Type, err)
	}
	text, err := a.callTool(t.Context(), "zulip_streams", json.RawMessage(`{}`))
	if err != nil || !strings.Contains(text, "Stream #7: reading") {
		t.Fatalf("tool: %v, %s", err, text)
	}
}
