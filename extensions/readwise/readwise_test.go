package main

import (
	"encoding/json"
	"fmt"
	"io"
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

const testToken = "synthetic-readwise-token"

type fakeReader struct {
	srv     *httptest.Server
	mu      sync.Mutex
	paths   []string
	posts   []map[string]any
	patches []map[string]any
}

func newFakeReader(t *testing.T) *fakeReader {
	f := &fakeReader{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Token "+testToken {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		f.mu.Lock()
		f.paths = append(f.paths, r.URL.RequestURI())
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/list/":
			switch {
			case r.URL.Query().Get("category") == "highlight":
				fmt.Fprint(w, `{"count":2,"nextPageCursor":null,"results":[{"id":"highlight-1","parent_id":"doc-1","content":"A highlighted passage","notes":"Why this matters"},{"id":"highlight-other","parent_id":"other","content":"Other article"}]}`)
			case r.URL.Query().Get("id") == "doc-1":
				fmt.Fprint(w, `{"count":1,"nextPageCursor":null,"results":[{"id":"doc-1","title":"Reading in the open","author":"Example Author","site_name":"Example","source_url":"https://example.com/article","reading_progress":0.4,"html_content":"<html><body><nav>ignore navigation</nav><article><h1>Reading in the open</h1><p>First paragraph.</p><p>Second paragraph with context.</p><script>do not execute this</script><form>ignore form</form></article></body></html>"}]}`)
			default:
				fmt.Fprint(w, `{"count":2,"nextPageCursor":"next-reading","results":[{"id":"doc-1","title":"Reading in the open","author":"Example Author","site_name":"Example","source_url":"https://example.com/article","reading_progress":0.4},{"id":"doc-2","title":"Another article","source_url":"https://example.org/another","reading_progress":0}]}`)
			}
		case "/save/":
			var in map[string]any
			_ = json.NewDecoder(io.LimitReader(r.Body, 65536)).Decode(&in)
			f.mu.Lock()
			f.posts = append(f.posts, in)
			f.mu.Unlock()
			fmt.Fprint(w, `{"id":"new-doc","url":"https://read.readwise.io/new/read/new-doc"}`)
		case "/update/doc-1/":
			var in map[string]any
			_ = json.NewDecoder(io.LimitReader(r.Body, 65536)).Decode(&in)
			f.mu.Lock()
			f.patches = append(f.patches, in)
			f.mu.Unlock()
			fmt.Fprint(w, `{"id":"doc-1","url":"https://read.readwise.io/new/read/doc-1"}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeReader) app() *app {
	return &app{client: newClient(f.srv.URL, testToken), receipts: newWriteStore("")}
}

func TestConformance(t *testing.T) {
	exttest.Check(t, manifest, exttest.Serve(t, program(newFakeReader(t).app())))
}

func TestManifest(t *testing.T) {
	if err := wire.ValidateJSON("Manifest", manifest); err != nil {
		t.Fatal(err)
	}
	var m wire.Manifest
	if err := json.Unmarshal(manifest, &m); err != nil {
		t.Fatal(err)
	}
	if m.ID != "readwise" || len(m.Secrets) != 1 || m.Secrets[0].Name != "access_token" {
		t.Fatalf("%+v", m)
	}
	if !slices.Equal(m.Permissions.Network, []string{"readwise.io"}) {
		t.Fatal(m.Permissions.Network)
	}
	if len(m.Contributes.Connectors[0].ConfirmAlways) != 2 {
		t.Fatal("save and update must require confirmation")
	}
}

func TestQueuePreservesCursorAndOpaqueIDs(t *testing.T) {
	f := newFakeReader(t)
	text, err := f.app().callTool(t.Context(), "readwise_documents", json.RawMessage(`{"location":"shortlist"}`))
	if err != nil || !strings.Contains(text, "doc-1") || !strings.Contains(text, "40%") || !strings.Contains(text, "next-reading") {
		t.Fatalf("%v %s", err, text)
	}
	if len(f.paths) != 1 || f.paths[0] != "/list/?location=shortlist" {
		t.Fatal(f.paths)
	}
}

func TestArticleTextExcludesScriptsAndForms(t *testing.T) {
	text, err := articleText(`<article><h1>Title</h1><p>First paragraph.</p><p>Second paragraph.</p><script>bad instruction</script><style>hidden text</style><form>do not submit</form></article>`)
	if err != nil || !strings.Contains(text, "First paragraph.") || !strings.Contains(text, "Second paragraph.") {
		t.Fatalf("%v %s", err, text)
	}
	for _, forbidden := range []string{"bad instruction", "hidden text", "do not submit", "<script"} {
		if strings.Contains(text, forbidden) {
			t.Fatal("unsafe content: " + text)
		}
	}
}

func TestReadLoadsHTMLOnDemandAndAssociatedHighlights(t *testing.T) {
	f := newFakeReader(t)
	text, err := f.app().callTool(t.Context(), "readwise_read", json.RawMessage(`{"id":"doc-1"}`))
	if err != nil || !strings.Contains(text, "First paragraph.") || !strings.Contains(text, "A highlighted passage") {
		t.Fatalf("%v %s", err, text)
	}
	for _, forbidden := range []string{"do not execute", "Other article"} {
		if strings.Contains(text, forbidden) {
			t.Fatal(text)
		}
	}
	if len(f.paths) != 2 || f.paths[0] != "/list/?id=doc-1&withHtmlContent=true" || f.paths[1] != "/list/?category=highlight" {
		t.Fatal(f.paths)
	}
}

func TestUpdateOnlyIncludesExplicitFields(t *testing.T) {
	f := newFakeReader(t)
	progress := 0.65
	raw, _ := json.Marshal(map[string]any{"id": "doc-1", "reading_progress": progress})
	out, err := f.app().doAction(t.Context(), "progress-1", "readwise_update", raw)
	if err != nil || !strings.Contains(out["text"].(string), "doc-1") {
		t.Fatalf("%v %+v", err, out)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.patches) != 1 || len(f.patches[0]) != 1 || f.patches[0]["reading_progress"] != progress {
		t.Fatal(f.patches)
	}
	if _, ok := f.patches[0]["notes"]; ok {
		t.Fatal("omitted notes were cleared")
	}
}

func TestSaveValidatesURLAndDeduplicates(t *testing.T) {
	f := newFakeReader(t)
	a := f.app()
	args := []byte(`{"url":"https://example.com/article#part"}`)
	for range 2 {
		if _, err := a.doAction(t.Context(), "save-1", "readwise_save", args); err != nil {
			t.Fatal(err)
		}
	}
	if len(f.posts) != 1 || f.posts[0]["url"] != "https://example.com/article" {
		t.Fatal(f.posts)
	}
	for _, invalid := range []string{`{"url":"file:///tmp/private"}`, `{"url":"https://me@example.com/private"}`, `{"url":"not-a-url"}`} {
		if _, err := a.doAction(t.Context(), "invalid-"+invalid, "readwise_save", []byte(invalid)); err == nil {
			t.Fatal("accepted " + invalid)
		}
	}
}

func TestPersistentReceiptAndUnknownOutcome(t *testing.T) {
	f := newFakeReader(t)
	a := f.app()
	store, err := openWriteStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a.receipts = store
	args := []byte(`{"url":"https://example.com/persisted"}`)
	if _, err := a.doAction(t.Context(), "restart-save", "readwise_save", args); err != nil {
		t.Fatal(err)
	}
	a.receipts, err = openWriteStore(filepath.Dir(store.path))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.doAction(t.Context(), "restart-save", "readwise_save", args); err != nil {
		t.Fatal(err)
	}
	if len(f.posts) != 1 {
		t.Fatal("restart repeated the save")
	}
	key, _ := json.Marshal(map[string]any{"action": "readwise_save", "url": "https://example.com/unknown"})
	raw, _ := json.Marshal(map[string]writeReceipt{"unknown-save": {Key: string(key), Status: "pending", StartedAt: time.Now()}})
	if err := os.WriteFile(store.path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	a.receipts, _ = openWriteStore(filepath.Dir(store.path))
	if _, err := a.doAction(t.Context(), "unknown-save", "readwise_save", []byte(`{"url":"https://example.com/unknown"}`)); err == nil || !strings.Contains(err.Error(), "inspect") {
		t.Fatal("unknown save was retried")
	}
	if len(f.posts) != 1 {
		t.Fatal("unknown outcome caused a second POST")
	}
}

func TestDetailViewAndLocalizedList(t *testing.T) {
	f := newFakeReader(t)
	a := f.app()
	for _, locale := range []string{"en", "ja", "zh-Hans"} {
		for _, version := range []int{1, 3, 6} {
			r := &ext.UIRequest{UIParams: wire.UIParams{Locale: locale, UIVersion: version}}
			v, err := a.render(t.Context(), r)
			if err != nil {
				t.Fatal(err)
			}
			if err := wire.CheckView(v); err != nil {
				t.Fatalf("%s %d: %v", locale, version, err)
			}
		}
	}
	v, err := detailView(&ext.UIRequest{UIParams: wire.UIParams{Locale: "zh-Hans", UIVersion: 6}}, pageState{DocumentID: "doc-1", Mode: "detail"}, document{
		ID: "doc-1", Title: "Article", SourceURL: "https://example.com/article", ReadingProgress: 0.4,
		HTMLContent: `<p>First paragraph.</p><script>never execute</script>`,
	}, []document{{ID: "highlight-1", Content: "Highlighted text", Notes: "A note"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := wire.CheckView(v); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(fmt.Sprint(v), "never execute") {
		t.Fatal("unsafe text in view")
	}
}

func TestSaveFormReviewAndConfirm(t *testing.T) {
	f := newFakeReader(t)
	a := f.app()
	st := pageState{Mode: "save", DraftID: "page-save"}
	v := saveView(&ext.UIRequest{UIParams: wire.UIParams{Locale: "zh-Hans", UIVersion: 6}}, st)
	if err := wire.CheckView(v); err != nil {
		t.Fatal(err)
	}
	st.SaveURL, st.Mode = "https://example.com/article", "review-save"
	v = reviewSaveView(&ext.UIRequest{UIParams: wire.UIParams{Locale: "zh-Hans", UIVersion: 6}}, st)
	if err := wire.CheckView(v); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(v.Sections[0].Items[1].Action.Confirm, st.SaveURL) {
		t.Fatal("confirmation lacks URL")
	}
	r := &ext.UIRequest{UIParams: wire.UIParams{Locale: "zh-Hans", UIVersion: 6, State: ext.EncodeState(st), Action: "save"}}
	res, err := a.action(t.Context(), r)
	if err != nil || !res.Pop || res.Toast == "" {
		t.Fatalf("%v %+v", err, res)
	}
}

func TestQueueLoadsMoreWithoutReplacingPreviousDocuments(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		next := r.URL.Query().Get("pageCursor") == ""
		cursor, id, title := "", "doc-2", "Article 2"
		if next {
			cursor, id, title = "next", "doc-1", "Article 1"
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"count": 2, "nextPageCursor": cursor, "results": []map[string]any{{"id": id, "title": title}}})
		t.Logf("request %s cursor %q -> %s", r.URL.RequestURI(), cursor, title)
	}))
	defer server.Close()
	a := &app{client: newClient(server.URL, testToken), receipts: newWriteStore("")}
	page, err := a.readQueue(t.Context(), pageState{Location: "new", Page: 1})
	if err != nil || len(page.Results) != 2 || page.Results[0].Title != "Article 1" || page.Results[1].Title != "Article 2" {
		t.Fatalf("%v %+v", err, page)
	}
	filtered, err := a.readQueue(t.Context(), pageState{Location: "new", Query: "Article 2", Page: 1})
	if err != nil || len(filtered.Results) != 1 || filtered.Results[0].Title != "Article 2" || filtered.Count != 1 {
		t.Fatalf("%v %+v", err, filtered)
	}
}

func TestNoTokenStatus(t *testing.T) {
	a := newApp()
	if a.status()["state"] != "not_configured" {
		t.Fatal(a.status())
	}
	v, err := a.render(t.Context(), &ext.UIRequest{})
	if err != nil || !strings.Contains(v.Sections[0].Items[0].Title, "token") {
		t.Fatalf("%v %+v", err, v)
	}
}
