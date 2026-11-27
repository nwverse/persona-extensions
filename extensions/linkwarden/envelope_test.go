package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// serveEnvelopes answers as Linkwarden does for these calls: the result is wrapped in a response field.
func serveEnvelopes(t *testing.T) *client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/collections":
			fmt.Fprint(w, `{"response":[{"id":5,"name":"Research"},{"id":8,"name":"Later"}]}`)
		case r.Method == http.MethodGet && r.URL.Path == "/links/101":
			fmt.Fprint(w, `{"response":{"id":101,"name":"Reading with context","url":"https://example.com/article","collection":{"id":5,"name":"Research"}}}`)
		case r.Method == http.MethodGet && r.URL.Path == "/links/101/highlights":
			fmt.Fprint(w, `{"response":[{"id":11,"linkId":101,"text":"A highlighted passage"}]}`)
		case r.Method == http.MethodPost && r.URL.Path == "/links":
			fmt.Fprint(w, `{"response":{"id":102,"name":"New article","url":"https://example.com/new"}}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return newClient(srv.URL, testKey)
}

func TestCollectionsAreReadFromResponse(t *testing.T) {
	items, err := serveEnvelopes(t).collections(t.Context())
	if err != nil || len(items) != 2 || items[0].Name != "Research" {
		t.Fatalf("%v %+v", err, items)
	}
}

func TestLinkIsReadFromResponse(t *testing.T) {
	item, err := serveEnvelopes(t).link(t.Context(), 101)
	if err != nil || item.ID != 101 || item.Name != "Reading with context" || item.Collection == nil || item.Collection.Name != "Research" {
		t.Fatalf("%v %+v", err, item)
	}
}

func TestHighlightsAreReadFromResponse(t *testing.T) {
	items, err := serveEnvelopes(t).highlights(t.Context(), 101)
	if err != nil || len(items) != 1 || items[0].Text != "A highlighted passage" {
		t.Fatalf("%v %+v", err, items)
	}
}

func TestSavedLinkIsReadFromResponse(t *testing.T) {
	item, err := serveEnvelopes(t).save(t.Context(), "https://example.com/new")
	if err != nil || item.ID != 102 {
		t.Fatalf("%v %+v", err, item)
	}
}
