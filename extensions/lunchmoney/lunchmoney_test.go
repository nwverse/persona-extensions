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

const testToken = "synthetic-lunchmoney-token"

type fakeLunchMoney struct {
	srv   *httptest.Server
	mu    sync.Mutex
	paths []string
}

func sampleTransaction() transaction {
	category := int64(83)
	return transaction{ID: 42, Date: "2026-11-10", Amount: "25.5000", Currency: "usd", ToBase: 25.5, Payee: "Example Bookstore", CategoryID: &category, Notes: "Keep the receipt with the reading archive.", Status: "reviewed"}
}

func newFakeLunchMoney(t *testing.T) *fakeLunchMoney {
	f := &fakeLunchMoney{}
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
		case "/v2/transactions":
			_ = json.NewEncoder(w).Encode(transactionPage{Transactions: []transaction{sampleTransaction()}, HasMore: true})
		case "/v2/manual_accounts":
			_ = json.NewEncoder(w).Encode(map[string]any{"manual_accounts": []account{{ID: 7, Name: "Everyday account", Balance: "1250.0000", Currency: "usd", BalanceAsOf: "2026-11-10T10:00:00Z", Status: "active"}}})
		case "/v2/plaid_accounts":
			_ = json.NewEncoder(w).Encode(map[string]any{"accounts": []account{{ID: 8, Name: "Synced account", Balance: "2400.0000", Currency: "usd", BalanceAsOf: "2026-11-10T10:00:00Z", Status: "active"}}})
		case "/v2/categories":
			if r.URL.Query().Get("format") != "flattened" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"categories": []category{{ID: 83, Name: "Books", IsIncome: false}}})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeLunchMoney) app() *app {
	a := &app{client: newClient(testToken)}
	a.client.http = f.srv.Client()
	a.client.http.Transport = rewriteHostTransport{base: http.DefaultTransport, host: f.srv.Listener.Addr().String()}
	return a
}

func TestConformance(t *testing.T) {
	exttest.Check(t, manifest, exttest.Serve(t, program(newFakeLunchMoney(t).app())))
}

func TestManifest(t *testing.T) {
	if err := wire.ValidateJSON("Manifest", manifest); err != nil {
		t.Fatal(err)
	}
	var m wire.Manifest
	if err := json.Unmarshal(manifest, &m); err != nil {
		t.Fatal(err)
	}
	if m.ID != "lunchmoney" || len(m.Secrets) != 1 || len(m.Contributes.Connectors[0].ConfirmAlways) != 0 || !slices.Equal(m.Permissions.Network, []string{}) {
		t.Fatalf("%+v", m)
	}
	if newApp().status()["state"] != "not_configured" {
		t.Fatal("unconfigured extension was ready")
	}
}

func TestTransactionsAccountsCategoriesAndPagination(t *testing.T) {
	f := newFakeLunchMoney(t)
	a := f.app()
	text, err := a.callTool(t.Context(), "lunchmoney_transactions", json.RawMessage(`{"start_date":"2026-11-01","end_date":"2026-11-10","category_id":83,"offset":50}`))
	if err != nil || !strings.Contains(text, "25.5000 usd") || !strings.Contains(text, "Next offset: 100") {
		t.Fatalf("%v %s", err, text)
	}
	for _, kind := range []string{"manual_accounts", "plaid_accounts"} {
		text, err = a.callTool(t.Context(), "lunchmoney_accounts", json.RawMessage(`{"kind":"`+kind+`"}`))
		if err != nil || !strings.Contains(text, "Account #") {
			t.Fatalf("%s %v %s", kind, err, text)
		}
	}
	text, err = a.callTool(t.Context(), "lunchmoney_categories", json.RawMessage(`{}`))
	if err != nil || !strings.Contains(text, "Category #83") {
		t.Fatalf("%v %s", err, text)
	}
	f.mu.Lock()
	paths := strings.Join(f.paths, "\n")
	f.mu.Unlock()
	for _, want := range []string{"start_date=2026-11-01", "end_date=2026-11-10", "category_id=83", "offset=50", "limit=50", "format=flattened"} {
		if !strings.Contains(paths, want) {
			t.Fatalf("missing %q in %s", want, paths)
		}
	}
}

type rewriteHostTransport struct {
	base http.RoundTripper
	host string
}

func (t rewriteHostTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	clone.URL.Scheme = "http"
	clone.URL.Host = t.host
	return t.base.RoundTrip(clone)
}

func TestDateAndFilterValidation(t *testing.T) {
	a := &app{client: newClient(testToken)}
	for _, raw := range []string{`{"start_date":"2026-11-01"}`, `{"start_date":"2026-11-11","end_date":"2026-11-01"}`, `{"start_date":"2026-02-30","end_date":"2026-03-01"}`, `{"category_id":-1}`, `{"offset":-1}`} {
		if _, err := a.callTool(t.Context(), "lunchmoney_transactions", json.RawMessage(raw)); err == nil {
			t.Fatal("accepted invalid query " + raw)
		}
	}
}

func TestLocalizedViewsAndBounds(t *testing.T) {
	f := newFakeLunchMoney(t)
	a := f.app()
	for _, locale := range []string{"en", "ja", "zh-Hans"} {
		for _, ui := range []int{1, 3, 6} {
			for _, mode := range []string{"", "categories", "accounts"} {
				r := &ext.UIRequest{UIParams: wire.UIParams{Locale: locale, UIVersion: ui}}
				st := pageState{Mode: mode, Kind: "manual_accounts", StartDate: "2026-11-01", EndDate: "2026-11-10", CategoryID: 83}
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
}
