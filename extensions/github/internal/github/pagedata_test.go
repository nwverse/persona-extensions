package github

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// listAPI is a GitHub whose 待我 review search has one PR titled title, or fails (502, GraphQL and REST alike) once
// fail is set; searches counts the requests for it. The tests below ask on one goroutine only, so it needs no lock.
type listAPI struct {
	title    string
	fail     bool
	searches int
}

// connector is a connector signed in to a, its 待我 review list read a while ago (stale) with a PR titled "Old".
func (a *listAPI) connector(t *testing.T) *Connector {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/user" {
			_, _ = w.Write([]byte(`{"login":"alice"}`))
			return
		}
		a.searches++
		if a.fail {
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte(`{"message":"Server Error"}`))
			return
		}
		_, _ = fmt.Fprintf(w, `{"data":{"search":{"nodes":[{"number":12,"title":%q,"url":"https://github.com/example-org/example-repo/pull/12",
"repository":{"nameWithOwner":"example-org/example-repo"}}]}}}`, a.title)
	}))
	t.Cleanup(srv.Close)
	m := &machine{tokens: []string{oldToken}}
	c := New(Config{Auth: AuthConfig{FromMachine: "mac"}}, "Mac", m.fetch)
	c.api = srv.URL
	c.check(context.Background())
	c.lists.tabs = map[string]pageList{TabReview: {at: time.Now().Add(-2 * listFresh),
		entries: []Entry{{Repo: "example-org/example-repo", Number: 12, PR: true, Title: "Old"}}}}
	return c
}

// overlap makes the moment two renders (the page and the card, cards of two app versions) overlap in: the first
// time an ask has looked at the cache and not yet taken the lock to read, another ask, not fresh, reads the list
// (GitHub answers "New"), and GitHub fails from then on. It is when that other read was. Deterministic: the other ask
// runs at that moment through the listLooked hook, not by the scheduler's luck.
func overlap(t *testing.T, c *Connector, a *listAPI) *time.Time {
	var other time.Time
	inside := false
	listLooked = func(tab string) {
		if inside { // the other ask's own look
			return
		}
		inside = true
		entries, at, err := c.List(context.Background(), tab, false)
		if err != nil || len(entries) != 1 || entries[0].Title != "New" {
			t.Errorf("the other ask: %v %+v", err, entries)
		}
		other = at
		a.fail = true
	}
	t.Cleanup(func() { listLooked = nil })
	return &other
}

// An ask that found the list stale, while another ask read it in the meantime, answers with what that one read: it
// does not search GitHub a second time, and so does not fall back on the older list when that search would fail
// (review of #627, P2).
func TestListLooksAgainBeforeReading(t *testing.T) {
	a := &listAPI{title: "New"}
	c := a.connector(t)
	other := overlap(t, c, a)
	entries, at, err := c.List(context.Background(), TabReview, false)
	if err != nil || len(entries) != 1 || entries[0].Title != "New" || !at.Equal(*other) {
		t.Fatalf("got %+v read at %v (%v), want the other ask's \"New\" read at %v", entries, at, err, *other)
	}
	if a.searches != 1 {
		t.Fatalf("GitHub was searched %d times, want once (the other ask's read)", a.searches)
	}
}

// An ask for a fresh list (the page's 刷新) still reads GitHub then, but when that read fails it falls back on the
// newest list there is, the other ask's, not the one it saw stale before.
func TestFreshListFallsBackOnTheNewest(t *testing.T) {
	a := &listAPI{title: "New"}
	c := a.connector(t)
	other := overlap(t, c, a)
	entries, at, err := c.List(context.Background(), TabReview, true)
	if err == nil {
		t.Fatalf("a fresh ask did not read GitHub again: %+v (%d searches)", entries, a.searches)
	}
	if len(entries) != 1 || entries[0].Title != "New" || !at.Equal(*other) {
		t.Fatalf("got %+v read at %v, want the other ask's \"New\" read at %v", entries, at, *other)
	}
	if a.searches < 2 {
		t.Fatalf("GitHub was searched %d times: the fresh ask did not read again", a.searches)
	}
}
