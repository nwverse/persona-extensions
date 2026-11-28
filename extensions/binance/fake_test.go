package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeBinance is a synthetic Binance: it checks signatures with a key made for the test and answers from canned
// JSON. Nothing here is a real account.
type fakeBinance struct {
	t       *testing.T
	srv     *httptest.Server
	pub     ed25519.PublicKey
	privPEM string
	apiKey  string

	mu       sync.Mutex
	requests []string // "METHOD host path?query"
	answers  map[string]func(q url.Values) (int, any)
}

func newFakeBinance(t *testing.T) *fakeBinance {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, _ := x509.MarshalPKCS8PrivateKey(priv)
	f := &fakeBinance{t: t, pub: pub, apiKey: "synthetic-test-key-0000000000000000",
		privPEM: string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})), answers: map[string]func(url.Values) (int, any){}}
	f.defaults()
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

// on sets the answer to "METHOD host path".
func (f *fakeBinance) on(key string, fn func(q url.Values) (int, any)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.answers[key] = fn
}

func fixed(v any) func(url.Values) (int, any) { return func(url.Values) (int, any) { return 200, v } }

func (f *fakeBinance) defaults() {
	f.on("GET api.binance.com /api/v3/time", func(url.Values) (int, any) {
		return 200, map[string]any{"serverTime": time.Now().UnixMilli()}
	})
	f.on("GET data-api.binance.vision /api/v3/ticker/price", func(q url.Values) (int, any) {
		if q.Get("symbol") != "" {
			return 200, map[string]string{"symbol": q.Get("symbol"), "price": "100.00"}
		}
		return 200, []map[string]string{{"symbol": "BTCUSDT", "price": "100.00"}, {"symbol": "ETHUSDT", "price": "10.00"}}
	})
	f.on("GET data-api.binance.vision /api/v3/exchangeInfo", fixed(map[string]any{"symbols": []any{spotSymbol("BTCUSDT", "BTC"), spotSymbol("ETHUSDT", "ETH")}}))
	f.on("GET fapi.binance.com /fapi/v1/premiumIndex", fixed([]map[string]string{{"symbol": "BTCUSDT", "markPrice": "100.00"}}))
	f.on("GET fapi.binance.com /fapi/v1/exchangeInfo", fixed(map[string]any{"symbols": []any{futSymbol("BTCUSDT", 0)}}))
	f.on("GET dapi.binance.com /dapi/v1/premiumIndex", fixed([]map[string]string{{"symbol": "BTCUSD_PERP", "markPrice": "100.0"}}))
	f.on("GET dapi.binance.com /dapi/v1/exchangeInfo", fixed(map[string]any{"symbols": []any{futSymbol("BTCUSD_PERP", 100)}}))
	f.on("GET api.binance.com /sapi/v1/account/apiRestrictions", fixed(map[string]any{"ipRestrict": true, "enableReading": true,
		"enableSpotAndMarginTrading": true, "enableFutures": true, "enableWithdrawals": false, "enableInternalTransfer": false,
		"permitsUniversalTransfer": false, "enableMargin": false, "enableVanillaOptions": false, "enablePortfolioMarginTrading": false,
		"enableFixApiTrade": false, "enableFixReadOnly": false, "createTime": 1}))
	f.on("GET fapi.binance.com /fapi/v1/positionSide/dual", fixed(map[string]bool{"dualSidePosition": false}))
	f.on("GET dapi.binance.com /dapi/v1/positionSide/dual", fixed(map[string]bool{"dualSidePosition": true}))
	f.on("GET fapi.binance.com /fapi/v1/symbolConfig", fixed([]map[string]any{{"symbol": "BTCUSDT", "leverage": 3}}))
	f.on("GET fapi.binance.com /fapi/v3/positionRisk", fixed([]map[string]string{{"symbol": "BTCUSDT", "positionSide": "BOTH",
		"positionAmt": "0.5", "entryPrice": "90", "markPrice": "100", "unRealizedProfit": "5", "liquidationPrice": "50", "notional": "50"}}))
	f.on("GET dapi.binance.com /dapi/v1/positionRisk", fixed([]map[string]string{{"symbol": "BTCUSD_PERP", "positionSide": "LONG",
		"positionAmt": "0", "leverage": "4", "markPrice": "100"}}))
	f.on("GET api.binance.com /api/v3/account", fixed(map[string]any{"balances": []map[string]string{
		{"asset": "USDT", "free": "150.5", "locked": "0"}, {"asset": "BTC", "free": "0.01", "locked": "0.01"}}}))
	f.on("POST api.binance.com /sapi/v1/asset/get-funding-asset", fixed([]map[string]string{{"asset": "ETH", "free": "1", "locked": "0", "freeze": "0"}}))
	f.on("GET api.binance.com /sapi/v1/simple-earn/flexible/position", fixed(map[string]any{"rows": []map[string]string{{"asset": "USDT", "totalAmount": "20"}}}))
	f.on("GET api.binance.com /sapi/v1/simple-earn/locked/position", fixed(map[string]any{"rows": []any{}}))
	f.on("GET fapi.binance.com /fapi/v3/account", fixed(map[string]any{"assets": []map[string]string{{"asset": "USDT", "marginBalance": "30"}}}))
	f.on("GET dapi.binance.com /dapi/v1/account", fixed(map[string]any{"assets": []any{}}))
	f.on("POST api.binance.com /api/v3/order", func(q url.Values) (int, any) {
		return 200, map[string]any{"orderId": 4242, "clientOrderId": q.Get("newClientOrderId"), "status": "NEW", "executedQty": "0",
			"origQty": q.Get("quantity"), "price": q.Get("price"), "side": q.Get("side"), "type": q.Get("type")}
	})
	f.on("DELETE api.binance.com /api/v3/order", func(q url.Values) (int, any) {
		return 200, map[string]any{"orderId": 4242, "status": "CANCELED", "executedQty": "0"}
	})
	f.on("GET api.binance.com /api/v3/order", func(q url.Values) (int, any) {
		return 200, map[string]any{"orderId": 4242, "clientOrderId": q.Get("origClientOrderId"), "status": "NEW", "executedQty": "0"}
	})
}

func spotSymbol(sym, base string) map[string]any {
	return map[string]any{"symbol": sym, "status": "TRADING", "baseAsset": base, "quoteAsset": "USDT",
		"orderTypes": []string{"LIMIT", "MARKET"}, "filters": []map[string]string{
			{"filterType": "PRICE_FILTER", "tickSize": "0.01", "minPrice": "0.01", "maxPrice": "1000000"},
			{"filterType": "LOT_SIZE", "stepSize": "0.001", "minQty": "0.001", "maxQty": "9000"},
			{"filterType": "NOTIONAL", "minNotional": "5"}}}
}

func futSymbol(sym string, contractSize float64) map[string]any {
	m := spotSymbol(sym, "BTC")
	if contractSize > 0 {
		m["contractStatus"], m["status"], m["contractSize"] = "TRADING", "", contractSize
		m["filters"] = []map[string]string{{"filterType": "PRICE_FILTER", "tickSize": "0.1"}, {"filterType": "LOT_SIZE", "stepSize": "1", "minQty": "1", "maxQty": "1000"}}
	}
	return m
}

func (f *fakeBinance) serve(w http.ResponseWriter, r *http.Request) {
	parts := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/"), "/", 2)
	host, path := parts[0], ""
	if len(parts) == 2 {
		path = "/" + parts[1]
	}
	key := r.Method + " " + host + " " + path
	f.mu.Lock()
	f.requests = append(f.requests, key+"?"+r.URL.RawQuery)
	fn := f.answers[key]
	f.mu.Unlock()
	if fn == nil {
		w.WriteHeader(404)
		_, _ = w.Write([]byte(`{"code":-1,"msg":"fake: no answer for ` + key + `"}`))
		return
	}
	q := r.URL.Query()
	if sig := q.Get("signature"); sig != "" {
		raw := r.URL.RawQuery
		i := strings.LastIndex(raw, "&signature=")
		b, _ := base64.StdEncoding.DecodeString(sig)
		if i < 0 || r.Header.Get("X-MBX-APIKEY") != f.apiKey || !ed25519.Verify(f.pub, []byte(raw[:i]), b) {
			w.WriteHeader(401)
			_, _ = w.Write([]byte(`{"code":-1022,"msg":"Signature for this request is not valid."}`))
			return
		}
	}
	status, body := fn(q)
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// hostURL sends every table host to the fake, the host as the first path segment.
func (f *fakeBinance) hostURL(host string) string {
	return f.srv.URL + "/" + strings.TrimPrefix(host, "https://")
}

func (f *fakeBinance) seen(prefix string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, r := range f.requests {
		if strings.HasPrefix(r, prefix) {
			n++
		}
	}
	return n
}
