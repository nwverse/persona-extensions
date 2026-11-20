package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// servePlaid answers GET /plaid_accounts as Lunch Money's v2 API does: the accounts are in plaid_accounts, and a
// synced account's balance date is balance_last_update (balance_as_of belongs to manual accounts).
func servePlaid(t *testing.T) *app {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/v2/plaid_accounts" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		fmt.Fprint(w, `{"plaid_accounts":[{"id":8,"plaid_item_id":"item-1","name":"Synced account","institution_name":"Example Bank","type":"cash","subtype":"checking","status":"active","balance":"2400.0000","currency":"usd","to_base":2400,"balance_last_update":"2026-11-10T10:00:00.000Z"}]}`)
	}))
	t.Cleanup(srv.Close)
	a := &app{client: newClient(testToken)}
	a.client.http = srv.Client()
	a.client.http.Transport = rewriteHostTransport{base: http.DefaultTransport, host: srv.Listener.Addr().String()}
	return a
}

func TestSyncedAccountsAreReadFromPlaidAccounts(t *testing.T) {
	items, err := servePlaid(t).client.accounts(t.Context(), "plaid_accounts")
	if err != nil || len(items) != 1 || items[0].Name != "Synced account" {
		t.Fatalf("%v %+v", err, items)
	}
}

func TestSyncedAccountShowsItsBalanceDate(t *testing.T) {
	text, err := servePlaid(t).callTool(t.Context(), "lunchmoney_accounts", json.RawMessage(`{"kind":"plaid_accounts"}`))
	if err != nil || !strings.Contains(text, "Synced account") || !strings.Contains(text, "as of: 2026-11-10T10:00:00.000Z") {
		t.Fatalf("%v %s", err, text)
	}
}
