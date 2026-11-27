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

// moreRedmine answers the assigned open issue list in batches of 50 with the total Redmine reports: 51 issues, or
// (full) 500.
type moreRedmine struct {
	srv   *httptest.Server
	mu    sync.Mutex
	paths []string
	total int
}

func (f *moreRedmine) records(offset int) []issue {
	n := min(50, f.total-offset)
	if n < 0 {
		n = 0
	}
	out := make([]issue, n)
	for i := range out {
		id := int64(offset + i + 1)
		out[i] = issue{ID: id, Subject: fmt.Sprintf("issue %d", id), Project: namedResource{ID: 7, Name: "Reading"}}
	}
	return out
}

func newMoreRedmine(t *testing.T, total int) *moreRedmine {
	f := &moreRedmine{total: total}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.paths = append(f.paths, r.URL.RequestURI())
		f.mu.Unlock()
		if r.URL.Path != "/issues.json" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
		_ = json.NewEncoder(w).Encode(issuePage{Issues: f.records(offset), TotalCount: f.total, Limit: 50, Offset: offset})
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *moreRedmine) app() *app {
	return &app{client: newClient(f.srv.URL, testToken), receipts: newWriteStore("")}
}

func TestLoadMoreRetainsEarlierIssues(t *testing.T) {
	f := newMoreRedmine(t, 51)
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
	if len(items) != 51 || items[0].Key != "issue-1" || items[len(items)-1].Key != "issue-51" {
		t.Fatalf("more did not retain the whole list: count=%d", len(items))
	}
	if result.View.Sections[0].More != nil {
		t.Fatal("the last batch still offers more")
	}
	if err := wire.CheckView(result.View); err != nil {
		t.Fatal(err)
	}
	if paths := strings.Join(f.paths, "\n"); !strings.Contains(paths, "offset=0") || !strings.Contains(paths, "offset=50") {
		t.Fatalf("offset requests: %s", paths)
	}
}

func TestLoadMoreStopsAtViewLimit(t *testing.T) {
	f := newMoreRedmine(t, 500)
	v, err := f.app().page(t.Context(), &ext.UIRequest{UIParams: wire.UIParams{Locale: "en", UIVersion: 3}}, pageState{Offset: 200})
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
