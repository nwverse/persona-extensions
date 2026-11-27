package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/nwverse/persona/sdk/go/ext"
	"github.com/nwverse/persona/sdk/go/wire"
)

// documentedMealie answers GET /api/recipes with the response Mealie's OpenAPI document lists for it
// (PaginationBase_RecipeSummary_): the paging fields are snake_case (`per_page`, `total_pages`), unlike the
// camelCase query parameters (`perPage`) and the camelCase recipe fields.
func documentedMealie(t *testing.T, total int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/recipes" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		items := []map[string]any{}
		for number := (page-1)*30 + 1; number <= min(page*30, total); number++ {
			items = append(items, map[string]any{"id": strconv.Itoa(number), "name": "recipe " + strconv.Itoa(number), "slug": "recipe-" + strconv.Itoa(number)})
		}
		pages := (total + 29) / 30
		var next any
		if page < pages {
			next = "/api/recipes?page=" + strconv.Itoa(page+1) + "&perPage=30"
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"page": page, "per_page": 30, "total": total, "total_pages": pages, "items": items, "next": next, "previous": nil})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestRecipeListReadsDocumentedPagingFields(t *testing.T) {
	a := &app{client: newClient(documentedMealie(t, 31).URL, testToken), receipts: newWriteStore("")}
	r := &ext.UIRequest{UIParams: wire.UIParams{Locale: "en", UIVersion: 3}}
	v, err := a.page(t.Context(), r, pageState{})
	if err != nil || len(v.Sections[0].Items) != 30 {
		t.Fatalf("first page: %+v, %v", v, err)
	}
	if v.Sections[0].More == nil {
		t.Fatal("the first of two pages offers no Load more")
	}
	r.Action, r.Values, r.State = "more", v.Sections[0].More.Action.Values, v.State
	result, err := a.action(t.Context(), r)
	if err != nil || result.View == nil || len(result.View.Sections[0].Items) != 31 || result.View.Sections[0].More != nil {
		t.Fatalf("second page: %+v, %v", result, err)
	}
}

func TestRecipesToolReportsNextPageFromDocumentedPagingFields(t *testing.T) {
	a := &app{client: newClient(documentedMealie(t, 31).URL, testToken), receipts: newWriteStore("")}
	text, err := a.callTool(t.Context(), "mealie_recipes", json.RawMessage(`{}`))
	if err != nil || !strings.Contains(text, "Next page: 2") {
		t.Fatalf("first page: %v, next page line missing (%d bytes)", err, len(text))
	}
	text, err = a.callTool(t.Context(), "mealie_recipes", json.RawMessage(`{"page":2}`))
	if err != nil || strings.Contains(text, "Next page") {
		t.Fatalf("last page: %v, still reports a next page", err)
	}
}
