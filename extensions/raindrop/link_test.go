package main

import (
	"testing"

	"github.com/nwverse/persona/sdk/go/ext"
	"github.com/nwverse/persona/sdk/go/wire"
)

// The view check takes a link only when it is https with a host, and refuses the whole view otherwise; many bookmarks are
// plain http, and some have no link at all.
func TestBookmarkDetailOpensWithoutAnHTTPSLink(t *testing.T) {
	a := &app{}
	for _, link := range []string{"http://example.com/a", "", "ftp://example.com/a", "https://"} {
		v := a.detailView(&ext.UIRequest{UIParams: wire.UIParams{Locale: "en", UIVersion: 3}}, pageState{Mode: "detail", Bookmark: 1}, raindrop{ID: 1, Title: "A bookmark", Link: link, Domain: "example.com"})
		if err := wire.CheckView(v); err != nil {
			t.Fatalf("link %q: %v", link, err)
		}
		for _, c := range v.Sections[0].Items {
			if c.Type == wire.ComponentLink {
				t.Fatalf("link %q: offered %q", link, c.URL)
			}
		}
	}
}

func TestBookmarkDetailKeepsAnHTTPSLink(t *testing.T) {
	a := &app{}
	v := a.detailView(&ext.UIRequest{UIParams: wire.UIParams{Locale: "en", UIVersion: 3}}, pageState{Mode: "detail", Bookmark: 1}, raindrop{ID: 1, Title: "A bookmark", Link: "https://example.com/a", Domain: "example.com"})
	if err := wire.CheckView(v); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, c := range v.Sections[0].Items {
		found = found || c.Type == wire.ComponentLink && c.URL == "https://example.com/a"
	}
	if !found {
		t.Fatal("the https link is gone")
	}
}
