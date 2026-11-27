package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/nwverse/persona/sdk/go/ext"
	"github.com/nwverse/persona/sdk/go/wire"
)

// moreArxiv holds total matching papers and answers each query with the slice start..start+max_results, the
// way the arXiv API does.
type moreArxiv struct {
	srv   *httptest.Server
	mu    sync.Mutex
	paths []string
	total int
}

func newMoreArxiv(t *testing.T, total int) *moreArxiv {
	f := &moreArxiv{total: total}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.paths = append(f.paths, r.URL.RequestURI())
		f.mu.Unlock()
		if r.URL.Path != "/api/query" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		start, _ := strconv.Atoi(r.URL.Query().Get("start"))
		count, _ := strconv.Atoi(r.URL.Query().Get("max_results"))
		var b strings.Builder
		b.WriteString(`<feed xmlns="http://www.w3.org/2005/Atom">`)
		for n := start; n < start+count && n < f.total; n++ {
			fmt.Fprintf(&b, `<entry><id>http://arxiv.org/abs/2601.%05dv1</id><title>paper %d</title><category term="cs.HC"/></entry>`, n+1, n+1)
		}
		b.WriteString(`</feed>`)
		w.Header().Set("Content-Type", "application/atom+xml")
		fmt.Fprint(w, b.String())
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *moreArxiv) app() *app { return &app{client: newClient(f.srv.URL)} }

func TestLoadMoreRetainsEarlierPapers(t *testing.T) {
	f := newMoreArxiv(t, 21)
	a := f.app()
	r := &ext.UIRequest{UIParams: wire.UIParams{Locale: "en", UIVersion: 3}}
	st := pageState{Query: "reading"}
	v, err := a.page(t.Context(), r, st)
	if err != nil || len(v.Sections[0].Items) != 20 || v.Sections[0].More == nil {
		t.Fatalf("initial view: %+v, %v", v, err)
	}
	r.Action, r.State = "more", ext.EncodeState(st)
	result, err := a.action(t.Context(), r)
	if err != nil || result.View == nil {
		t.Fatalf("more action: %+v, %v", result, err)
	}
	items := result.View.Sections[0].Items
	if len(items) != 21 || items[0].Key != "paper-2601.00001v1" || items[len(items)-1].Key != "paper-2601.00021v1" {
		t.Fatalf("more did not retain the whole list: count=%d", len(items))
	}
	if result.View.Sections[0].More != nil {
		t.Fatal("a short final batch still offers more")
	}
	if err := wire.CheckView(result.View); err != nil {
		t.Fatal(err)
	}
}

func TestLoadMoreStopsAtViewLimit(t *testing.T) {
	f := newMoreArxiv(t, 1000)
	v, err := f.app().page(t.Context(), &ext.UIRequest{UIParams: wire.UIParams{Locale: "en", UIVersion: 3}}, pageState{Query: "reading", Start: 80})
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Sections[0].Items) != 100 || v.Sections[0].More != nil {
		t.Fatalf("view cap: items=%d more=%+v", len(v.Sections[0].Items), v.Sections[0].More)
	}
	if err := wire.CheckView(v); err != nil {
		t.Fatal(err)
	}
	for _, path := range f.paths {
		start, count := queryInt(path, "start"), queryInt(path, "max_results")
		if start+count > 100 {
			t.Fatalf("asked for papers past the cap: %s", path)
		}
	}
}

func queryInt(requestURI, key string) int {
	u, err := url.ParseRequestURI(requestURI)
	if err != nil {
		return 0
	}
	n, _ := strconv.Atoi(u.Query().Get(key))
	return n
}
