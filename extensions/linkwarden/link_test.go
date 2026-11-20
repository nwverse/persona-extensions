package main

import (
	"testing"

	"github.com/nwverse/persona/sdk/go/ext"
	"github.com/nwverse/persona/sdk/go/wire"
)

// The view check takes a link only when it is https with a host, and refuses the whole view otherwise; many bookmarks are
// plain http.
func TestBookmarkDetailOpensWithoutAnHTTPSLink(t *testing.T) {
	for _, address := range []string{"http://example.com/a", "ftp://example.com/a", "https://"} {
		v := detailView(&ext.UIRequest{UIParams: wire.UIParams{Locale: "en", UIVersion: 3}}, pageState{Mode: "detail", LinkID: 1}, link{ID: 1, Name: "A bookmark", URL: address}, nil)
		if err := wire.CheckView(v); err != nil {
			t.Fatalf("url %q: %v", address, err)
		}
		for _, c := range v.Sections[0].Items {
			if c.Type == wire.ComponentLink {
				t.Fatalf("url %q: offered %q", address, c.URL)
			}
		}
	}
}

func TestBookmarkDetailKeepsAnHTTPSLink(t *testing.T) {
	v := detailView(&ext.UIRequest{UIParams: wire.UIParams{Locale: "en", UIVersion: 3}}, pageState{Mode: "detail", LinkID: 1}, link{ID: 1, Name: "A bookmark", URL: "https://example.com/a"}, nil)
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
