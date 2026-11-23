package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/nwverse/persona/sdk/go/ext"
	"github.com/nwverse/persona/sdk/go/wire"
)

func TestActiveFilterStopFlowAndViewBounds(t *testing.T) {
	t.Run("switch filter to timesheets control", func(t *testing.T) {
		f := newFakeKimai(t)
		a := f.app()
		res, err := a.action(t.Context(), &ext.UIRequest{UIParams: wire.UIParams{
			Locale:    "en",
			UIVersion: 6,
			Action:    "view",
			Values:    map[string]string{"view": "timesheets"},
		}})
		if err != nil || res.View == nil || wire.CheckView(res.View) != nil {
			t.Fatalf("control timesheets filter failed: %v %+v", err, res)
		}
	})

	t.Run("switch filter to active renders stop button and stops timer", func(t *testing.T) {
		f := newFakeKimai(t)
		a := f.app()

		initial, err := a.render(t.Context(), &ext.UIRequest{UIParams: wire.UIParams{Locale: "zh-Hans", UIVersion: 6}})
		if err != nil {
			t.Fatalf("initial render failed: %v", err)
		}
		activeRes, err := a.action(t.Context(), &ext.UIRequest{UIParams: wire.UIParams{
			Locale:    "zh-Hans",
			UIVersion: 6,
			Action:    "view",
			State:     initial.State,
			Values:    map[string]string{"view": "active"},
		}})
		if err != nil || activeRes.View == nil {
			t.Fatalf("switching to active view failed: %v %+v", err, activeRes)
		}
		if err := wire.CheckView(activeRes.View); err != nil {
			t.Fatalf("active view CheckView failed: %v", err)
		}

		var stopAction *wire.Action
		for _, sec := range activeRes.View.Sections {
			for _, comp := range sec.Items {
				if comp.Action != nil && comp.Action.ID == "stop" {
					stopAction = comp.Action
					break
				}
			}
		}
		if stopAction == nil {
			t.Fatalf("expected stop button after selecting active filter, got sections: %+v", activeRes.View.Sections)
		}

		recentRes, err := a.action(t.Context(), &ext.UIRequest{UIParams: wire.UIParams{
			Locale:    "zh-Hans",
			UIVersion: 6,
			Action:    "view",
			State:     activeRes.View.State,
			Values:    map[string]string{"view": "recent"},
		}})
		if err != nil || recentRes.View == nil || recentRes.View.Filter == nil || recentRes.View.Filter.Value != "recent" {
			t.Fatalf("switching from active back to recent failed: %v %+v", err, recentRes)
		}

		stopRes, err := a.action(t.Context(), &ext.UIRequest{UIParams: wire.UIParams{
			Locale:    "zh-Hans",
			UIVersion: 6,
			Action:    "stop",
			State:     activeRes.View.State,
			Values:    stopAction.Values,
		}})
		if err != nil || !stopRes.Pop || f.stops != 1 {
			t.Fatalf("stopping timer from active filter view failed: %v %+v stops=%d", err, stopRes, f.stops)
		}
	})

	t.Run("many recent and active entries stay within wire.ItemsMost", func(t *testing.T) {
		entries := make([]timesheet, 180)
		for i := range entries {
			it := sampleTimesheet()
			it.ID = int64(i + 1)
			entries[i] = it
		}
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(entries)
		}))
		t.Cleanup(srv.Close)

		a := &app{client: newClient(srv.URL, testToken), receipts: newWriteStore("")}
		for _, view := range []string{"recent", "active"} {
			res, err := a.action(t.Context(), &ext.UIRequest{UIParams: wire.UIParams{
				Locale:    "en",
				UIVersion: 6,
				Action:    "view",
				Values:    map[string]string{"view": view},
			}})
			if err != nil || res.View == nil {
				t.Fatalf("%s view failed: %v", view, err)
			}
			if err := wire.CheckView(res.View); err != nil {
				t.Fatalf("%s view with 180 entries failed CheckView: %v", view, err)
			}
		}
	})
}
