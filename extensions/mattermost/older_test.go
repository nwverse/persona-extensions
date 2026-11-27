package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/nwverse/persona/sdk/go/ext"
	"github.com/nwverse/persona/sdk/go/wire"
)

// serverPosts serves a channel of total posts, post-1 the newest, the way Mattermost does: without `before` the newest
// posts of the page, with `before` only posts strictly older than that post (`CreateAt < CreateAt of the post`), and
// `prev_post_id` is the post right before the oldest one of the list (GetPrevPostIdFromPostList), which is not in the
// list itself.
func serverPosts(t *testing.T, total int) *app {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v4/users/user-2":
			_ = json.NewEncoder(w).Encode(user{ID: "user-2", Username: "reviewer"})
		case "/api/v4/channels/channel-1/posts":
			first := 1
			if before := r.URL.Query().Get("before"); before != "" {
				number, _ := strconv.Atoi(strings.TrimPrefix(before, "post-"))
				first = number + 1
			}
			out := postList{Posts: map[string]post{}}
			last := 0
			for number := first; number < first+30 && number <= total; number++ {
				id := fmt.Sprintf("post-%d", number)
				out.Order = append(out.Order, id)
				out.Posts[id] = post{ID: id, UserID: "user-2", ChannelID: "channel-1", Message: fmt.Sprintf("message %d", number), CreateAt: int64(total-number+1) * 1000}
				last = number
			}
			if last > 0 && last < total {
				out.PrevPostID = fmt.Sprintf("post-%d", last+1)
			}
			_ = json.NewEncoder(w).Encode(out)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return &app{client: newClient(srv.URL, testToken)}
}

func TestLoadMoreSkipsNoMessage(t *testing.T) {
	a := serverPosts(t, 70)
	v, err := a.page(t.Context(), &ext.UIRequest{UIParams: wire.UIParams{Locale: "en", UIVersion: 3}}, pageState{Mode: "posts", ChannelID: "channel-1"})
	if err != nil || len(v.Sections[0].Items) != 30 || v.Sections[0].More == nil {
		t.Fatalf("initial view: %d rows, %v", len(v.Sections[0].Items), err)
	}
	v = loadMore(t, a, v)
	items := v.Sections[0].Items
	if len(items) != 60 {
		t.Fatalf("first Load more: %d rows", len(items))
	}
	for i, item := range items {
		if want := fmt.Sprintf("message %d", i+1); !strings.HasSuffix(item.Text, want) {
			t.Fatalf("row %d is %q, want %q", i+1, item.Text, want)
		}
	}
}

func TestPostsToolOffersTheOldestPostAsTheCursor(t *testing.T) {
	a := serverPosts(t, 70)
	text, err := a.callTool(t.Context(), "mattermost_posts", json.RawMessage(`{"channel_id":"channel-1"}`))
	if err != nil || !strings.Contains(text, "Older messages before: post-30\n") {
		t.Fatalf("first batch: %v, %s", err, text[max(0, len(text)-80):])
	}
	text, err = a.callTool(t.Context(), "mattermost_posts", json.RawMessage(`{"channel_id":"channel-1","before":"post-30"}`))
	if err != nil || !strings.Contains(text, "message 31\n") {
		t.Fatalf("second batch: %v, %s", err, text[:min(len(text), 120)])
	}
}
