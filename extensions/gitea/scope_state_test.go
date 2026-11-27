package main

import (
	"strings"
	"testing"

	"github.com/nwverse/persona/sdk/go/ext"
	"github.com/nwverse/persona/sdk/go/wire"
)

func TestInitialRenderSearchScopeAndHanCommentState(t *testing.T) {
	t.Run("preset owner and repo list control", func(t *testing.T) {
		f := newFakeGitea(t)
		v, err := f.app().page(t.Context(), &ext.UIRequest{UIParams: wire.UIParams{Locale: "en", UIVersion: 6}}, pageState{Mode: "list", Owner: "reading", Repo: "notes"})
		if err != nil || wire.CheckView(v) != nil {
			t.Fatalf("control list failed: %v", err)
		}
	})

	t.Run("initial render shows search prompt and accepts owner/repo from q", func(t *testing.T) {
		f := newFakeGitea(t)
		a := f.app()
		initial, err := a.render(t.Context(), &ext.UIRequest{UIParams: wire.UIParams{Locale: "zh-Hans", UIVersion: 6}})
		if err != nil {
			t.Fatalf("initial render with empty state failed: %v", err)
		}
		if err := wire.CheckView(initial); err != nil {
			t.Fatalf("initial CheckView failed: %v", err)
		}
		if initial.Search == nil || initial.Search.Value != "" {
			t.Fatalf("expected empty search value on initial render, got %+v", initial.Search)
		}

		scoped, err := a.action(t.Context(), &ext.UIRequest{UIParams: wire.UIParams{
			Locale:    "zh-Hans",
			UIVersion: 6,
			Action:    "scope",
			State:     initial.State,
			Values:    map[string]string{"q": "reading/notes"},
		}})
		if err != nil || scoped.View == nil {
			t.Fatalf("scope action with q=reading/notes failed: %v %+v", err, scoped)
		}
		if err := wire.CheckView(scoped.View); err != nil {
			t.Fatalf("scoped CheckView failed: %v", err)
		}
		if scoped.View.Search == nil || scoped.View.Search.Value != "reading/notes" {
			t.Fatalf("expected reading/notes search value, got %+v", scoped.View.Search)
		}
	})

	t.Run("4000-Han-character PR comment round-trips through confirm-comment", func(t *testing.T) {
		f := newFakeGitea(t)
		a := f.app()
		hanBody := strings.Repeat("审", 4000)
		reviewRes, err := a.action(t.Context(), &ext.UIRequest{UIParams: wire.UIParams{
			Locale:    "zh-Hans",
			UIVersion: 6,
			Action:    "review-comment",
			State:     ext.EncodeState(pageState{Mode: "comment", Owner: "reading", Repo: "notes", PRID: 42, DraftID: "han-1"}),
			Values:    map[string]string{"body": hanBody},
		}})
		if err != nil || reviewRes.View == nil || wire.CheckView(reviewRes.View) != nil {
			t.Fatalf("review-comment failed: %v %+v", err, reviewRes)
		}
		confirmRes, err := a.action(t.Context(), &ext.UIRequest{UIParams: wire.UIParams{
			Locale:    "zh-Hans",
			UIVersion: 6,
			Action:    "confirm-comment",
			State:     reviewRes.View.State,
		}})
		if err != nil || !confirmRes.Pop || len(f.comments) != 1 || f.comments[0]["body"] != hanBody {
			t.Fatalf("confirm-comment with 4000 Han chars failed: %v %+v %v", err, confirmRes, f.comments)
		}
	})
}
