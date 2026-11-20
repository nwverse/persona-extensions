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

// moreMemos answers the note list in pages of 50: the first page has no token and the page after page k is
// token "t<k>". total notes are served.
type moreMemos struct {
	srv    *httptest.Server
	mu     sync.Mutex
	tokens []string
	total  int
}

func newMoreMemos(t *testing.T, total int) *moreMemos {
	f := &moreMemos{total: total}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/memos" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		token := r.URL.Query().Get("pageToken")
		f.mu.Lock()
		f.tokens = append(f.tokens, token)
		f.mu.Unlock()
		pageNumber := 1
		if token != "" {
			pageNumber, _ = strconv.Atoi(strings.TrimPrefix(token, "t"))
			pageNumber++
		}
		first := (pageNumber-1)*50 + 1
		out := memoPage{}
		for number := first; number < first+50 && number <= f.total; number++ {
			item := sampleMemo()
			item.Name, item.Snippet = fmt.Sprintf("memos/memo-%d", number), fmt.Sprintf("note %d", number)
			out.Memos = append(out.Memos, item)
		}
		if first+49 < f.total {
			out.NextPageToken = fmt.Sprintf("t%d", pageNumber)
		}
		_ = json.NewEncoder(w).Encode(out)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *moreMemos) app() *app {
	return &app{client: newClient(f.srv.URL, testToken), username: "reader", creator: "users/reader", receipts: newWriteStore("")}
}

func (f *moreMemos) requested(token string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, t := range f.tokens {
		if t == token {
			return true
		}
	}
	return false
}

// loadMore presses Load more on the view and returns the view the extension answers.
func loadMore(t *testing.T, a *app, v *wire.View) *wire.View {
	t.Helper()
	r := &ext.UIRequest{UIParams: wire.UIParams{Locale: "en", UIVersion: 3, Action: "more", State: v.State, Values: v.Sections[0].More.Action.Values}}
	result, err := a.action(t.Context(), r)
	if err != nil || result.View == nil {
		t.Fatalf("more action: %+v, %v", result, err)
	}
	return result.View
}

func TestLoadMoreRetainsEarlierNotes(t *testing.T) {
	f := newMoreMemos(t, 101)
	a := f.app()
	r := &ext.UIRequest{UIParams: wire.UIParams{Locale: "en", UIVersion: 3}}
	v, err := a.page(t.Context(), r, pageState{})
	if err != nil || len(v.Sections[0].Items) != 50 || v.Sections[0].More == nil {
		t.Fatalf("initial view: %+v, %v", v, err)
	}
	v = loadMore(t, a, v)
	if len(v.Sections[0].Items) != 100 || v.Sections[0].More == nil {
		t.Fatalf("first Load more: count=%d more=%+v", len(v.Sections[0].Items), v.Sections[0].More)
	}
	v = loadMore(t, a, v)
	items := v.Sections[0].Items
	if len(items) != 101 || items[0].Key != "memo-memo-1" || items[len(items)-1].Key != "memo-memo-101" {
		t.Fatalf("more did not retain the whole list: count=%d", len(items))
	}
	if v.Sections[0].More != nil {
		t.Fatal("the last page still offers more")
	}
	if err := wire.CheckView(v); err != nil {
		t.Fatal(err)
	}
	r.State = v.State
	again, err := a.render(t.Context(), r)
	if err != nil || len(again.Sections[0].Items) != 101 {
		t.Fatalf("rendering the returned state again: %+v, %v", again, err)
	}
}

func TestLoadMoreStopsAtViewLimit(t *testing.T) {
	f := newMoreMemos(t, 500)
	a := f.app()
	v, err := a.page(t.Context(), &ext.UIRequest{UIParams: wire.UIParams{Locale: "en", UIVersion: 3}}, pageState{})
	if err != nil {
		t.Fatal(err)
	}
	for range 4 {
		if v.Sections[0].More == nil {
			t.Fatalf("Load more missing at %d notes", len(v.Sections[0].Items))
		}
		v = loadMore(t, a, v)
	}
	if len(v.Sections[0].Items) != 250 || v.Sections[0].More != nil {
		t.Fatalf("view cap: items=%d more=%+v", len(v.Sections[0].Items), v.Sections[0].More)
	}
	if err := wire.CheckView(v); err != nil {
		t.Fatal(err)
	}
	if f.requested("t5") {
		t.Fatalf("loaded past the cap: %v", f.tokens)
	}
}
