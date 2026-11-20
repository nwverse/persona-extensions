package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// answering is an app whose Lunch Money answers with the given transactions and categories.
func answering(t *testing.T, transactions []transaction, categories []category) *app {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v2/transactions":
			_ = json.NewEncoder(w).Encode(transactionPage{Transactions: transactions})
		case "/v2/categories":
			_ = json.NewEncoder(w).Encode(map[string]any{"categories": categories})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	a := &app{client: newClient(testToken)}
	a.client.http = srv.Client()
	a.client.http.Transport = rewriteHostTransport{base: http.DefaultTransport, host: srv.Listener.Addr().String()}
	return a
}

// Lunch Money answers category_id and group_id as the integer id or null, so the tool output names the id or says
// there is none; it must never print the address of the decoded pointer.
func TestTransactionsToolNamesCategoryID(t *testing.T) {
	filed, loose := sampleTransaction(), sampleTransaction()
	loose.ID, loose.CategoryID = 43, nil
	a := answering(t, []transaction{filed, loose}, nil)
	text, err := a.callTool(t.Context(), "lunchmoney_transactions", json.RawMessage(`{"start_date":"2026-11-01","end_date":"2026-11-10"}`))
	if err != nil || !strings.Contains(text, "category: 83 | status") || !strings.Contains(text, "category: none | status") || strings.Contains(text, "0x") {
		t.Fatalf("%v\n%s", err, text)
	}
}

func TestCategoriesToolNamesGroupID(t *testing.T) {
	group := int64(12)
	a := answering(t, nil, []category{{ID: 83, Name: "Books", GroupID: &group}, {ID: 84, Name: "Fiction"}})
	text, err := a.callTool(t.Context(), "lunchmoney_categories", json.RawMessage(`{}`))
	if err != nil || !strings.Contains(text, "Books | group: 12 |") || !strings.Contains(text, "Fiction | group: none |") || strings.Contains(text, "0x") {
		t.Fatalf("%v\n%s", err, text)
	}
}
