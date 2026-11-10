package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/sdk/go/ext"
	"github.com/nwverse/persona/sdk/go/exttest"
	"github.com/nwverse/persona/sdk/go/wire"
)

const testToken = "synthetic-mealie-token"

type fakeMealie struct {
	srv       *httptest.Server
	mu        sync.Mutex
	paths     []string
	imports   []map[string]any
	failWrite bool
}

func sampleRecipe() recipe {
	return recipe{ID: "123e4567-e89b-42d3-a456-426614174000", Name: "Weeknight tomato pasta", Slug: "weeknight-tomato-pasta", Description: "A quick dinner with a saved source.", TotalTime: "30 minutes", PrepTime: "10 minutes", RecipeServings: 2, OriginalURL: "https://recipes.example.com/tomato-pasta", RecipeIngredient: []ingredient{{Title: "Sauce", Display: "200 g tomatoes"}, {Display: "100 g pasta"}}, RecipeInstructions: []step{{Text: "Cook the pasta."}, {Text: "Add the sauce and serve."}}}
}

func newFakeMealie(t *testing.T) *fakeMealie {
	f := &fakeMealie{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+testToken {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		f.mu.Lock()
		f.paths = append(f.paths, r.URL.RequestURI())
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/recipes":
			items := []recipe{}
			if r.URL.Query().Get("page") == "1" {
				items = append(items, sampleRecipe())
			}
			_ = json.NewEncoder(w).Encode(recipePage{Items: items, Page: 1, PerPage: 30, Total: 31, TotalPages: 2})
		case "/api/recipes/weeknight-tomato-pasta":
			_ = json.NewEncoder(w).Encode(sampleRecipe())
		case "/api/recipes/create/url":
			if f.failWrite {
				http.Error(w, "synthetic interrupted import", http.StatusServiceUnavailable)
				return
			}
			var in map[string]any
			if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in["url"] == "" || len(in) != 1 {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			f.mu.Lock()
			f.imports = append(f.imports, in)
			f.mu.Unlock()
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode("weeknight-tomato-pasta")
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeMealie) app() *app {
	return &app{client: newClient(f.srv.URL, testToken), receipts: newWriteStore("")}
}

func TestConformance(t *testing.T) {
	exttest.Check(t, manifest, exttest.Serve(t, program(newFakeMealie(t).app())))
}

func TestManifestAndURL(t *testing.T) {
	if err := wire.ValidateJSON("Manifest", manifest); err != nil {
		t.Fatal(err)
	}
	var m wire.Manifest
	if err := json.Unmarshal(manifest, &m); err != nil {
		t.Fatal(err)
	}
	if m.ID != "mealie" || len(m.Secrets) != 1 || len(m.Contributes.Connectors[0].ConfirmAlways) != 1 || !slices.Equal(m.Permissions.Network, []string{}) {
		t.Fatalf("%+v", m)
	}
	for _, raw := range []string{"https://mealie.example.com", "http://127.0.0.1:9000/", "https://[::1]:9000"} {
		if _, err := instanceURL(raw); err != nil {
			t.Fatal(raw, err)
		}
	}
	for _, raw := range []string{"http://mealie.example.com", "https://me@example.com", "not-a-url"} {
		if _, err := instanceURL(raw); err == nil {
			t.Fatal("accepted " + raw)
		}
	}
	for _, raw := range []string{"http://127.0.0.1/admin", "http://[::1]/admin", "http://10.0.0.1/recipe", "https://localhost/recipe", "https://printer.local/recipe", "https://user:password@recipes.example.com", "file:///tmp/recipe"} {
		if _, err := publicImportURL(raw); err == nil {
			t.Fatal("accepted private import " + raw)
		}
	}
	if _, err := publicImportURL("https://recipes.example.com/dinner"); err != nil {
		t.Fatal(err)
	}
}

func TestRecipesIngredientsInstructionsAndPagination(t *testing.T) {
	f := newFakeMealie(t)
	a := f.app()
	text, err := a.callTool(t.Context(), "mealie_recipes", json.RawMessage(`{"query":"pasta"}`))
	if err != nil || !strings.Contains(text, "Next page: 2") || !strings.Contains(text, "weeknight-tomato-pasta") {
		t.Fatalf("%v %s", err, text)
	}
	text, err = a.callTool(t.Context(), "mealie_recipe", json.RawMessage(`{"slug":"weeknight-tomato-pasta"}`))
	if err != nil || !strings.Contains(text, "200 g tomatoes") || !strings.Contains(text, "Cook the pasta") {
		t.Fatalf("%v %s", err, text)
	}
	f.mu.Lock()
	paths := strings.Join(f.paths, "\n")
	f.mu.Unlock()
	for _, want := range []string{"search=pasta", "perPage=30", "page=1", "orderBy=date_updated"} {
		if !strings.Contains(paths, want) {
			t.Fatalf("missing %q in %s", want, paths)
		}
	}
	if _, err := a.callTool(t.Context(), "mealie_recipe", json.RawMessage(`{"slug":"../users"}`)); err == nil {
		t.Fatal("accepted recipe path traversal")
	}
}

func TestImportReplayAndValidation(t *testing.T) {
	f := newFakeMealie(t)
	a := f.app()
	args := []byte(`{"url":"https://recipes.example.com/dinner"}`)
	for range 2 {
		out, err := a.doAction(t.Context(), "import-1", "mealie_import_recipe", args)
		if err != nil || !strings.Contains(out["text"].(string), "weeknight-tomato-pasta") {
			t.Fatalf("%v %v", err, out)
		}
	}
	if len(f.imports) != 1 || f.imports[0]["url"] != "https://recipes.example.com/dinner" {
		t.Fatal("import replay repeated or changed the URL")
	}
	if _, err := describeAction("mealie_import_recipe", args); err != nil {
		t.Fatal(err)
	}
	if _, err := a.doAction(t.Context(), "import-1", "mealie_import_recipe", []byte(`{"url":"https://recipes.example.com/other"}`)); err == nil {
		t.Fatal("request ID accepted a different import")
	}
}

func TestPersistentReplayAndUnknownOutcome(t *testing.T) {
	f := newFakeMealie(t)
	a := f.app()
	store, err := openWriteStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a.receipts = store
	args := []byte(`{"url":"https://recipes.example.com/dinner"}`)
	if _, err := a.doAction(t.Context(), "persist-1", "mealie_import_recipe", args); err != nil {
		t.Fatal(err)
	}
	a.receipts, err = openWriteStore(filepath.Dir(store.path))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.doAction(t.Context(), "persist-1", "mealie_import_recipe", args); err != nil {
		t.Fatal(err)
	}
	if len(f.imports) != 1 {
		t.Fatal("restart repeated import")
	}
	raw, _ := json.Marshal(map[string]writeReceipt{"persist-1": {Key: string(args), Status: "pending", StartedAt: time.Now()}})
	if err := os.WriteFile(store.path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	a.receipts, _ = openWriteStore(filepath.Dir(store.path))
	if _, err := a.doAction(t.Context(), "persist-1", "mealie_import_recipe", args); err == nil || !strings.Contains(err.Error(), "inspect") {
		t.Fatal("unknown import was retried")
	}
}

func TestLocalizedViewsAndBounds(t *testing.T) {
	f := newFakeMealie(t)
	a := f.app()
	for _, locale := range []string{"en", "ja", "zh-Hans"} {
		for _, ui := range []int{1, 3, 6} {
			for _, mode := range []string{"", "detail", "import", "review-import"} {
				r := &ext.UIRequest{UIParams: wire.UIParams{Locale: locale, UIVersion: ui}}
				st := pageState{Mode: mode, Slug: "weeknight-tomato-pasta", URL: "https://recipes.example.com/dinner", DraftID: "draft-1"}
				v, err := a.page(t.Context(), r, st)
				if err != nil {
					t.Fatalf("%s %d %s: %v", locale, ui, mode, err)
				}
				if err := wire.CheckView(v); err != nil {
					t.Fatalf("%s %d %s: %v", locale, ui, mode, err)
				}
			}
		}
	}
	item := sampleRecipe()
	item.Description = strings.Repeat("recipe context\n", 10000)
	v := detailView(&ext.UIRequest{UIParams: wire.UIParams{Locale: "en", UIVersion: 6}}, pageState{Slug: item.Slug}, item)
	if err := wire.CheckView(v); err != nil {
		t.Fatal(err)
	}
	if newApp().status()["state"] != "not_configured" {
		t.Fatal("unconfigured extension was ready")
	}
}

func TestFailedImportIsUnknown(t *testing.T) {
	f := newFakeMealie(t)
	f.failWrite = true
	if _, err := f.app().doAction(t.Context(), "failed-1", "mealie_import_recipe", []byte(`{"url":"https://recipes.example.com/dinner"}`)); err == nil || !strings.Contains(err.Error(), "unknown") {
		t.Fatal("failed import was safe to retry")
	}
}
