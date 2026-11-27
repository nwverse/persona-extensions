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

func TestEditPushBackAndListBounds(t *testing.T) {
	t.Run("select workspace control", func(t *testing.T) {
		f := newFakeClickUp(t)
		res, err := f.app().action(t.Context(), &ext.UIRequest{UIParams: wire.UIParams{
			Locale:    "en",
			UIVersion: 6,
			Action:    "team",
			Values:    map[string]string{"id": "team-1", "name": "Personal"},
		}})
		if err != nil || res.View == nil || wire.CheckView(res.View) != nil {
			t.Fatalf("control team failed: %v %+v", err, res)
		}
	})

	t.Run("edit push from detailView preserves workspace on back", func(t *testing.T) {
		f := newFakeClickUp(t)
		a := f.app()
		detail := detailView(&ext.UIRequest{UIParams: wire.UIParams{Locale: "zh-Hans", UIVersion: 6}}, pageState{
			Mode:     "detail",
			TeamID:   "team-1",
			TeamName: "Personal",
			TaskID:   "task-1",
			DraftID:  "d1",
		}, sampleTask())
		if err := wire.CheckView(detail); err != nil {
			t.Fatalf("detail CheckView failed: %v", err)
		}
		var pushState string
		for _, comp := range detail.Sections[0].Items {
			if comp.Action != nil && comp.Action.ID == "edit" && comp.Action.Push != nil {
				pushState = comp.Action.Push.State
				break
			}
		}
		if pushState == "" {
			t.Fatal("expected edit push state")
		}
		backRes, err := a.action(t.Context(), &ext.UIRequest{UIParams: wire.UIParams{
			Locale:    "zh-Hans",
			UIVersion: 6,
			Action:    "back",
			State:     pushState,
		}})
		if err != nil || backRes.View == nil || wire.CheckView(backRes.View) != nil {
			t.Fatalf("back from edit push failed: %v %+v", err, backRes)
		}
	})

	t.Run("spaces view caps >300 spaces and oversized draft fails before CheckView", func(t *testing.T) {
		spaces := make([]map[string]string, 320)
		for i := range spaces {
			spaces[i] = map[string]string{"id": fmt.Sprintf("space-%d", i+1), "name": fmt.Sprintf("Space %d", i+1)}
		}
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"spaces": spaces})
		}))
		t.Cleanup(srv.Close)

		a := &app{client: newClient(srv.URL, testToken), receipts: newWriteStore("")}
		v, err := a.page(t.Context(), &ext.UIRequest{UIParams: wire.UIParams{Locale: "en", UIVersion: 6}}, pageState{
			Mode:     "spaces",
			TeamID:   "team-1",
			TeamName: "Personal",
		})
		if err != nil {
			t.Fatalf("spaces page failed: %v", err)
		}
		if err := wire.CheckView(v); err != nil {
			t.Fatalf("spaces page with 320 spaces failed CheckView: %v", err)
		}

		if res, err := newFakeClickUp(t).app().action(t.Context(), &ext.UIRequest{UIParams: wire.UIParams{
			Locale:    "en",
			UIVersion: 6,
			Action:    "review-create",
			State:     ext.EncodeState(pageState{Mode: "create", TeamID: "team-1", ListID: "list-1", DraftID: "big-1"}),
			Values:    map[string]string{"name": "Big draft", "description": strings.Repeat("x", 20000)},
		}}); err == nil && (res.View == nil || wire.CheckView(res.View) != nil) {
			t.Fatal("review-create returned a View exceeding wire.StateMost without error")
		}
	})
}
