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

// moreGitLab answers the merge request list in batches of 50: with full set every page up to 10 is full and
// reports a next page, otherwise page 1 is full and page 2 holds a single merge request.
type moreGitLab struct {
	srv   *httptest.Server
	mu    sync.Mutex
	paths []string
	full  bool
}

func (f *moreGitLab) records(page int) ([]mergeRequest, string) {
	n, next := 50, strconv.Itoa(page+1)
	switch {
	case f.full && page > 10, !f.full && page > 2:
		return nil, ""
	case !f.full && page == 2:
		n, next = 1, ""
	case f.full && page == 10:
		next = ""
	}
	out := make([]mergeRequest, n)
	for i := range out {
		iid := int64((page-1)*50 + i + 1)
		out[i] = mergeRequest{ID: 1000 + iid, IID: iid, ProjectID: 17, Title: fmt.Sprintf("change-%d", iid), State: "opened"}
	}
	return out, next
}

func newMoreGitLab(t *testing.T, full bool) *moreGitLab {
	f := &moreGitLab{full: full}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.paths = append(f.paths, r.URL.RequestURI())
		f.mu.Unlock()
		if r.URL.Path != "/merge_requests" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		items, next := f.records(page)
		w.Header().Set("X-Next-Page", next)
		_ = json.NewEncoder(w).Encode(items)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *moreGitLab) app() *app {
	return &app{client: newClient(f.srv.URL, testToken), receipts: newWriteStore("")}
}

func TestLoadMoreRetainsEarlierMergeRequests(t *testing.T) {
	f := newMoreGitLab(t, false)
	a := f.app()
	r := &ext.UIRequest{UIParams: wire.UIParams{Locale: "en", UIVersion: 3}}
	st := pageState{Scope: "assigned_to_me", Page: 1, Mode: "list"}
	v, err := a.page(t.Context(), r, st)
	if err != nil || len(v.Sections[0].Items) != 50 || v.Sections[0].More == nil {
		t.Fatalf("initial view: %+v, %v", v, err)
	}
	result, err := a.action(t.Context(), withAction(r, "more", st))
	if err != nil || result.View == nil {
		t.Fatalf("more action: %+v, %v", result, err)
	}
	items := result.View.Sections[0].Items
	if len(items) != 51 || items[0].Key != "mr-1001" || items[len(items)-1].Key != "mr-1051" {
		t.Fatalf("more did not retain the whole list: count=%d", len(items))
	}
	if result.View.Sections[0].More != nil {
		t.Fatal("the last page still offers more")
	}
	if err := wire.CheckView(result.View); err != nil {
		t.Fatal(err)
	}
	if paths := strings.Join(f.paths, "\n"); !strings.Contains(paths, "page=1") || !strings.Contains(paths, "page=2") {
		t.Fatalf("page requests: %s", paths)
	}
}

func TestLoadMoreStopsAtViewLimit(t *testing.T) {
	f := newMoreGitLab(t, true)
	v, err := f.app().page(t.Context(), &ext.UIRequest{UIParams: wire.UIParams{Locale: "en", UIVersion: 3}}, pageState{Scope: "assigned_to_me", Page: 5, Mode: "list"})
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
