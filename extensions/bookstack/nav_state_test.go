package main

import (
	"strings"
	"testing"

	"github.com/nwverse/persona/sdk/go/ext"
	"github.com/nwverse/persona/sdk/go/wire"
)

func TestSearchDetailBackAndMarkdownStateBounds(t *testing.T) {
	t.Run("open book control", func(t *testing.T) {
		f := newFakeBookStack(t)
		res, err := f.app().action(t.Context(), &ext.UIRequest{UIParams: wire.UIParams{
			Locale:    "en",
			UIVersion: 6,
			Action:    "book",
			Values:    map[string]string{"id": "7", "name": "Reading"},
		}})
		if err != nil || res.View == nil || wire.CheckView(res.View) != nil {
			t.Fatalf("control book failed: %v %+v", err, res)
		}
	})

	t.Run("open page from searchView and tap back returns to search results", func(t *testing.T) {
		f := newFakeBookStack(t)
		a := f.app()

		searchRes, err := a.action(t.Context(), &ext.UIRequest{UIParams: wire.UIParams{
			Locale:    "zh-Hans",
			UIVersion: 6,
			Action:    "search",
			Values:    map[string]string{"q": "context"},
		}})
		if err != nil || searchRes.View == nil || wire.CheckView(searchRes.View) != nil {
			t.Fatalf("search failed: %v %+v", err, searchRes)
		}
		row := searchRes.View.Sections[0].Items[0]
		detailRes, err := a.action(t.Context(), &ext.UIRequest{UIParams: wire.UIParams{
			Locale:    "zh-Hans",
			UIVersion: 6,
			Action:    row.Action.ID,
			State:     searchRes.View.State,
			Values:    row.Action.Values,
		}})
		if err != nil || detailRes.View == nil || wire.CheckView(detailRes.View) != nil {
			t.Fatalf("detail from search failed: %v %+v", err, detailRes)
		}
		backRes, err := a.action(t.Context(), &ext.UIRequest{UIParams: wire.UIParams{
			Locale:    "zh-Hans",
			UIVersion: 6,
			Action:    "back",
			State:     detailRes.View.State,
		}})
		if err != nil || backRes.View == nil || wire.CheckView(backRes.View) != nil {
			t.Fatalf("back from search detail failed: %v %+v", err, backRes)
		}
		if backRes.View.Search == nil || backRes.View.Search.Value != "context" {
			t.Fatalf("expected back to restore search view, got %+v", backRes.View.Search)
		}
	})

	t.Run("3500-Han Markdown keeps edit action and >16KB draft is rejected", func(t *testing.T) {
		item := samplePage()
		item.Markdown = strings.Repeat("章", 3500)
		v := detailView(&ext.UIRequest{UIParams: wire.UIParams{Locale: "zh-Hans", UIVersion: 6}}, pageState{
			Mode:     "detail",
			BookID:   7,
			BookName: "Reading",
			PageID:   42,
		}, item, item.Markdown)
		if err := wire.CheckView(v); err != nil {
			t.Fatalf("detailView CheckView failed: %v", err)
		}
		hasEdit := false
		for _, comp := range v.Sections[0].Items {
			if comp.Action != nil && comp.Action.ID == "edit" && comp.Action.Push != nil {
				hasEdit = true
			}
		}
		if !hasEdit {
			t.Fatal("expected edit push button for 3500-Han-character Markdown page")
		}

		f := newFakeBookStack(t)
		if res, err := f.app().action(t.Context(), &ext.UIRequest{UIParams: wire.UIParams{
			Locale:    "en",
			UIVersion: 6,
			Action:    "review-create",
			State:     ext.EncodeState(pageState{Mode: "create", BookID: 7, BookName: "Reading", DraftID: "big-1"}),
			Values:    map[string]string{"name": "Oversized draft", "markdown": strings.Repeat("a", 20000)},
		}}); err == nil && (res.View == nil || wire.CheckView(res.View) != nil) {
			t.Fatal("review-create returned a View exceeding wire.StateMost without error")
		}
	})
}
