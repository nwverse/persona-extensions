package main

import (
	"context"
	"encoding/json"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona-extensions/pluginkit"
	"github.com/nwverse/persona/sdk/go/ext"
	"github.com/nwverse/persona/sdk/go/exttest"
	"github.com/nwverse/persona/sdk/go/wire"
)

const tradeCfg = `{"trading_enabled": true, "markets": ["spot","usdm","coinm"], "symbol_whitelist": ["BTCUSDT"], "max_order_usdt": 20, "timezone": "UTC"}`

// testApp is an app on the fake, started with cfg and the fake's key, its caches filled by one round.
func testApp(t *testing.T, f *fakeBinance, cfg string, dir string) *app {
	t.Helper()
	a := newApp()
	a.hostURL, a.noLoop = f.hostURL, true
	in := &ext.Init{InitializeParams: wire.InitializeParams{Config: json.RawMessage(cfg),
		Secrets:   map[string]string{"api_key": f.apiKey, "ed25519_private_key": f.privPEM},
		Extension: &wire.ExtensionInfo{ID: "binance", DataDir: dir}}}
	if _, err := a.init(context.Background(), in, nil); err != nil {
		t.Fatal(err)
	}
	a.round(context.Background(), time.Now(), func(string, time.Duration, time.Time) bool { return true })
	if a.locked != "" {
		t.Fatalf("writes locked after the first round: %s (last error %v)", a.locked, a.lastErr)
	}
	return a
}

func TestProtocolConformance(t *testing.T) {
	f := newFakeBinance(t)
	a := newApp()
	a.hostURL, a.noLoop = f.hostURL, true
	exttest.Check(t, manifest, exttest.Serve(t, program(a)))
}

func TestManifestInboxRules(t *testing.T) {
	m, err := pluginkit.CheckManifest(authorManifest)
	if err != nil {
		t.Fatal(err)
	}
	if !m.Fallback(connectorID) {
		t.Fatal("fallback not declared")
	}
	if err := pluginkit.CheckStandardSettings(authorManifest); err != nil {
		t.Fatal(err)
	}
}

// Coding agents get the read tools only; every write asks on the phone.
func TestAgentsReadOnlyAndWritesConfirmed(t *testing.T) {
	var m struct {
		Contributes struct {
			Connectors []struct {
				AgentTools    []string `json:"agent_tools"`
				ConfirmAlways []string `json:"confirm_always"`
			} `json:"connectors"`
		} `json:"contributes"`
	}
	if err := json.Unmarshal(authorManifest, &m); err != nil {
		t.Fatal(err)
	}
	c := m.Contributes.Connectors[0]
	reads := map[string]bool{}
	for _, r := range readTools {
		reads[r.Name] = true
	}
	for _, n := range c.AgentTools {
		if strings.Contains(n, "*") || !reads[n] {
			t.Errorf("agent_tools has %q: only exact read tool names", n)
		}
	}
	if len(c.ConfirmAlways) != 1 || c.ConfirmAlways[0] != "binance_*" {
		t.Errorf("confirm_always = %v", c.ConfirmAlways)
	}
	for _, w := range writeTools {
		if !strings.HasPrefix(w.Name, "binance_") || reads[w.Name] {
			t.Errorf("write %s", w.Name)
		}
	}
	for _, p := range paperTools {
		if !strings.HasPrefix(p.Name, "binance_") || reads[p.Name] {
			t.Errorf("paper %s", p.Name)
		}
	}
}

func TestReadToolsOnFake(t *testing.T) {
	f := newFakeBinance(t)
	a := testApp(t, f, tradeCfg, t.TempDir())
	ctx := context.Background()
	out, err := a.callTool(ctx, "binance_overview", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	// spot 150.5 USDT + 0.02 BTC × 100 + funding 1 ETH × 10 + earn 20 + usdm 30 = 212.5
	if !strings.Contains(out, "Total ≈ 212.5 USDT") {
		t.Fatalf("overview:\n%s", out)
	}
	out, err = a.callTool(ctx, "binance_positions", json.RawMessage(`{"market":"usdm"}`))
	if err != nil || !strings.Contains(out, "BTCUSDT | LONG | 0.5") || !strings.Contains(out, "3x") {
		t.Fatalf("positions %v:\n%s", err, out)
	}
	out, err = a.callTool(ctx, "binance_limits", json.RawMessage(`{}`))
	if err != nil || !strings.Contains(out, "max_order_usdt: 20") {
		t.Fatalf("limits %v:\n%s", err, out)
	}
}

var clientIDRule = regexp.MustCompile(`^[\.A-Z\:/a-z0-9_-]{1,36}$`)

func TestPlaceAndCancelOnce(t *testing.T) {
	f := newFakeBinance(t)
	dir := t.TempDir()
	a := testApp(t, f, tradeCfg, dir)
	args := json.RawMessage(`{"symbol":"BTCUSDT","side":"BUY","type":"LIMIT","quantity":"0.1","price":"96"}`)
	d, err := a.describe("binance_spot_order", args)
	if err != nil {
		t.Fatal(err)
	}
	summary := d["summary"].(string)
	for _, want := range []string{"BTCUSDT", "买入", "0.1 BTC", "96", "-4.00%", "≈ 9.6 USDT", "单笔上限 20"} {
		if !strings.Contains(summary, want) {
			t.Errorf("card lacks %q:\n%s", want, summary)
		}
	}
	ctx := context.Background()
	r1, err := a.do(ctx, "req-1", "binance_spot_order", args)
	if err != nil {
		t.Fatal(err)
	}
	r2, err := a.do(ctx, "req-1", "binance_spot_order", args)
	if err != nil || r2["text"] != r1["text"] || r1["message_ref"] != "4242" {
		t.Fatalf("repeat: %v %v %v", r1, r2, err)
	}
	if n := f.seen("POST api.binance.com /api/v3/order"); n != 1 {
		t.Fatalf("orders sent: %d", n)
	}
	cid := clientOrderID("req-1")
	if !clientIDRule.MatchString(cid) {
		t.Fatalf("client id %q breaks Binance's rule", cid)
	}
	if f.seen("POST api.binance.com /api/v3/order?") == 0 || !strings.Contains(strings.Join(f.requests, "\n"), "newClientOrderId="+cid) {
		t.Fatal("client id not sent")
	}
	used, n := a.ledger.usage(a.cfg.dayKey(time.Now()))
	if n != 1 || used < 9.59 || used > 9.61 {
		t.Fatalf("ledger used %v orders %d", used, n)
	}
	if _, err := a.describe("binance_spot_cancel", json.RawMessage(`{"symbol":"BTCUSDT","order_id":4242}`)); err != nil {
		t.Fatal(err)
	}
	c, err := a.do(ctx, "req-2", "binance_spot_cancel", json.RawMessage(`{"symbol":"BTCUSDT","order_id":4242}`))
	if err != nil || !strings.Contains(c["text"].(string), "已撤销") {
		t.Fatalf("cancel %v %v", c, err)
	}
	// The ledger survives a restart.
	b := testApp(t, f, tradeCfg, dir)
	if r, ok := b.ledger.result("req-1"); !ok || r.Text != r1["text"] {
		t.Fatal("result lost across restart")
	}
}

func TestRejectedOrderReleasesAllowance(t *testing.T) {
	f := newFakeBinance(t)
	a := testApp(t, f, tradeCfg, t.TempDir())
	f.on("POST api.binance.com /api/v3/order", func(url.Values) (int, any) {
		return 400, map[string]any{"code": -2010, "msg": "Account has insufficient balance for requested action."}
	})
	_, err := a.do(context.Background(), "req-x", "binance_spot_order",
		json.RawMessage(`{"symbol":"BTCUSDT","side":"BUY","type":"LIMIT","quantity":"0.1","price":"96"}`))
	if err == nil || !strings.Contains(err.Error(), "-2010") {
		t.Fatalf("got %v", err)
	}
	if used, n := a.ledger.usage(a.cfg.dayKey(time.Now())); used != 0 || n != 0 {
		t.Fatalf("allowance kept: %v %d", used, n)
	}
}

// A changed guard setting takes effect when the hub restarts the program with it.
func TestGuardSettingChangeTakesEffect(t *testing.T) {
	f := newFakeBinance(t)
	args := json.RawMessage(`{"symbol":"BTCUSDT","side":"BUY","type":"LIMIT","quantity":"0.3","price":"99"}`)
	a := testApp(t, f, tradeCfg, t.TempDir())
	if _, err := a.describe("binance_spot_order", args); err == nil || !strings.Contains(err.Error(), "单笔上限 20") {
		t.Fatalf("20 U limit: %v", err)
	}
	b := testApp(t, f, strings.Replace(tradeCfg, `"max_order_usdt": 20`, `"max_order_usdt": 50`, 1), t.TempDir())
	if _, err := b.describe("binance_spot_order", args); err != nil {
		t.Fatalf("50 U limit: %v", err)
	}
}

func TestMarketOrderSlippage(t *testing.T) {
	f := newFakeBinance(t)
	a := testApp(t, f, tradeCfg, t.TempDir())
	args := json.RawMessage(`{"symbol":"BTCUSDT","side":"BUY","type":"MARKET","quote_amount":"10"}`)
	if _, err := a.describe("binance_spot_order", args); err != nil {
		t.Fatal(err)
	}
	f.on("GET data-api.binance.vision /api/v3/ticker/price", func(q url.Values) (int, any) {
		return 200, map[string]string{"symbol": "BTCUSDT", "price": "101.50"}
	})
	_, err := a.do(context.Background(), "req-m", "binance_spot_order", args)
	if err == nil || !strings.Contains(err.Error(), "滑点保护") {
		t.Fatalf("got %v", err)
	}
	if f.seen("POST api.binance.com /api/v3/order") != 0 {
		t.Fatal("order sent despite slippage")
	}
}

func TestSettingsCheck(t *testing.T) {
	f := newFakeBinance(t)
	a := newApp()
	a.hostURL, a.noLoop = f.hostURL, true
	secrets := map[string]string{"api_key": f.apiKey, "ed25519_private_key": f.privPEM}
	ctx := context.Background()
	// Hub saves secrets one at a time: either half must pass local validation while the other is unset.
	if ok, msg := a.checkSettings(ctx, json.RawMessage(tradeCfg), map[string]string{"api_key": f.apiKey}); !ok {
		t.Fatalf("api_key alone refused: %s", msg)
	}
	if ok, _ := a.checkSettings(ctx, json.RawMessage(tradeCfg), map[string]string{"api_key": "bad key with spaces"}); ok {
		t.Fatal("malformed api_key alone accepted")
	}
	if ok, msg := a.checkSettings(ctx, json.RawMessage(tradeCfg), map[string]string{"ed25519_private_key": f.privPEM}); !ok {
		t.Fatalf("ed25519_private_key alone refused: %s", msg)
	}
	// Single-line PEM (as pasted into password/SecureField or CLI) and literal \n escapes must also parse.
	flatPEM := strings.ReplaceAll(strings.TrimSpace(f.privPEM), "\n", " ")
	escapedPEM := strings.ReplaceAll(strings.TrimSpace(f.privPEM), "\n", `\n`)
	for label, pemVal := range map[string]string{"single-line": flatPEM, "escaped": escapedPEM} {
		if ok, msg := a.checkSettings(ctx, json.RawMessage(tradeCfg), map[string]string{"api_key": f.apiKey, "ed25519_private_key": pemVal}); !ok {
			t.Fatalf("%s PEM refused: %s", label, msg)
		}
	}
	if ok, _ := a.checkSettings(ctx, json.RawMessage(tradeCfg), map[string]string{"ed25519_private_key": "-----BEGIN PRIVATE KEY-----"}); ok {
		t.Fatal("truncated PEM accepted")
	}
	if ok, _ := a.checkSettings(ctx, json.RawMessage(tradeCfg), map[string]string{"ed25519_private_key": "not a key"}); ok {
		t.Fatal("malformed ed25519_private_key alone accepted")
	}
	if ok, msg := a.checkSettings(ctx, json.RawMessage(tradeCfg), secrets); !ok {
		t.Fatalf("good key refused: %s", msg)
	}
	f.on("GET api.binance.com /sapi/v1/account/apiRestrictions", fixed(map[string]any{"ipRestrict": true, "enableReading": true,
		"enableSpotAndMarginTrading": true, "enableWithdrawals": true}))
	if ok, msg := a.checkSettings(ctx, json.RawMessage(tradeCfg), secrets); ok || !strings.Contains(msg, "提现") {
		t.Fatalf("withdrawal key: %v %s", ok, msg)
	}
	f.on("GET api.binance.com /sapi/v1/account/apiRestrictions", fixed(map[string]any{"ipRestrict": false, "enableReading": true,
		"enableSpotAndMarginTrading": true}))
	if ok, msg := a.checkSettings(ctx, json.RawMessage(tradeCfg), secrets); ok || !strings.Contains(msg, "IP") {
		t.Fatalf("no IP: %v %s", ok, msg)
	}
	bad := map[string]string{"api_key": f.apiKey, "ed25519_private_key": "not a key"}
	if ok, _ := a.checkSettings(ctx, json.RawMessage(tradeCfg), bad); ok {
		t.Fatal("bad PEM accepted")
	}
	if ok, msg := a.checkSettings(ctx, json.RawMessage(`{"proxy_url": "http://127.0.0.1:3128"}`), nil); ok || !strings.Contains(msg, "host.persona.internal") {
		t.Fatalf("loopback proxy: %v %s", ok, msg)
	}
	if strings.Contains(strings.Join(f.requests, "\n"), f.apiKey) {
		t.Fatal("API key in a URL")
	}
}

func TestInboxThingsCarryFallbackTier(t *testing.T) {
	a := newApp()
	a.raise(event{Thread: "price:BTCUSDT", Kind: "price_move", Tier: pluginkit.TierFYI, Title: "BTCUSDT 1 小时 +6%", Body: "x", At: time.Now()})
	things := a.inbox.collect("my rules")
	if err := pluginkit.CheckThings(things, "my rules", true); err != nil {
		t.Fatal(err)
	}
}

func TestFailedOrderIsNeverSentTwice(t *testing.T) {
	resolveGap = time.Millisecond
	args := json.RawMessage(`{"symbol":"BTCUSDT","side":"BUY","type":"LIMIT","quantity":"0.1","price":"96"}`)
	bad := func(url.Values) (int, any) { return 502, map[string]any{"msg": "Bad Gateway"} }
	day := func(a *app) (float64, int) { return a.ledger.usage(a.cfg.dayKey(time.Now())) }

	t.Run("5xx, then found by client id", func(t *testing.T) {
		f := newFakeBinance(t)
		a := testApp(t, f, tradeCfg, t.TempDir())
		f.on("POST api.binance.com /api/v3/order", bad)
		r, err := a.do(context.Background(), "r1", "binance_spot_order", args)
		if err != nil || !strings.Contains(r["text"].(string), "已下单") {
			t.Fatalf("%v %v", r, err)
		}
		if f.seen("POST api.binance.com /api/v3/order") != 1 {
			t.Fatal("resent")
		}
	})
	t.Run("5xx and the query fails too: unknown, allowance kept, no retry", func(t *testing.T) {
		f := newFakeBinance(t)
		a := testApp(t, f, tradeCfg, t.TempDir())
		f.on("POST api.binance.com /api/v3/order", bad)
		f.on("GET api.binance.com /api/v3/order", bad)
		r, err := a.do(context.Background(), "r2", "binance_spot_order", args)
		if err != nil || !strings.Contains(r["text"].(string), "结果不明") {
			t.Fatalf("%v %v", r, err)
		}
		if used, n := day(a); used < 9.5 || n != 1 {
			t.Fatalf("allowance released: %v %d", used, n)
		}
		r2, _ := a.do(context.Background(), "r2", "binance_spot_order", args)
		if r2["text"] != r["text"] || f.seen("POST api.binance.com /api/v3/order") != 1 {
			t.Fatal("a repeat sent the order again")
		}
		if len(a.inbox.collect("")) == 0 {
			t.Fatal("no alert to check the order")
		}
	})
	t.Run("5xx and Binance says no such order: released, not resent", func(t *testing.T) {
		f := newFakeBinance(t)
		a := testApp(t, f, tradeCfg, t.TempDir())
		f.on("POST api.binance.com /api/v3/order", bad)
		f.on("GET api.binance.com /api/v3/order", func(url.Values) (int, any) {
			return 400, map[string]any{"code": -2013, "msg": "Order does not exist."}
		})
		_, err := a.do(context.Background(), "r3", "binance_spot_order", args)
		if err == nil || !strings.Contains(err.Error(), "没下成") {
			t.Fatalf("%v", err)
		}
		if used, n := day(a); used != 0 || n != 0 {
			t.Fatalf("allowance kept: %v %d", used, n)
		}
		if f.seen("POST api.binance.com /api/v3/order") != 1 {
			t.Fatal("resent")
		}
	})
}

func TestOpenOrdersCarryIntoTheNextDay(t *testing.T) {
	l, _ := openLedger("")
	_ = l.update(func(s *ledgerState) error {
		l.roll("2026-01-01")
		s.UsedUSDT = 300
		s.Placed = append(s.Placed, placedOrder{Status: "NEW", Counts: true, Notional: 120},
			placedOrder{Status: "FILLED", Counts: true, Notional: 180})
		return nil
	})
	if used, _ := l.usage("2026-01-02"); used != 120 {
		t.Fatalf("carried %v, want the open order's 120", used)
	}
}

func TestMarketOrderCountsWorstCase(t *testing.T) {
	env := testEnv(t, `{"max_order_usdt": 10}`)
	// 0.1 × 100 = 10, but up to 1% slippage makes 10.1: over the limit.
	_, err := planOrder(env, marketSpot, json.RawMessage(`{"symbol":"BTCUSDT","side":"BUY","type":"MARKET","quantity":"0.1"}`))
	if err == nil || !strings.Contains(err.Error(), "单笔上限") {
		t.Fatalf("got %v", err)
	}
}
