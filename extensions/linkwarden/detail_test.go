package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/nwverse/persona/sdk/go/ext"
	"github.com/nwverse/persona/sdk/go/wire"
)

func TestArchiveTwoLinksAndDetailViewBounds(t *testing.T) {
	t.Run("archive_two_links", func(t *testing.T) {
		var mu sync.Mutex
		var archived []int64
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer "+testKey {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			switch r.URL.Path {
			case "/links/101":
				fmt.Fprint(w, `{"response":{"id":101,"name":"First link","url":"https://example.com/1"}}`)
			case "/links/102":
				fmt.Fprint(w, `{"response":{"id":102,"name":"Second link","url":"https://example.com/2"}}`)
			case "/links/101/highlights", "/links/102/highlights":
				fmt.Fprint(w, `{"response":[]}`)
			case "/links/101/archive":
				mu.Lock()
				archived = append(archived, 101)
				mu.Unlock()
				fmt.Fprint(w, `{"response":"ok"}`)
			case "/links/102/archive":
				mu.Lock()
				archived = append(archived, 102)
				mu.Unlock()
				fmt.Fprint(w, `{"response":"ok"}`)
			default:
				w.WriteHeader(http.StatusNotFound)
			}
		}))
		defer srv.Close()

		store, err := openWriteStore(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		a := &app{client: newClient(srv.URL, testKey), receipts: store}
		for _, id := range []string{"101", "102"} {
			detail, err := a.action(t.Context(), &ext.UIRequest{
				UIParams: wire.UIParams{
					Locale:    "en",
					UIVersion: 6,
					Action:    "detail",
					Values:    map[string]string{"id": id},
				},
			})
			if err != nil {
				t.Fatalf("detail %s: %v", id, err)
			}
			if err := wire.CheckView(detail.View); err != nil {
				t.Fatalf("detail %s CheckView: %v", id, err)
			}
			done, err := a.action(t.Context(), &ext.UIRequest{
				UIParams: wire.UIParams{
					Locale:    "en",
					UIVersion: 6,
					State:     detail.View.State,
					Action:    "archive",
				},
			})
			if err != nil || !done.Pop {
				t.Fatalf("archive %s: %v %+v", id, err, done)
			}
		}
		mu.Lock()
		defer mu.Unlock()
		if !slices.Equal(archived, []int64{101, 102}) {
			t.Fatalf("archived = %v, want [101 102]", archived)
		}
	})

	t.Run("long_note_and_highlights", func(t *testing.T) {
		v := detailView(
			&ext.UIRequest{UIParams: wire.UIParams{Locale: "en", UIVersion: 6}},
			pageState{LinkID: 101, Mode: "detail", DraftID: "d1"},
			link{
				ID:          101,
				Name:        "Long article",
				URL:         "https://example.com/long",
				Note:        strings.Repeat("note line\n", 180),
				Description: strings.Repeat("desc line\n", 180),
			},
			[]highlight{{ID: 1, Text: strings.Repeat("quote line\n", 100)}},
		)
		if err := wire.CheckView(v); err != nil {
			t.Fatalf("CheckView: %v", err)
		}
	})

	t.Run("empty_highlight_and_nameless_collection", func(t *testing.T) {
		v := detailView(
			&ext.UIRequest{UIParams: wire.UIParams{Locale: "en", UIVersion: 6}},
			pageState{LinkID: 101, Mode: "detail", DraftID: "d1"},
			link{
				ID:         101,
				Name:       "Saved link",
				URL:        "https://example.com/item",
				Collection: &collection{ID: 5, Name: ""},
			},
			[]highlight{{ID: 1, Text: "", Note: ""}},
		)
		if err := wire.CheckView(v); err != nil {
			t.Fatalf("CheckView: %v", err)
		}
	})

	t.Run("normal_detail_control", func(t *testing.T) {
		v := detailView(
			&ext.UIRequest{UIParams: wire.UIParams{Locale: "en", UIVersion: 6}},
			pageState{LinkID: 101, Mode: "detail", DraftID: "d1"},
			link{
				ID:         101,
				Name:       "Normal link",
				URL:        "https://example.com/normal",
				Collection: &collection{ID: 5, Name: "Research"},
				Note:       "Short note",
			},
			[]highlight{{ID: 1, Text: "Short quote"}},
		)
		if err := wire.CheckView(v); err != nil {
			t.Fatalf("CheckView: %v", err)
		}
	})
}
