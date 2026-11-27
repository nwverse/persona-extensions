package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nwverse/persona/sdk/go/ext"
	"github.com/nwverse/persona/sdk/go/wire"
)

func TestLongIDRowKeysAndHanSpaceNames(t *testing.T) {
	t.Run("short ASCII space control", func(t *testing.T) {
		f := newFakeAnytype(t)
		a := f.app()
		res, err := a.action(t.Context(), &ext.UIRequest{UIParams: wire.UIParams{
			Locale:    "en",
			UIVersion: 6,
			Action:    "space",
			Values:    map[string]string{"id": "space-1", "name": "Personal"},
		}})
		if err != nil || res.View == nil || wire.CheckView(res.View) != nil {
			t.Fatalf("control space action failed: %v %+v", err, res)
		}
	})

	t.Run("long Anytype IDs and 120-Han space names pass CheckView and round-trip", func(t *testing.T) {
		longSpaceID := "bafyreig" + strings.Repeat("a", 120) + ".258"
		longObjID := "bafyreih" + strings.Repeat("b", 120)
		hanSpaceName := strings.Repeat("知", 120)

		obj := sampleObject()
		obj.ID = longObjID
		obj.SpaceID = longSpaceID

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			switch r.URL.Path {
			case "/v1/spaces":
				_ = json.NewEncoder(w).Encode(pageOf([]space{{ID: longSpaceID, Name: hanSpaceName}}, false))
			case "/v1/spaces/" + longSpaceID + "/objects":
				_ = json.NewEncoder(w).Encode(pageOf([]knowledgeObject{obj}, false))
			case "/v1/search":
				_ = json.NewEncoder(w).Encode(pageOf([]knowledgeObject{obj}, false))
			default:
				w.WriteHeader(http.StatusNotFound)
			}
		}))
		t.Cleanup(srv.Close)

		a := &app{client: newClient(srv.URL, testToken), receipts: newWriteStore("")}
		spacesView, err := a.render(t.Context(), &ext.UIRequest{UIParams: wire.UIParams{Locale: "zh-Hans", UIVersion: 6}})
		if err != nil {
			t.Fatalf("spaces render failed: %v", err)
		}
		if err := wire.CheckView(spacesView); err != nil {
			t.Fatalf("spaces CheckView failed on long space ID: %v", err)
		}

		spaceRow := spacesView.Sections[0].Items[0]
		objectsRes, err := a.action(t.Context(), &ext.UIRequest{UIParams: wire.UIParams{
			Locale:    "zh-Hans",
			UIVersion: 6,
			Action:    "space",
			State:     spacesView.State,
			Values:    spaceRow.Action.Values,
		}})
		if err != nil || objectsRes.View == nil {
			t.Fatalf("opening space with 120-Han name failed: %v %+v", err, objectsRes)
		}
		if err := wire.CheckView(objectsRes.View); err != nil {
			t.Fatalf("objects CheckView failed on long object ID: %v", err)
		}

		createRes, err := a.action(t.Context(), &ext.UIRequest{UIParams: wire.UIParams{
			Locale:    "zh-Hans",
			UIVersion: 6,
			Action:    "create",
			State:     objectsRes.View.State,
		}})
		if err != nil || createRes.View == nil || wire.CheckView(createRes.View) != nil {
			t.Fatalf("create action from Han-named space failed: %v %+v", err, createRes)
		}

		searchRes, err := a.action(t.Context(), &ext.UIRequest{UIParams: wire.UIParams{
			Locale:    "zh-Hans",
			UIVersion: 6,
			Action:    "search",
			State:     spacesView.State,
			Values:    map[string]string{"q": "context"},
		}})
		if err != nil || searchRes.View == nil {
			t.Fatalf("search action failed: %v %+v", err, searchRes)
		}
		if err := wire.CheckView(searchRes.View); err != nil {
			t.Fatalf("searchView CheckView failed on long SpaceID+ObjectID: %v", err)
		}
		if got := searchRes.View.Sections[0].Items[0].Action.Values["id"]; got != longObjID {
			t.Fatalf("expected exact object ID %q in row action, got %q", longObjID, got)
		}
	})
}
