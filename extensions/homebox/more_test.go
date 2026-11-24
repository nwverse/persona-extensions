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

func moreEntityID(n int) string { return fmt.Sprintf("00000000-0000-4000-8000-%012d", n) }

// moreHomebox answers the inventory list in pages of 30 with the total Homebox reports: 31 entities, or (full) 300.
type moreHomebox struct {
	srv   *httptest.Server
	mu    sync.Mutex
	paths []string
	total int
}

func (f *moreHomebox) records(page int) []entity {
	n := min(30, f.total-(page-1)*30)
	if n < 0 {
		n = 0
	}
	out := make([]entity, n)
	for i := range out {
		number := (page-1)*30 + i + 1
		out[i] = entity{ID: moreEntityID(number), Name: fmt.Sprintf("item %d", number), Quantity: 1}
	}
	return out
}

func newMoreHomebox(t *testing.T, total int) *moreHomebox {
	f := &moreHomebox{total: total}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.paths = append(f.paths, r.URL.RequestURI())
		f.mu.Unlock()
		switch r.URL.Path {
		case "/api/v1/entities":
			page, _ := strconv.Atoi(r.URL.Query().Get("page"))
			_ = json.NewEncoder(w).Encode(entityPage{Items: f.records(page), Page: page, PageSize: 30, Total: f.total})
		case "/api/v1/entity-types":
			_ = json.NewEncoder(w).Encode([]entityType{{ID: typeID, Name: "Item"}})
		case "/api/v1/entities/tree":
			_ = json.NewEncoder(w).Encode([]treeItem{{ID: locationID, Name: "Study", Type: "location"}})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *moreHomebox) app() *app {
	return &app{client: newClient(f.srv.URL, testToken), receipts: newWriteStore("")}
}

func (f *moreHomebox) requests() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return strings.Join(f.paths, "\n")
}

func TestLoadMoreRetainsEarlierEntities(t *testing.T) {
	f := newMoreHomebox(t, 31)
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
	if len(items) != 31 || items[0].Key != "entity-"+moreEntityID(1) || items[len(items)-1].Key != "entity-"+moreEntityID(31) {
		t.Fatalf("more did not retain the whole list: count=%d", len(items))
	}
	if result.View.Sections[0].More != nil {
		t.Fatal("the last page still offers more")
	}
	if err := wire.CheckView(result.View); err != nil {
		t.Fatal(err)
	}
	if paths := f.requests(); !strings.Contains(paths, "page=1&") || !strings.Contains(paths, "page=2&") {
		t.Fatalf("page requests: %s", paths)
	}
}

func TestLoadMoreStopsAtViewLimit(t *testing.T) {
	f := newMoreHomebox(t, 300)
	v, err := f.app().page(t.Context(), &ext.UIRequest{UIParams: wire.UIParams{Locale: "en", UIVersion: 3}}, pageState{Page: 9})
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Sections[0].Items) != 250 || v.Sections[0].More != nil {
		t.Fatalf("view cap: items=%d more=%+v", len(v.Sections[0].Items), v.Sections[0].More)
	}
	if err := wire.CheckView(v); err != nil {
		t.Fatal(err)
	}
	if paths := f.requests(); strings.Contains(paths, "page=10&") {
		t.Fatalf("loaded past the cap: %s", paths)
	}
}
