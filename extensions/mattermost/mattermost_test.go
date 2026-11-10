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

const testToken = "synthetic-mattermost-token"

type fakeMattermost struct {
	srv   *httptest.Server
	mu    sync.Mutex
	paths []string
}

func newFakeMattermost(t *testing.T) *fakeMattermost {
	f := &fakeMattermost{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+testToken {
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
		case "/api/v4/users/me":
			_ = json.NewEncoder(w).Encode(user{ID: "user-1", Username: "reader"})
		case "/api/v4/users/me/teams":
			_ = json.NewEncoder(w).Encode([]team{{ID: "team-1", DisplayName: "Example Team"}})
		case "/api/v4/users/me/teams/team-1/channels":
			_ = json.NewEncoder(w).Encode([]channel{{ID: "channel-1", TeamID: "team-1", DisplayName: "Reading", Type: "O"}, {ID: "channel-quiet", TeamID: "team-1", DisplayName: "Quiet", Type: "O"}})
		case "/api/v4/users/me/channels/channel-1/unread":
			_ = json.NewEncoder(w).Encode(unread{TeamID: "team-1", ChannelID: "channel-1", MessageCount: 4, MentionCount: 1})
		case "/api/v4/users/me/channels/channel-quiet/unread":
			_ = json.NewEncoder(w).Encode(unread{TeamID: "team-1", ChannelID: "channel-quiet"})
		case "/api/v4/channels/channel-1/posts":
			item := post{ID: "post-1", CreateAt: 1760000000000, UserID: "user-2", ChannelID: "channel-1", Message: "Review the source before the next reading session.", RootID: "post-root"}
			_ = json.NewEncoder(w).Encode(postList{Order: []string{"post-1"}, Posts: map[string]post{"post-1": item}, PrevPostID: "post-older"})
		case "/api/v4/users/user-2":
			_ = json.NewEncoder(w).Encode(user{ID: "user-2", Username: "reviewer", Nickname: "Example Reviewer"})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeMattermost) app() *app { return &app{client: newClient(f.srv.URL, testToken)} }

func TestConformance(t *testing.T) {
	exttest.Check(t, manifest, exttest.Serve(t, program(newFakeMattermost(t).app())))
}

func TestManifestAndURL(t *testing.T) {
	if err := wire.ValidateJSON("Manifest", manifest); err != nil {
		t.Fatal(err)
	}
	var m wire.Manifest
	if err := json.Unmarshal(manifest, &m); err != nil {
		t.Fatal(err)
	}
	if m.ID != "mattermost" || len(m.Secrets) != 1 || len(m.Contributes.Connectors[0].ConfirmAlways) != 0 || !slices.Equal(m.Permissions.Network, []string{}) {
		t.Fatalf("%+v", m)
	}
	for _, raw := range []string{"https://mattermost.example.com", "http://127.0.0.1:8065/", "https://[::1]:8065"} {
		if _, err := instanceURL(raw); err != nil {
			t.Fatal(raw, err)
		}
	}
	for _, raw := range []string{"http://mattermost.example.com", "https://me@example.com", "not-a-url"} {
		if _, err := instanceURL(raw); err == nil {
			t.Fatal("accepted " + raw)
		}
	}
	if newApp().status()["state"] != "not_configured" {
		t.Fatal("unconfigured extension was ready")
	}
}

func TestUnreadChannelsPostsAndAuthors(t *testing.T) {
	f := newFakeMattermost(t)
	a := f.app()
	text, err := a.callTool(t.Context(), "mattermost_unread", json.RawMessage(`{}`))
	if err != nil || !strings.Contains(text, "unread: 4") || !strings.Contains(text, "mentions: 1") || strings.Contains(text, "Quiet") {
		t.Fatalf("%v %s", err, text)
	}
	text, err = a.callTool(t.Context(), "mattermost_channels", json.RawMessage(`{"team_id":"team-1"}`))
	if err != nil || !strings.Contains(text, "channel-1") {
		t.Fatalf("%v %s", err, text)
	}
	text, err = a.callTool(t.Context(), "mattermost_posts", json.RawMessage(`{"channel_id":"channel-1","before":"post-older"}`))
	if err != nil || !strings.Contains(text, "Example Reviewer (reviewer)") || !strings.Contains(text, "Older messages before: post-older") {
		t.Fatalf("%v %s", err, text)
	}
	f.mu.Lock()
	paths := strings.Join(f.paths, "\n")
	f.mu.Unlock()
	for _, want := range []string{"before=post-older", "per_page=30", "/users/me/teams", "/unread"} {
		if !strings.Contains(paths, want) {
			t.Fatalf("missing %q in %s", want, paths)
		}
	}
	if _, err := a.callTool(t.Context(), "mattermost_posts", json.RawMessage(`{"channel_id":"../users"}`)); err == nil {
		t.Fatal("accepted channel path traversal")
	}
}

func TestLocalizedViewsAndBounds(t *testing.T) {
	f := newFakeMattermost(t)
	a := f.app()
	for _, locale := range []string{"en", "ja", "zh-Hans"} {
		for _, ui := range []int{1, 3, 6} {
			for _, mode := range []string{"", "channels", "posts"} {
				r := &ext.UIRequest{UIParams: wire.UIParams{Locale: locale, UIVersion: ui}}
				st := pageState{Mode: mode, TeamID: "team-1", ChannelID: "channel-1"}
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
