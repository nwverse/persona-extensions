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

// moreAnytype holds total objects in space-1 and answers them in batches of 50 by offset.
type moreAnytype struct {
	srv   *httptest.Server
	mu    sync.Mutex
	paths []string
	total int
}

func newMoreAnytype(t *testing.T, total int) *moreAnytype {
	f := &moreAnytype{total: total}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.paths = append(f.paths, r.URL.RequestURI())
		f.mu.Unlock()
		if r.URL.Path != "/v1/spaces/space-1/objects" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
		var items []knowledgeObject
		for n := offset; n < offset+50 && n < f.total; n++ {
			items = append(items, knowledgeObject{ID: fmt.Sprintf("obj-%d", n+1), Name: fmt.Sprintf("note-%d", n+1)})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(pageOf(items, offset+50 < f.total))
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *moreAnytype) app() *app {
	return &app{client: newClient(f.srv.URL, testToken), receipts: newWriteStore("")}
}

func TestLoadMoreRetainsEarlierObjects(t *testing.T) {
	f := newMoreAnytype(t, 51)
	a := f.app()
	r := &ext.UIRequest{UIParams: wire.UIParams{Locale: "en", UIVersion: 3}}
	st := pageState{Mode: "objects", SpaceID: "space-1", SpaceName: "Personal"}
	v, err := a.page(t.Context(), r, st)
	if err != nil || len(v.Sections[0].Items) != 50 || v.Sections[0].More == nil {
		t.Fatalf("initial view: %+v, %v", v, err)
	}
	result, err := a.action(t.Context(), withAction(r, "more", st))
	if err != nil || result.View == nil {
		t.Fatalf("more action: %+v, %v", result, err)
	}
	items := result.View.Sections[0].Items
	if len(items) != 51 || items[0].Key != "object-obj-1" || items[len(items)-1].Key != "object-obj-51" {
		t.Fatalf("more did not retain the whole list: count=%d", len(items))
	}
	if result.View.Sections[0].More != nil {
		t.Fatal("the last batch still offers more")
	}
	if err := wire.CheckView(result.View); err != nil {
		t.Fatal(err)
	}
	if paths := strings.Join(f.paths, "\n"); !strings.Contains(paths, "offset=0") || !strings.Contains(paths, "offset=50") {
		t.Fatalf("batch requests: %s", paths)
	}
}

func TestLoadMoreStopsAtViewLimit(t *testing.T) {
	f := newMoreAnytype(t, 1000)
	v, err := f.app().page(t.Context(), &ext.UIRequest{UIParams: wire.UIParams{Locale: "en", UIVersion: 3}}, pageState{Mode: "objects", SpaceID: "space-1", SpaceName: "Personal", Offset: 200})
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
