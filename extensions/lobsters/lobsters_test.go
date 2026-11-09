package main

import (
	"encoding/json"
	"fmt"
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

type fakeLobsters struct {
	srv  *httptest.Server
	mu   sync.Mutex
	hits map[string]int
}

func newFakeLobsters(t *testing.T) *fakeLobsters {
	f := &fakeLobsters{hits: map[string]int{}}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.hits[r.URL.Path]++
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/hottest.json":
			fmt.Fprint(w, `[{"short_id":"story1","title":"Why local-first matters","url":"https://example.com/local-first","score":42,"comment_count":2,"description_plain":"Keep context local","submitter_user":"author","tags":["programming","privacy"],"comments_url":"https://lobste.rs/s/story1/local_first"},{"short_id":"story2","title":"A different topic","url":"https://example.org/other","score":1,"comment_count":0,"submitter_user":"other","tags":["hardware"],"comments_url":"https://lobste.rs/s/story2/other"}]`)
		case "/newest.json":
			fmt.Fprint(w, `[{"short_id":"story1","title":"Why local-first matters","url":"https://example.com/local-first","score":42,"comment_count":2,"submitter_user":"author","tags":["programming"],"comments_url":"https://lobste.rs/s/story1/local_first"}]`)
		case "/s/story1.json":
			fmt.Fprint(w, `{"short_id":"story1","title":"Why local-first matters","url":"https://example.com/local-first","score":42,"comment_count":3,"description_plain":"Keep context local","submitter_user":"author","tags":["programming"],"comments_url":"https://lobste.rs/s/story1/local_first","comments":[{"short_id":"c1","created_at":"2026-11-09T10:00:00Z","is_deleted":false,"is_moderated":false,"score":5,"comment_plain":"Useful context","depth":0,"commenting_user":"reader"},{"short_id":"c2","created_at":"2026-11-09T11:00:00Z","is_deleted":false,"is_moderated":false,"score":3,"comment_plain":"A follow-up","depth":1,"commenting_user":"another"},{"short_id":"deleted","is_deleted":true,"is_moderated":false,"score":0,"comment_plain":"do not show","depth":0,"commenting_user":"deleted"}]}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeLobsters) app() *app { return &app{client: newClient(f.srv.URL)} }

func TestConformance(t *testing.T) {
	exttest.Check(t, manifest, exttest.Serve(t, program(newFakeLobsters(t).app())))
}

func TestManifest(t *testing.T) {
	if err := wire.ValidateJSON("Manifest", manifest); err != nil {
		t.Fatal(err)
	}
	var m wire.Manifest
	if err := json.Unmarshal(manifest, &m); err != nil {
		t.Fatal(err)
	}
	if m.ID != "lobsters" || len(m.Secrets) != 0 {
		t.Fatalf("%+v", m)
	}
	if !slices.Equal(m.Permissions.Network, []string{"lobste.rs"}) {
		t.Fatal(m.Permissions.Network)
	}
	if len(m.Contributes.Connectors[0].Capabilities) != 2 {
		t.Fatal("public read-only connector expected")
	}
}

func TestInterestsAndFilters(t *testing.T) {
	f := newFakeLobsters(t)
	a := f.app()
	a.cfg = config{Tags: []string{"privacy"}, MinScore: 10}
	text, err := a.callTool(t.Context(), "lobsters_stories", json.RawMessage(`{"filter":"hot","query":"local"}`))
	if err != nil || !strings.Contains(text, "Why local-first matters") || strings.Contains(text, "different topic") {
		t.Fatalf("%v %s", err, text)
	}
	filtered := filterStories([]story{{Title: "No match", Tags: []string{"hardware"}}, {Title: "Match", Tags: []string{"programming"}}}, config{}, []string{"programming"}, "", 0)
	if len(filtered) != 1 {
		t.Fatal(filtered)
	}
}

func TestStoryDiscussionSkipsDeletedComments(t *testing.T) {
	f := newFakeLobsters(t)
	text, err := f.app().callTool(t.Context(), "lobsters_story", json.RawMessage(`{"id":"story1"}`))
	if err != nil || !strings.Contains(text, "Useful context") || !strings.Contains(text, "A follow-up") || strings.Contains(text, "do not show") {
		t.Fatalf("%v %s", err, text)
	}
}

func TestCacheAvoidsDuplicateFetch(t *testing.T) {
	f := newFakeLobsters(t)
	c := newClient(f.srv.URL)
	for range 2 {
		if _, err := c.stories(t.Context(), false); err != nil {
			t.Fatal(err)
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.hits["/hottest.json"] != 1 {
		t.Fatal(f.hits)
	}
}

func TestStaleReadIsNotFresh(t *testing.T) {
	f := newFakeLobsters(t)
	c := newClient(f.srv.URL)
	if _, err := c.stories(t.Context(), false); err != nil {
		t.Fatal(err)
	}
	f.srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusInternalServerError) })
	if _, err := c.get(t.Context(), "/hottest.json", 0); err == nil || !strings.Contains(err.Error(), "stale") {
		t.Fatal(err)
	}
}

func TestLocalizedViewsAndDetails(t *testing.T) {
	f := newFakeLobsters(t)
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
	r := &ext.UIRequest{UIParams: wire.UIParams{Locale: "zh-Hans", UIVersion: 6, State: ext.EncodeState(pageState{Mode: "detail", StoryID: "story1"})}}
	v, err := a.page(t.Context(), r, pageState{Mode: "detail", StoryID: "story1"})
	if err != nil {
		t.Fatal(err)
	}
	if err := wire.CheckView(v); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(fmt.Sprint(v), "do not show") {
		t.Fatal("deleted comment was rendered")
	}
}

func TestDetailBounds(t *testing.T) {
	item := story{ShortID: "story1", Title: "Article", URL: "https://example.com", DescriptionPlain: strings.Repeat("context ", 2000), Comments: []comment{{User: "reader", Text: strings.Repeat("note ", 1000)}}}
	v := detailView(&ext.UIRequest{UIParams: wire.UIParams{Locale: "en", UIVersion: 6}}, pageState{Mode: "detail", StoryID: "story1"}, item)
	if err := wire.CheckView(v); err != nil {
		t.Fatal(err)
	}
}

func TestBadIDAndStatus(t *testing.T) {
	a := newFakeLobsters(t).app()
	if _, err := a.callTool(t.Context(), "lobsters_story", json.RawMessage(`{"id":"../private"}`)); err == nil {
		t.Fatal("invalid ID accepted")
	}
	if a.status()["state"] != "ok" {
		t.Fatal(a.status())
	}
	if _, err := a.callTool(t.Context(), "lobsters_stories", json.RawMessage(`{"filter":"hot"}`)); err != nil {
		t.Fatal(err)
	}
	if a.status()["last_sync"] == nil {
		t.Fatal("successful read did not update status")
	}
}
