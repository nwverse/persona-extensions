package main

import (
	"encoding/json"
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
