package main

import (
	"strings"
	"testing"

	"github.com/nwverse/persona/sdk/go/ext"
	"github.com/nwverse/persona/sdk/go/wire"
)

func TestDefaultScopeCommentBackAndReviewBounds(t *testing.T) {
	t.Run("preset scope list control", func(t *testing.T) {
		f := newFakeGitLab(t)
		v, err := f.app().page(t.Context(), &ext.UIRequest{UIParams: wire.UIParams{Locale: "en", UIVersion: 6}}, pageState{Scope: "assigned_to_me", Mode: "list"})
		if err != nil || wire.CheckView(v) != nil {
			t.Fatalf("control list failed: %v", err)
		}
	})

	t.Run("initial render with empty state defaults scope and passes CheckView", func(t *testing.T) {
		f := newFakeGitLab(t)
		v, err := f.app().render(t.Context(), &ext.UIRequest{UIParams: wire.UIParams{Locale: "zh-Hans", UIVersion: 6}})
		if err != nil {
			t.Fatalf("initial render failed: %v", err)
		}
		if err := wire.CheckView(v); err != nil {
			t.Fatalf("initial CheckView failed: %v", err)
		}
		if v.Filter == nil || v.Filter.Value != "assigned_to_me" {
			t.Fatalf("expected default assigned_to_me filter, got %+v", v.Filter)
		}
	})

	t.Run("comment push from detailView preserves scope on back", func(t *testing.T) {
		f := newFakeGitLab(t)
		a := f.app()
		detail, err := a.page(t.Context(), &ext.UIRequest{UIParams: wire.UIParams{Locale: "zh-Hans", UIVersion: 6}}, pageState{
			Scope:   "reviews_for_me",
			Query:   "reading",
			Page:    1,
			Project: "17",
			IID:     42,
			Mode:    "detail",
		})
		if err != nil || wire.CheckView(detail) != nil {
			t.Fatalf("detail failed: %v", err)
		}
		var pushState string
		for _, comp := range detail.Sections[0].Items {
			if comp.Action != nil && comp.Action.ID == "comment" && comp.Action.Push != nil {
				pushState = comp.Action.Push.State
				break
			}
		}
		if pushState == "" {
			t.Fatal("expected comment push state")
		}
		backRes, err := a.action(t.Context(), &ext.UIRequest{UIParams: wire.UIParams{
			Locale:    "zh-Hans",
			UIVersion: 6,
			Action:    "back",
			State:     pushState,
		}})
		if err != nil || backRes.View == nil || wire.CheckView(backRes.View) != nil {
			t.Fatalf("back from comment push failed: %v %+v", err, backRes)
		}
		if backRes.View.Filter == nil || backRes.View.Filter.Value != "reviews_for_me" {
			t.Fatalf("expected reviews_for_me scope preserved, got %+v", backRes.View.Filter)
		}
	})

	t.Run("reviewCommentView caps >300-line comment within wire.ItemsMost", func(t *testing.T) {
		f := newFakeGitLab(t)
		a := f.app()
		res, err := a.action(t.Context(), &ext.UIRequest{UIParams: wire.UIParams{
			Locale:    "en",
			UIVersion: 6,
			Action:    "review-comment",
			State:     ext.EncodeState(pageState{Scope: "assigned_to_me", Project: "17", IID: 42, Mode: "comment", DraftID: "draft-lines"}),
			Values:    map[string]string{"body": strings.Repeat("review note\n", 340)},
		}})
		if err != nil || res.View == nil {
			t.Fatalf("review-comment failed: %v %+v", err, res)
		}
		if err := wire.CheckView(res.View); err != nil {
			t.Fatalf("review-comment with 340 lines failed CheckView: %v", err)
		}
	})
}
