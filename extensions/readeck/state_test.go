package main

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/nwverse/persona/sdk/go/ext"
	"github.com/nwverse/persona/sdk/go/wire"
)

// The page state holds the search query (clipped to 200 characters) and the reviewed draft's labels (20 labels of up
// to 100 characters each, joined with ", "). Both are counted in characters wherever they are limited, so Han
// characters, three bytes each, have to survive the next action as well. clipText keeps limit-1 characters and adds
// an ellipsis, so a longer query is held as exactly 200 characters.
func TestSearchQueryOf200CharactersLoadsMore(t *testing.T) {
	for name, query := range map[string]string{
		"han exact":   strings.Repeat("字", 200),
		"han clipped": strings.Repeat("字", 300),
		"ascii":       strings.Repeat("a", 200),
	} {
		f := newMoreReadeck(t, 31)
		a := f.app()
		r := &ext.UIRequest{UIParams: wire.UIParams{Locale: "zh-Hans", UIVersion: 6, Action: "search", Values: map[string]string{"q": query}}}
		result, err := a.action(t.Context(), r)
		if err != nil || result.View == nil || result.View.Sections[0].More == nil {
			t.Fatalf("%s search: %+v, %v", name, result, err)
		}
		var st pageState
		if err := (&ext.UIRequest{UIParams: wire.UIParams{State: result.View.State}}).DecodeState(&st); err != nil || utf8.RuneCountInString(st.Query) > 200 {
			t.Fatalf("%s: the state holds %d characters: %v", name, utf8.RuneCountInString(st.Query), err)
		}
		r.Action, r.Values, r.State = "more", result.View.Sections[0].More.Action.Values, result.View.State
		if result, err = a.action(t.Context(), r); err != nil || result.View == nil {
			t.Fatalf("%s more: %+v, %v", name, result, err)
		}
	}
}

// The worst draft the validation accepts: a 1000-character title, 20 labels of 100 characters and a URL of 1024 bytes
// has to review, fit the SDK's state limit and confirm.
func TestWebFlowSavesTwentyLabelsOf100Characters(t *testing.T) {
	suffixes := []rune("甲乙丙丁戊己庚辛壬癸子丑寅卯辰巳午未申酉")
	labels := make([]string, len(suffixes))
	for i, suffix := range suffixes {
		labels[i] = strings.Repeat("字", 99) + string(suffix)
	}
	f := newFakeReadeck(t)
	a := f.app()
	pageURL := "https://articles.example.com/" + strings.Repeat("a", 1024-len("https://articles.example.com/"))
	review, err := a.action(t.Context(), &ext.UIRequest{UIParams: wire.UIParams{Locale: "zh-Hans", UIVersion: 6, Action: "review-save", State: ext.EncodeState(pageState{Mode: "save", DraftID: "draft-1"}), Values: map[string]string{"url": pageURL, "title": strings.Repeat("字", 1000), "labels": strings.Join(labels, ", ")}}})
	if err != nil || review.View == nil {
		t.Fatalf("review: %v %+v", err, review)
	}
	if err := wire.CheckView(review.View); err != nil {
		t.Fatalf("the review view does not fit the state limit: %v", err)
	}
	confirm, err := a.action(t.Context(), &ext.UIRequest{UIParams: wire.UIParams{Locale: "zh-Hans", UIVersion: 6, Action: "confirm-save", State: review.View.State}})
	if err != nil || !confirm.Pop {
		t.Fatalf("confirm: %v %+v", err, confirm)
	}
	if len(f.creates) != 1 {
		t.Fatalf("the server received %v", f.creates)
	}
	got, _ := f.creates[0]["labels"].([]any)
	if len(got) != 20 || got[0] != labels[0] || got[19] != labels[19] {
		t.Fatalf("the server received labels %v", got)
	}
}
