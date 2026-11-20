package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestKeyIsSentInTheAuthorizationHeader uses a server that reads the token as Jellyfin does when its legacy
// authorization option is off (the default): only from Authorization: MediaBrowser Token="...". X-Emby-Token is not
// read, so a request that carries the key only there has no token and is answered 401.
func TestKeyIsSentInTheAuthorizationHeader(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != `MediaBrowser Token="`+testToken+`"` {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"Id": userID, "Name": "reader"})
	}))
	t.Cleanup(srv.Close)
	got, err := newClient(srv.URL, testToken).currentUser(t.Context())
	if err != nil || got.ID != userID {
		t.Fatalf("%v %+v", err, got)
	}
}

func TestKeyThatBreaksTheHeaderIsRefused(t *testing.T) {
	for _, key := range []string{`abc"def`, "abc,def", "abc\ndef"} {
		if err := newClient("http://127.0.0.1:1", key).do(t.Context(), http.MethodGet, "/Users/Me", nil, nil); err == nil || !strings.Contains(err.Error(), "not allowed") {
			t.Fatalf("%q: %v", key, err)
		}
	}
}

// TestCurrentUserReportsWhyJellyfinRefused: when GET /Users/Me fails, the answer has no Id, but the error to give is the
// request's, not that the user ID is invalid.
func TestCurrentUserReportsWhyJellyfinRefused(t *testing.T) {
	for _, tc := range []struct {
		status int
		want   error
	}{
		{http.StatusUnauthorized, errToken},
		{http.StatusForbidden, errToken},
		{http.StatusTooManyRequests, errRateLimited},
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(tc.status) }))
		_, err := newClient(srv.URL, testToken).currentUser(t.Context())
		srv.Close()
		if !errors.Is(err, tc.want) {
			t.Fatalf("status %d: got %v, want %v", tc.status, err, tc.want)
		}
	}
}

// An answer with no usable Id is still refused.
func TestCurrentUserRefusesAnswerWithoutAnID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"Name": "reader"})
	}))
	t.Cleanup(srv.Close)
	if _, err := newClient(srv.URL, testToken).currentUser(t.Context()); err == nil || !strings.Contains(err.Error(), "invalid current user ID") {
		t.Fatalf("%v", err)
	}
}
