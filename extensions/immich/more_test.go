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

func moreAssetID(n int) string { return fmt.Sprintf("00000000-0000-4000-8000-%012d", n) }

// moreImmich answers the favorites list in pages of 40: the first page has no cursor and the page after page k is
// cursor "c<k>". total assets are served.
type moreImmich struct {
	srv     *httptest.Server
	mu      sync.Mutex
	cursors []string
	total   int
}

func newMoreImmich(t *testing.T, total int) *moreImmich {
	f := &moreImmich{total: total}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in map[string]any
		if r.URL.Path != "/api/search/metadata" || json.NewDecoder(r.Body).Decode(&in) != nil {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		cursor, _ := in["cursor"].(string)
		f.mu.Lock()
		f.cursors = append(f.cursors, cursor)
		f.mu.Unlock()
		pageNumber := 1
		if cursor != "" {
			pageNumber, _ = strconv.Atoi(strings.TrimPrefix(cursor, "c"))
			pageNumber++
		}
		first := (pageNumber-1)*40 + 1
		var out searchResponse
		for number := first; number < first+40 && number <= f.total; number++ {
			item := sampleAsset()
			item.ID, item.OriginalFileName = moreAssetID(number), fmt.Sprintf("photo-%d.jpg", number)
			out.Assets.Items = append(out.Assets.Items, item)
		}
		if first+39 < f.total {
			next := fmt.Sprintf("c%d", pageNumber)
			out.Assets.NextCursor = &next
		}
		_ = json.NewEncoder(w).Encode(out)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *moreImmich) app() *app {
	return &app{client: newClient(f.srv.URL, testToken), receipts: newWriteStore("")}
}

func (f *moreImmich) requested(cursor string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.cursors {
		if c == cursor {
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

func TestLoadMoreRetainsEarlierAssets(t *testing.T) {
	f := newMoreImmich(t, 81)
	a := f.app()
	r := &ext.UIRequest{UIParams: wire.UIParams{Locale: "en", UIVersion: 3}}
	v, err := a.page(t.Context(), r, pageState{})
	if err != nil || len(v.Sections[0].Items) != 40 || v.Sections[0].More == nil {
		t.Fatalf("initial view: %+v, %v", v, err)
	}
	v = loadMore(t, a, v)
	if len(v.Sections[0].Items) != 80 || v.Sections[0].More == nil {
		t.Fatalf("first Load more: count=%d more=%+v", len(v.Sections[0].Items), v.Sections[0].More)
	}
	v = loadMore(t, a, v)
	items := v.Sections[0].Items
	if len(items) != 81 || items[0].Key != "asset-"+moreAssetID(1) || items[len(items)-1].Key != "asset-"+moreAssetID(81) {
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
	if err != nil || len(again.Sections[0].Items) != 81 {
		t.Fatalf("rendering the returned state again: %+v, %v", again, err)
	}
}

func TestLoadMoreStopsAtViewLimit(t *testing.T) {
	f := newMoreImmich(t, 500)
	a := f.app()
	v, err := a.page(t.Context(), &ext.UIRequest{UIParams: wire.UIParams{Locale: "en", UIVersion: 3}}, pageState{})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20 && v.Sections[0].More != nil; i++ {
		v = loadMore(t, a, v)
	}
	if len(v.Sections[0].Items) != 250 || v.Sections[0].More != nil {
		t.Fatalf("view cap: items=%d more=%+v", len(v.Sections[0].Items), v.Sections[0].More)
	}
	if err := wire.CheckView(v); err != nil {
		t.Fatal(err)
	}
	if f.requested("c7") {
		t.Fatalf("loaded past the cap: %v", f.cursors)
	}
}
