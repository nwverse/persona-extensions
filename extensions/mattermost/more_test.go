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

// moreMattermost answers the channel posts in batches of 30: the first batch has no before and the batch after batch
// k is read with before "b<k>". total posts are served, each message with the given number of lines.
type moreMattermost struct {
	srv     *httptest.Server
	mu      sync.Mutex
	befores []string
	total   int
	lines   int
}

func newMoreMattermost(t *testing.T, total, lines int) *moreMattermost {
	f := &moreMattermost{total: total, lines: lines}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v4/users/user-2":
			_ = json.NewEncoder(w).Encode(user{ID: "user-2", Username: "reviewer"})
		case "/api/v4/channels/channel-1/posts":
			before := r.URL.Query().Get("before")
			f.mu.Lock()
			f.befores = append(f.befores, before)
			f.mu.Unlock()
			batch := 1
			if before != "" {
				batch, _ = strconv.Atoi(strings.TrimPrefix(before, "b"))
				batch++
			}
			first := (batch-1)*30 + 1
			out := postList{Posts: map[string]post{}}
			for number := first; number < first+30 && number <= f.total; number++ {
				id := fmt.Sprintf("post-%d", number)
				message := fmt.Sprintf("message %d", number)
				for range f.lines - 1 {
					message += "\nmore text"
				}
				out.Order = append(out.Order, id)
				out.Posts[id] = post{ID: id, UserID: "user-2", ChannelID: "channel-1", Message: message}
			}
			if first+29 < f.total {
				out.PrevPostID = fmt.Sprintf("b%d", batch)
			}
			_ = json.NewEncoder(w).Encode(out)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *moreMattermost) app() *app { return &app{client: newClient(f.srv.URL, testToken)} }

func (f *moreMattermost) requested(before string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, b := range f.befores {
		if b == before {
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

func TestLoadMoreRetainsEarlierMessages(t *testing.T) {
	f := newMoreMattermost(t, 61, 1)
	a := f.app()
	r := &ext.UIRequest{UIParams: wire.UIParams{Locale: "en", UIVersion: 3}}
	v, err := a.page(t.Context(), r, pageState{Mode: "posts", ChannelID: "channel-1"})
	if err != nil || len(v.Sections[0].Items) != 30 || v.Sections[0].More == nil {
		t.Fatalf("initial view: %+v, %v", v, err)
	}
	v = loadMore(t, a, v)
	if len(v.Sections[0].Items) != 60 || v.Sections[0].More == nil {
		t.Fatalf("first Load more: count=%d more=%+v", len(v.Sections[0].Items), v.Sections[0].More)
	}
	v = loadMore(t, a, v)
	items := v.Sections[0].Items
	if len(items) != 61 || !strings.HasSuffix(items[0].Text, "message 1") || !strings.HasSuffix(items[len(items)-1].Text, "message 61") {
		t.Fatalf("more did not retain the whole list: count=%d", len(items))
	}
	if v.Sections[0].More != nil {
		t.Fatal("the last batch still offers more")
	}
	if err := wire.CheckView(v); err != nil {
		t.Fatal(err)
	}
	r.State = v.State
	again, err := a.render(t.Context(), r)
	if err != nil || len(again.Sections[0].Items) != 61 {
		t.Fatalf("rendering the returned state again: %+v, %v", again, err)
	}
}

func TestLoadMoreStopsAtViewLimit(t *testing.T) {
	f := newMoreMattermost(t, 500, 2)
	a := f.app()
	v, err := a.page(t.Context(), &ext.UIRequest{UIParams: wire.UIParams{Locale: "en", UIVersion: 3}}, pageState{Mode: "posts", ChannelID: "channel-1"})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20 && v.Sections[0].More != nil; i++ {
		v = loadMore(t, a, v)
	}
	if len(v.Sections[0].Items) != 250 || v.Sections[0].More != nil {
		t.Fatalf("view cap: components=%d more=%+v", len(v.Sections[0].Items), v.Sections[0].More)
	}
	if err := wire.CheckView(v); err != nil {
		t.Fatal(err)
	}
	if f.requested("b5") {
		t.Fatalf("loaded past the cap: %v", f.befores)
	}
}
