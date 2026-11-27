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

// moreGoodLinks answers the unread list in batches of 50: with full set every batch up to offset 450 is full
// and reports hasMore, otherwise the first batch is full and the second holds a single link.
type moreGoodLinks struct {
	srv   *httptest.Server
	mu    sync.Mutex
	paths []string
	full  bool
}

func (f *moreGoodLinks) records(offset int) linkPage {
	n, more := 50, true
	switch {
	case f.full && offset > 450, !f.full && offset > 50:
		return linkPage{}
	case !f.full && offset == 50:
		n, more = 1, false
	case f.full && offset == 450:
		more = false
	}
	out := make([]link, n)
	for i := range out {
		id := offset + i + 1
		out[i] = link{ID: fmt.Sprintf("link-%d", id), URL: fmt.Sprintf("https://articles.example.com/%d", id), Title: fmt.Sprintf("article-%d", id)}
	}
	return linkPage{Data: out, HasMore: more}
}

func newMoreGoodLinks(t *testing.T, full bool) *moreGoodLinks {
	f := &moreGoodLinks{full: full}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.paths = append(f.paths, r.URL.RequestURI())
		f.mu.Unlock()
		if r.URL.Path != "/api/v1/lists/unread" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
		_ = json.NewEncoder(w).Encode(f.records(offset))
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *moreGoodLinks) app() *app {
	return &app{client: newClient(f.srv.URL, testToken), receipts: newWriteStore("")}
}

func TestLoadMoreRetainsEarlierLinks(t *testing.T) {
	f := newMoreGoodLinks(t, false)
	a := f.app()
	r := &ext.UIRequest{UIParams: wire.UIParams{Locale: "en", UIVersion: 3}}
	st := pageState{View: "unread"}
	v, err := a.page(t.Context(), r, st)
	if err != nil || len(v.Sections[0].Items) != 50 || v.Sections[0].More == nil {
		t.Fatalf("initial view: %+v, %v", v, err)
	}
	result, err := a.action(t.Context(), withAction(r, "more", st))
	if err != nil || result.View == nil {
		t.Fatalf("more action: %+v, %v", result, err)
	}
	items := result.View.Sections[0].Items
	if len(items) != 51 || items[0].Key != "link-link-1" || items[len(items)-1].Key != "link-link-51" {
		t.Fatalf("more did not retain the whole list: count=%d", len(items))
	}
	if result.View.Sections[0].More != nil {
		t.Fatal("the last batch still offers more")
	}
	if err := wire.CheckView(result.View); err != nil {
		t.Fatal(err)
	}
	if paths := strings.Join(f.paths, "\n"); !strings.Contains(paths, "offset=0") || !strings.Contains(paths, "offset=50") {
		t.Fatalf("batch requests: %s", paths)
	}
}

func TestLoadMoreStopsAtViewLimit(t *testing.T) {
	f := newMoreGoodLinks(t, true)
	v, err := f.app().page(t.Context(), &ext.UIRequest{UIParams: wire.UIParams{Locale: "en", UIVersion: 3}}, pageState{View: "unread", Offset: 200})
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Sections[0].Items) != 250 || v.Sections[0].More != nil {
		t.Fatalf("view cap: items=%d more=%+v", len(v.Sections[0].Items), v.Sections[0].More)
	}
	if err := wire.CheckView(v); err != nil {
		t.Fatal(err)
	}
	if paths := strings.Join(f.paths, "\n"); strings.Contains(paths, "offset=250") {
		t.Fatalf("loaded past the cap: %s", paths)
	}
}
