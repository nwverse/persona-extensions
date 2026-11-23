package main

import (
	"slices"
	"testing"

	"github.com/nwverse/persona/sdk/go/ext"
	"github.com/nwverse/persona/sdk/go/wire"
)

func TestDetailActionChecksViewOnLoopbackHTTPInstance(t *testing.T) {
	t.Run("loopback http", func(t *testing.T) {
		f := newFakeJira(t)
		a := f.app()
		r := &ext.UIRequest{UIParams: wire.UIParams{Locale: "zh-Hans", UIVersion: 6, Action: "detail", Values: map[string]string{"id": "ENG-123"}}}
		res, err := a.action(t.Context(), r)
		if err != nil || res.View == nil {
			t.Fatalf("detail: %+v, %v", res, err)
		}
		if err := wire.CheckView(res.View); err != nil {
			t.Fatalf("CheckView: %v", err)
		}
	})

	t.Run("https browse link", func(t *testing.T) {
		var item issue
		item.Key, item.URL = "ENG-123", "https://jira.example.com/browse/ENG-123"
		item.Fields.Summary = "Review reading context"
		v := detailView(&ext.UIRequest{UIParams: wire.UIParams{Locale: "en", UIVersion: 6}}, pageState{IssueKey: "ENG-123", Mode: "detail"}, item)
		if err := wire.CheckView(v); err != nil {
			t.Fatalf("CheckView: %v", err)
		}
		hasLink := slices.ContainsFunc(v.Sections[0].Items, func(c wire.Component) bool {
			return c.Type == wire.ComponentLink && c.URL == item.URL
		})
		if !hasLink {
			t.Fatalf("missing HTTPS browse link in %+v", v.Sections[0].Items)
		}
	})
}
