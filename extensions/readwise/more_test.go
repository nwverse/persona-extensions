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

// moreReader answers the reading queue in pages of 100, like Reader: the first page has no cursor and the page after
// page k is asked for with the cursor "c<k>". total documents are served.
type moreReader struct {
	srv     *httptest.Server
	mu      sync.Mutex
	cursors []string
	total   int
}

func newMoreReader(t *testing.T, total int) *moreReader {
	f := &moreReader{total: total}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/list/" || r.Header.Get("Authorization") != "Token "+testToken {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		cursor := r.URL.Query().Get("pageCursor")
		f.mu.Lock()
		f.cursors = append(f.cursors, cursor)
		f.mu.Unlock()
		pageNumber := 1
		if cursor != "" {
			pageNumber, _ = strconv.Atoi(strings.TrimPrefix(cursor, "c"))
			pageNumber++
		}
		first := (pageNumber-1)*100 + 1
		out := listPage{Count: f.total}
		for number := first; number < first+100 && number <= f.total; number++ {
			out.Results = append(out.Results, document{ID: fmt.Sprintf("doc-%d", number), Title: fmt.Sprintf("Article %d", number)})
		}
		if first+99 < f.total {
			out.Cursor = fmt.Sprintf("c%d", pageNumber)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *moreReader) app() *app {
	return &app{client: newClient(f.srv.URL, testToken), receipts: newWriteStore("")}
}

func (f *moreReader) requested(cursor string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.cursors {
		if c == cursor {
			return true
		}
	}
	return false
}

// documentRows lists the document rows of the view's first section (it also holds the Save button).
func documentRows(v *wire.View) (keys []string) {
	for _, c := range v.Sections[0].Items {
		if c.Type == wire.ComponentRow {
			keys = append(keys, c.Key)
		}
	}
	return keys
}

func loadMore(t *testing.T, a *app, v *wire.View) *wire.View {
	t.Helper()
	r := &ext.UIRequest{UIParams: wire.UIParams{Locale: "en", UIVersion: 3, Action: "more", State: v.State, Values: v.Sections[0].More.Action.Values}}
	result, err := a.action(t.Context(), r)
	if err != nil || result.View == nil {
		t.Fatalf("more action: %+v, %v", result, err)
	}
	return result.View
}

func TestLoadMoreExtendsTheQueue(t *testing.T) {
	f := newMoreReader(t, 150)
	a := f.app()
	v, err := a.page(t.Context(), &ext.UIRequest{UIParams: wire.UIParams{Locale: "en", UIVersion: 3}}, pageState{})
	if err != nil || len(documentRows(v)) != 100 || v.Sections[0].More == nil {
		t.Fatalf("initial view: %+v, %v", v, err)
	}
	v = loadMore(t, a, v)
	keys := documentRows(v)
	if len(keys) != 150 || keys[0] != "document-doc-1" || keys[len(keys)-1] != "document-doc-150" {
		t.Fatalf("more did not extend the queue: count=%d", len(keys))
	}
	if v.Sections[0].More != nil {
		t.Fatal("the last page still offers more")
	}
	if err := wire.CheckView(v); err != nil {
		t.Fatal(err)
	}
}

// Every page holds 100 documents, so the queue is cut at 250 rows to stay within the hub's 300 component limit.
func TestLoadMoreStopsAtViewLimit(t *testing.T) {
	f := newMoreReader(t, 1000)
	a := f.app()
	v, err := a.page(t.Context(), &ext.UIRequest{UIParams: wire.UIParams{Locale: "en", UIVersion: 3}}, pageState{})
	if err != nil {
		t.Fatal(err)
	}
	for v.Sections[0].More != nil {
		v = loadMore(t, a, v)
	}
	if n := len(documentRows(v)); n != 250 {
		t.Fatalf("view cap: rows=%d", n)
	}
	if err := wire.CheckView(v); err != nil {
		t.Fatal(err)
	}
	if f.requested("c3") {
		t.Fatal("loaded past the cap")
	}
}
