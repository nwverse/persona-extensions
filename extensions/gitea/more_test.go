package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/nwverse/persona/sdk/go/ext"
	"github.com/nwverse/persona/sdk/go/wire"
)

// moreGitea answers the open pull request list in batches of 50: with full set every page up to 10 is full,
// otherwise page 1 is full and page 2 holds a single pull request.
type moreGitea struct {
	srv   *httptest.Server
	mu    sync.Mutex
	paths []string
	full  bool
}

func (f *moreGitea) records(page int) []pullRequest {
	n := 50
	switch {
	case f.full && page > 10, !f.full && page > 2:
		return nil
	case !f.full && page == 2:
		n = 1
	}
	out := make([]pullRequest, n)
	for i := range out {
		number := int64((page-1)*50 + i + 1)
		out[i] = pullRequest{ID: number, Number: number, Title: fmt.Sprintf("change-%d", number), State: "open"}
	}
	return out
}

func newMoreGitea(t *testing.T, full bool) *moreGitea {
	f := &moreGitea{full: full}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.paths = append(f.paths, r.URL.RequestURI())
		f.mu.Unlock()
		if r.URL.Path != "/api/v1/repos/reading/notes/pulls" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		_ = json.NewEncoder(w).Encode(f.records(page))
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *moreGitea) app() *app {
	return &app{client: newClient(f.srv.URL, testToken), receipts: newWriteStore("")}
}

func TestLoadMoreRetainsEarlierPullRequests(t *testing.T) {
	f := newMoreGitea(t, false)
	a := f.app()
	r := &ext.UIRequest{UIParams: wire.UIParams{Locale: "en", UIVersion: 3}}
	st := pageState{Owner: "reading", Repo: "notes", Page: 1, Mode: "list"}
	v, err := a.page(t.Context(), r, st)
	if err != nil || len(v.Sections[0].Items) != 50 || v.Sections[0].More == nil {
		t.Fatalf("initial view: %+v, %v", v, err)
	}
	result, err := a.action(t.Context(), withAction(r, "more", st))
	if err != nil || result.View == nil {
		t.Fatalf("more action: %+v, %v", result, err)
	}
	items := result.View.Sections[0].Items
	if len(items) != 51 || items[0].Key != "pr-1" || items[len(items)-1].Key != "pr-51" {
		t.Fatalf("more did not retain the whole list: count=%d", len(items))
	}
	if result.View.Sections[0].More != nil {
		t.Fatal("a short final batch still offers more")
	}
	if err := wire.CheckView(result.View); err != nil {
		t.Fatal(err)
	}
	if paths := strings.Join(f.paths, "\n"); !strings.Contains(paths, "page=1") || !strings.Contains(paths, "page=2") {
		t.Fatalf("page requests: %s", paths)
	}
}

func TestLoadMoreStopsAtViewLimit(t *testing.T) {
	f := newMoreGitea(t, true)
	v, err := f.app().page(t.Context(), &ext.UIRequest{UIParams: wire.UIParams{Locale: "en", UIVersion: 3}}, pageState{Owner: "reading", Repo: "notes", Page: 5, Mode: "list"})
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Sections[0].Items) != 250 || v.Sections[0].More != nil {
		t.Fatalf("view cap: items=%d more=%+v", len(v.Sections[0].Items), v.Sections[0].More)
	}
	if err := wire.CheckView(v); err != nil {
		t.Fatal(err)
	}
	if paths := strings.Join(f.paths, "\n"); strings.Contains(paths, "page=6") {
		t.Fatalf("loaded past the cap: %s", paths)
	}
}
