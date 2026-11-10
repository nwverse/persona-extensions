package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/nwverse/persona/sdk/go/ext"
	"github.com/nwverse/persona/sdk/go/exttest"
	"github.com/nwverse/persona/sdk/go/wire"
)

const testToken = "synthetic-paperless-token"

type fakePaperless struct {
	srv   *httptest.Server
	mu    sync.Mutex
	paths []string
}

func sampleDocument() document {
	correspondent, docType := int64(7), int64(3)
	item := document{ID: 42, Title: "Example receipt archive", Content: "Synthetic OCR text.\nKeep the receipt context available.", Created: "2026-11-01", Modified: "2026-11-10T10:00:00Z", OriginalFileName: "receipt-example.pdf", MIMEType: "application/pdf", PageCount: 2, Correspondent: &correspondent, DocumentType: &docType, Tags: []int64{4, 5}}
	item.SearchHit.Highlights = `Receipt <span class="match">context</span> &amp; source <script>not rendered</script>`
	item.Notes = []note{{ID: 1, Note: "Review this with the monthly archive.", Created: "2026-11-10T10:00:00Z"}}
	item.Notes[0].User.Username = "reader"
	return item
}

func newFakePaperless(t *testing.T) *fakePaperless {
	f := &fakePaperless{}
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
		case "/api/documents/":
			items := []document{}
			next := (*string)(nil)
			if r.URL.Query().Get("page") == "1" {
				items = append(items, sampleDocument())
				nextURL := f.srv.URL + "/api/documents/?page=2&page_size=25"
				next = &nextURL
			}
			_ = json.NewEncoder(w).Encode(page[document]{Count: 26, Next: next, Results: items})
		case "/api/documents/42/":
			_ = json.NewEncoder(w).Encode(sampleDocument())
		case "/api/tags/":
			_ = json.NewEncoder(w).Encode(page[namedObject]{Count: 1, Results: []namedObject{{ID: 4, Name: "Receipts", DocumentCount: 12}}})
		case "/api/correspondents/":
			_ = json.NewEncoder(w).Encode(page[namedObject]{Count: 1, Results: []namedObject{{ID: 7, Name: "Example supplier", DocumentCount: 8}}})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakePaperless) app() *app { return &app{client: newClient(f.srv.URL, testToken)} }

func TestConformance(t *testing.T) {
	exttest.Check(t, manifest, exttest.Serve(t, program(newFakePaperless(t).app())))
}

func TestManifestAndURL(t *testing.T) {
	if err := wire.ValidateJSON("Manifest", manifest); err != nil {
		t.Fatal(err)
	}
	var m wire.Manifest
	if err := json.Unmarshal(manifest, &m); err != nil {
		t.Fatal(err)
	}
	if m.ID != "paperless" || len(m.Secrets) != 1 || len(m.Contributes.Connectors[0].ConfirmAlways) != 0 || !slices.Equal(m.Permissions.Network, []string{}) {
		t.Fatalf("%+v", m)
	}
	for _, raw := range []string{"https://paperless.example.com", "http://127.0.0.1:8000/", "https://[::1]:8000"} {
		if _, err := instanceURL(raw); err != nil {
			t.Fatal(raw, err)
		}
	}
	for _, raw := range []string{"http://paperless.example.com", "https://me@example.com", "not-a-url"} {
		if _, err := instanceURL(raw); err == nil {
			t.Fatal("accepted " + raw)
		}
	}
}

func TestSearchOCRNotesAndPagination(t *testing.T) {
	f := newFakePaperless(t)
	a := f.app()
	text, err := a.callTool(t.Context(), "paperless_search", json.RawMessage(`{"query":"receipt"}`))
	if err != nil || !strings.Contains(text, "Document #42") || !strings.Contains(text, "Next page: 2") || strings.Contains(text, "<span") {
		t.Fatalf("%v %s", err, text)
	}
	text, err = a.callTool(t.Context(), "paperless_document", json.RawMessage(`{"document_id":42}`))
	if err != nil || !strings.Contains(text, "Synthetic OCR") || !strings.Contains(text, "monthly archive") {
		t.Fatalf("%v %s", err, text)
	}
	for _, name := range []string{"paperless_tags", "paperless_correspondents"} {
		text, err = a.callTool(t.Context(), name, json.RawMessage(`{}`))
		if err != nil || !strings.Contains(text, "documents:") {
			t.Fatalf("%s %v %s", name, err, text)
		}
	}
	f.mu.Lock()
	paths := strings.Join(f.paths, "\n")
	f.mu.Unlock()
	for _, want := range []string{"page=1", "page_size=25", "query=receipt", "ordering=-added", "/api/tags/", "/api/correspondents/"} {
		if !strings.Contains(paths, want) {
			t.Fatalf("missing %q in %s", want, paths)
		}
	}
	if _, err := a.callTool(t.Context(), "paperless_document", json.RawMessage(`{"document_id":-1}`)); err == nil {
		t.Fatal("accepted invalid document ID")
	}
}

func TestLocalizedViewsAndBounds(t *testing.T) {
	f := newFakePaperless(t)
	a := f.app()
	for _, locale := range []string{"en", "ja", "zh-Hans"} {
		for _, ui := range []int{1, 3, 6} {
			for _, mode := range []string{"", "detail", "tags", "correspondents"} {
				r := &ext.UIRequest{UIParams: wire.UIParams{Locale: locale, UIVersion: ui}}
				v, err := a.page(t.Context(), r, pageState{Mode: mode, Query: "receipt", DocumentID: 42, Page: 1})
				if err != nil {
					t.Fatalf("%s %d %s: %v", locale, ui, mode, err)
				}
				if err := wire.CheckView(v); err != nil {
					t.Fatalf("%s %d %s: %v", locale, ui, mode, err)
				}
			}
		}
	}
	item := sampleDocument()
	item.Content = strings.Repeat("OCR context\n", 10000)
	v := detailView(&ext.UIRequest{UIParams: wire.UIParams{Locale: "en", UIVersion: 6}}, pageState{DocumentID: 42}, item, f.app().client.originalURL(42))
	if err := wire.CheckView(v); err != nil {
		t.Fatal(err)
	}
	if newApp().status()["state"] != "not_configured" {
		t.Fatal("unconfigured extension was ready")
	}
}

func TestUntrustedHighlightAndDownloadBoundary(t *testing.T) {
	if got := plainText(`before <span class="match">match</span> <script>alert(1)</script> &amp; after`); got != "before match & after" {
		t.Fatal(got)
	}
	if got := newClient("https://paperless.example.com", "x").originalURL(42); got != "https://paperless.example.com/api/documents/42/download/?original=true" {
		t.Fatal(got)
	}
	if got := newClient("http://127.0.0.1:8000", "x").originalURL(42); got != "" {
		t.Fatal("link component does not allow an HTTP attachment URL")
	}
	f := newFakePaperless(t)
	a := f.app()
	if got := a.client.originalURL(42); got != "" {
		t.Fatal("local API details unexpectedly exposed an HTTP attachment link")
	}
	r := &ext.UIRequest{UIParams: wire.UIParams{Locale: "en", UIVersion: 6}}
	v, err := a.page(t.Context(), r, pageState{Mode: "detail", DocumentID: 42})
	if err != nil || wire.CheckView(v) != nil {
		t.Fatal(err)
	}
	for _, path := range f.paths {
		if strings.Contains(path, "download") {
			t.Fatal("document detail automatically downloaded an attachment")
		}
	}
}
