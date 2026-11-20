package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Paperless answers correspondent and document_type as the integer id or null, so the tool output names the id or
// says there is none; it must never print the address of the decoded pointer.
func TestToolOutputNamesCorrespondentAndTypeIDs(t *testing.T) {
	a := newFakePaperless(t).app()
	text, err := a.callTool(t.Context(), "paperless_search", json.RawMessage(`{"query":"receipt"}`))
	if err != nil || !strings.Contains(text, "correspondent: 7\n") || strings.Contains(text, "0x") {
		t.Fatalf("search: %v\n%s", err, text)
	}
	text, err = a.callTool(t.Context(), "paperless_document", json.RawMessage(`{"document_id":42}`))
	if err != nil || !strings.Contains(text, "correspondent: 7 | document type: 3\n") || strings.Contains(text, "0x") {
		t.Fatalf("document: %v\n%s", err, text)
	}
}

func TestToolOutputSaysNoneForUnassignedDocument(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/documents/":
			_ = json.NewEncoder(w).Encode(page[document]{Count: 1, Results: []document{{ID: 43, Title: "Unfiled"}}})
		case "/api/documents/43/":
			_ = json.NewEncoder(w).Encode(document{ID: 43, Title: "Unfiled"})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	a := &app{client: newClient(srv.URL, testToken)}
	text, err := a.callTool(t.Context(), "paperless_search", json.RawMessage(`{"query":"unfiled"}`))
	if err != nil || !strings.Contains(text, "correspondent: none\n") {
		t.Fatalf("search: %v\n%s", err, text)
	}
	text, err = a.callTool(t.Context(), "paperless_document", json.RawMessage(`{"document_id":43}`))
	if err != nil || !strings.Contains(text, "correspondent: none | document type: none\n") {
		t.Fatalf("document: %v\n%s", err, text)
	}
}
