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

// moreVikunja answers the assigned task list in pages of 50 with the page count Vikunja reports: 51 tasks (two
// pages), or (full) 500 tasks (ten pages).
type moreVikunja struct {
	srv   *httptest.Server
	mu    sync.Mutex
	paths []string
	total int
}

func (f *moreVikunja) records(page int) []task {
	n := min(50, f.total-(page-1)*50)
	if n < 0 {
		n = 0
	}
	out := make([]task, n)
	for i := range out {
		id := int64((page-1)*50 + i + 1)
		out[i] = task{ID: id, Title: fmt.Sprintf("task %d", id), Identifier: fmt.Sprintf("READ-%d", id)}
	}
	return out
}

func newMoreVikunja(t *testing.T, total int) *moreVikunja {
	f := &moreVikunja{total: total}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.paths = append(f.paths, r.URL.RequestURI())
		f.mu.Unlock()
		switch r.URL.Path {
		case "/api/v1/user":
			_ = json.NewEncoder(w).Encode(user{ID: 1, Username: "reader"})
		case "/api/v1/tasks":
			page, _ := strconv.Atoi(r.URL.Query().Get("page"))
			w.Header().Set("X-Pagination-Total", strconv.Itoa(f.total))
			w.Header().Set("X-Pagination-Total-Pages", strconv.Itoa((f.total+49)/50))
			_ = json.NewEncoder(w).Encode(f.records(page))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *moreVikunja) app() *app {
	return &app{client: newClient(f.srv.URL, testToken), receipts: newWriteStore("")}
}

func TestLoadMoreRetainsEarlierTasks(t *testing.T) {
	f := newMoreVikunja(t, 51)
	a := f.app()
	r := &ext.UIRequest{UIParams: wire.UIParams{Locale: "en", UIVersion: 3}}
	v, err := a.page(t.Context(), r, pageState{})
	if err != nil || len(v.Sections[0].Items) != 50 || v.Sections[0].More == nil {
		t.Fatalf("initial view: %+v, %v", v, err)
	}
	r.Action, r.Values, r.State = "more", v.Sections[0].More.Action.Values, v.State
	result, err := a.action(t.Context(), r)
	if err != nil || result.View == nil {
		t.Fatalf("more action: %+v, %v", result, err)
	}
	items := result.View.Sections[0].Items
	if len(items) != 51 || items[0].Key != "task-1" || items[len(items)-1].Key != "task-51" {
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
	f := newMoreVikunja(t, 500)
	v, err := f.app().page(t.Context(), &ext.UIRequest{UIParams: wire.UIParams{Locale: "en", UIVersion: 3}}, pageState{Page: 5})
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
