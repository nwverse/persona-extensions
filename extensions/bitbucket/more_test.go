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

// moreBitbucket holds total open pull requests and answers them in pages of 50 by start offset, the way
// Bitbucket Server does: every page but the last carries nextPageStart.
type moreBitbucket struct {
	srv   *httptest.Server
	mu    sync.Mutex
	paths []string
	total int
}

func newMoreBitbucket(t *testing.T, total int) *moreBitbucket {
	f := &moreBitbucket{total: total}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+testToken {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		f.mu.Lock()
		f.paths = append(f.paths, r.URL.RequestURI())
		f.mu.Unlock()
		if r.URL.Path != "/projects/ENG/repos/reader/pull-requests" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		start, _ := strconv.Atoi(r.URL.Query().Get("start"))
		page := prPage{Limit: 50, Start: start}
		for n := start; n < start+50 && n < f.total; n++ {
			page.Values = append(page.Values, pullRequest{ID: int64(n + 1), Title: fmt.Sprintf("change-%d", n+1), State: "OPEN", Open: true})
		}
		page.Size = len(page.Values)
		if start+50 < f.total {
			next := start + 50
			page.NextPageStart = &next
		} else {
			page.IsLastPage = true
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(page)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *moreBitbucket) app() *app {
	return &app{client: newClient(f.srv.URL, testToken), receipts: newWriteStore("")}
}

func TestLoadMoreRetainsEarlierPullRequests(t *testing.T) {
	f := newMoreBitbucket(t, 51)
	a := f.app()
	r := &ext.UIRequest{UIParams: wire.UIParams{Locale: "en", UIVersion: 3}}
	st := pageState{Project: "ENG", Repo: "reader", Mode: "list"}
	v, err := a.page(t.Context(), r, st)
	if err != nil || len(v.Sections[0].Items) != 50 || v.Sections[0].More == nil {
		t.Fatalf("initial view: %+v, %v", v, err)
	}
	r.Action, r.State = "more", ext.EncodeState(st)
	result, err := a.action(t.Context(), r)
	if err != nil || result.View == nil {
		t.Fatalf("more action: %+v, %v", result, err)
	}
	items := result.View.Sections[0].Items
	if len(items) != 51 || items[0].Key != "pr-1" || items[len(items)-1].Key != "pr-51" {
		t.Fatalf("more did not retain the whole list: count=%d", len(items))
	}
	if result.View.Sections[0].More != nil {
		t.Fatal("the last page still offers more")
	}
	if err := wire.CheckView(result.View); err != nil {
		t.Fatal(err)
	}
	if paths := strings.Join(f.paths, "\n"); !strings.Contains(paths, "start=0") || !strings.Contains(paths, "start=50") {
		t.Fatalf("page requests: %s", paths)
	}
}

func TestLoadMoreStopsAtViewLimit(t *testing.T) {
	f := newMoreBitbucket(t, 1000)
	v, err := f.app().page(t.Context(), &ext.UIRequest{UIParams: wire.UIParams{Locale: "en", UIVersion: 3}}, pageState{Project: "ENG", Repo: "reader", Mode: "list", Start: 200})
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Sections[0].Items) != 250 || v.Sections[0].More != nil {
		t.Fatalf("view cap: items=%d more=%+v", len(v.Sections[0].Items), v.Sections[0].More)
	}
	if err := wire.CheckView(v); err != nil {
		t.Fatal(err)
	}
	if paths := strings.Join(f.paths, "\n"); strings.Contains(paths, "start=250") {
		t.Fatalf("loaded past the cap: %s", paths)
	}
}
