package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/sdk/go/ext"
	"github.com/nwverse/persona/sdk/go/exttest"
	"github.com/nwverse/persona/sdk/go/wire"
)

const testKey = "synthetic-karakeep-key"

type fakeKarakeep struct {
	srv     *httptest.Server
	mu      sync.Mutex
	paths   []string
	posts   []map[string]any
	patches []map[string]any
}

func newFakeKarakeep(t *testing.T) *fakeKarakeep {
	f := &fakeKarakeep{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+testKey {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		f.mu.Lock()
		f.paths = append(f.paths, r.URL.RequestURI())
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/bookmarks/search":
			fmt.Fprint(w, `{"bookmarks":[{"id":"bookmark-1","title":"Reading with context","type":"link","url":"https://example.com/article","description":"A saved article","note":"Why this matters","favourited":false,"archived":false}],"nextCursor":"next-page"}`)
		case "/bookmarks":
			if r.Method == http.MethodPost {
				var in map[string]any
				_ = json.NewDecoder(io.LimitReader(r.Body, 65536)).Decode(&in)
				f.mu.Lock()
				f.posts = append(f.posts, in)
				f.mu.Unlock()
				fmt.Fprint(w, `{"id":"new-bookmark","title":"Saved article","type":"link","url":"https://example.com/new"}`)
				return
			}
			fmt.Fprint(w, `{"bookmarks":[{"id":"bookmark-1","title":"Reading with context","type":"link","url":"https://example.com/article","description":"A saved article","note":"Why this matters","favourited":false,"archived":false}],"nextCursor":null}`)
		case "/bookmarks/bookmark-1":
			if r.Method == http.MethodPatch {
				var in map[string]any
				_ = json.NewDecoder(io.LimitReader(r.Body, 65536)).Decode(&in)
				f.mu.Lock()
				f.patches = append(f.patches, in)
				f.mu.Unlock()
				fmt.Fprint(w, `{"id":"bookmark-1","title":"Reading with context"}`)
				return
			}
			fmt.Fprint(w, `{"id":"bookmark-1","title":"Reading with context","type":"link","url":"https://example.com/article","description":"A saved article","note":"Why this matters","content":"The reading context"}`)
		case "/highlights":
			fmt.Fprint(w, `{"bookmarks":[{"id":"highlight-1","bookmarkId":"bookmark-1","text":"A highlighted passage","note":"A note"},{"id":"highlight-other","bookmarkId":"other","text":"Another bookmark"}],"nextCursor":null}`)
		case "/lists":
			fmt.Fprint(w, `[{"id":"list-1","name":"Research","type":"manual"}]`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeKarakeep) app() *app {
	return &app{client: newClient(f.srv.URL, testKey), receipts: newWriteStore("")}
}

func TestConformance(t *testing.T) {
	exttest.Check(t, manifest, exttest.Serve(t, program(newFakeKarakeep(t).app())))
}

func TestManifest(t *testing.T) {
	if err := wire.ValidateJSON("Manifest", manifest); err != nil {
		t.Fatal(err)
	}
	var m wire.Manifest
	if err := json.Unmarshal(manifest, &m); err != nil {
		t.Fatal(err)
	}
	if m.ID != "karakeep" || len(m.Secrets) != 1 || m.Secrets[0].Name != "api_key" {
		t.Fatalf("%+v", m)
	}
	if len(m.Contributes.Connectors[0].ConfirmAlways) != 2 {
		t.Fatal("save and update must be confirmed")
	}
	if !slices.Equal(m.Permissions.Network, []string{}) {
		t.Fatal(m.Permissions.Network)
	}
}

func TestInstanceURLValidation(t *testing.T) {
	for raw, want := range map[string]string{
		"https://karakeep.example.com":     "https://karakeep.example.com/api",
		"http://127.0.0.1:3000":            "http://127.0.0.1:3000/api",
		"https://karakeep.example.com/api": "https://karakeep.example.com/api",
	} {
		got, err := instanceURL(raw)
		if err != nil || got != want {
			t.Fatalf("%q: %v %q", raw, err, got)
		}
	}
	for _, raw := range []string{"http://karakeep.example.com", "https://me@example.com", "not-a-url"} {
		if _, err := instanceURL(raw); err == nil {
			t.Fatal("accepted " + raw)
		}
	}
}

func TestSearchAndAssociatedHighlights(t *testing.T) {
	f := newFakeKarakeep(t)
	text, err := f.app().callTool(t.Context(), "karakeep_bookmarks", json.RawMessage(`{"query":"context","cursor":"old"}`))
	if err != nil || !strings.Contains(text, "bookmark-1") || !strings.Contains(text, "next-page") {
		t.Fatalf("%v %s", err, text)
	}
	text, err = f.app().callTool(t.Context(), "karakeep_read", json.RawMessage(`{"id":"bookmark-1"}`))
	if err != nil || !strings.Contains(text, "Why this matters") || !strings.Contains(text, "A highlighted passage") || strings.Contains(text, "Another bookmark") {
		t.Fatalf("%v %s", err, text)
	}
}

func TestSaveAndPartialUpdateReplay(t *testing.T) {
	f := newFakeKarakeep(t)
	a := f.app()
	for range 2 {
		if _, err := a.doAction(t.Context(), "save-1", "karakeep_save", []byte(`{"url":"https://example.com/new#part"}`)); err != nil {
			t.Fatal(err)
		}
	}
	if len(f.posts) != 1 || f.posts[0]["url"] != "https://example.com/new" {
		t.Fatal(f.posts)
	}
	if _, err := a.doAction(t.Context(), "fav-1", "karakeep_update", []byte(`{"id":"bookmark-1","favourited":true}`)); err != nil {
		t.Fatal(err)
	}
	if len(f.patches) != 1 || f.patches[0]["favourited"] != true || len(f.patches[0]) != 1 {
		t.Fatal(f.patches)
	}
}

func TestPersistentReplayAndUnknownOutcome(t *testing.T) {
	f := newFakeKarakeep(t)
	a := f.app()
	store, err := openWriteStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a.receipts = store
	args := []byte(`{"url":"https://example.com/persisted"}`)
	if _, err := a.doAction(t.Context(), "persist-1", "karakeep_save", args); err != nil {
		t.Fatal(err)
	}
	a.receipts, err = openWriteStore(filepath.Dir(store.path))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.doAction(t.Context(), "persist-1", "karakeep_save", args); err != nil {
		t.Fatal(err)
	}
	if len(f.posts) != 1 {
		t.Fatal("restart repeated the save")
	}
	key, _ := json.Marshal(map[string]any{"action": "karakeep_save", "url": "https://example.com/unknown"})
	raw, _ := json.Marshal(map[string]writeReceipt{"persist-1": {Key: string(key), Status: "pending", StartedAt: time.Now()}})
	if err := os.WriteFile(store.path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	a.receipts, _ = openWriteStore(filepath.Dir(store.path))
	if _, err := a.doAction(t.Context(), "persist-1", "karakeep_save", []byte(`{"url":"https://example.com/unknown"}`)); err == nil || !strings.Contains(err.Error(), "inspect") {
		t.Fatal("unknown write was retried")
	}
	if len(f.posts) != 1 {
		t.Fatal("unknown write caused another POST")
	}
}

func TestLocalizedViewsAndSaveReview(t *testing.T) {
	f := newFakeKarakeep(t)
	a := f.app()
	for _, locale := range []string{"en", "ja", "zh-Hans"} {
		for _, ui := range []int{1, 3, 6} {
			r := &ext.UIRequest{UIParams: wire.UIParams{Locale: locale, UIVersion: ui}}
			v, err := a.render(t.Context(), r)
			if err != nil {
				t.Fatal(err)
			}
			if err := wire.CheckView(v); err != nil {
				t.Fatalf("%s %d: %v", locale, ui, err)
			}
		}
	}
	st := pageState{Mode: "save", DraftID: "save-1"}
	v := saveView(&ext.UIRequest{UIParams: wire.UIParams{Locale: "zh-Hans", UIVersion: 6}}, st)
	if err := wire.CheckView(v); err != nil {
		t.Fatal(err)
	}
	st.SaveURL, st.Mode = "https://example.com/article", "review-save"
	v = reviewSaveView(&ext.UIRequest{UIParams: wire.UIParams{Locale: "zh-Hans", UIVersion: 6}}, st)
	if err := wire.CheckView(v); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(v.Sections[0].Items[1].Action.Confirm, st.SaveURL) {
		t.Fatal("confirmation lacks URL")
	}
}

func TestMoreKeepsExistingBookmarksAndUsesCursor(t *testing.T) {
	f := newFakeKarakeep(t)
	a := f.app()
	v, err := a.render(t.Context(), &ext.UIRequest{UIParams: wire.UIParams{Locale: "en", UIVersion: 6, State: ext.EncodeState(pageState{Cursor: "next-page"})}})
	if err != nil {
		t.Fatal(err)
	}
	if v == nil || len(v.Sections[0].Items) == 0 {
		t.Fatal("existing bookmark was not retained")
	}
	r := &ext.UIRequest{UIParams: wire.UIParams{Locale: "en", UIVersion: 6, State: v.State, Action: "more"}}
	if _, err := a.action(t.Context(), r); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	found := false
	for _, path := range f.paths {
		if strings.Contains(path, "cursor=next-page") {
			found = true
		}
	}
	if !found {
		t.Fatal("next cursor was not carried forward")
	}
}

func TestDetailBoundsAndNoConfig(t *testing.T) {
	v := detailView(&ext.UIRequest{UIParams: wire.UIParams{Locale: "en", UIVersion: 6}}, pageState{BookmarkID: "bookmark-1", Mode: "detail"}, bookmark{ID: "bookmark-1", Title: "Article", URL: "https://example.com", Text: strings.Repeat("text ", 3000)}, nil)
	if err := wire.CheckView(v); err != nil {
		t.Fatal(err)
	}
	a := newApp()
	if a.status()["state"] != "not_configured" {
		t.Fatal(a.status())
	}
	v, err := a.render(t.Context(), &ext.UIRequest{})
	if err != nil || !strings.Contains(v.Sections[0].Items[0].Title, "instance URL") {
		t.Fatalf("%v %+v", err, v)
	}
}
