package main

import (
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

// moreJira answers the assigned issues in pages of 50 like /search/jql: the first page has no token and the page
// after page k is token "t<k>". total issues are served.
type moreJira struct {
	srv    *httptest.Server
	mu     sync.Mutex
	tokens []string
	total  int
}

func newMoreJira(t *testing.T, total int) *moreJira {
	f := &moreJira{total: total}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/search/jql" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		token := r.URL.Query().Get("nextPageToken")
		f.mu.Lock()
		f.tokens = append(f.tokens, token)
		f.mu.Unlock()
		pageNumber := 1
		if token != "" {
			pageNumber, _ = strconv.Atoi(strings.TrimPrefix(token, "t"))
			pageNumber++
		}
		first := (pageNumber-1)*50 + 1
		var issues []string
		for number := first; number < first+50 && number <= f.total; number++ {
			issues = append(issues, fmt.Sprintf(`{"id":"%d","key":"ENG-%d","fields":{"summary":"issue %d","status":{"name":"To Do"},"project":{"key":"ENG"}}}`, number, number, number))
		}
		body := `{"issues":[` + strings.Join(issues, ",") + `]`
		if first+49 < f.total {
			body += fmt.Sprintf(`,"nextPageToken":"t%d","isLast":false`, pageNumber)
		} else {
			body += `,"isLast":true`
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, body+"}")
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *moreJira) app() *app {
	return &app{client: newClient(f.srv.URL, testToken), receipts: newWriteStore("")}
}

func (f *moreJira) requested(token string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, t := range f.tokens {
		if t == token {
			return true
		}
	}
	return false
}

// issueRows counts the issue rows of the view's first section (it also holds the Create button).
func issueRows(v *wire.View) (keys []string) {
	for _, c := range v.Sections[0].Items {
		if c.Type == wire.ComponentRow {
			keys = append(keys, c.Key)
		}
	}
	return keys
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

func TestLoadMoreRetainsEarlierIssues(t *testing.T) {
	f := newMoreJira(t, 101)
	a := f.app()
	r := &ext.UIRequest{UIParams: wire.UIParams{Locale: "en", UIVersion: 3}}
	v, err := a.page(t.Context(), r, pageState{})
	if err != nil || len(issueRows(v)) != 50 || v.Sections[0].More == nil {
		t.Fatalf("initial view: %+v, %v", v, err)
	}
	v = loadMore(t, a, v)
	if n := len(issueRows(v)); n != 100 || v.Sections[0].More == nil {
		t.Fatalf("first Load more: count=%d more=%+v", n, v.Sections[0].More)
	}
	v = loadMore(t, a, v)
	keys := issueRows(v)
	if len(keys) != 101 || keys[0] != "issue-ENG-1" || keys[len(keys)-1] != "issue-ENG-101" {
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
	if err != nil || len(issueRows(again)) != 101 {
		t.Fatalf("rendering the returned state again: %+v, %v", again, err)
	}
}

func TestLoadMoreStopsAtViewLimit(t *testing.T) {
	f := newMoreJira(t, 1000)
	a := f.app()
	v, err := a.page(t.Context(), &ext.UIRequest{UIParams: wire.UIParams{Locale: "en", UIVersion: 3}}, pageState{})
	if err != nil {
		t.Fatal(err)
	}
	for v.Sections[0].More != nil {
		v = loadMore(t, a, v)
	}
	if n := len(issueRows(v)); n != 250 {
		t.Fatalf("view cap: rows=%d", n)
	}
	if err := wire.CheckView(v); err != nil {
		t.Fatal(err)
	}
	if f.requested("t5") {
		t.Fatal("loaded past the cap")
	}
}

// The query orders by update time, so an issue updated between two reads can be at the end of the first page and at the
// start of the second. Row keys must stay unique for the view to pass the check.
func TestLoadMoreListsAMovedIssueOnce(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		issue := func(n int) string {
			return fmt.Sprintf(`{"id":"%d","key":"ENG-%d","fields":{"summary":"issue %d","status":{"name":"To Do"},"project":{"key":"ENG"}}}`, n, n, n)
		}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("nextPageToken") == "p2" {
			fmt.Fprint(w, `{"issues":[`+issue(2)+`,`+issue(3)+`],"isLast":true}`)
			return
		}
		fmt.Fprint(w, `{"issues":[`+issue(1)+`,`+issue(2)+`],"nextPageToken":"p2","isLast":false}`)
	}))
	t.Cleanup(srv.Close)
	a := &app{client: newClient(srv.URL, testToken), receipts: newWriteStore("")}
	v, err := a.page(t.Context(), &ext.UIRequest{UIParams: wire.UIParams{Locale: "en", UIVersion: 3}}, pageState{})
	if err != nil || v.Sections[0].More == nil {
		t.Fatalf("initial view: %+v, %v", v, err)
	}
	v = loadMore(t, a, v)
	if keys := strings.Join(issueRows(v), " "); keys != "issue-ENG-1 issue-ENG-2 issue-ENG-3" {
		t.Fatalf("rows: %s", keys)
	}
	if err := wire.CheckView(v); err != nil {
		t.Fatal(err)
	}
}
