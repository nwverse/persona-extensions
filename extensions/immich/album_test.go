package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestAlbumAssetsAreSearchedByAlbumID uses a server that answers as Immich 3 does: GET /albums/{id} has no assets, and
// the album's assets are found by POST /search/metadata with filter.albumIds.
func TestAlbumAssetsAreSearchedByAlbumID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/albums/"+albumID:
			_ = json.NewEncoder(w).Encode(map[string]any{"id": albumID, "albumName": "Reading trips", "assetCount": 1})
		case r.Method == http.MethodPost && r.URL.Path == "/api/search/metadata":
			var in struct {
				Filter struct {
					AlbumIDs struct {
						Any []string `json:"any"`
					} `json:"albumIds"`
				} `json:"filter"`
			}
			if json.NewDecoder(r.Body).Decode(&in) != nil || len(in.Filter.AlbumIDs.Any) != 1 || in.Filter.AlbumIDs.Any[0] != albumID {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			var out searchResponse
			out.Assets.Items = []asset{sampleAsset()}
			out.Assets.Count = 1
			_ = json.NewEncoder(w).Encode(out)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	a := &app{client: newClient(srv.URL, testToken), receipts: newWriteStore("")}
	text, err := a.callTool(t.Context(), "immich_album", json.RawMessage(`{"album_id":"`+albumID+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "Reading trips") || !strings.Contains(text, "reading-trip.jpg") {
		t.Fatalf("the album's assets are missing: %s", text)
	}
}
