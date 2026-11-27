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

// morePaperless answers the document list in pages of 25 with a next link while documents remain: 26 documents, or
// (full) 250. Every document carries a search highlight, so each one renders a row and a text line.
type morePaperless struct {
	srv   *httptest.Server
	mu    sync.Mutex
	paths []string
	total int
}

func (f *morePaperless) records(pageNumber int) []document {
	n := min(25, f.total-(pageNumber-1)*25)
	if n < 0 {
		n = 0
	}
	out := make([]document, n)
	for i := range out {
		id := int64((pageNumber-1)*25 + i + 1)
		out[i] = document{ID: id, Title: fmt.Sprintf("document %d", id), Created: "2026-11-01", PageCount: 1}
		out[i].SearchHit.Highlights = "matched text"
	}
	return out
}

func newMorePaperless(t *testing.T, total int) *morePaperless {
	f := &morePaperless{total: total}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.paths = append(f.paths, r.URL.RequestURI())
		f.mu.Unlock()
		if r.URL.Path != "/api/documents/" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		pageNumber, _ := strconv.Atoi(r.URL.Query().Get("page"))
		var next *string
		if pageNumber*25 < f.total {
			nextURL := fmt.Sprintf("%s/api/documents/?page=%d&page_size=25", f.srv.URL, pageNumber+1)
			next = &nextURL
		}
		_ = json.NewEncoder(w).Encode(page[document]{Count: f.total, Next: next, Results: f.records(pageNumber)})
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *morePaperless) app() *app { return &app{client: newClient(f.srv.URL, testToken)} }

func rowKeys(section wire.Section) []string {
	var out []string
	for _, item := range section.Items {
		if item.Type == wire.ComponentRow {
			out = append(out, item.Key)
		}
	}
	return out
}

func TestLoadMoreRetainsEarlierDocuments(t *testing.T) {
	f := newMorePaperless(t, 26)
	a := f.app()
	r := &ext.UIRequest{UIParams: wire.UIParams{Locale: "en", UIVersion: 3}}
	v, err := a.page(t.Context(), r, pageState{})
	if err != nil || len(rowKeys(v.Sections[0])) != 25 || v.Sections[0].More == nil {
		t.Fatalf("initial view: %+v, %v", v, err)
	}
	r.Action, r.Values, r.State = "more", v.Sections[0].More.Action.Values, v.State
	result, err := a.action(t.Context(), r)
	if err != nil || result.View == nil {
		t.Fatalf("more action: %+v, %v", result, err)
	}
	keys := rowKeys(result.View.Sections[0])
	if len(keys) != 26 || keys[0] != "document-1" || keys[len(keys)-1] != "document-26" {
		t.Fatalf("more did not retain the whole list: count=%d", len(keys))
	}
	if result.View.Sections[0].More != nil {
		t.Fatal("the last page still offers more")
	}
	if err := wire.CheckView(result.View); err != nil {
		t.Fatal(err)
	}
	if paths := strings.Join(f.paths, "\n"); !strings.Contains(paths, "page=1&") || !strings.Contains(paths, "page=2&") {
		t.Fatalf("page requests: %s", paths)
	}
}

func TestLoadMoreStopsAtViewLimit(t *testing.T) {
	f := newMorePaperless(t, 250)
	v, err := f.app().page(t.Context(), &ext.UIRequest{UIParams: wire.UIParams{Locale: "en", UIVersion: 3}}, pageState{Page: 7})
	if err != nil {
		t.Fatal(err)
	}
	if keys := rowKeys(v.Sections[0]); len(keys) != 125 || v.Sections[0].More != nil {
		t.Fatalf("view cap: rows=%d more=%+v", len(keys), v.Sections[0].More)
	}
	if err := wire.CheckView(v); err != nil {
		t.Fatal(err)
	}
	if paths := strings.Join(f.paths, "\n"); strings.Contains(paths, "page=6&") {
		t.Fatalf("loaded past the cap: %s", paths)
	}
}
