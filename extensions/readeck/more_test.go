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

// moreReadeck answers the unread bookmark list in batches of 30 with a next link while bookmarks remain: 31
// bookmarks, or (full) 300.
type moreReadeck struct {
	srv   *httptest.Server
	mu    sync.Mutex
	paths []string
	total int
}

func (f *moreReadeck) records(offset int) []bookmark {
	n := min(30, f.total-offset)
	if n < 0 {
		n = 0
	}
	out := make([]bookmark, n)
	for i := range out {
		out[i] = bookmark{ID: fmt.Sprintf("bm_%d", offset+i+1), Title: fmt.Sprintf("bookmark %d", offset+i+1)}
	}
	return out
}

func newMoreReadeck(t *testing.T, total int) *moreReadeck {
	f := &moreReadeck{total: total}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.paths = append(f.paths, r.URL.RequestURI())
		f.mu.Unlock()
		if r.URL.Path != "/api/bookmarks" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
		w.Header().Set("Total-Count", strconv.Itoa(f.total))
		if offset+30 < f.total {
			w.Header().Set("Link", fmt.Sprintf(`<%s/api/bookmarks?offset=%d&limit=30>; rel="next"`, f.srv.URL, offset+30))
		}
		_ = json.NewEncoder(w).Encode(f.records(offset))
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *moreReadeck) app() *app {
	return &app{client: newClient(f.srv.URL, testToken), receipts: newWriteStore("")}
}

func TestLoadMoreRetainsEarlierBookmarks(t *testing.T) {
	f := newMoreReadeck(t, 31)
	a := f.app()
	r := &ext.UIRequest{UIParams: wire.UIParams{Locale: "en", UIVersion: 3}}
	v, err := a.page(t.Context(), r, pageState{})
	if err != nil || len(v.Sections[0].Items) != 30 || v.Sections[0].More == nil {
		t.Fatalf("initial view: %+v, %v", v, err)
	}
	r.Action, r.Values, r.State = "more", v.Sections[0].More.Action.Values, v.State
	result, err := a.action(t.Context(), r)
	if err != nil || result.View == nil {
		t.Fatalf("more action: %+v, %v", result, err)
	}
	items := result.View.Sections[0].Items
	if len(items) != 31 || items[0].Key != "bookmark-bm_1" || items[len(items)-1].Key != "bookmark-bm_31" {
		t.Fatalf("more did not retain the whole list: count=%d", len(items))
	}
	if result.View.Sections[0].More != nil {
		t.Fatal("the last batch still offers more")
	}
	if err := wire.CheckView(result.View); err != nil {
		t.Fatal(err)
	}
	if paths := strings.Join(f.paths, "\n"); !strings.Contains(paths, "offset=0&") || !strings.Contains(paths, "offset=30&") {
		t.Fatalf("offset requests: %s", paths)
	}
}

func TestLoadMoreStopsAtViewLimit(t *testing.T) {
	f := newMoreReadeck(t, 300)
	v, err := f.app().page(t.Context(), &ext.UIRequest{UIParams: wire.UIParams{Locale: "en", UIVersion: 3}}, pageState{Offset: 270})
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Sections[0].Items) != 250 || v.Sections[0].More != nil {
		t.Fatalf("view cap: items=%d more=%+v", len(v.Sections[0].Items), v.Sections[0].More)
	}
	if err := wire.CheckView(v); err != nil {
		t.Fatal(err)
	}
	if paths := strings.Join(f.paths, "\n"); strings.Contains(paths, "offset=270&") {
		t.Fatalf("loaded past the cap: %s", paths)
	}
}
