package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/nwverse/persona/sdk/go/ext"
	"github.com/nwverse/persona/sdk/go/wire"
)

func TestDetailViewWhenAllCommentsAreDeletedOrModerated(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/s/storydel.json":
			fmt.Fprint(w, `{"short_id":"storydel","title":"Moderated thread","url":"https://example.com/post","score":12,"comment_count":2,"description_plain":"Context","submitter_user":"author","tags":["programming"],"comments_url":"https://lobste.rs/s/storydel/moderated","comments":[{"short_id":"c1","is_deleted":true,"is_moderated":false,"score":0,"comment_plain":"deleted","depth":0,"commenting_user":"u1"},{"short_id":"c2","is_deleted":false,"is_moderated":true,"score":0,"comment_plain":"moderated","depth":0,"commenting_user":"u2"}]}`)
		case "/s/storyvis.json":
			fmt.Fprint(w, `{"short_id":"storyvis","title":"Active thread","url":"https://example.com/post","score":12,"comment_count":2,"description_plain":"Context","submitter_user":"author","tags":["programming"],"comments_url":"https://lobste.rs/s/storyvis/active","comments":[{"short_id":"c1","is_deleted":true,"is_moderated":false,"score":0,"comment_plain":"deleted","depth":0,"commenting_user":"u1"},{"short_id":"c2","is_deleted":false,"is_moderated":false,"score":3,"comment_plain":"Visible context","depth":0,"commenting_user":"u2"}]}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	a := &app{client: newClient(srv.URL)}

	for _, tc := range []struct {
		name         string
		id           string
		wantSections int
	}{
		{"all deleted or moderated", "storydel", 2},
		{"visible comment", "storyvis", 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &ext.UIRequest{UIParams: wire.UIParams{Locale: "zh-Hans", UIVersion: 6, Action: "detail", Values: map[string]string{"id": tc.id}}}
			res, err := a.action(t.Context(), r)
			if err != nil || res.View == nil {
				t.Fatalf("detail: %+v, %v", res, err)
			}
			if err := wire.CheckView(res.View); err != nil {
				t.Fatalf("CheckView: %v", err)
			}
			if len(res.View.Sections) != tc.wantSections {
				t.Fatalf("sections=%d, want %d", len(res.View.Sections), tc.wantSections)
			}
		})
	}
}
