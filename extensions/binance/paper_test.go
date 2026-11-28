package main

import (
	"context"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/sdk/go/ext"
	"github.com/nwverse/persona/sdk/go/wire"
)

const (
	paperSpotLimit10  = `{"symbol":"BTCUSDT","side":"BUY","type":"LIMIT","quantity":"0.1","price":"100"}`
	paperSpotMarket10 = `{"symbol":"BTCUSDT","side":"BUY","type":"MARKET","quote_amount":"10"}`
)

func describeActionRPC(t *testing.T, a *app, name, args string) (map[string]any, error) {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"name": name, "args": json.RawMessage(args)})
	if err != nil {
		t.Fatal(err)
	}
	result, err := a.connector(context.Background(), "actions.describe", raw)
	if err != nil {
		return nil, err
	}
	return result.(map[string]any), nil
}

func executeActionRPC(t *testing.T, a *app, name, args, id string) (map[string]any, error) {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"name": name, "args": json.RawMessage(args), "request_id": id})
	if err != nil {
		t.Fatal(err)
	}
	result, err := a.connector(context.Background(), "actions.do", raw)
	if err != nil {
		return nil, err
	}
	return result.(map[string]any), nil
}

func TestPaperTradingIsolation(t *testing.T) {
	const paperSpot = "binance_paper_spot_order"
	const paperCfg = `{"paper_trading":true,"max_daily_usdt":15}`
	f := newFakeBinance(t)
	dir := t.TempDir()
	// Start with an existing live ledger, as a user enabling paper mode would.
	live := testApp(t, f, tradeCfg, dir)
	if _, err := live.do(context.Background(), "live-first", "binance_spot_order", json.RawMessage(paperSpotLimit10)); err != nil {
		t.Fatal(err)
	}

	a := newApp()
	a.hostURL, a.noLoop = f.hostURL, true
	in := &ext.Init{InitializeParams: wire.InitializeParams{Config: mergeJSON(t, tradeCfg, paperCfg),
		Extension: &wire.ExtensionInfo{ID: "binance", DataDir: dir}}}
	if _, err := a.init(context.Background(), in, nil); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.stop)
	a.round(context.Background(), time.Now(), func(string, time.Duration, time.Time) bool { return true })
	if got := a.status()["state"]; got != "ok" {
		t.Fatalf("keyless paper status = %v", got)
	}
	list, err := a.connector(context.Background(), "actions.list", nil)
	if err != nil || len(list.(map[string]any)["tools"].([]tool)) != 3 {
		t.Fatalf("paper catalog %v %v", list, err)
	}
	for _, tl := range list.(map[string]any)["tools"].([]tool) {
		if !strings.HasPrefix(tl.Name, paperActionPrefix) {
			t.Fatalf("real action in paper catalog: %s", tl.Name)
		}
	}
	d, err := describeActionRPC(t, a, paperSpot, paperSpotMarket10)
	if err != nil || !strings.Contains(d["summary"].(string), "纸面模拟") {
		t.Fatalf("paper card: %v %v", d, err)
	}
	pricesBefore := f.seen("GET data-api.binance.vision /api/v3/ticker/price")
	result, err := executeActionRPC(t, a, paperSpot, paperSpotMarket10, "paper-first")
	if err != nil || !strings.Contains(result["text"].(string), "模拟成交 0.1 @ 100") || result["message_ref"] != nil {
		t.Fatalf("paper result %v %v", result, err)
	}
	if f.seen("GET data-api.binance.vision /api/v3/ticker/price") != pricesBefore+1 {
		t.Fatal("paper order did not fetch a fresh price")
	}
	if _, err := describeActionRPC(t, a, paperSpot, paperSpotMarket10); err == nil || !strings.Contains(err.Error(), "剩余额度") {
		t.Fatalf("paper daily allowance not applied: %v", err)
	}
	if _, err := executeActionRPC(t, a, "binance_spot_cancel", `{"symbol":"BTCUSDT","order_id":4242}`, "cancel"); err == nil {
		t.Fatal("live cancel accepted in paper mode")
	}
	if _, err := executeActionRPC(t, a, "binance_spot_order", paperSpotLimit10, "old-live"); err == nil {
		t.Fatal("pending live order accepted in paper mode")
	}
	if _, err := executeActionRPC(t, live, paperSpot, paperSpotLimit10, "old-paper"); err == nil {
		t.Fatal("pending paper order accepted in live mode")
	}
	if f.seen("POST api.binance.com /api/v3/order") != 1 || f.seen("DELETE api.binance.com /api/v3/order") != 0 {
		t.Fatal("paper path reached a live exchange write")
	}

	after := testApp(t, f, string(mergeJSON(t, tradeCfg, paperCfg)), dir)
	got, err := executeActionRPC(t, after, paperSpot, paperSpotMarket10, "paper-first")
	if err != nil || got["text"] != result["text"] {
		t.Fatalf("paper result not persisted: %v %v", got, err)
	}
	paperState, liveState := after.paper.snapshot(), after.ledger.snapshot()
	if len(paperState.Placed) != 1 || paperState.Placed[0].Status != "FILLED" || paperState.Placed[0].FillPrice != "100" ||
		len(liveState.Placed) != 1 || liveState.Placed[0].RequestID != "live-first" {
		t.Fatalf("paper ledger %v, live ledger %v", paperState.Placed, liveState.Placed)
	}
}

func TestPaperMarketOrderSlippageGuard(t *testing.T) {
	const paperSpot = "binance_paper_spot_order"
	f := newFakeBinance(t)
	a := testApp(t, f, string(mergeJSON(t, tradeCfg, `{"paper_trading":true}`)), t.TempDir())
	if _, err := describeActionRPC(t, a, paperSpot, paperSpotMarket10); err != nil {
		t.Fatal(err)
	}
	f.on("GET data-api.binance.vision /api/v3/ticker/price", func(url.Values) (int, any) {
		return 200, map[string]string{"symbol": "BTCUSDT", "price": "101.50"}
	})
	if _, err := executeActionRPC(t, a, paperSpot, paperSpotMarket10, "paper-slip"); err == nil || !strings.Contains(err.Error(), "滑点保护") {
		t.Fatalf("paper slippage guard: got %v", err)
	}
	if len(a.paper.snapshot().Placed) != 0 || f.seen("POST api.binance.com /api/v3/order") != 0 {
		t.Fatal("slipped paper order was recorded or sent")
	}
}

func TestPaperLimitOutcomeAndLedgerFailure(t *testing.T) {
	const paperSpot = "binance_paper_spot_order"
	f := newFakeBinance(t)
	a := testApp(t, f, string(mergeJSON(t, tradeCfg, `{"paper_trading":true}`)), t.TempDir())
	args := strings.Replace(paperSpotLimit10, "100", "96", 1)
	result, err := executeActionRPC(t, a, paperSpot, args, "no-fill")
	if err != nil || !strings.Contains(result["text"].(string), "模拟未成交") {
		t.Fatalf("expected unfilled limit: %v %v", result, err)
	}
	path := a.paper.path
	a.paper.path = filepath.Join(path, "missing", "paper-ledger.json")
	if _, err := executeActionRPC(t, a, paperSpot, paperSpotLimit10, "disk-fail"); err == nil || !strings.Contains(err.Error(), "纸面台账写入失败") {
		t.Fatalf("expected write error: %v", err)
	}
	if _, ok := a.paper.result("disk-fail"); ok || len(a.paper.snapshot().Placed) != 1 {
		t.Fatal("failed transaction left a paper order or retry result behind")
	}
	a.paper.path = path
	if _, err := executeActionRPC(t, a, paperSpot, paperSpotLimit10, "disk-fail"); err != nil {
		t.Fatalf("retry after ledger recovered: %v", err)
	}
	if _, err := os.Stat(path); err != nil || f.seen("POST api.binance.com /api/v3/order") != 0 {
		t.Fatalf("paper ledger missing or exchange order sent: %v", err)
	}
}

func TestPaperFuturesNeverReachExchangeWrite(t *testing.T) {
	const paper = "binance_paper_usdm_order"
	f := newFakeBinance(t)
	a := testApp(t, f, string(mergeJSON(t, tradeCfg, `{"paper_trading":true}`)), t.TempDir())
	d, err := describeActionRPC(t, a, paper, paperSpotLimit10)
	if err != nil || !strings.Contains(d["summary"].(string), "U 本位合约") || !strings.Contains(d["summary"].(string), "纸面模拟") {
		t.Fatalf("paper futures describe: %v %v", d, err)
	}
	before := f.seen("GET fapi.binance.com /fapi/v1/premiumIndex")
	r, err := executeActionRPC(t, a, paper, paperSpotLimit10, "paper-futures")
	if err != nil || !strings.Contains(r["text"].(string), "模拟成交") {
		t.Fatalf("paper futures execute: %v %v", r, err)
	}
	if f.seen("GET fapi.binance.com /fapi/v1/premiumIndex") != before+1 || f.seen("POST fapi.binance.com /fapi/v1/order") != 0 {
		t.Fatal("paper futures must read a fresh mark without submitting an order")
	}
}

func TestPaperConfigValidation(t *testing.T) {
	if _, err := parseConfig(json.RawMessage(`{"paper_trading":"true"}`)); err == nil {
		t.Fatal("accepted non-boolean paper_trading")
	}
	cfg, err := parseConfig(nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ProxyURL != "" || cfg.TradingEnabled || cfg.PaperTrading {
		t.Fatalf("unexpected default config: %+v", cfg)
	}
}

func TestLiveOrderIdempotencyAcrossModeSwitch(t *testing.T) {
	f := newFakeBinance(t)
	dir := t.TempDir()
	live := testApp(t, f, tradeCfg, dir)

	// 1. Place a real spot order and cancel it in live mode so both results live in ledger.json.
	liveRes, err := executeActionRPC(t, live, "binance_spot_order", paperSpotLimit10, "live-done")
	if err != nil {
		t.Fatal(err)
	}
	cancelRes, err := executeActionRPC(t, live, "binance_spot_cancel", `{"symbol":"BTCUSDT","order_id":4242}`, "live-cancel-done")
	if err != nil {
		t.Fatal(err)
	}
	// 2. Record a reserved/in-flight live order that has not yet stored a terminal writeResult.
	inFlightCID := clientOrderID("live-inflight")
	if err := live.ledger.update(func(s *ledgerState) error {
		live.ledger.roll(live.cfg.dayKey(time.Now()))
		s.UsedUSDT += 10
		s.Orders++
		savePlacedOrder(s, placedOrder{
			Market: marketSpot, Symbol: "BTCUSDT", ClientID: inFlightCID, Side: "BUY", Type: "LIMIT",
			Quantity: "0.1", Price: "100", Notional: 10, Counts: true, Status: "PENDING_SUBMIT",
			At: time.Now(), RequestID: "live-inflight",
		})
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// 3. Switch to paper_trading: true on the same data directory (keeping credentials loaded).
	paperApp := testApp(t, f, string(mergeJSON(t, tradeCfg, `{"paper_trading":true}`)), dir)

	// Retrying the completed live order must replay from a.ledger without hitting Binance or paper-ledger.json.
	replayed, err := executeActionRPC(t, paperApp, "binance_spot_order", paperSpotLimit10, "live-done")
	if err != nil || replayed["text"] != liveRes["text"] || replayed["message_ref"] != liveRes["message_ref"] {
		t.Fatalf("live order retry in paper mode: got (%v, %v), want %v", replayed, err, liveRes)
	}
	// Retrying the completed live cancel must also replay from a.ledger.
	replayedCancel, err := executeActionRPC(t, paperApp, "binance_spot_cancel", `{"symbol":"BTCUSDT","order_id":4242}`, "live-cancel-done")
	if err != nil || replayedCancel["text"] != cancelRes["text"] {
		t.Fatalf("live cancel retry in paper mode: got (%v, %v), want %v", replayedCancel, err, cancelRes)
	}
	// Retrying the reserved/in-flight live order in paper mode must be caught by a.ledger and never resent.
	if _, err := executeActionRPC(t, paperApp, "binance_spot_order", paperSpotLimit10, "live-inflight"); err == nil ||
		!strings.Contains(err.Error(), inFlightCID) || !strings.Contains(err.Error(), "不会重发") {
		t.Fatalf("in-flight live retry in paper mode: got %v, want refusal citing %s", err, inFlightCID)
	}
	// A brand-new unseen live order in paper mode is still rejected by checkActionMode.
	if _, err := executeActionRPC(t, paperApp, "binance_spot_order", paperSpotLimit10, "live-unseen"); err == nil ||
		!strings.Contains(err.Error(), "下单模式已切换") {
		t.Fatalf("unseen live order in paper mode: got %v", err)
	}
	if f.seen("POST api.binance.com /api/v3/order") != 1 || f.seen("DELETE api.binance.com /api/v3/order") != 1 {
		t.Fatalf("cross-mode retries triggered extra exchange writes: %v", f.requests)
	}
	if len(paperApp.paper.snapshot().Placed) != 0 || len(paperApp.paper.snapshot().Results) != 0 {
		t.Fatalf("live retries leaked into paper ledger: %+v", paperApp.paper.snapshot())
	}

	// 4. Returning to live mode and retrying the in-flight live order reconciles via GET without resending POST.
	liveAgain := testApp(t, f, tradeCfg, dir)
	recovered, err := executeActionRPC(t, liveAgain, "binance_spot_order", paperSpotLimit10, "live-inflight")
	if err != nil {
		t.Fatalf("in-flight live recovery failed: %v", err)
	}
	if text := recovered["text"].(string); !strings.Contains(text, "已下单") || !strings.Contains(text, "client id "+inFlightCID) {
		t.Fatalf("unexpected recovered text: %q", text)
	}
	if f.seen("POST api.binance.com /api/v3/order") != 1 {
		t.Fatalf("in-flight recovery resent POST: %v", f.requests)
	}
	if saved, ok := liveAgain.ledger.result("live-inflight"); !ok || saved.Text != recovered["text"] {
		t.Fatalf("recovered result not stored in live ledger: ok=%v saved=%+v", ok, saved)
	}
}

func TestPaperCoinMOrderUsesCoinMHostAndNeverWritesExchange(t *testing.T) {
	const paperCoinM = "binance_paper_coinm_order"
	const coinmArgs = `{"symbol":"BTCUSD_PERP","side":"BUY","type":"LIMIT","quantity":"1","price":"100","position_side":"LONG"}`
	f := newFakeBinance(t)
	cfg := `{"trading_enabled":true,"paper_trading":true,"markets":["spot","usdm","coinm"],"symbol_whitelist":["BTCUSD_PERP"],"max_order_usdt":200,"timezone":"UTC"}`
	a := testApp(t, f, cfg, t.TempDir())

	d, err := describeActionRPC(t, a, paperCoinM, coinmArgs)
	if err != nil || !strings.Contains(d["summary"].(string), "币本位合约") || !strings.Contains(d["summary"].(string), "纸面模拟") {
		t.Fatalf("paper coinm describe: %v %v", d, err)
	}
	before := f.seen("GET dapi.binance.com /dapi/v1/premiumIndex")
	r, err := executeActionRPC(t, a, paperCoinM, coinmArgs, "paper-coinm-1")
	if err != nil || !strings.Contains(r["text"].(string), "币本位合约") || !strings.Contains(r["text"].(string), "模拟成交 1 @ 100") {
		t.Fatalf("paper coinm execute: %v %v", r, err)
	}
	if f.seen("GET dapi.binance.com /dapi/v1/premiumIndex") != before+1 {
		t.Fatalf("paper coinm did not read mark price from dapi.binance.com: %v", f.requests)
	}
	if f.seen("GET data-api.binance.vision /dapi/v1/premiumIndex") != 0 {
		t.Fatalf("unexpected /dapi/v1/premiumIndex hit on hostPublic")
	}
	if f.seen("POST dapi.binance.com /dapi/v1/order") != 0 {
		t.Fatalf("paper coinm sent a live exchange order")
	}
}
