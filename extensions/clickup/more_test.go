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

// moreClickUp holds total assigned tasks and answers them in pages of 100 (page is 0-based), flagging the last page.
type moreClickUp struct {
	srv   *httptest.Server
	mu    sync.Mutex
	paths []string
	total int
}

func newMoreClickUp(t *testing.T, total int) *moreClickUp {
	f := &moreClickUp{total: total}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != testToken {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		f.mu.Lock()
		f.paths = append(f.paths, r.URL.RequestURI())
		f.mu.Unlock()
		switch r.URL.Path {
		case "/user":
			_ = json.NewEncoder(w).Encode(map[string]any{"user": user{ID: 42, Username: "reader"}})
		case "/team/team-1/task":
			page, _ := strconv.Atoi(r.URL.Query().Get("page"))
			var out taskPage
			for n := page * 100; n < (page+1)*100 && n < f.total; n++ {
				out.Tasks = append(out.Tasks, task{ID: fmt.Sprintf("t%d", n+1), Name: fmt.Sprintf("task-%d", n+1)})
			}
			out.Last = (page+1)*100 >= f.total
			_ = json.NewEncoder(w).Encode(out)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *moreClickUp) app() *app {
	return &app{client: newClient(f.srv.URL, testToken), receipts: newWriteStore("")}
}

func TestLoadMoreRetainsEarlierTasks(t *testing.T) {
	f := newMoreClickUp(t, 101)
	a := f.app()
	r := &ext.UIRequest{UIParams: wire.UIParams{Locale: "en", UIVersion: 3}}
	st := pageState{Mode: "tasks", TeamID: "team-1", TeamName: "Personal"}
	v, err := a.page(t.Context(), r, st)
	if err != nil || len(v.Sections[0].Items) != 100 || v.Sections[0].More == nil {
		t.Fatalf("initial view: %+v, %v", v, err)
	}
	result, err := a.action(t.Context(), withAction(r, "more", st))
	if err != nil || result.View == nil {
		t.Fatalf("more action: %+v, %v", result, err)
	}
	items := result.View.Sections[0].Items
	if len(items) != 101 || items[0].Key != "task-t1" || items[len(items)-1].Key != "task-t101" {
		t.Fatalf("more did not retain the whole list: count=%d", len(items))
	}
	if result.View.Sections[0].More != nil {
		t.Fatal("the last page still offers more")
	}
	if err := wire.CheckView(result.View); err != nil {
		t.Fatal(err)
	}
	if paths := strings.Join(f.paths, "\n"); !strings.Contains(paths, "page=0") || !strings.Contains(paths, "page=1") {
		t.Fatalf("page requests: %s", paths)
	}
}

func TestLoadMoreStopsAtViewLimit(t *testing.T) {
	f := newMoreClickUp(t, 1000)
	v, err := f.app().page(t.Context(), &ext.UIRequest{UIParams: wire.UIParams{Locale: "en", UIVersion: 3}}, pageState{Mode: "tasks", TeamID: "team-1", TeamName: "Personal", Page: 4})
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Sections[0].Items) != 200 || v.Sections[0].More != nil {
		t.Fatalf("view cap: items=%d more=%+v", len(v.Sections[0].Items), v.Sections[0].More)
	}
	if err := wire.CheckView(v); err != nil {
		t.Fatal(err)
	}
	if paths := strings.Join(f.paths, "\n"); strings.Contains(paths, "page=2") {
		t.Fatalf("loaded past the cap: %s", paths)
	}
}
