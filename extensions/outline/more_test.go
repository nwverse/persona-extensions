package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/nwverse/persona/sdk/go/ext"
	"github.com/nwverse/persona/sdk/go/wire"
)

// moreOutline answers the document search in batches of 25 with a nextPath while results remain: 26 results, or
// (full) 250. Every result carries a context snippet, so each one renders a row and a text line.
type moreOutline struct {
	srv     *httptest.Server
	mu      sync.Mutex
	offsets []int
	total   int
}

func (f *moreOutline) records(offset int) []searchResult {
	n := min(25, f.total-offset)
	if n < 0 {
		n = 0
	}
	out := make([]searchResult, n)
	for i := range out {
		number := offset + i + 1
		out[i] = searchResult{Context: "matched text", Document: document{ID: fmt.Sprintf("doc-%d", number), Title: fmt.Sprintf("document %d", number), UpdatedAt: "2026-11-10T10:00:00Z"}}
	}
	return out
}

func newMoreOutline(t *testing.T, total int) *moreOutline {
	f := &moreOutline{total: total}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in map[string]any
		if r.URL.Path != "/api/documents.search" || json.NewDecoder(r.Body).Decode(&in) != nil {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		offset, _ := in["offset"].(float64)
		f.mu.Lock()
		f.offsets = append(f.offsets, int(offset))
		f.mu.Unlock()
		pagination := map[string]any{"offset": int(offset), "limit": 25}
		if int(offset)+25 < f.total {
			pagination["nextPath"] = fmt.Sprintf("/api/documents.search?offset=%d&limit=25", int(offset)+25)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "data": f.records(int(offset)), "pagination": pagination})
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *moreOutline) app() *app {
	return &app{client: newClient(f.srv.URL, testToken), receipts: newWriteStore("")}
}

func resultKeys(section wire.Section) []string {
	var out []string
	for _, item := range section.Items {
		if item.Type == wire.ComponentRow {
			out = append(out, item.Key)
		}
	}
	return out
}

func TestLoadMoreRetainsEarlierResults(t *testing.T) {
	f := newMoreOutline(t, 26)
	a := f.app()
	r := &ext.UIRequest{UIParams: wire.UIParams{Locale: "en", UIVersion: 3}}
	v, err := a.page(t.Context(), r, pageState{Query: "reading"})
	if err != nil || len(resultKeys(v.Sections[0])) != 25 || v.Sections[0].More == nil {
		t.Fatalf("initial view: %+v, %v", v, err)
	}
	r.Action, r.Values, r.State = "more", v.Sections[0].More.Action.Values, v.State
	result, err := a.action(t.Context(), r)
	if err != nil || result.View == nil {
		t.Fatalf("more action: %+v, %v", result, err)
	}
	keys := resultKeys(result.View.Sections[0])
	if len(keys) != 26 || keys[0] != "document-doc-1" || keys[len(keys)-1] != "document-doc-26" {
		t.Fatalf("more did not retain the whole list: count=%d", len(keys))
	}
	if result.View.Sections[0].More != nil {
		t.Fatal("the last batch still offers more")
	}
	if err := wire.CheckView(result.View); err != nil {
		t.Fatal(err)
	}
	if len(f.offsets) < 2 || f.offsets[0] != 0 || f.offsets[len(f.offsets)-1] != 25 {
		t.Fatalf("offset requests: %v", f.offsets)
	}
}

func TestLoadMoreStopsAtViewLimit(t *testing.T) {
	f := newMoreOutline(t, 250)
	v, err := f.app().page(t.Context(), &ext.UIRequest{UIParams: wire.UIParams{Locale: "en", UIVersion: 3}}, pageState{Query: "reading", Offset: 150})
	if err != nil {
		t.Fatal(err)
	}
	if keys := resultKeys(v.Sections[0]); len(keys) != 125 || v.Sections[0].More != nil {
		t.Fatalf("view cap: results=%d more=%+v", len(keys), v.Sections[0].More)
	}
	if err := wire.CheckView(v); err != nil {
		t.Fatal(err)
	}
	for _, offset := range f.offsets {
		if offset >= 125 {
			t.Fatalf("loaded past the cap: %v", f.offsets)
		}
	}
}
