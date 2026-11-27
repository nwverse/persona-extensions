package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nwverse/persona/sdk/go/ext"
	"github.com/nwverse/persona/sdk/go/wire"
)

// documentedRaindrop answers the way Raindrop's reference documents a bookmark: its collection is an object whose
// only documented field is `collection.$id` ("Collection that the raindrop resides in"); there is no title in it.
func documentedRaindrop(t *testing.T) *app {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/rest/v1/collections":
			fmt.Fprint(w, `{"result":true,"items":[{"_id":10,"title":"Research"}]}`)
		case "/rest/v1/collections/childrens":
			fmt.Fprint(w, `{"result":true,"items":[{"_id":12,"title":"AI","parent":{"$id":10}}]}`)
		case "/rest/v1/raindrops/0":
			fmt.Fprint(w, `{"result":true,"items":[{"_id":501,"title":"Filed article","link":"https://example.com/filed","collection":{"$id":12}},{"_id":502,"title":"Unsorted article","link":"https://example.com/unsorted","collection":{"$id":-1}}],"count":2}`)
		case "/rest/v1/raindrop":
			fmt.Fprint(w, `{"result":true,"item":{"_id":503,"title":"Saved article","link":"https://example.com/new","collection":{"$id":12}}}`)
		default:
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"result":false,"errorMessage":"not found"}`)
		}
	}))
	t.Cleanup(srv.Close)
	return &app{api: newAPIClient(srv.URL, syntheticToken), receipts: newSaveStore("")}
}

func TestSavedToastNamesTheBookmarksCollection(t *testing.T) {
	a := documentedRaindrop(t)
	args := saveArgs{URL: "https://example.com/new", CollectionID: 12}
	res, err := a.saveFromPage(t.Context(), &ext.UIRequest{UIParams: wire.UIParams{Locale: "en", UIVersion: 6}}, pageState{Mode: "review", DraftID: "page-1", Save: args})
	if err != nil || !res.Pop || res.Toast != "Saved to Research / AI." {
		t.Fatalf("%v %+v", err, res)
	}
}

func TestSearchTextGivesEachBookmarksCollection(t *testing.T) {
	text, err := documentedRaindrop(t).callTool(t.Context(), "raindrop_search", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"ID: 501 | collection ID: 12", "ID: 502 | collection ID: -1"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in %s", want, text)
		}
	}
}
