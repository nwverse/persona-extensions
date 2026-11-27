package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"

	"github.com/nwverse/persona/sdk/go/ext"
	"github.com/nwverse/persona/sdk/go/wire"
)

// moreFreshRSS serves total unread articles newest first, 30 per answer. The continuation of an answer is the number of
// articles served before the next one, as a positive integer.
type moreFreshRSS struct {
	srv   *httptest.Server
	mu    sync.Mutex
	reads []string
	total int
}

func moreArticleID(number int) string {
	return fmt.Sprintf("tag:google.com,2005:reader/item/%016x", number)
}

func newMoreFreshRSS(t *testing.T, total int) *moreFreshRSS {
	f := &moreFreshRSS{total: total}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/accounts/ClientLogin":
			_, _ = w.Write([]byte("SID=" + testAuth + "\nAuth=" + testAuth + "\n"))
		case "/reader/api/0/stream/contents/reading-list":
			f.mu.Lock()
			f.reads = append(f.reads, r.URL.Query().Get("c"))
			f.mu.Unlock()
			start, _ := strconv.Atoi(r.URL.Query().Get("c"))
			var out articlePage
			for number := start + 1; number <= start+30 && number <= f.total; number++ {
				item := sampleArticle()
				item.ID, item.Title = moreArticleID(number), fmt.Sprintf("article %d", number)
				out.Items = append(out.Items, item)
			}
			if start+30 < f.total {
				out.Continuation = strconv.Itoa(start + 30)
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(out)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *moreFreshRSS) app() *app {
	return &app{client: newClient(f.srv.URL, testUsername, testPassword), receipts: newWriteStore("")}
}

func (f *moreFreshRSS) requested(continuation string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.reads {
		if c == continuation {
			return true
		}
	}
	return false
}

// articleRows lists the article rows of the view's first section.
func articleRows(v *wire.View) (keys []string) {
	for _, c := range v.Sections[0].Items {
		if c.Type == wire.ComponentRow {
			keys = append(keys, c.Key)
		}
	}
	return keys
}

// press runs an action on the view's state and returns the view the extension answers.
func press(t *testing.T, a *app, v *wire.View, action string, values map[string]string) *wire.View {
	t.Helper()
	r := &ext.UIRequest{UIParams: wire.UIParams{Locale: "en", UIVersion: 3, Action: action, State: v.State, Values: values}}
	result, err := a.action(t.Context(), r)
	if err != nil || result.View == nil {
		t.Fatalf("%s action: %+v, %v", action, result, err)
	}
	return result.View
}

func loadMore(t *testing.T, a *app, v *wire.View) *wire.View {
	t.Helper()
	return press(t, a, v, "more", v.Sections[0].More.Action.Values)
}

func TestLoadMoreRetainsEarlierArticles(t *testing.T) {
	f := newMoreFreshRSS(t, 61)
	a := f.app()
	r := &ext.UIRequest{UIParams: wire.UIParams{Locale: "en", UIVersion: 3}}
	v, err := a.page(t.Context(), r, pageState{})
	if err != nil || len(articleRows(v)) != 30 || v.Sections[0].More == nil {
		t.Fatalf("initial view: %+v, %v", v, err)
	}
	v = loadMore(t, a, v)
	if n := len(articleRows(v)); n != 60 || v.Sections[0].More == nil {
		t.Fatalf("first Load more: count=%d more=%+v", n, v.Sections[0].More)
	}
	v = loadMore(t, a, v)
	keys := articleRows(v)
	if len(keys) != 61 || keys[0] != "article-"+moreArticleID(1) || keys[len(keys)-1] != "article-"+moreArticleID(61) {
		t.Fatalf("more did not retain the whole list: count=%d", len(keys))
	}
	if v.Sections[0].More != nil {
		t.Fatal("the last page still offers more")
	}
	if err := wire.CheckView(v); err != nil {
		t.Fatal(err)
	}
	r.State = v.State
	again, err := a.render(t.Context(), r)
	if err != nil || len(articleRows(again)) != 61 {
		t.Fatalf("rendering the returned state again: %+v, %v", again, err)
	}
}

func TestLoadMoreStopsAtViewLimit(t *testing.T) {
	f := newMoreFreshRSS(t, 1000)
	a := f.app()
	v, err := a.page(t.Context(), &ext.UIRequest{UIParams: wire.UIParams{Locale: "en", UIVersion: 3}}, pageState{})
	if err != nil {
		t.Fatal(err)
	}
	for v.Sections[0].More != nil {
		v = loadMore(t, a, v)
	}
	if n := len(articleRows(v)); n != 250 {
		t.Fatalf("view cap: rows=%d", n)
	}
	if err := wire.CheckView(v); err != nil {
		t.Fatal(err)
	}
	if f.requested("270") {
		t.Fatal("loaded past the cap")
	}
}

// Opening an article and going back must show the list as it was, not the page after it.
func TestBackFromArticleShowsTheSameList(t *testing.T) {
	f := newMoreFreshRSS(t, 61)
	a := f.app()
	v, err := a.page(t.Context(), &ext.UIRequest{UIParams: wire.UIParams{Locale: "en", UIVersion: 3}}, pageState{})
	if err != nil {
		t.Fatal(err)
	}
	v = loadMore(t, a, v)
	detail := press(t, a, v, "detail", map[string]string{"id": moreArticleID(5)})
	if detail.Title != "article 5" {
		t.Fatalf("detail: %q", detail.Title)
	}
	back := press(t, a, detail, "back", nil)
	keys := articleRows(back)
	if len(keys) != 60 || keys[0] != "article-"+moreArticleID(1) || keys[len(keys)-1] != "article-"+moreArticleID(60) || back.Sections[0].More == nil {
		t.Fatalf("back: count=%d more=%+v", len(keys), back.Sections[0].More)
	}
}

// An article shown after Load more must open: it is not on the first page of the stream.
func TestOpenArticleFromALaterPage(t *testing.T) {
	f := newMoreFreshRSS(t, 61)
	a := f.app()
	v, err := a.page(t.Context(), &ext.UIRequest{UIParams: wire.UIParams{Locale: "en", UIVersion: 3}}, pageState{})
	if err != nil {
		t.Fatal(err)
	}
	v = loadMore(t, a, v)
	detail := press(t, a, v, "detail", map[string]string{"id": moreArticleID(45)})
	if detail.Title != "article 45" {
		t.Fatalf("detail: %q", detail.Title)
	}
	if err := wire.CheckView(detail); err != nil {
		t.Fatal(err)
	}
}
