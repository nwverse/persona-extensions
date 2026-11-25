package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

var t0 = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

func testMarket() *market {
	m := newMarket()
	m.prices = map[string]float64{"BTCUSDT": 100, "ETHUSDT": 10}
	m.pricesAt = t0
	m.marks[marketUSDM] = map[string]float64{"BTCUSDT": 100}
	m.marks[marketCoinM] = map[string]float64{"BTCUSD_PERP": 100}
	m.marksAt[marketUSDM], m.marksAt[marketCoinM] = t0, t0
	rule := func(mkt, sym string, cs float64, tick, step, minQty string) *symbolRules {
		return &symbolRules{Market: mkt, Symbol: sym, Status: "TRADING", Base: "BTC", Quote: "USDT", TickSize: ratOf(tick),
			StepSize: ratOf(step), MinQty: ratOf(minQty), MaxQty: ratOf("9000"), MinNotional: ratOf("5"), ContractSize: cs,
			OrderTypes: map[string]bool{"LIMIT": true, "MARKET": true}}
	}
	m.rules[marketSpot] = map[string]*symbolRules{"BTCUSDT": rule(marketSpot, "BTCUSDT", 0, "0.01", "0.001", "0.001"),
		"ETHUSDT": rule(marketSpot, "ETHUSDT", 0, "0.01", "0.001", "0.001")}
	m.rules[marketUSDM] = map[string]*symbolRules{"BTCUSDT": rule(marketUSDM, "BTCUSDT", 0, "0.1", "0.001", "0.001")}
	cm := rule(marketCoinM, "BTCUSD_PERP", 100, "0.1", "1", "1")
	cm.MinNotional = nil
	m.rules[marketCoinM] = map[string]*symbolRules{"BTCUSD_PERP": cm}
	for _, k := range []string{marketSpot, marketUSDM, marketCoinM} {
		m.rulesAt[k] = t0
	}
	return m
}

func testEnv(t *testing.T, cfgJSON string) *guardEnv {
	t.Helper()
	base := `{"trading_enabled": true, "markets": ["spot", "usdm", "coinm"], "symbol_whitelist": ["BTCUSDT", "BTCUSD_PERP"], "timezone": "UTC"}`
	raw := mergeJSON(t, base, cfgJSON)
	cfg, err := parseConfig(raw)
	if err != nil {
		t.Fatal(err)
	}
	return &guardEnv{cfg: &cfg, now: t0, market: testMarket(), perms: keyPerms{Checked: true, Spot: true, Futures: true, IP: true},
		leverage: func(mkt, s string) (int, bool) { return 3, true },
		dual:     func(mkt string) (bool, bool) { return mkt == marketCoinM, true },
		position: func(mkt, s, side string) (float64, bool) {
			if mkt == marketUSDM && side == "BOTH" {
				return 0.5, true
			}
			if mkt == marketCoinM && side == "LONG" {
				return 2, true
			}
			return 0, true
		}}
}

func mergeJSON(t *testing.T, a, b string) json.RawMessage {
	m := map[string]any{}
	if err := json.Unmarshal([]byte(a), &m); err != nil {
		t.Fatal(err)
	}
	if b != "" {
		if err := json.Unmarshal([]byte(b), &m); err != nil {
			t.Fatal(err)
		}
	}
	out, _ := json.Marshal(m)
	return out
}

func TestDefaultsMatchManifest(t *testing.T) {
	var m struct {
		Configuration struct {
			Properties map[string]struct {
				Default any `json:"default"`
			} `json:"properties"`
		} `json:"configuration"`
	}
	if err := json.Unmarshal(authorManifest, &m); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"max_order_usdt": 200.0, "max_daily_usdt": 1000.0, "max_leverage": 5.0, "limit_deviation_pct": 5.0,
		"market_orders": true, "market_slippage_pct": 1.0, "trading_enabled": false, "max_orders_per_day": 20.0,
		"paper_trading": false}
	for k, v := range want {
		if got := m.Configuration.Properties[k].Default; got != v {
			t.Errorf("manifest default %s = %v, want %v (the agreed guard default)", k, got, v)
		}
	}
	if got := m.Configuration.Properties["proxy_url"].Default; got != nil && got != "" {
		t.Errorf("manifest default proxy_url = %v, want empty", got)
	}
	c := defaultConfig()
	if c.ProxyURL != "" || c.MaxOrderUSDT != 200 || c.MaxDailyUSDT != 1000 || c.MaxLeverage != 5 || c.LimitDeviationPct != 5 || c.MarketSlippagePct != 1 || !c.MarketOrders || c.TradingEnabled ||
		c.PaperTrading {
		t.Errorf("defaultConfig drifted from the manifest: %+v", c)
	}
}

func TestOrderGuards(t *testing.T) {
	for _, c := range []struct {
		name, cfg, mkt, args, want string
	}{
		{"spot limit ok", "", marketSpot, `{"symbol":"BTCUSDT","side":"BUY","type":"LIMIT","quantity":"0.1","price":"96"}`, ""},
		{"trading off", `{"trading_enabled": false}`, marketSpot, `{"symbol":"BTCUSDT","side":"BUY","type":"LIMIT","quantity":"0.1","price":"96"}`, "已关闭"},
		{"market not allowed", `{"markets": ["spot"]}`, marketUSDM, `{"symbol":"BTCUSDT","side":"BUY","type":"LIMIT","quantity":"0.1","price":"96"}`, "没有允许"},
		{"not whitelisted", "", marketSpot, `{"symbol":"ETHUSDT","side":"BUY","type":"LIMIT","quantity":"1","price":"10"}`, "不在允许"},
		{"empty whitelist allows nothing", `{"symbol_whitelist": []}`, marketSpot, `{"symbol":"BTCUSDT","side":"BUY","type":"LIMIT","quantity":"0.1","price":"96"}`, "不在允许"},
		{"limit too far", "", marketSpot, `{"symbol":"BTCUSDT","side":"BUY","type":"LIMIT","quantity":"0.1","price":"94.99"}`, "偏离"},
		{"limit deviation setting", `{"limit_deviation_pct": 10}`, marketSpot, `{"symbol":"BTCUSDT","side":"BUY","type":"LIMIT","quantity":"0.1","price":"91"}`, ""},
		{"over per-order limit", "", marketSpot, `{"symbol":"BTCUSDT","side":"BUY","type":"LIMIT","quantity":"2.1","price":"100"}`, "单笔上限"},
		{"per-order limit setting", `{"max_order_usdt": 20}`, marketSpot, `{"symbol":"BTCUSDT","side":"BUY","type":"LIMIT","quantity":"0.21","price":"100"}`, "单笔上限 20"},
		{"tick", "", marketSpot, `{"symbol":"BTCUSDT","side":"BUY","type":"LIMIT","quantity":"0.1","price":"99.999"}`, "最小价格单位"},
		{"step", "", marketSpot, `{"symbol":"BTCUSDT","side":"BUY","type":"LIMIT","quantity":"0.0001","price":"99"}`, "最小数量单位"},
		{"min notional", "", marketSpot, `{"symbol":"BTCUSDT","side":"BUY","type":"LIMIT","quantity":"0.01","price":"99"}`, "最小下单金额"},
		{"market by quote", "", marketSpot, `{"symbol":"BTCUSDT","side":"BUY","type":"MARKET","quote_amount":"50"}`, ""},
		{"market off", `{"market_orders": false}`, marketSpot, `{"symbol":"BTCUSDT","side":"BUY","type":"MARKET","quote_amount":"50"}`, "市价单"},
		{"market with price", "", marketSpot, `{"symbol":"BTCUSDT","side":"BUY","type":"MARKET","quantity":"0.1","price":"100"}`, "不能带 price"},
		{"stop orders are not offered", "", marketSpot, `{"symbol":"BTCUSDT","side":"BUY","type":"STOP_LOSS","quantity":"0.1"}`, "条件单"},
		{"unknown argument", "", marketSpot, `{"symbol":"BTCUSDT","side":"BUY","type":"LIMIT","quantity":"0.1","price":"99","stopPrice":"1"}`, "invalid arguments"},
		{"exponent refused", "", marketSpot, `{"symbol":"BTCUSDT","side":"BUY","type":"LIMIT","quantity":1e-1,"price":"99"}`, "quantity"},
		{"usdm over leverage", `{"max_leverage": 2}`, marketUSDM, `{"symbol":"BTCUSDT","side":"BUY","type":"LIMIT","quantity":"0.1","price":"99"}`, "最大杠杆 2x"},
		{"usdm reduce-only ok over leverage, not counted", `{"max_leverage": 2, "max_order_usdt": 1}`, marketUSDM, `{"symbol":"BTCUSDT","side":"SELL","type":"LIMIT","quantity":"0.5","price":"99","reduce_only":true}`, ""},
		{"usdm reduce-only beyond position", "", marketUSDM, `{"symbol":"BTCUSDT","side":"SELL","type":"LIMIT","quantity":"0.6","price":"99","reduce_only":true}`, "超过当前持仓"},
		{"usdm reduce-only wrong way", "", marketUSDM, `{"symbol":"BTCUSDT","side":"BUY","type":"LIMIT","quantity":"0.1","price":"99","reduce_only":true}`, "方向"},
		{"usdm one-way refuses position_side", "", marketUSDM, `{"symbol":"BTCUSDT","side":"BUY","type":"LIMIT","quantity":"0.1","price":"99","position_side":"LONG"}`, "单向持仓"},
		{"coinm hedge needs side", "", marketCoinM, `{"symbol":"BTCUSD_PERP","side":"BUY","type":"LIMIT","quantity":"1","price":"99"}`, "position_side"},
		{"coinm hedge open long", "", marketCoinM, `{"symbol":"BTCUSD_PERP","side":"BUY","type":"LIMIT","quantity":"1","price":"99","position_side":"LONG"}`, ""},
		{"coinm hedge refuses reduce_only", "", marketCoinM, `{"symbol":"BTCUSD_PERP","side":"SELL","type":"LIMIT","quantity":"1","price":"99","position_side":"LONG","reduce_only":true}`, "reduce_only"},
		{"coinm hedge close long", `{"max_order_usdt": 1}`, marketCoinM, `{"symbol":"BTCUSD_PERP","side":"SELL","type":"MARKET","quantity":"2","position_side":"LONG"}`, ""},
		{"coinm contract notional", "", marketCoinM, `{"symbol":"BTCUSD_PERP","side":"BUY","type":"LIMIT","quantity":"3","price":"99","position_side":"LONG"}`, "单笔上限"},
	} {
		t.Run(c.name, func(t *testing.T) {
			env := testEnv(t, c.cfg)
			p, err := planOrder(env, c.mkt, json.RawMessage(c.args))
			if c.want == "" {
				if err != nil {
					t.Fatalf("refused: %v", err)
				}
				if p.Params.Get("symbol") == "" {
					t.Fatal("no params")
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("got %v, want %q", err, c.want)
			}
		})
	}
}

func TestDailyAllowanceAndCount(t *testing.T) {
	env := testEnv(t, `{"max_daily_usdt": 100}`)
	env.usedUSDT = 95
	args := json.RawMessage(`{"symbol":"BTCUSDT","side":"BUY","type":"LIMIT","quantity":"0.06","price":"99"}`)
	if _, err := planOrder(env, marketSpot, args); err == nil || !strings.Contains(err.Error(), "剩余额度") {
		t.Fatalf("daily: %v", err)
	}
	env = testEnv(t, `{"max_orders_per_day": 2}`)
	env.ordersDone = 2
	if _, err := planOrder(env, marketSpot, args); err == nil || !strings.Contains(err.Error(), "笔数上限") {
		t.Fatalf("count: %v", err)
	}
}

func TestStalePricesRefuse(t *testing.T) {
	env := testEnv(t, "")
	env.now = t0.Add(3 * time.Minute)
	_, err := planOrder(env, marketSpot, json.RawMessage(`{"symbol":"BTCUSDT","side":"BUY","type":"LIMIT","quantity":"0.1","price":"99"}`))
	if err == nil || !strings.Contains(err.Error(), "过期") {
		t.Fatalf("got %v", err)
	}
}

func TestLockedKeyRefusesEveryWrite(t *testing.T) {
	env := testEnv(t, "")
	env.locked = "这把 Key 开了 提现 权限"
	if err := checkWritable(env, marketSpot); err == nil || !strings.Contains(err.Error(), "锁定") {
		t.Fatalf("got %v", err)
	}
	env = testEnv(t, "")
	env.perms.Futures = false
	if err := checkWritable(env, marketUSDM); err == nil {
		t.Fatal("futures write without the futures permission")
	}
}
