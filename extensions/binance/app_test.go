package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"regexp"
	"strconv"
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
		if err != nil {
			t.Fatalf("%v %v", r, err)
		}
		text := r["text"].(string)
		cid := clientOrderID("r1")
		if !strings.Contains(text, "已下单") || !strings.Contains(text, "client id "+cid) || r["message_ref"] != "4242" {
			t.Fatalf("recovered result missing derived cid or order ref: %v (want cid %s)", r, cid)
		}
		if strings.Contains(text, "502") || strings.Contains(text, "Bad Gateway") || strings.Contains(text, "Binance HTTP") {
			t.Fatalf("recovered result leaked stale write error: %s", text)
		}
		if saved, ok := a.ledger.result("r1"); !ok || saved.Text != text {
			t.Fatalf("ledger result mismatch: ok=%v saved=%+v want=%q", ok, saved, text)
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

func TestLedgerRollbackKeepsMapsNonNil(t *testing.T) {
	l, err := openLedger("")
	if err != nil {
		t.Fatal(err)
	}
	if err := l.update(func(s *ledgerState) error {
		return errors.New("boom")
	}); err == nil {
		t.Fatal("expected rollback error")
	}
	if err := l.update(func(s *ledgerState) error {
		s.Results["req-1"] = writeResult{Text: "ok"}
		s.Notified["spot:BTCUSDT:1"] = "FILLED"
		s.Snap["2026-01-01"] = 100
		return nil
	}); err != nil {
		t.Fatalf("update after rollback failed: %v", err)
	}
}

func TestResolveUnknownRespectsContext(t *testing.T) {
	f := newFakeBinance(t)
	a := testApp(t, f, tradeCfg, t.TempDir())
	oldGap := resolveGap
	resolveGap = 5 * time.Second
	t.Cleanup(func() { resolveGap = oldGap })

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	_, v := a.resolveUnknown(ctx, marketSpot, "BTCUSDT", "cid-cancel")
	if v != verdictUnknown {
		t.Fatalf("got verdict %v, want verdictUnknown", v)
	}
	if time.Since(start) > time.Second {
		t.Fatalf("resolveUnknown ignored canceled context and took %v", time.Since(start))
	}
}

func TestLoopRefreshesStaleFuturesRulesIndependently(t *testing.T) {
	f := newFakeBinance(t)
	a := newApp()
	a.hostURL, a.noLoop = f.hostURL, true
	in := &ext.Init{InitializeParams: wire.InitializeParams{
		Config:    json.RawMessage(tradeCfg),
		Secrets:   map[string]string{"api_key": f.apiKey, "ed25519_private_key": f.privPEM},
		Extension: &wire.ExtensionInfo{ID: "binance", DataDir: t.TempDir()},
	}}
	if _, err := a.init(context.Background(), in, nil); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	// First round: spot rules succeed, USD-M exchangeInfo fails transiently.
	f.on("GET fapi.binance.com /fapi/v1/exchangeInfo", func(url.Values) (int, any) {
		return 503, map[string]any{"msg": "unavailable"}
	})
	a.round(context.Background(), now, func(string, time.Duration, time.Time) bool { return true })
	if a.market.rulesAge(marketSpot, now) > time.Minute {
		t.Fatalf("spot rules not populated")
	}
	if a.market.rulesAge(marketUSDM, now) <= 2*time.Hour {
		t.Fatalf("expected usdm rules to still be stale after 503")
	}
	// Next round 15s later: spot rules are fresh and periodic "rules" is not due, but stale usdm rules must refresh.
	f.on("GET fapi.binance.com /fapi/v1/exchangeInfo", fixed(map[string]any{"symbols": []any{futSymbol("BTCUSDT", 0)}}))
	next := now.Add(15 * time.Second)
	a.round(context.Background(), next, func(string, time.Duration, time.Time) bool { return false })
	if a.market.rulesAge(marketUSDM, next) > time.Minute {
		t.Fatalf("usdm rules stayed stale despite spot being fresh")
	}
}

func TestIncomeChunksSevenDayWindowsAndTransfersSetsLimit(t *testing.T) {
	f := newFakeBinance(t)
	a := testApp(t, f, tradeCfg, t.TempDir())
	var incomeWindows int
	f.on("GET fapi.binance.com /fapi/v1/income", func(q url.Values) (int, any) {
		incomeWindows++
		st, err1 := strconv.ParseInt(q.Get("startTime"), 10, 64)
		et, err2 := strconv.ParseInt(q.Get("endTime"), 10, 64)
		if err1 != nil || err2 != nil || et < st || et-st > int64((7*24*time.Hour)/time.Millisecond) {
			t.Errorf("income window out of 7d bound: start=%s end=%s", q.Get("startTime"), q.Get("endTime"))
		}
		return 200, []map[string]string{{"symbol": "BTCUSDT", "incomeType": "FUNDING_FEE", "income": "1.5", "asset": "USDT"}}
	})
	out, err := a.callTool(context.Background(), "binance_income", json.RawMessage(`{"days": 20}`))
	if err != nil {
		t.Fatal(err)
	}
	if incomeWindows != 3 {
		t.Fatalf("expected 3 seven-day windows for 20 days, got %d", incomeWindows)
	}
	if !strings.Contains(out, "FUNDING_FEE USDT: 4.5") {
		t.Fatalf("unexpected income summary:\n%s", out)
	}

	var depLimit, wdLimit string
	f.on("GET api.binance.com /sapi/v1/capital/deposit/hisrec", func(q url.Values) (int, any) {
		depLimit = q.Get("limit")
		return 200, []any{}
	})
	f.on("GET api.binance.com /sapi/v1/capital/withdraw/history", func(q url.Values) (int, any) {
		wdLimit = q.Get("limit")
		return 200, []any{}
	})
	if _, err := a.callTool(context.Background(), "binance_transfers", json.RawMessage(`{"days": 30}`)); err != nil {
		t.Fatal(err)
	}
	if depLimit != "1000" || wdLimit != "1000" {
		t.Fatalf("expected explicit limit=1000 for deposits/withdrawals, got dep=%q wd=%q", depLimit, wdLimit)
	}
}

func TestLateReconcileAndSettleAbsentAccounting(t *testing.T) {
	f := newFakeBinance(t)
	a := testApp(t, f, tradeCfg, t.TempDir())
	now := time.Now()
	day := a.cfg.dayKey(now)

	// Seed a NOT_FOUND reduce-only order and a NOT_FOUND normal order, plus an UNKNOWN normal order.
	if err := a.ledger.update(func(s *ledgerState) error {
		a.ledger.roll(day)
		s.UsedUSDT = 15
		s.Orders = 1
		s.Placed = []placedOrder{
			{Market: marketUSDM, Symbol: "BTCUSDT", ClientID: "cid-ro", Side: "SELL", Type: "LIMIT", Notional: 50, Reducing: true, Counts: false, Status: "NOT_FOUND", At: now, RequestID: "req-ro"},
			{Market: marketSpot, Symbol: "BTCUSDT", ClientID: "cid-norm", Side: "BUY", Type: "LIMIT", Notional: 10, Reducing: false, Counts: false, Status: "NOT_FOUND", At: now, RequestID: "req-norm"},
			{Market: marketSpot, Symbol: "BTCUSDT", ClientID: "cid-unk", Side: "BUY", Type: "LIMIT", Notional: 15, Reducing: false, Counts: true, Status: "UNKNOWN", At: now.Add(-5 * time.Minute), RequestID: "req-unk"},
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// followOrders finds cid-ro and cid-norm on Binance, while cid-unk returns -2013 and settles absent.
	f.on("GET fapi.binance.com /fapi/v1/order", func(q url.Values) (int, any) {
		return 200, map[string]any{"orderId": 7001, "clientOrderId": q.Get("origClientOrderId"), "status": "FILLED", "executedQty": "0.5"}
	})
	f.on("GET api.binance.com /api/v3/order", func(q url.Values) (int, any) {
		if q.Get("origClientOrderId") == "cid-unk" {
			return 400, map[string]any{"code": -2013, "msg": "Order does not exist."}
		}
		return 200, map[string]any{"orderId": 7002, "clientOrderId": q.Get("origClientOrderId"), "status": "NEW", "executedQty": "0"}
	})

	a.followOrders(context.Background(), now)

	// Expected: cid-unk (-15 USDT, -1 order) + cid-ro (+0 USDT, +1 order) + cid-norm (+10 USDT, +1 order) => UsedUSDT=10, Orders=2.
	used, orders := a.ledger.usage(day)
	if used < 9.99 || used > 10.01 || orders != 2 {
		t.Fatalf("after reconciliation: used=%v orders=%d, want used=10 orders=2", used, orders)
	}
	snap := a.ledger.snapshot()
	if snap.Placed[0].Counts {
		t.Fatalf("reduce-only recovered order must not set Counts=true: %+v", snap.Placed[0])
	}
	if !snap.Placed[1].Counts {
		t.Fatalf("normal recovered order must set Counts=true: %+v", snap.Placed[1])
	}
	if snap.Placed[2].Status != "NOT_FOUND" || snap.Placed[2].Counts {
		t.Fatalf("settled absent order not cleared: %+v", snap.Placed[2])
	}
}

func TestLiqWarningResetsWhenPositionClosed(t *testing.T) {
	f := newFakeBinance(t)
	a := testApp(t, f, tradeCfg, t.TempDir())
	now := time.Now()
	hasLiq := func() bool {
		for _, th := range a.inbox.collect("") {
			if th.Kind == "liq_risk" {
				return true
			}
		}
		return false
	}
	// Default LiqWarnPct is 15% (warn=0.15): d=0.02 -> level 3, d=0.12 -> level 1.
	a.inbox = newInbox()
	a.watchPositions(marketUSDM, []position{{Symbol: "BTCUSDT", Side: "BOTH", Amount: 1, Mark: 100, Liquidation: 98, Leverage: 3}}, now)
	if !hasLiq() {
		t.Fatalf("expected initial level-3 liq_risk alert, got %+v", a.inbox.collect(""))
	}
	// Close position completely so liqLevel resets.
	a.watchPositions(marketUSDM, nil, now.Add(time.Minute))
	// Reopen same market/symbol/direction at level 1 (d=0.12 < 0.15): must alert again.
	a.inbox = newInbox()
	a.watchPositions(marketUSDM, []position{{Symbol: "BTCUSDT", Side: "BOTH", Amount: 1, Mark: 100, Liquidation: 88, Leverage: 3}}, now.Add(2*time.Minute))
	if !hasLiq() {
		t.Fatalf("expected level-1 liq_risk alert after position closed and reopened, got %+v", a.inbox.collect(""))
	}
}

func TestDepositsWaitForCompletionAndUseUniqueThreads(t *testing.T) {
	f := newFakeBinance(t)
	a := testApp(t, f, tradeCfg, t.TempDir())
	ctx := context.Background()
	now := time.Now()

	// Seed empty history.
	f.on("GET api.binance.com /sapi/v1/capital/deposit/hisrec", fixed([]any{}))
	a.watchDeposits(ctx, now)

	// Pending deposit (status=0) must NOT alert or be marked seen.
	f.on("GET api.binance.com /sapi/v1/capital/deposit/hisrec", fixed([]map[string]any{
		{"id": "dep-1", "txId": "tx-1", "coin": "USDT", "amount": "100", "status": 0, "network": "TRX"},
	}))
	a.watchDeposits(ctx, now.Add(time.Minute))
	if got := a.inbox.collect(""); len(got) != 0 {
		t.Fatalf("pending deposit triggered alert: %+v", got)
	}

	// dep-1 completes (status=1) alongside a second identical-amount deposit dep-2 (status=6 credited).
	f.on("GET api.binance.com /sapi/v1/capital/deposit/hisrec", fixed([]map[string]any{
		{"id": "dep-1", "txId": "tx-1", "coin": "USDT", "amount": "100", "status": 1, "network": "TRX"},
		{"id": "dep-2", "txId": "tx-2", "coin": "USDT", "amount": "100", "status": 6, "network": "TRX"},
	}))
	a.watchDeposits(ctx, now.Add(2*time.Minute))
	got := a.inbox.collect("")
	if len(got) != 2 {
		t.Fatalf("expected 2 distinct deposit alerts, got %d: %+v", len(got), got)
	}
	if got[0].Thread == got[1].Thread || !strings.HasPrefix(got[0].Thread, "deposit:dep-") {
		t.Fatalf("expected unique deposit:<id> threads, got %q and %q", got[0].Thread, got[1].Thread)
	}
}

func TestWithdrawalsUseUniqueThreads(t *testing.T) {
	f := newFakeBinance(t)
	a := testApp(t, f, tradeCfg, t.TempDir())
	ctx := context.Background()
	now := time.Now()

	f.on("GET api.binance.com /sapi/v1/capital/withdraw/history", fixed([]any{}))
	a.watchWithdrawals(ctx, now)

	// Two distinct withdrawals with identical amount/coin/network must produce two distinct inbox threads.
	f.on("GET api.binance.com /sapi/v1/capital/withdraw/history", fixed([]map[string]any{
		{"id": "wd-1", "coin": "USDT", "amount": "50", "network": "TRX"},
		{"id": "wd-2", "coin": "USDT", "amount": "50", "network": "TRX"},
	}))
	a.watchWithdrawals(ctx, now.Add(time.Minute))
	got := a.inbox.collect("")
	if len(got) != 2 {
		t.Fatalf("expected 2 distinct withdrawal alerts, got %d: %+v", len(got), got)
	}
	if got[0].Thread == got[1].Thread || !strings.HasPrefix(got[0].Thread, "withdraw:wd-") {
		t.Fatalf("expected unique withdraw:<id> threads, got %q and %q", got[0].Thread, got[1].Thread)
	}
}

func TestCoinMReadOrderPriceUsesCoinMHost(t *testing.T) {
	f := newFakeBinance(t)
	coinmCfg := `{"trading_enabled": true, "markets": ["spot","usdm","coinm"], "symbol_whitelist": ["BTCUSD_PERP"], "max_order_usdt": 200, "timezone": "UTC"}`
	a := testApp(t, f, coinmCfg, t.TempDir())
	f.on("POST dapi.binance.com /dapi/v1/order", func(q url.Values) (int, any) {
		return 200, map[string]any{"orderId": 8801, "clientOrderId": q.Get("newClientOrderId"), "status": "NEW", "executedQty": "0"}
	})

	ctx := context.Background()
	beforeCoinM := f.seen("GET dapi.binance.com /dapi/v1/premiumIndex")
	price, err := a.readOrderPrice(ctx, marketCoinM, "BTCUSD_PERP")
	if err != nil {
		t.Fatalf("readOrderPrice(coinm) failed: %v", err)
	}
	if price != 100 {
		t.Fatalf("readOrderPrice(coinm) = %v, want 100", price)
	}
	if f.seen("GET dapi.binance.com /dapi/v1/premiumIndex") != beforeCoinM+1 {
		t.Fatalf("expected exact host dapi.binance.com /dapi/v1/premiumIndex hit, requests: %v", f.requests)
	}
	if f.seen("GET data-api.binance.vision /dapi/v1/premiumIndex") != 0 {
		t.Fatalf("unexpected /dapi/v1/premiumIndex call on hostPublic")
	}

	args := json.RawMessage(`{"symbol":"BTCUSD_PERP","side":"BUY","type":"LIMIT","quantity":"1","price":"100","position_side":"LONG"}`)
	r, err := a.do(ctx, "coinm-live-1", "binance_coinm_order", args)
	if err != nil {
		t.Fatalf("live coinm order failed: %v", err)
	}
	if !strings.Contains(r["text"].(string), "币本位合约") || !strings.Contains(r["text"].(string), clientOrderID("coinm-live-1")) {
		t.Fatalf("unexpected live coinm result: %v", r)
	}
	if f.seen("GET dapi.binance.com /dapi/v1/premiumIndex") != beforeCoinM+2 || f.seen("POST dapi.binance.com /dapi/v1/order") != 1 {
		t.Fatalf("coinm live order did not hit dapi.binance.com cleanly: %v", f.requests)
	}
}

func TestRequestIDArgsVerificationAndAbsentReplay(t *testing.T) {
	oldGap := resolveGap
	resolveGap = time.Millisecond
	t.Cleanup(func() { resolveGap = oldGap })

	argsA := json.RawMessage(`{"symbol":"BTCUSDT","side":"BUY","type":"LIMIT","quantity":"0.1","price":"96"}`)
	argsAReordered := json.RawMessage(`{"price": "96", "quantity": "0.1", "type": "LIMIT", "side": "BUY", "symbol": "BTCUSDT"}`)
	argsB := json.RawMessage(`{"symbol":"BTCUSDT","side":"BUY","type":"LIMIT","quantity":"0.2","price":"96"}`)
	cancelArgs := json.RawMessage(`{"symbol":"BTCUSDT","order_id":4242}`)
	ctx := context.Background()

	t.Run("completed live order rejects mismatched args and action on same request_id", func(t *testing.T) {
		f := newFakeBinance(t)
		a := testApp(t, f, tradeCfg, t.TempDir())

		r1, err := a.do(ctx, "req-idem-1", "binance_spot_order", argsA)
		if err != nil {
			t.Fatal(err)
		}
		// Identical args with different JSON key order replay cleanly.
		r2, err := a.do(ctx, "req-idem-1", "binance_spot_order", argsAReordered)
		if err != nil || r2["text"] != r1["text"] {
			t.Fatalf("canonical retry failed: got (%v, %v), want %v", r2, err, r1)
		}
		// Different order quantity with same request_id must be rejected.
		if _, err := a.do(ctx, "req-idem-1", "binance_spot_order", argsB); err == nil || !strings.Contains(err.Error(), "拒绝重放") {
			t.Fatalf("mismatched order args: got %v, want replay conflict error", err)
		}
		// Different action name with same request_id must be rejected.
		if _, err := a.do(ctx, "req-idem-1", "binance_spot_cancel", cancelArgs); err == nil || !strings.Contains(err.Error(), "拒绝重放") {
			t.Fatalf("mismatched action name: got %v, want replay conflict error", err)
		}
		if f.seen("POST api.binance.com /api/v3/order") != 1 || f.seen("DELETE api.binance.com /api/v3/order") != 0 {
			t.Fatalf("unexpected extra writes: %v", f.requests)
		}
	})

	t.Run("in-flight PENDING_SUBMIT rejects mismatched args on same request_id", func(t *testing.T) {
		f := newFakeBinance(t)
		a := testApp(t, f, tradeCfg, t.TempDir())
		if err := a.ledger.update(func(s *ledgerState) error {
			savePlacedOrder(s, placedOrder{
				Market: marketSpot, Symbol: "BTCUSDT", ClientID: clientOrderID("req-inflight-mismatch"),
				Side: "BUY", Type: "LIMIT", Quantity: "0.1", Price: "96", Notional: 9.6, Counts: true,
				Status: "PENDING_SUBMIT", At: time.Now(), RequestID: "req-inflight-mismatch",
				ArgsKey: argsKey("binance_spot_order", argsA),
			})
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := a.do(ctx, "req-inflight-mismatch", "binance_spot_order", argsB); err == nil || !strings.Contains(err.Error(), "拒绝重放") {
			t.Fatalf("in-flight mismatch: got %v, want replay conflict error", err)
		}
		if f.seen("GET api.binance.com /api/v3/order") != 0 || f.seen("POST api.binance.com /api/v3/order") != 0 {
			t.Fatalf("mismatched in-flight retry should not call Binance: %v", f.requests)
		}
	})

	t.Run("verdictAbsent retries keep returning error and reject mismatched args", func(t *testing.T) {
		f := newFakeBinance(t)
		a := testApp(t, f, tradeCfg, t.TempDir())
		f.on("POST api.binance.com /api/v3/order", func(url.Values) (int, any) {
			return 502, map[string]any{"msg": "Bad Gateway"}
		})
		f.on("GET api.binance.com /api/v3/order", func(url.Values) (int, any) {
			return 400, map[string]any{"code": -2013, "msg": "Order does not exist."}
		})
		if _, err := a.do(ctx, "req-absent-1", "binance_spot_order", argsA); err == nil || !strings.Contains(err.Error(), "没下成") {
			t.Fatalf("first call: got %v, want absent error", err)
		}
		// Retry with identical args must STILL return an error (never turn NOT_FOUND into nil/success) and not resend.
		if res, err := a.do(ctx, "req-absent-1", "binance_spot_order", argsA); err == nil || res != nil || !strings.Contains(err.Error(), "没下成") {
			t.Fatalf("retry of verdictAbsent: got (%v, %v), want (nil, absent error)", res, err)
		}
		// Retry with mismatched args must reject with conflict error.
		if _, err := a.do(ctx, "req-absent-1", "binance_spot_order", argsB); err == nil || !strings.Contains(err.Error(), "拒绝重放") {
			t.Fatalf("mismatched retry of verdictAbsent: got %v, want replay conflict error", err)
		}
		if f.seen("POST api.binance.com /api/v3/order") != 1 {
			t.Fatalf("verdictAbsent retry resent POST: %d", f.seen("POST api.binance.com /api/v3/order"))
		}
	})

	t.Run("recoverInFlightOrder verdictAbsent retries keep returning error", func(t *testing.T) {
		f := newFakeBinance(t)
		a := testApp(t, f, tradeCfg, t.TempDir())
		f.on("GET api.binance.com /api/v3/order", func(url.Values) (int, any) {
			return 400, map[string]any{"code": -2013, "msg": "Order does not exist."}
		})
		if err := a.ledger.update(func(s *ledgerState) error {
			a.ledger.roll(a.cfg.dayKey(time.Now()))
			s.UsedUSDT = 9.6
			s.Orders = 1
			savePlacedOrder(s, placedOrder{
				Market: marketSpot, Symbol: "BTCUSDT", ClientID: clientOrderID("req-recover-absent"),
				Side: "BUY", Type: "LIMIT", Quantity: "0.1", Price: "96", Notional: 9.6, Counts: true,
				Status: "PENDING_SUBMIT", At: time.Now(), RequestID: "req-recover-absent",
				ArgsKey: argsKey("binance_spot_order", argsA),
			})
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := a.do(ctx, "req-recover-absent", "binance_spot_order", argsA); err == nil || !strings.Contains(err.Error(), "没下成") {
			t.Fatalf("first recover absent: got %v", err)
		}
		if res, err := a.do(ctx, "req-recover-absent", "binance_spot_order", argsA); err == nil || res != nil || !strings.Contains(err.Error(), "没下成") {
			t.Fatalf("second recover absent: got (%v, %v), want (nil, absent error)", res, err)
		}
	})

	t.Run("paper order rejects mismatched args on same request_id", func(t *testing.T) {
		f := newFakeBinance(t)
		a := testApp(t, f, string(mergeJSON(t, tradeCfg, `{"paper_trading":true}`)), t.TempDir())
		if _, err := a.do(ctx, "req-paper-1", "binance_paper_spot_order", argsA); err != nil {
			t.Fatal(err)
		}
		if _, err := a.do(ctx, "req-paper-1", "binance_paper_spot_order", argsB); err == nil || !strings.Contains(err.Error(), "拒绝重放") {
			t.Fatalf("paper mismatch: got %v, want replay conflict error", err)
		}
	})

	t.Run("legacy ledger entries without args_key remain compatible and NOT_FOUND still errors", func(t *testing.T) {
		f := newFakeBinance(t)
		a := testApp(t, f, tradeCfg, t.TempDir())
		if err := a.ledger.update(func(s *ledgerState) error {
			s.Results["legacy-ok"] = writeResult{Text: "legacy ok", At: time.Now()}
			s.Results["legacy-absent"] = writeResult{Text: "没下成：旧账本记录", At: time.Now()}
			savePlacedOrder(s, placedOrder{
				Market: marketSpot, Symbol: "BTCUSDT", ClientID: "cid-legacy", Status: "NOT_FOUND",
				At: time.Now(), RequestID: "legacy-absent",
			})
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if r, err := a.do(ctx, "legacy-ok", "binance_spot_order", argsA); err != nil || r["text"] != "legacy ok" {
			t.Fatalf("legacy ok replay: got (%v, %v)", r, err)
		}
		if r, err := a.do(ctx, "legacy-absent", "binance_spot_order", argsA); err == nil || r != nil || !strings.Contains(err.Error(), "没下成") {
			t.Fatalf("legacy absent replay: got (%v, %v), want error", r, err)
		}
	})
}

func TestSpotExchangeInfoIgnoresUSDMOnlyAndInvalidSymbols(t *testing.T) {
	validSpot := map[string]map[string]any{
		"BTCUSDT": spotSymbol("BTCUSDT", "BTC"),
		"ETHUSDT": spotSymbol("ETHUSDT", "ETH"),
	}
	strictExchangeInfo := func(q url.Values) (int, any) {
		var syms []string
		if err := json.Unmarshal([]byte(q.Get("symbols")), &syms); err != nil {
			return 400, map[string]any{"code": -1100, "msg": "bad symbols"}
		}
		var out []any
		for _, s := range syms {
			info, ok := validSpot[s]
			if !ok {
				return 400, map[string]any{"code": -1121, "msg": "Invalid symbol."}
			}
			out = append(out, info)
		}
		return 200, map[string]any{"symbols": out}
	}

	t.Run("loop intersects candidates with known spot tickers", func(t *testing.T) {
		f := newFakeBinance(t)
		f.on("GET data-api.binance.vision /api/v3/exchangeInfo", strictExchangeInfo)
		cfg := `{"trading_enabled": true, "markets": ["spot","usdm"], "symbol_whitelist": ["BTCUSDT","1000PEPEUSDT","TYPOUSDT"], "watch_symbols": ["ETHUSDT","BADWATCH","BTCUSD_PERP"], "timezone": "UTC"}`
		a := testApp(t, f, cfg, t.TempDir())
		if a.market.rule(marketSpot, "BTCUSDT") == nil || a.market.rule(marketSpot, "ETHUSDT") == nil {
			t.Fatalf("expected valid spot rules BTCUSDT & ETHUSDT despite USD-M-only and typo symbols")
		}
		if got := f.seen("GET data-api.binance.vision /api/v3/exchangeInfo"); got != 1 {
			t.Fatalf("expected 1 clean batch exchangeInfo call after ticker intersection, got %d (%v)", got, f.requests)
		}
	})

	t.Run("refreshRules falls back per symbol on -1121 when prices cache is empty", func(t *testing.T) {
		f := newFakeBinance(t)
		f.on("GET data-api.binance.vision /api/v3/exchangeInfo", strictExchangeInfo)
		cl := newClient(f.srv.Client(), nil)
		cl.hostURL = f.hostURL
		m := newMarket()
		now := time.Now()
		if err := m.refreshRules(context.Background(), cl, marketSpot, []string{"1000PEPEUSDT", "BTCUSDT", "TYPOUSDT"}, now); err != nil {
			t.Fatalf("refreshRules failed on mixed valid/invalid symbols: %v", err)
		}
		if m.rule(marketSpot, "BTCUSDT") == nil {
			t.Fatalf("BTCUSDT spot rule missing after -1121 fallback")
		}
		if m.rule(marketSpot, "1000PEPEUSDT") != nil || m.rule(marketSpot, "TYPOUSDT") != nil {
			t.Fatalf("invalid symbols unexpectedly stored in spot rules")
		}
	})
}

func TestToolOpenOrdersUnmarshalValidAndMalformed(t *testing.T) {
	f := newFakeBinance(t)
	a := testApp(t, f, tradeCfg, t.TempDir())
	ctx := context.Background()

	f.on("GET api.binance.com /api/v3/openOrders", fixed([]map[string]any{{
		"symbol": "BTCUSDT", "orderId": 4242, "clientOrderId": "pa-test-1",
		"side": "BUY", "type": "LIMIT", "price": "96.00", "origQty": "0.1",
		"executedQty": "0", "status": "NEW", "time": 1735787045000, "timeInForce": "GTC",
	}}))
	out, err := a.callTool(ctx, "binance_open_orders", json.RawMessage(`{"market":"spot"}`))
	if err != nil {
		t.Fatalf("valid openOrders failed: %v", err)
	}
	for _, want := range []string{"BTCUSDT", "4242", "pa-test-1", "BUY", "LIMIT", "96.00", "2025-01-02T03:04:05Z"} {
		if !strings.Contains(out, want) {
			t.Fatalf("openOrders output missing %q:\n%s", want, out)
		}
	}

	// Malformed time field previously caused extra unmarshal failure and out-of-range panic on extra[i].
	f.on("GET api.binance.com /api/v3/openOrders", fixed([]map[string]any{{
		"symbol": "BTCUSDT", "orderId": 4242, "clientOrderId": "pa-test-1",
		"side": "BUY", "type": "LIMIT", "price": "96.00", "origQty": "0.1",
		"executedQty": "0", "status": "NEW", "time": "not-a-number",
	}}))
	if _, err := a.callTool(ctx, "binance_open_orders", json.RawMessage(`{"market":"spot"}`)); err == nil || !strings.Contains(err.Error(), "cannot be read") {
		t.Fatalf("malformed openOrders: got %v, want unmarshal error without panic", err)
	}
}
