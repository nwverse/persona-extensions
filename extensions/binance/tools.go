package main

import (
	"encoding/json"
)

type tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"input_schema"`
}

func obj(props map[string]any, required ...string) map[string]any {
	s := map[string]any{"type": "object", "properties": props, "additionalProperties": false}
	if len(required) > 0 {
		s["required"] = required
	}
	return s
}

var (
	pSymbol  = map[string]any{"type": "string", "description": "Binance symbol, e.g. BTCUSDT (spot / USDⓈ-M) or BTCUSD_PERP (COIN-M)"}
	pMarket  = map[string]any{"type": "string", "enum": []string{"spot", "usdm", "coinm"}, "description": "spot, usdm (USDⓈ-M futures) or coinm (COIN-M futures)"}
	pNumber  = map[string]any{"type": []string{"string", "number"}}
	pOrderID = map[string]any{"type": "integer", "minimum": 1, "description": "Binance order id"}
	pClient  = map[string]any{"type": "string", "maxLength": 36, "description": "client order id (alternative to order_id)"}
)

var readTools = []tool{
	{"binance_overview", "Total Binance assets in USDT by account (spot, funding, earn, USDⓈ-M, COIN-M), the largest assets, change since yesterday's snapshot and today's order allowance used. Read-only.", obj(map[string]any{})},
	{"binance_balances", "Per-asset balances (free, locked, USDT value) of one or every account. Read-only.", obj(map[string]any{
		"account":  map[string]any{"type": "string", "enum": []string{"spot", "funding", "earn", "usdm", "coinm"}},
		"min_usdt": map[string]any{"type": "number", "minimum": 0, "description": "hide assets worth less (default 1)"},
	})},
	{"binance_positions", "Open futures positions: side, size, entry, mark, unrealized PnL, leverage, liquidation price and distance. Read-only.", obj(map[string]any{
		"market": map[string]any{"type": "string", "enum": []string{"usdm", "coinm"}},
	})},
	{"binance_open_orders", "Open orders of a market (all symbols, or one). Read-only.", obj(map[string]any{"market": pMarket, "symbol": pSymbol}, "market")},
	{"binance_order", "One order's status and fills, by order_id or client_id. Read-only.", obj(map[string]any{
		"market": pMarket, "symbol": pSymbol, "order_id": pOrderID, "client_id": pClient}, "market", "symbol")},
	{"binance_trades", "Own trades of a symbol over the last days (at most 30). Read-only.", obj(map[string]any{
		"market": pMarket, "symbol": pSymbol, "days": map[string]any{"type": "integer", "minimum": 1, "maximum": 30}}, "market", "symbol")},
	{"binance_income", "USDⓈ-M futures income (realized PnL, funding fees, commissions) over the last days (at most 30). Read-only.", obj(map[string]any{
		"days": map[string]any{"type": "integer", "minimum": 1, "maximum": 30}})},
	{"binance_transfers", "Deposit and withdrawal history (records only; the extension cannot withdraw) over the last days (at most 90). Read-only.", obj(map[string]any{
		"days": map[string]any{"type": "integer", "minimum": 1, "maximum": 90}})},
	{"binance_price", "Spot price and 24 h change of up to 20 symbols (public data, no key). Read-only.", obj(map[string]any{
		"symbols": map[string]any{"type": "array", "items": pSymbol, "minItems": 1, "maxItems": 20}}, "symbols")},
	{"binance_limits", "The order guards now: trading on or off, paper trading mode, markets, whitelist, per-order and daily limits, today's use, max leverage, limit deviation, market slippage. Read-only.", obj(map[string]any{})},
	{"binance_status", "The API key's permissions as Binance reports them, whether writes are locked and why, and the request weight used. Read-only.", obj(map[string]any{})},
}

func orderSchema(futures bool) map[string]any {
	props := map[string]any{
		"symbol":        pSymbol,
		"side":          map[string]any{"type": "string", "enum": []string{"BUY", "SELL"}},
		"type":          map[string]any{"type": "string", "enum": []string{"LIMIT", "MARKET"}},
		"quantity":      withDesc(pNumber, "size in the base asset (COIN-M: contracts), on the symbol's step"),
		"price":         withDesc(pNumber, "limit price, on the symbol's tick; LIMIT only"),
		"time_in_force": map[string]any{"type": "string", "enum": []string{"GTC", "IOC", "FOK"}, "description": "LIMIT only, default GTC"},
	}
	if futures {
		props["reduce_only"] = map[string]any{"type": "boolean", "description": "one-way mode only: only reduce the position"}
		props["position_side"] = map[string]any{"type": "string", "enum": []string{"LONG", "SHORT"}, "description": "hedge mode only (required there)"}
	} else {
		props["quote_amount"] = withDesc(pNumber, "MARKET only: amount of the quote asset to spend or receive, instead of quantity")
	}
	return obj(props, "symbol", "side", "type")
}

func withDesc(base map[string]any, d string) map[string]any {
	out := map[string]any{}
	for k, v := range base {
		out[k] = v
	}
	out["description"] = d
	return out
}

var cancelSchema = obj(map[string]any{"symbol": pSymbol, "order_id": pOrderID, "client_id": pClient}, "symbol")

var writeTools = []tool{
	{"binance_spot_order", "Place one spot LIMIT or MARKET order. The user confirms it on the phone; the guards (whitelist, per-order and daily limits, price deviation, market slippage) refuse anything outside the settings.", orderSchema(false)},
	{"binance_spot_cancel", "Cancel one spot order. The user confirms it on the phone.", cancelSchema},
	{"binance_usdm_order", "Place one USDⓈ-M futures LIMIT or MARKET order (no leverage change; refused above the max leverage). The user confirms it on the phone.", orderSchema(true)},
	{"binance_usdm_cancel", "Cancel one USDⓈ-M futures order. The user confirms it on the phone.", cancelSchema},
	{"binance_coinm_order", "Place one COIN-M futures LIMIT or MARKET order, quantity in contracts. The user confirms it on the phone.", orderSchema(true)},
	{"binance_coinm_cancel", "Cancel one COIN-M futures order. The user confirms it on the phone.", cancelSchema},
}

var paperTools = []tool{
	{"binance_paper_spot_order", "Simulate one spot LIMIT or MARKET order in the paper ledger. The user confirms it on the phone; no exchange order is placed.", orderSchema(false)},
	{"binance_paper_usdm_order", "Simulate one USDⓈ-M futures LIMIT or MARKET order in the paper ledger. The user confirms it on the phone; no exchange order is placed.", orderSchema(true)},
	{"binance_paper_coinm_order", "Simulate one COIN-M futures LIMIT or MARKET order in the paper ledger. The user confirms it on the phone; no exchange order is placed.", orderSchema(true)},
}

func decodeArgs(raw json.RawMessage, v any) error { return decodeStrict(raw, v) }
