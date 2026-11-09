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

const testKey = "synthetic-linkwarden-key"

type fakeLinkwarden struct {
	srv      *httptest.Server
	mu       sync.Mutex
	paths    []string
	posts    []map[string]any
	archives []int64
}

func newFakeLinkwarden(t *testing.T) *fakeLinkwarden {
	f := &fakeLinkwarden{}
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
		case "/search":
			fmt.Fprint(w, `{"links":[{"id":101,"name":"Reading with context","url":"https://example.com/article","description":"A saved article","note":"Why this matters","collection":{"id":5,"name":"Research"}}]}`)
		case "/collections":
			fmt.Fprint(w, `[{"id":5,"name":"Research"},{"id":8,"name":"Later"}]`)
		case "/links/101":
			fmt.Fprint(w, `{"id":101,"name":"Reading with context","url":"https://example.com/article","description":"A saved article","note":"Why this matters","collection":{"id":5,"name":"Research"}}`)
		case "/links/101/highlights":
			fmt.Fprint(w, `[{"id":11,"linkId":101,"text":"A highlighted passage","note":"A note"}]`)
		case "/links":
			var in map[string]any
			_ = json.NewDecoder(io.LimitReader(r.Body, 65536)).Decode(&in)
			f.mu.Lock()
			f.posts = append(f.posts, in)
			f.mu.Unlock()
			fmt.Fprint(w, `{"id":102,"name":"New article","url":"https://example.com/new"}`)
		case "/links/101/archive":
			f.mu.Lock()
			f.archives = append(f.archives, 101)
			f.mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeLinkwarden) app() *app {
	return &app{client: newClient(f.srv.URL, testKey), receipts: newWriteStore("")}
}

func TestConformance(t *testing.T) {
	exttest.Check(t, manifest, exttest.Serve(t, program(newFakeLinkwarden(t).app())))
}

func TestManifest(t *testing.T) {
	if err := wire.ValidateJSON("Manifest", manifest); err != nil {
		t.Fatal(err)
	}
	var m wire.Manifest
	if err := json.Unmarshal(manifest, &m); err != nil {
		t.Fatal(err)
	}
	if m.ID != "linkwarden" || len(m.Secrets) != 1 || m.Secrets[0].Name != "api_key" {
		t.Fatalf("%+v", m)
	}
	if len(m.Contributes.Connectors[0].ConfirmAlways) != 2 {
		t.Fatal("save and archive must be confirmed")
	}
	if !slices.Equal(m.Permissions.Network, []string{}) {
		t.Fatal(m.Permissions.Network)
	}
}

func TestInstanceURLValidation(t *testing.T) {
	for raw, want := range map[string]string{
		"https://linkwarden.example.com":        "https://linkwarden.example.com/api/v1",
		"http://127.0.0.1:3000":                 "http://127.0.0.1:3000/api/v1",
		"https://linkwarden.example.com/api/v1": "https://linkwarden.example.com/api/v1",
	} {
		got, err := instanceURL(raw)
		if err != nil || got != want {
			t.Fatalf("%q: %v %q", raw, err, got)
		}
	}
	for _, raw := range []string{"http://linkwarden.example.com", "https://me@example.com", "not-a-url"} {
		if _, err := instanceURL(raw); err == nil {
			t.Fatal("accepted " + raw)
		}
	}
}

func TestSearchCollectionsAndHighlights(t *testing.T) {
	f := newFakeLinkwarden(t)
	text, err := f.app().callTool(t.Context(), "linkwarden_search", json.RawMessage(`{"query":"context","collection_id":5}`))
	if err != nil || !strings.Contains(text, "Reading with context") || !strings.Contains(text, "Research") {
		t.Fatalf("%v %s", err, text)
	}
	text, err = f.app().callTool(t.Context(), "linkwarden_read", json.RawMessage(`{"id":101}`))
	if err != nil || !strings.Contains(text, "Why this matters") || !strings.Contains(text, "A highlighted passage") {
		t.Fatalf("%v %s", err, text)
	}
}

func TestSaveAndArchiveReplay(t *testing.T) {
	f := newFakeLinkwarden(t)
	a := f.app()
	for range 2 {
		if _, err := a.doAction(t.Context(), "save-1", "linkwarden_save", []byte(`{"url":"https://example.com/new#part"}`)); err != nil {
			t.Fatal(err)
		}
	}
	if len(f.posts) != 1 || f.posts[0]["url"] != "https://example.com/new" {
		t.Fatal(f.posts)
	}
	for range 2 {
		if _, err := a.doAction(t.Context(), "archive-1", "linkwarden_archive", []byte(`{"id":101}`)); err != nil {
			t.Fatal(err)
		}
	}
	if len(f.archives) != 1 {
		t.Fatal(f.archives)
	}
}

func TestPersistentReplayAndUnknownOutcome(t *testing.T) {
	f := newFakeLinkwarden(t)
	a := f.app()
	store, err := openWriteStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a.receipts = store
	args := []byte(`{"url":"https://example.com/persisted"}`)
	if _, err := a.doAction(t.Context(), "persist-1", "linkwarden_save", args); err != nil {
		t.Fatal(err)
	}
	a.receipts, err = openWriteStore(filepath.Dir(store.path))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.doAction(t.Context(), "persist-1", "linkwarden_save", args); err != nil {
		t.Fatal(err)
	}
	if len(f.posts) != 1 {
		t.Fatal("restart repeated the save")
	}
	key, _ := json.Marshal(map[string]any{"action": "linkwarden_save", "url": "https://example.com/unknown"})
	raw, _ := json.Marshal(map[string]writeReceipt{"persist-1": {Key: string(key), Status: "pending", StartedAt: time.Now()}})
	if err := os.WriteFile(store.path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	a.receipts, _ = openWriteStore(filepath.Dir(store.path))
	if _, err := a.doAction(t.Context(), "persist-1", "linkwarden_save", []byte(`{"url":"https://example.com/unknown"}`)); err == nil || !strings.Contains(err.Error(), "inspect") {
		t.Fatal("unknown write was retried")
	}
	if len(f.posts) != 1 {
		t.Fatal("unknown write caused another POST")
	}
}

func TestLocalizedViewsAndSaveReview(t *testing.T) {
	f := newFakeLinkwarden(t)
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

func TestInvalidWritesAndReceiptConflicts(t *testing.T) {
	f := newFakeLinkwarden(t)
	a := f.app()
	for _, tc := range []struct{ name, args string }{
		{"linkwarden_save", `{"url":"file:///tmp/private"}`},
		{"linkwarden_save", `{"url":"https://me@example.com/private"}`},
		{"linkwarden_archive", `{"id":0}`},
		{"linkwarden_archive", `{"id":101,"url":"https://example.com/other"}`},
	} {
		if _, err := a.doAction(t.Context(), "bad-"+tc.args, tc.name, []byte(tc.args)); err == nil {
			t.Fatal("accepted " + tc.args)
		}
	}
	if len(f.posts) != 0 || len(f.archives) != 0 {
		t.Fatal("invalid write reached API")
	}
	if _, err := a.doAction(t.Context(), "save-1", "linkwarden_save", []byte(`{"url":"https://example.com/new"}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := a.doAction(t.Context(), "save-1", "linkwarden_save", []byte(`{"url":"https://example.com/other"}`)); err == nil {
		t.Fatal("receipt accepted different URL")
	}
}

func TestDetailBoundsAndNoConfig(t *testing.T) {
	v := detailView(&ext.UIRequest{UIParams: wire.UIParams{Locale: "en", UIVersion: 6}}, pageState{LinkID: 101, Mode: "detail"}, link{ID: 101, Name: "Article", URL: "https://example.com", Description: strings.Repeat("text ", 3000)}, nil)
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
