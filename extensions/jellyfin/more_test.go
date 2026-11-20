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

func moreMediaID(n int) string { return fmt.Sprintf("%032x", n) }

// moreJellyfin answers the resume list in batches of 30 with the total Jellyfin reports: 31 items, or (full) 300.
type moreJellyfin struct {
	srv   *httptest.Server
	mu    sync.Mutex
	paths []string
	total int
}

func (f *moreJellyfin) records(start int) []mediaItem {
	n := min(30, f.total-start)
	if n < 0 {
		n = 0
	}
	out := make([]mediaItem, n)
	for i := range out {
		out[i] = mediaItem{ID: moreMediaID(start + i + 1), Name: fmt.Sprintf("item %d", start+i+1), Type: "Episode"}
	}
	return out
}

func newMoreJellyfin(t *testing.T, total int) *moreJellyfin {
	f := &moreJellyfin{total: total}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.paths = append(f.paths, r.URL.RequestURI())
		f.mu.Unlock()
		switch r.URL.Path {
		case "/Users/Me":
			_ = json.NewEncoder(w).Encode(user{ID: userID, Name: "Reader"})
		case "/UserItems/Resume":
			start, _ := strconv.Atoi(r.URL.Query().Get("startIndex"))
			_ = json.NewEncoder(w).Encode(itemPage{Items: f.records(start), TotalRecordCount: f.total, StartIndex: start})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *moreJellyfin) app() *app {
	return &app{client: newClient(f.srv.URL, testToken), receipts: newWriteStore("")}
}

func TestLoadMoreRetainsEarlierItems(t *testing.T) {
	f := newMoreJellyfin(t, 31)
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
	if len(items) != 31 || items[0].Key != "item-"+moreMediaID(1) || items[len(items)-1].Key != "item-"+moreMediaID(31) {
		t.Fatalf("more did not retain the whole list: count=%d", len(items))
	}
	if result.View.Sections[0].More != nil {
		t.Fatal("the last batch still offers more")
	}
	if err := wire.CheckView(result.View); err != nil {
		t.Fatal(err)
	}
	if paths := strings.Join(f.paths, "\n"); !strings.Contains(paths, "startIndex=0&") || !strings.Contains(paths, "startIndex=30&") {
		t.Fatalf("start index requests: %s", paths)
	}
}

func TestLoadMoreStopsAtViewLimit(t *testing.T) {
	f := newMoreJellyfin(t, 300)
	v, err := f.app().page(t.Context(), &ext.UIRequest{UIParams: wire.UIParams{Locale: "en", UIVersion: 3}}, pageState{StartIndex: 270})
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Sections[0].Items) != 250 || v.Sections[0].More != nil {
		t.Fatalf("view cap: items=%d more=%+v", len(v.Sections[0].Items), v.Sections[0].More)
	}
	if err := wire.CheckView(v); err != nil {
		t.Fatal(err)
	}
	if paths := strings.Join(f.paths, "\n"); strings.Contains(paths, "startIndex=270&") {
		t.Fatalf("loaded past the cap: %s", paths)
	}
}
