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

// moreOpenProject answers the assigned work packages in pages of 50 (offset is the 1-based page number): with
// full set every page up to 10 is full and links the next page, otherwise page 1 is full and page 2 holds a
// single work package.
type moreOpenProject struct {
	srv   *httptest.Server
	mu    sync.Mutex
	paths []string
	full  bool
}

func (f *moreOpenProject) records(page int) ([]workPackage, bool) {
	n, next := 50, true
	switch {
	case f.full && page > 10, !f.full && page > 2:
		return nil, false
	case !f.full && page == 2:
		n, next = 1, false
	case f.full && page == 10:
		next = false
	}
	out := make([]workPackage, n)
	for i := range out {
		id := int64((page-1)*50 + i + 1)
		out[i] = workPackage{ID: id, Subject: fmt.Sprintf("task-%d", id)}
	}
	return out, next
}

func newMoreOpenProject(t *testing.T, full bool) *moreOpenProject {
	f := &moreOpenProject{full: full}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.paths = append(f.paths, r.URL.RequestURI())
		f.mu.Unlock()
		switch r.URL.Path {
		case "/api/v3/users/me":
			_ = json.NewEncoder(w).Encode(currentUser{ID: 42, Name: "Reader", Login: "reader"})
		case "/api/v3/work_packages":
			page, _ := strconv.Atoi(r.URL.Query().Get("offset"))
			items, next := f.records(page)
			var out collection[workPackage]
			out.Embedded.Elements = items
			out.Total, out.Count, out.PageSize, out.Offset = 0, len(items), 50, page
			if next {
				out.Links.NextByOffset = link{Href: "/api/v3/work_packages?offset=" + strconv.Itoa(page+1)}
			}
			_ = json.NewEncoder(w).Encode(out)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *moreOpenProject) app() *app {
	return &app{client: newClient(f.srv.URL, testToken), receipts: newWriteStore("")}
}

func TestLoadMoreRetainsEarlierWorkPackages(t *testing.T) {
	f := newMoreOpenProject(t, false)
	a := f.app()
	r := &ext.UIRequest{UIParams: wire.UIParams{Locale: "en", UIVersion: 3}}
	st := pageState{Offset: 1}
	v, err := a.page(t.Context(), r, st)
	if err != nil || len(v.Sections[0].Items) != 50 || v.Sections[0].More == nil {
		t.Fatalf("initial view: %+v, %v", v, err)
	}
	result, err := a.action(t.Context(), withAction(r, "more", st))
	if err != nil || result.View == nil {
		t.Fatalf("more action: %+v, %v", result, err)
	}
	items := result.View.Sections[0].Items
	if len(items) != 51 || items[0].Key != "work-1" || items[len(items)-1].Key != "work-51" {
		t.Fatalf("more did not retain the whole list: count=%d", len(items))
	}
	if result.View.Sections[0].More != nil {
		t.Fatal("the last page still offers more")
	}
	if err := wire.CheckView(result.View); err != nil {
		t.Fatal(err)
	}
	if paths := strings.Join(f.paths, "\n"); !strings.Contains(paths, "offset=1") || !strings.Contains(paths, "offset=2") {
		t.Fatalf("page requests: %s", paths)
	}
}

func TestLoadMoreStopsAtViewLimit(t *testing.T) {
	f := newMoreOpenProject(t, true)
	v, err := f.app().page(t.Context(), &ext.UIRequest{UIParams: wire.UIParams{Locale: "en", UIVersion: 3}}, pageState{Offset: 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Sections[0].Items) != 250 || v.Sections[0].More != nil {
		t.Fatalf("view cap: items=%d more=%+v", len(v.Sections[0].Items), v.Sections[0].More)
	}
	if err := wire.CheckView(v); err != nil {
		t.Fatal(err)
	}
	if paths := strings.Join(f.paths, "\n"); strings.Contains(paths, "offset=6") {
		t.Fatalf("loaded past the cap: %s", paths)
	}
}
