package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/nwverse/persona/sdk/go/ext"
	"github.com/nwverse/persona/sdk/go/wire"
)

// A fake Lobsters that answers one story with the given url and comments_url.
func lobstersWith(t *testing.T, link, discussion string) *app {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/s/story1.json" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = fmt.Fprintf(w, `{"short_id":"story1","title":"An old post","url":%q,"score":4,"comment_count":0,"description_plain":"","submitter_user":"author","tags":["web"],"comments_url":%q,"comments":[]}`, link, discussion)
	}))
	t.Cleanup(srv.Close)
	return &app{client: newClient(srv.URL)}
}

func links(v *wire.View) []string {
	var out []string
	for _, c := range v.Sections[0].Items {
		if c.Type == wire.ComponentLink {
			out = append(out, c.URL)
		}
	}
	return out
}

// The view check takes a link only when it is https with a host, and refuses the whole view otherwise; many submitted
// links are plain http.
func TestStoryDetailOpensWithoutAnHTTPSLink(t *testing.T) {
	for _, c := range []struct{ link, discussion string }{
		{"http://example.com/old", "https://lobste.rs/s/story1/an_old_post"},
		{"", "https://lobste.rs/s/story1/an_old_post"},
		{"https://example.com/new", "http://lobste.rs/s/story1/an_old_post"},
		{"ftp://example.com/old", ""},
	} {
		a := lobstersWith(t, c.link, c.discussion)
		v, err := a.page(t.Context(), &ext.UIRequest{UIParams: wire.UIParams{Locale: "en", UIVersion: 3}}, pageState{Mode: "detail", StoryID: "story1"})
		if err != nil {
			t.Fatalf("%+v: %v", c, err)
		}
		if err := wire.CheckView(v); err != nil {
			t.Fatalf("%+v: %v", c, err)
		}
		for _, u := range links(v) {
			if u != "https://lobste.rs/s/story1/an_old_post" && u != "https://example.com/new" {
				t.Fatalf("%+v: offered %q", c, u)
			}
		}
	}
}

func TestStoryDetailKeepsHTTPSLinks(t *testing.T) {
	a := lobstersWith(t, "https://example.com/new", "https://lobste.rs/s/story1/an_old_post")
	v, err := a.page(t.Context(), &ext.UIRequest{UIParams: wire.UIParams{Locale: "en", UIVersion: 3}}, pageState{Mode: "detail", StoryID: "story1"})
	if err != nil {
		t.Fatal(err)
	}
	if err := wire.CheckView(v); err != nil {
		t.Fatal(err)
	}
	got := links(v)
	if len(got) != 2 || got[0] != "https://example.com/new" || got[1] != "https://lobste.rs/s/story1/an_old_post" {
		t.Fatalf("links: %v", got)
	}
}
