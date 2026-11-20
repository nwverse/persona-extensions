package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/nwverse/persona/sdk/go/ext"
	"github.com/nwverse/persona/sdk/go/wire"
)

// moreLunchMoney answers the transaction list in batches of 50 with has_more while transactions remain: 51
// transactions, or (full) 500. Every transaction carries notes, so each one renders two text lines.
type moreLunchMoney struct {
	srv   *httptest.Server
	mu    sync.Mutex
	paths []string
	total int
}

func (f *moreLunchMoney) records(offset int) []transaction {
	n := min(50, f.total-offset)
	if n < 0 {
		n = 0
	}
	out := make([]transaction, n)
	for i := range out {
		out[i] = transaction{ID: int64(offset + i + 1), Date: "2026-11-10", Amount: "1.0000", Currency: "usd", Payee: fmt.Sprintf("payee-%d", offset+i+1), Notes: "note"}
	}
	return out
}

func newMoreLunchMoney(t *testing.T, total int) *moreLunchMoney {
	f := &moreLunchMoney{total: total}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.paths = append(f.paths, r.URL.RequestURI())
		f.mu.Unlock()
		if r.URL.Path != "/v2/transactions" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
		_ = json.NewEncoder(w).Encode(transactionPage{Transactions: f.records(offset), HasMore: offset+50 < f.total})
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *moreLunchMoney) app() *app {
	a := &app{client: newClient(testToken)}
	a.client.http = f.srv.Client()
	a.client.http.Transport = rewriteHostTransport{base: http.DefaultTransport, host: f.srv.Listener.Addr().String()}
	return a
}

func TestLoadMoreRetainsEarlierTransactions(t *testing.T) {
	f := newMoreLunchMoney(t, 51)
	a := f.app()
	r := &ext.UIRequest{UIParams: wire.UIParams{Locale: "en", UIVersion: 3}}
	v, err := a.page(t.Context(), r, pageState{})
	if err != nil || len(v.Sections[1].Items) != 100 || v.Sections[1].More == nil {
		t.Fatalf("initial view: %+v, %v", v, err)
	}
	r.Action, r.Values, r.State = "more", v.Sections[1].More.Action.Values, v.State
	result, err := a.action(t.Context(), r)
	if err != nil || result.View == nil {
		t.Fatalf("more action: %+v, %v", result, err)
	}
	items := result.View.Sections[1].Items
	if len(items) != 102 || !strings.HasPrefix(items[0].Text, "payee-1 ") || !strings.HasPrefix(items[len(items)-2].Text, "payee-51 ") {
		t.Fatalf("more did not retain the whole list: components=%d", len(items))
	}
	if result.View.Sections[1].More != nil {
		t.Fatal("the last batch still offers more")
	}
	if err := wire.CheckView(result.View); err != nil {
		t.Fatal(err)
	}
	if paths := strings.Join(f.paths, "\n"); !strings.Contains(paths, "offset=0") || !strings.Contains(paths, "offset=50") {
		t.Fatalf("offset requests: %s", paths)
	}
}

func TestLoadMoreStopsAtViewLimit(t *testing.T) {
	f := newMoreLunchMoney(t, 500)
	v, err := f.app().page(t.Context(), &ext.UIRequest{UIParams: wire.UIParams{Locale: "en", UIVersion: 3}}, pageState{Offset: 300})
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Sections[1].Items) != 250 || v.Sections[1].More != nil {
		t.Fatalf("view cap: components=%d more=%+v", len(v.Sections[1].Items), v.Sections[1].More)
	}
	if err := wire.CheckView(v); err != nil {
		t.Fatal(err)
	}
	if paths := strings.Join(f.paths, "\n"); strings.Contains(paths, "offset=150") {
		t.Fatalf("loaded past the cap: %s", paths)
	}
}
