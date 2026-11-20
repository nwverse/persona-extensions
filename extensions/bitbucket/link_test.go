package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/nwverse/persona/sdk/go/ext"
	"github.com/nwverse/persona/sdk/go/wire"
)

// A fake Bitbucket that answers pull request 42 with the given links.self href, or none.
func bitbucketWithHref(t *testing.T, href string) *app {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/projects/ENG/repos/reader/pull-requests/42":
			links := `"links":{"self":[]}`
			if href != "" {
				links = fmt.Sprintf(`"links":{"self":[{"href":%q}]}`, href)
			}
			_, _ = fmt.Fprintf(w, `{"id":42,"title":"Preserve reading context","description":"Keep notes with their source","state":"OPEN","open":true,"author":{"user":{"name":"author","displayName":"Example Author"}},"fromRef":{"displayId":"feature/reading"},"toRef":{"displayId":"main"},"reviewers":[],%s}`, links)
		case "/projects/ENG/repos/reader/pull-requests/42/activities":
			_, _ = w.Write([]byte(`{"values":[]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return &app{client: newClient(srv.URL, testToken), receipts: newWriteStore("")}
}

func detailOf(t *testing.T, href string) *wire.View {
	t.Helper()
	a := bitbucketWithHref(t, href)
	v, err := a.page(t.Context(), &ext.UIRequest{UIParams: wire.UIParams{Locale: "en", UIVersion: 3}}, pageState{Mode: "detail", Project: "ENG", Repo: "reader", PRID: 42})
	if err != nil {
		t.Fatalf("href %q: %v", href, err)
	}
	if err := wire.CheckView(v); err != nil {
		t.Fatalf("href %q: %v", href, err)
	}
	return v
}

// The view check takes a link only when it is https with a host, and refuses the whole view otherwise; an instance served
// over http, such as a local one, links its pull requests with http.
func TestPullRequestDetailOpensWithoutAnHTTPSLink(t *testing.T) {
	for _, href := range []string{"http://localhost:7990/projects/ENG/repos/reader/pull-requests/42", "", "/projects/ENG/repos/reader/pull-requests/42"} {
		for _, c := range detailOf(t, href).Sections[0].Items {
			if c.Type == wire.ComponentLink {
				t.Fatalf("href %q: offered %q", href, c.URL)
			}
		}
	}
}

func TestPullRequestDetailKeepsAnHTTPSLink(t *testing.T) {
	const href = "https://bitbucket.example.com/projects/ENG/repos/reader/pull-requests/42"
	found := false
	for _, c := range detailOf(t, href).Sections[0].Items {
		found = found || c.Type == wire.ComponentLink && c.URL == href
	}
	if !found {
		t.Fatal("the https link is gone")
	}
}
