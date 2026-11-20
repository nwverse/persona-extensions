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

// morePlane answers the project list (100 per page) and the work item list (50 per page) with Plane's cursors: the
// first page has no cursor, the page after page k is "<per page>:k:0". total records are served in both lists.
type morePlane struct {
	srv     *httptest.Server
	mu      sync.Mutex
	cursors []string
	total   int
}

func (f *morePlane) page(cursor string, perPage int) (first, n int, next string) {
	pageNumber := 1
	if cursor != "" {
		parts := strings.Split(cursor, ":")
		pageNumber, _ = strconv.Atoi(parts[1])
		pageNumber++
	}
	first = (pageNumber-1)*perPage + 1
	n = max(0, min(perPage, f.total-first+1))
	if first+n-1 < f.total {
		next = fmt.Sprintf("%d:%d:0", perPage, pageNumber)
	}
	return first, n, next
}

func newMorePlane(t *testing.T, total int) *morePlane {
	f := &morePlane{total: total}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cursor := r.URL.Query().Get("cursor")
		f.mu.Lock()
		f.cursors = append(f.cursors, cursor)
		f.mu.Unlock()
		switch r.URL.Path {
		case "/workspaces/personal/projects/":
			first, n, next := f.page(cursor, 100)
			out := page[project]{NextCursor: next, NextPageResults: next != ""}
			for i := range n {
				out.Results = append(out.Results, project{ID: fmt.Sprintf("project-%d", first+i), Name: fmt.Sprintf("project %d", first+i), Identifier: "P"})
			}
			_ = json.NewEncoder(w).Encode(out)
		case "/workspaces/personal/projects/project-1/work-items/":
			first, n, next := f.page(cursor, 50)
			out := page[workItem]{NextCursor: next, NextPageResults: next != ""}
			for i := range n {
				out.Results = append(out.Results, workItem{ID: fmt.Sprintf("item-id-%d", first+i), Name: fmt.Sprintf("item %d", first+i), SequenceID: int64(first + i)})
			}
			_ = json.NewEncoder(w).Encode(out)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *morePlane) app() *app {
	return &app{client: newClient(f.srv.URL, testToken), workspace: "personal", receipts: newWriteStore("")}
}

func (f *morePlane) requested(cursor string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.cursors {
		if c == cursor {
			return true
		}
	}
	return false
}

// loadMore presses Load more on the view and returns the view the extension answers.
func loadMore(t *testing.T, a *app, v *wire.View) *wire.View {
	t.Helper()
	r := &ext.UIRequest{UIParams: wire.UIParams{Locale: "en", UIVersion: 3, Action: "more", State: v.State, Values: v.Sections[0].More.Action.Values}}
	result, err := a.action(t.Context(), r)
	if err != nil || result.View == nil {
		t.Fatalf("more action: %+v, %v", result, err)
	}
	return result.View
}

func TestLoadMoreRetainsEarlierWorkItems(t *testing.T) {
	f := newMorePlane(t, 101)
	a := f.app()
	r := &ext.UIRequest{UIParams: wire.UIParams{Locale: "en", UIVersion: 3}}
	v, err := a.page(t.Context(), r, pageState{Mode: "items", ProjectID: "project-1", ProjectName: "Reading"})
	if err != nil || len(v.Sections[0].Items) != 50 || v.Sections[0].More == nil {
		t.Fatalf("initial view: %+v, %v", v, err)
	}
	v = loadMore(t, a, v)
	if len(v.Sections[0].Items) != 100 || v.Sections[0].More == nil {
		t.Fatalf("first Load more: count=%d more=%+v", len(v.Sections[0].Items), v.Sections[0].More)
	}
	v = loadMore(t, a, v)
	items := v.Sections[0].Items
	if len(items) != 101 || items[0].Key != "item-1" || items[len(items)-1].Key != "item-101" {
		t.Fatalf("more did not retain the whole list: count=%d", len(items))
	}
	if v.Sections[0].More != nil {
		t.Fatal("the last page still offers more")
	}
	if err := wire.CheckView(v); err != nil {
		t.Fatal(err)
	}
}

func TestLoadMoreRetainsEarlierProjects(t *testing.T) {
	f := newMorePlane(t, 101)
	a := f.app()
	r := &ext.UIRequest{UIParams: wire.UIParams{Locale: "en", UIVersion: 3}}
	v, err := a.page(t.Context(), r, pageState{})
	if err != nil || len(v.Sections[0].Items) != 100 || v.Sections[0].More == nil {
		t.Fatalf("initial view: %+v, %v", v, err)
	}
	v = loadMore(t, a, v)
	items := v.Sections[0].Items
	if len(items) != 101 || items[0].Key != "project-project-1" || items[len(items)-1].Key != "project-project-101" {
		t.Fatalf("more did not retain the whole list: count=%d", len(items))
	}
	if v.Sections[0].More != nil {
		t.Fatal("the last page still offers more")
	}
	if err := wire.CheckView(v); err != nil {
		t.Fatal(err)
	}
}

func TestLoadMoreStopsAtViewLimit(t *testing.T) {
	f := newMorePlane(t, 500)
	st := pageState{Mode: "items", ProjectID: "project-1", ProjectName: "Reading", Cursor: "50:5:0", History: []string{"", "50:1:0", "50:2:0", "50:3:0", "50:4:0"}}
	v, err := f.app().page(t.Context(), &ext.UIRequest{UIParams: wire.UIParams{Locale: "en", UIVersion: 3}}, st)
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Sections[0].Items) != 250 || v.Sections[0].More != nil {
		t.Fatalf("view cap: items=%d more=%+v", len(v.Sections[0].Items), v.Sections[0].More)
	}
	if err := wire.CheckView(v); err != nil {
		t.Fatal(err)
	}
	if f.requested("50:5:0") {
		t.Fatalf("loaded past the cap: %v", f.cursors)
	}
}
