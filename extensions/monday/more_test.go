package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/nwverse/persona/sdk/go/ext"
	"github.com/nwverse/persona/sdk/go/wire"
)

var itemsCursor = regexp.MustCompile(`cursor: "([^"]*)"`)
var itemByID = regexp.MustCompile(`items\(ids: \[(\d+)\]\)`)

// moreMonday serves the items of board 7 in pages of 50: the first page has no cursor, the page after page k is
// asked for with the cursor "c<k>" the answer before it carried. total items are served.
type moreMonday struct {
	srv     *httptest.Server
	mu      sync.Mutex
	cursors []string
	total   int
}

func newMoreMonday(t *testing.T, total int) *moreMonday {
	f := &moreMonday{total: total}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Query string `json:"query"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if m := itemByID.FindStringSubmatch(in.Query); m != nil {
			it := sampleItem()
			it.Name = "item " + m[1]
			fields := map[string]any{}
			raw, _ := json.Marshal(it)
			_ = json.Unmarshal(raw, &fields)
			fields["id"], fields["board"] = m[1], map[string]string{"id": "7"}
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"items": []map[string]any{fields}}})
			return
		}
		if !strings.Contains(in.Query, "items_page") {
			_ = json.NewEncoder(w).Encode(map[string]any{"data": boardPage{Boards: []board{sampleBoard()}}})
			return
		}
		pageNumber, cursor := 1, ""
		if m := itemsCursor.FindStringSubmatch(in.Query); m != nil {
			cursor = m[1]
			if _, err := fmt.Sscanf(cursor, "c%d", &pageNumber); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			pageNumber++
		}
		f.mu.Lock()
		f.cursors = append(f.cursors, cursor)
		f.mu.Unlock()
		first := (pageNumber-1)*50 + 1
		page := itemsPage{}
		for number := first; number < first+50 && number <= f.total; number++ {
			it := sampleItem()
			it.ID, it.Name = int64(number), fmt.Sprintf("item %d", number)
			page.Items = append(page.Items, it)
		}
		if first+49 < f.total {
			page.Cursor = fmt.Sprintf("c%d", pageNumber)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"boards": []map[string]any{{"items_page": page}}}})
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *moreMonday) app() *app {
	return &app{client: newClient(f.srv.URL, testToken), receipts: newWriteStore("")}
}

// The Load more row carries no values, so the next cursor has to be in the view state the extension answered with.
func TestLoadMoreReadsTheNextPage(t *testing.T) {
	f := newMoreMonday(t, 101)
	a := f.app()
	r := &ext.UIRequest{UIParams: wire.UIParams{Locale: "en", UIVersion: 3}}
	v, err := a.page(t.Context(), r, pageState{Mode: "items", BoardID: 7, BoardName: "Reading"})
	if err != nil || len(v.Sections[0].Items) != 50 || v.Sections[0].More == nil {
		t.Fatalf("initial view: %+v, %v", v, err)
	}
	for _, want := range []string{"item 51", "item 101"} {
		r.Action, r.State, r.Values = "more", v.State, nil
		result, err := a.action(t.Context(), r)
		if err != nil || result.View == nil {
			t.Fatalf("more action: %+v, %v", result, err)
		}
		v = result.View
		if first := v.Sections[0].Items[0]; first.Title != want {
			t.Fatalf("after more: first row %q, want %q", first.Title, want)
		}
		if err := wire.CheckView(v); err != nil {
			t.Fatal(err)
		}
	}
	if v.Sections[0].More != nil {
		t.Fatal("the last page still offers more")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if got := strings.Join(f.cursors, ","); got != ",c1,c2" {
		t.Fatalf("cursors requested: %q", got)
	}
}

// Going back from an item shows the page the item was opened from, and keeps offering the page after it.
func TestBackShowsThePageTheItemWasOpenedFrom(t *testing.T) {
	f := newMoreMonday(t, 101)
	a := f.app()
	r := &ext.UIRequest{UIParams: wire.UIParams{Locale: "en", UIVersion: 3}}
	v, err := a.page(t.Context(), r, pageState{Mode: "items", BoardID: 7, BoardName: "Reading"})
	if err != nil {
		t.Fatal(err)
	}
	run := func(action string, v *wire.View, values map[string]string) *wire.View {
		t.Helper()
		r.Action, r.State, r.Values = action, v.State, values
		result, err := a.action(t.Context(), r)
		if err != nil || result.View == nil {
			t.Fatalf("%s action: %+v, %v", action, result, err)
		}
		return result.View
	}
	v = run("more", v, nil)
	v = run("detail", v, map[string]string{"id": "60"})
	v = run("back", v, nil)
	items := v.Sections[0].Items
	if len(items) != 50 || items[0].Title != "item 51" || v.Sections[0].More == nil {
		t.Fatalf("back: count=%d more=%+v", len(items), v.Sections[0].More)
	}
}
