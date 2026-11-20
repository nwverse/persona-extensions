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

// moreYouTrack answers the assigned issue list in batches of 50: with full set every batch up to $skip=450 is full,
// otherwise the first batch is full and the second holds a single issue.
type moreYouTrack struct {
	srv   *httptest.Server
	mu    sync.Mutex
	paths []string
	full  bool
}

func (f *moreYouTrack) records(skip int) []issue {
	n := 50
	switch {
	case f.full && skip >= 500, !f.full && skip > 50:
		return nil
	case !f.full && skip == 50:
		n = 1
	}
	out := make([]issue, n)
	for i := range out {
		id := skip + i + 1
		out[i] = issue{ID: fmt.Sprintf("2-%d", id), IDReadable: fmt.Sprintf("READ-%d", id), Summary: fmt.Sprintf("issue %d", id), Project: project{ID: "0-7", Name: "Reading", ShortName: "READ"}}
	}
	return out
}

func newMoreYouTrack(t *testing.T, full bool) *moreYouTrack {
	f := &moreYouTrack{full: full}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.paths = append(f.paths, r.URL.RequestURI())
		f.mu.Unlock()
		if r.URL.Path != "/api/issues" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		skip, _ := strconv.Atoi(r.URL.Query().Get("$skip"))
		_ = json.NewEncoder(w).Encode(f.records(skip))
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *moreYouTrack) app() *app {
	return &app{client: newClient(f.srv.URL, testToken), receipts: newWriteStore("")}
}

func TestLoadMoreRetainsEarlierIssues(t *testing.T) {
	f := newMoreYouTrack(t, false)
	a := f.app()
	r := &ext.UIRequest{UIParams: wire.UIParams{Locale: "en", UIVersion: 3}}
	v, err := a.page(t.Context(), r, pageState{})
	if err != nil || len(v.Sections[0].Items) != 50 || v.Sections[0].More == nil {
		t.Fatalf("initial view: %+v, %v", v, err)
	}
	result, err := a.action(t.Context(), withAction(r, "more", pageState{}))
	if err != nil || result.View == nil {
		t.Fatalf("more action: %+v, %v", result, err)
	}
	items := result.View.Sections[0].Items
	if len(items) != 51 || items[0].Key != "issue-2-1" || items[len(items)-1].Key != "issue-2-51" {
		t.Fatalf("more did not retain the whole list: count=%d", len(items))
	}
	if result.View.Sections[0].More != nil {
		t.Fatal("a short final batch still offers more")
	}
	if err := wire.CheckView(result.View); err != nil {
		t.Fatal(err)
	}
	if paths := strings.Join(f.paths, "\n"); !strings.Contains(paths, "%24skip=0") || !strings.Contains(paths, "%24skip=50") {
		t.Fatalf("skip requests: %s", paths)
	}
}

func TestLoadMoreStopsAtViewLimit(t *testing.T) {
	f := newMoreYouTrack(t, true)
	v, err := f.app().page(t.Context(), &ext.UIRequest{UIParams: wire.UIParams{Locale: "en", UIVersion: 3}}, pageState{Skip: 200})
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Sections[0].Items) != 250 || v.Sections[0].More != nil {
		t.Fatalf("view cap: items=%d more=%+v", len(v.Sections[0].Items), v.Sections[0].More)
	}
	if err := wire.CheckView(v); err != nil {
		t.Fatal(err)
	}
	if paths := strings.Join(f.paths, "\n"); strings.Contains(paths, "%24skip=250") {
		t.Fatalf("loaded past the cap: %s", paths)
	}
}
