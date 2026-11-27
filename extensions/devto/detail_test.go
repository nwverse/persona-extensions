package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/nwverse/persona/sdk/go/ext"
	"github.com/nwverse/persona/sdk/go/wire"
)

func TestDetailViewCapsItemsSkipsEmptyCommentsAndChecksHTTPS(t *testing.T) {
	var manyComments []comment
	for i := range 80 {
		manyComments = append(manyComments, comment{
			ID:       "m" + strconv.Itoa(i),
			BodyHTML: "<p>comment line 1</p><p>comment line 2</p>",
			User:     struct{ Name string }{Name: "Reader"},
		})
	}
	articles := map[string]article{
		"1": {
			ID:           1,
			Title:        "Long technical walkthrough",
			Description:  strings.Repeat("summary line\n", 60),
			URL:          "https://dev.to/example/long",
			BodyMarkdown: strings.Repeat("body line\n", 350),
			User:         struct{ Name, Username string }{Name: "Author", Username: "author"},
		},
		"2": {
			ID:           2,
			Title:        "Image reaction thread",
			URL:          "https://dev.to/example/images",
			BodyMarkdown: "Short body.",
			User:         struct{ Name, Username string }{Name: "Author", Username: "author"},
		},
		"3": {
			ID:           3,
			Title:        "Plain HTTP link",
			URL:          "http://dev.to/example/http",
			BodyMarkdown: "Short body.",
			User:         struct{ Name, Username string }{Name: "Author", Username: "author"},
		},
		"4": {
			ID:           4,
			Title:        "Normal HTTPS article",
			Description:  "Short summary.",
			URL:          "https://dev.to/example/normal",
			BodyMarkdown: "Short body.",
			User:         struct{ Name, Username string }{Name: "Author", Username: "author"},
		},
	}
	threads := map[string][]comment{
		"1": manyComments,
		"2": {{ID: "c1", BodyHTML: `<p><img src="https://example.com/reaction.png"></p>`, User: struct{ Name string }{Name: "Reader"}}},
		"3": {},
		"4": {{ID: "c4", BodyHTML: "<p>Visible comment</p>", User: struct{ Name string }{Name: "Reader"}}},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasPrefix(r.URL.Path, "/articles/") {
			id := strings.TrimPrefix(r.URL.Path, "/articles/")
			if item, ok := articles[id]; ok {
				_ = json.NewEncoder(w).Encode(item)
				return
			}
		}
		if r.URL.Path == "/comments" {
			if list, ok := threads[r.URL.Query().Get("a_id")]; ok {
				_ = json.NewEncoder(w).Encode(list)
				return
			}
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	a := &app{client: newClient(srv.URL)}
	for _, tc := range []struct {
		name string
		id   string
	}{
		{name: "long_article", id: "1"},
		{name: "empty_html_comments", id: "2"},
		{name: "http_article_url", id: "3"},
		{name: "https_article_control", id: "4"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res, err := a.action(t.Context(), &ext.UIRequest{
				UIParams: wire.UIParams{
					Locale:    "en",
					UIVersion: 6,
					Action:    "detail",
					Values:    map[string]string{"id": tc.id},
				},
			})
			if err != nil {
				t.Fatalf("detail: %v", err)
			}
			if err := wire.CheckView(res.View); err != nil {
				t.Fatalf("CheckView: %v", err)
			}
		})
	}
}
