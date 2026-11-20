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

// moreKimai answers the timesheet history in pages of 30: page 1 is full and page 2 holds a single entry, or (full)
// every page up to 10 is full. Each entry renders two text lines.
type moreKimai struct {
	srv   *httptest.Server
	mu    sync.Mutex
	paths []string
	full  bool
}

func (f *moreKimai) records(page int) []timesheet {
	n := 30
	switch {
	case f.full && page > 10, !f.full && page > 2:
		return nil
	case !f.full && page == 2:
		n = 1
	}
	out := make([]timesheet, n)
	for i := range out {
		number := (page-1)*30 + i + 1
		out[i] = sampleTimesheet()
		out[i].ID, out[i].Begin = int64(number), fmt.Sprintf("entry-%d", number)
	}
	return out
}

func newMoreKimai(t *testing.T, full bool) *moreKimai {
	f := &moreKimai{full: full}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.paths = append(f.paths, r.URL.RequestURI())
		f.mu.Unlock()
		if r.URL.Path != "/api/timesheets" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		_ = json.NewEncoder(w).Encode(f.records(page))
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *moreKimai) app() *app {
	return &app{client: newClient(f.srv.URL, testToken), receipts: newWriteStore("")}
}

func TestLoadMoreRetainsEarlierEntries(t *testing.T) {
	f := newMoreKimai(t, false)
	a := f.app()
	r := &ext.UIRequest{UIParams: wire.UIParams{Locale: "en", UIVersion: 3}}
	st := pageState{View: "timesheets", Page: 1}
	v, err := a.page(t.Context(), r, st)
	if err != nil || len(v.Sections[0].Items) != 60 || v.Sections[0].More == nil {
		t.Fatalf("initial view: %+v, %v", v, err)
	}
	r.Action, r.Values, r.State = "more", v.Sections[0].More.Action.Values, v.State
	result, err := a.action(t.Context(), r)
	if err != nil || result.View == nil {
		t.Fatalf("more action: %+v, %v", result, err)
	}
	items := result.View.Sections[0].Items
	if len(items) != 62 || !strings.HasSuffix(items[0].Text, "entry-1") || !strings.HasSuffix(items[len(items)-2].Text, "entry-31") {
		t.Fatalf("more did not retain the whole list: components=%d", len(items))
	}
	if result.View.Sections[0].More != nil {
		t.Fatal("a short final page still offers more")
	}
	if err := wire.CheckView(result.View); err != nil {
		t.Fatal(err)
	}
	if paths := strings.Join(f.paths, "\n"); !strings.Contains(paths, "page=1&") || !strings.Contains(paths, "page=2&") {
		t.Fatalf("page requests: %s", paths)
	}
}

func TestLoadMoreStopsAtViewLimit(t *testing.T) {
	f := newMoreKimai(t, true)
	v, err := f.app().page(t.Context(), &ext.UIRequest{UIParams: wire.UIParams{Locale: "en", UIVersion: 3}}, pageState{View: "timesheets", Page: 6})
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Sections[0].Items) != 250 || v.Sections[0].More != nil {
		t.Fatalf("view cap: components=%d more=%+v", len(v.Sections[0].Items), v.Sections[0].More)
	}
	if err := wire.CheckView(v); err != nil {
		t.Fatal(err)
	}
	if paths := strings.Join(f.paths, "\n"); strings.Contains(paths, "page=6&") {
		t.Fatalf("loaded past the cap: %s", paths)
	}
}
