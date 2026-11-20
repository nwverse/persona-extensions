package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/nwverse/persona/sdk/go/ext"
	"github.com/nwverse/persona/sdk/go/wire"
)

// The page state holds the search query (clipped to 200 characters) and the reviewed draft's name (up to 255
// characters). Both are counted in characters wherever they are limited, so Han characters, three bytes each, have to
// survive the next action as well. clipText keeps limit-1 characters and adds an ellipsis, so a longer query is held
// as exactly 200 characters.
func newSearchAnytype(t *testing.T, total int) *app {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/search" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
		var items []knowledgeObject
		for n := offset; n < offset+50 && n < total; n++ {
			items = append(items, knowledgeObject{ID: fmt.Sprintf("obj-%d", n+1), Name: fmt.Sprintf("note-%d", n+1)})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(pageOf(items, offset+50 < total))
	}))
	t.Cleanup(srv.Close)
	return &app{client: newClient(srv.URL, testToken), receipts: newWriteStore("")}
}

func TestSearchQueryOf200CharactersLoadsMore(t *testing.T) {
	for name, query := range map[string]string{
		"han exact":   strings.Repeat("字", 200),
		"han clipped": strings.Repeat("字", 300),
		"ascii":       strings.Repeat("a", 200),
	} {
		a := newSearchAnytype(t, 51)
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

func TestWebFlowCreatesA255CharacterName(t *testing.T) {
	for name, title := range map[string]string{
		"han":   strings.Repeat("字", 255),
		"ascii": strings.Repeat("a", 255),
	} {
		f := newFakeAnytype(t)
		a := f.app()
		review, err := a.action(t.Context(), &ext.UIRequest{UIParams: wire.UIParams{Locale: "zh-Hans", UIVersion: 6, Action: "review-create", State: ext.EncodeState(pageState{Mode: "create", SpaceID: "space-1", DraftID: "draft-1"}), Values: map[string]string{"name": title, "body": "保留来源上下文。", "type_key": "page"}}})
		if err != nil || review.View == nil || wire.CheckView(review.View) != nil {
			t.Fatalf("%s review: %v %+v", name, err, review)
		}
		confirm, err := a.action(t.Context(), &ext.UIRequest{UIParams: wire.UIParams{Locale: "zh-Hans", UIVersion: 6, Action: "confirm-create", State: review.View.State}})
		if err != nil || !confirm.Pop {
			t.Fatalf("%s confirm: %v %+v", name, err, confirm)
		}
		if len(f.creates) != 1 || f.creates[0]["name"] != title {
			t.Fatalf("%s: the server received %v", name, f.creates)
		}
	}
}
