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

func TestDetailCommentsAndProjectsBounds(t *testing.T) {
	t.Run("short detailView control", func(t *testing.T) {
		v := detailView(&ext.UIRequest{UIParams: wire.UIParams{Locale: "en", UIVersion: 6}}, pageState{Mode: "detail", TaskID: 42, DraftID: "d1"}, sampleTask())
		if err := wire.CheckView(v); err != nil {
			t.Fatalf("control detailView failed: %v", err)
		}
	})

	t.Run("detailView with multiline description and many comments stays within wire.ItemsMost", func(t *testing.T) {
		item := sampleTask()
		item.Description = strings.Repeat("step context\n", 180)
		for i := range 160 {
			item.Comments = append(item.Comments, comment{
				ID:      int64(i + 2),
				Comment: fmt.Sprintf("comment %d", i+2),
				Author:  user{Username: "reader"},
			})
		}
		v := detailView(&ext.UIRequest{UIParams: wire.UIParams{Locale: "zh-Hans", UIVersion: 6}}, pageState{Mode: "detail", TaskID: 42, DraftID: "d2"}, item)
		if err := wire.CheckView(v); err != nil {
			t.Fatalf("detailView with multiline description and 161 comments failed CheckView: %v", err)
		}
	})

	t.Run("projects view caps >300 active projects within wire.ItemsMost", func(t *testing.T) {
		projects := make([]project, 320)
		for i := range projects {
			projects[i] = project{ID: int64(i + 1), Title: fmt.Sprintf("Project %d", i+1), Identifier: fmt.Sprintf("P%d", i+1)}
		}
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(projects)
		}))
		t.Cleanup(srv.Close)

		a := &app{client: newClient(srv.URL, testToken), receipts: newWriteStore("")}
		v, err := a.page(t.Context(), &ext.UIRequest{UIParams: wire.UIParams{Locale: "en", UIVersion: 6}}, pageState{Mode: "projects"})
		if err != nil {
			t.Fatalf("projects view failed: %v", err)
		}
		if err := wire.CheckView(v); err != nil {
			t.Fatalf("projects view with 320 projects failed CheckView: %v", err)
		}
	})
}
