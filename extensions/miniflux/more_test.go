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

// moreMiniflux holds total unread entries and answers them in batches of 50 by offset.
type moreMiniflux struct {
	srv   *httptest.Server
	mu    sync.Mutex
	paths []string
	total int
}

func newMoreMiniflux(t *testing.T, total int) *moreMiniflux {
	f := &moreMiniflux{total: total}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Auth-Token") != testToken {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		f.mu.Lock()
		f.paths = append(f.paths, r.URL.RequestURI())
		f.mu.Unlock()
		if r.URL.Path != "/v1/entries" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
		out := entryPage{Total: f.total}
		for n := offset; n < offset+50 && n < f.total; n++ {
			out.Entries = append(out.Entries, entry{ID: int64(n + 1), Title: fmt.Sprintf("article-%d", n+1)})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *moreMiniflux) app() *app {
	return &app{client: newClient(f.srv.URL, testToken), receipts: newWriteStore("")}
}

func TestLoadMoreRetainsEarlierEntries(t *testing.T) {
	f := newMoreMiniflux(t, 51)
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
	if len(items) != 51 || items[0].Key != "entry-1" || items[len(items)-1].Key != "entry-51" {
		t.Fatalf("more did not retain the whole list: count=%d", len(items))
	}
	if result.View.Sections[0].More != nil {
		t.Fatal("a short final batch still offers more")
	}
	if err := wire.CheckView(result.View); err != nil {
		t.Fatal(err)
	}
	if paths := strings.Join(f.paths, "\n"); !strings.Contains(paths, "offset=0") || !strings.Contains(paths, "offset=50") {
		t.Fatalf("batch requests: %s", paths)
	}
}

func TestLoadMoreStopsAtViewLimit(t *testing.T) {
	f := newMoreMiniflux(t, 1000)
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
