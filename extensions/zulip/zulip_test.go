package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/nwverse/persona/sdk/go/ext"
	"github.com/nwverse/persona/sdk/go/exttest"
	"github.com/nwverse/persona/sdk/go/wire"
)

const testEmail = "reader@example.com"
const testToken = "synthetic-zulip-token"

type fakeZulip struct {
	srv   *httptest.Server
	mu    sync.Mutex
	paths []string
}

func newFakeZulip(t *testing.T) *fakeZulip {
	f := &fakeZulip{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		email, password, ok := r.BasicAuth()
		if !ok || email != testEmail || password != testToken {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		f.mu.Lock()
		f.paths = append(f.paths, r.URL.RequestURI())
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/users/me/subscriptions":
			_ = json.NewEncoder(w).Encode(map[string]any{"result": "success", "streams": []stream{{ID: 7, Name: "reading", Description: "Reading and research"}}})
		case "/api/v1/users/me/subscriptions/7/topics":
			_ = json.NewEncoder(w).Encode(map[string]any{"result": "success", "topics": []topic{{Name: "source context", MaxID: 42}}})
		case "/api/v1/messages":
			var narrow []map[string]any
			if err := json.Unmarshal([]byte(r.URL.Query().Get("narrow")), &narrow); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			if len(narrow) == 0 || r.URL.Query().Get("apply_markdown") != "false" || r.URL.Query().Get("allow_empty_topic_name") != "true" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			if r.URL.Query().Get("use_first_unread_anchor") != "true" && r.URL.Query().Get("anchor") == "" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"result": "success", "found_oldest": false, "found_newest": false, "found_anchor": true, "messages": []message{{ID: 42, Content: "Keep source context before the next reading session.", ContentType: "text/x-markdown", SenderFullName: "Example Reviewer", SenderID: 8, Timestamp: 1760000000, StreamID: 7, Subject: "source context"}}})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeZulip) app() *app { return &app{client: newClient(f.srv.URL, testEmail, testToken)} }

func TestConformance(t *testing.T) {
	exttest.Check(t, manifest, exttest.Serve(t, program(newFakeZulip(t).app())))
}

func TestManifestAndURL(t *testing.T) {
	if err := wire.ValidateJSON("Manifest", manifest); err != nil {
		t.Fatal(err)
	}
	var m wire.Manifest
	if err := json.Unmarshal(manifest, &m); err != nil {
		t.Fatal(err)
	}
	if m.ID != "zulip" || len(m.Secrets) != 2 || len(m.Contributes.Connectors[0].ConfirmAlways) != 0 || !slices.Equal(m.Permissions.Network, []string{}) {
		t.Fatalf("%+v", m)
	}
	for _, raw := range []string{"https://example.zulipchat.com", "http://127.0.0.1:9991/", "https://[::1]:9991"} {
		if _, err := instanceURL(raw); err != nil {
			t.Fatal(raw, err)
		}
	}
	for _, raw := range []string{"http://example.zulipchat.com", "https://me@example.com", "not-a-url"} {
		if _, err := instanceURL(raw); err == nil {
			t.Fatal("accepted " + raw)
		}
	}
	if newApp().status()["state"] != "not_configured" {
		t.Fatal("unconfigured extension was ready")
	}
}

func TestStreamsTopicsMessagesAndUnread(t *testing.T) {
	f := newFakeZulip(t)
	a := f.app()
	text, err := a.callTool(t.Context(), "zulip_streams", json.RawMessage(`{}`))
	if err != nil || !strings.Contains(text, "Stream #7") {
		t.Fatalf("%v %s", err, text)
	}
	text, err = a.callTool(t.Context(), "zulip_topics", json.RawMessage(`{"stream_id":7}`))
	if err != nil || !strings.Contains(text, "source context") {
		t.Fatalf("%v %s", err, text)
	}
	text, err = a.callTool(t.Context(), "zulip_messages", json.RawMessage(`{"stream_id":7,"topic":"source context","first_unread":true}`))
	if err != nil || !strings.Contains(text, "Example Reviewer") || !strings.Contains(text, "next anchor") {
		t.Fatalf("%v %s", err, text)
	}
	f.mu.Lock()
	paths := strings.Join(f.paths, "\n")
	f.mu.Unlock()
	for _, want := range []string{"use_first_unread_anchor=true", "apply_markdown=false", "allow_empty_topic_name=true", "num_before=15", "num_after=15", "operator%22%3A%22stream%22", "operator%22%3A%22topic%22"} {
		if !strings.Contains(paths, want) {
			t.Fatalf("missing %q in %s", want, paths)
		}
	}
	if _, err := a.callTool(t.Context(), "zulip_messages", json.RawMessage(`{"stream_id":7,"topic":"bad\nname"}`)); err == nil {
		t.Fatal("accepted invalid topic")
	}
}

func TestLocalizedViewsAndBounds(t *testing.T) {
	f := newFakeZulip(t)
	a := f.app()
	for _, locale := range []string{"en", "ja", "zh-Hans"} {
		for _, ui := range []int{1, 3, 6} {
			for _, mode := range []string{"", "topics", "messages"} {
				r := &ext.UIRequest{UIParams: wire.UIParams{Locale: locale, UIVersion: ui}}
				st := pageState{Mode: mode, StreamID: 7, Topic: "source context"}
				v, err := a.page(t.Context(), r, st)
				if err != nil {
					t.Fatalf("%s %d %s: %v", locale, ui, mode, err)
				}
				if err := wire.CheckView(v); err != nil {
					t.Fatalf("%s %d %s: %v", locale, ui, mode, err)
				}
			}
		}
	}
	if got := textComponents(strings.Repeat("message\n", 1000)); len(got) > 220 {
		t.Fatal("message component limit exceeded")
	}
}
