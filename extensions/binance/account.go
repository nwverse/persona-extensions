package main

import (
	"context"
	"net/url"
	"strings"
)

// holding is one asset in one account.
type holding struct {
	Account string // spot, funding, earn, usdm, coinm
	Asset   string
	Free    float64
	Locked  float64
	USDT    float64 // estimated value
}

func (h holding) Total() float64 { return h.Free + h.Locked }

// position is one open futures position.
type position struct {
	Market      string
	Symbol      string
	Side        string // LONG / SHORT / BOTH
	Amount      float64
	Entry       float64
	Mark        float64
	PnL         float64
	Liquidation float64
	Leverage    int
	Notional    float64
}

// Direction is LONG or SHORT for a one-way (BOTH) position, by sign.
func (p position) Direction() string {
	if p.Side != "BOTH" && p.Side != "" {
		return p.Side
	}
	if p.Amount < 0 {
		return "SHORT"
	}
	return "LONG"
}

// LiqDistance is how far the mark price is from liquidation, as a fraction of the mark (0 when there is none).
func (p position) LiqDistance() float64 {
	if p.Liquidation <= 0 || p.Mark <= 0 {
		return 0
	}
	d := (p.Mark - p.Liquidation) / p.Mark
	if p.Direction() == "SHORT" {
		d = -d
	}
	return d
}

var stableUSD = map[string]bool{"USDT": true, "USDC": true, "FDUSD": true, "BUSD": true, "TUSD": true, "USDP": true, "DAI": true}

// valueUSDT estimates qty of asset in USDT from spot prices.
func valueUSDT(m *market, asset string, qty float64) float64 {
	if qty == 0 {
		return 0
	}
	if asset == "USDT" {
		return qty
	}
	if p := m.spotPrice(asset + "USDT"); p > 0 {
		return qty * p
	}
	if p := m.spotPrice("USDT" + asset); p > 0 {
		return qty / p
	}
	if stableUSD[asset] {
		return qty
	}
	if p, b := m.spotPrice(asset+"BTC"), m.spotPrice("BTCUSDT"); p > 0 && b > 0 {
		return qty * p * b
	}
	return 0
}

// earn's locked products are named LD<asset> in the spot account for flexible earn; leave those to the earn list.
func isEarnShadow(asset string) bool { return strings.HasPrefix(asset, "LD") && len(asset) > 2 }

func (a *app) spotHoldings(ctx context.Context) ([]holding, error) {
	var acc struct {
		Balances []struct {
			Asset  string `json:"asset"`
			Free   string `json:"free"`
			Locked string `json:"locked"`
		} `json:"balances"`
	}
	if err := a.client().signed(ctx, hostSpot, "GET", "/api/v3/account", url.Values{"omitZeroBalances": {"true"}}, &acc); err != nil {
		return nil, err
	}
	var out []holding
	for _, b := range acc.Balances {
		h := holding{Account: "spot", Asset: b.Asset, Free: floatOf(b.Free), Locked: floatOf(b.Locked)}
		if h.Total() == 0 || isEarnShadow(b.Asset) {
			continue
		}
		h.USDT = valueUSDT(a.market, h.Asset, h.Total())
		out = append(out, h)
	}
	return out, nil
}

func (a *app) fundingHoldings(ctx context.Context) ([]holding, error) {
	var rows []struct {
		Asset  string `json:"asset"`
		Free   string `json:"free"`
		Locked string `json:"locked"`
		Freeze string `json:"freeze"`
	}
	if err := a.client().signed(ctx, hostSpot, "POST", "/sapi/v1/asset/get-funding-asset", nil, &rows); err != nil {
		return nil, err
	}
	var out []holding
	for _, r := range rows {
		h := holding{Account: "funding", Asset: r.Asset, Free: floatOf(r.Free), Locked: floatOf(r.Locked) + floatOf(r.Freeze)}
		if h.Total() == 0 {
			continue
		}
		h.USDT = valueUSDT(a.market, h.Asset, h.Total())
		out = append(out, h)
	}
	return out, nil
}

func (a *app) earnHoldings(ctx context.Context) ([]holding, error) {
	var out []holding
	for _, kind := range []string{"flexible", "locked"} {
		var page struct {
			Rows []struct {
				Asset       string `json:"asset"`
				TotalAmount string `json:"totalAmount"`
				Amount      string `json:"amount"`
			} `json:"rows"`
		}
		path := "/sapi/v1/simple-earn/" + kind + "/position"
		if err := a.client().signed(ctx, hostSpot, "GET", path, url.Values{"size": {"100"}}, &page); err != nil {
			return out, err
		}
		for _, r := range page.Rows {
			amt := floatOf(r.TotalAmount)
			if kind == "locked" {
				amt = floatOf(r.Amount)
			}
			if amt == 0 {
				continue
			}
			out = append(out, holding{Account: "earn", Asset: r.Asset, Locked: amt, USDT: valueUSDT(a.market, r.Asset, amt)})
		}
	}
	return out, nil
}

// futuresHoldings are a futures wallet's assets: margin balance (wallet + unrealized PnL) per asset.
func (a *app) futuresHoldings(ctx context.Context, mkt string) ([]holding, error) {
	path := "/fapi/v3/account"
	if mkt == marketCoinM {
		path = "/dapi/v1/account"
	}
	var acc struct {
		Assets []struct {
			Asset            string `json:"asset"`
			WalletBalance    string `json:"walletBalance"`
			UnrealizedProfit string `json:"unrealizedProfit"`
			MarginBalance    string `json:"marginBalance"`
		} `json:"assets"`
	}
	if err := a.client().signed(ctx, marketHost(mkt), "GET", path, nil, &acc); err != nil {
		return nil, err
	}
	var out []holding
	for _, s := range acc.Assets {
		bal := floatOf(s.MarginBalance)
		if bal == 0 {
			bal = floatOf(s.WalletBalance) + floatOf(s.UnrealizedProfit)
		}
		if bal == 0 {
			continue
		}
		out = append(out, holding{Account: mkt, Asset: s.Asset, Free: bal, USDT: valueUSDT(a.market, s.Asset, bal)})
	}
	return out, nil
}

// positions are the open positions of a futures market, with leverage.
func (a *app) readPositions(ctx context.Context, mkt string) ([]position, error) {
	var rows []struct {
		Symbol           string `json:"symbol"`
		PositionSide     string `json:"positionSide"`
		PositionAmt      string `json:"positionAmt"`
		EntryPrice       string `json:"entryPrice"`
		MarkPrice        string `json:"markPrice"`
		UnRealizedProfit string `json:"unRealizedProfit"`
		LiquidationPrice string `json:"liquidationPrice"`
		Leverage         string `json:"leverage"`
		Notional         string `json:"notional"`
	}
	path := "/fapi/v3/positionRisk"
	if mkt == marketCoinM {
		path = "/dapi/v1/positionRisk"
	}
	if err := a.client().signed(ctx, marketHost(mkt), "GET", path, nil, &rows); err != nil {
		return nil, err
	}
	var out []position
	for _, r := range rows {
		amt := floatOf(r.PositionAmt)
		if mkt == marketCoinM {
			if lev := int(floatOf(r.Leverage)); lev > 0 {
				a.mu.Lock()
				if a.leverage[marketCoinM] == nil {
					a.leverage[marketCoinM] = map[string]int{}
				}
				a.leverage[marketCoinM][r.Symbol] = lev
				a.mu.Unlock()
			}
		}
		if amt == 0 {
			continue
		}
		p := position{Market: mkt, Symbol: r.Symbol, Side: r.PositionSide, Amount: amt, Entry: floatOf(r.EntryPrice),
			Mark: floatOf(r.MarkPrice), PnL: floatOf(r.UnRealizedProfit), Liquidation: floatOf(r.LiquidationPrice),
			Leverage: int(floatOf(r.Leverage)), Notional: floatOf(r.Notional)}
		if mkt == marketCoinM {
			if rule := a.market.rule(marketCoinM, r.Symbol); rule != nil && rule.ContractSize > 0 {
				p.Notional = abs(amt) * rule.ContractSize
			}
		}
		if mkt == marketUSDM && p.Leverage == 0 {
			p.Leverage = a.leverageOf(r.Symbol)
		}
		out = append(out, p)
	}
	return out, nil
}

// refreshLeverage reads the USDⓈ-M leverage of every symbol (symbolConfig) into the cache.
func (a *app) refreshLeverage(ctx context.Context) error {
	var rows []struct {
		Symbol   string `json:"symbol"`
		Leverage int    `json:"leverage"`
	}
	if err := a.client().signed(ctx, hostUSDM, "GET", "/fapi/v1/symbolConfig", nil, &rows); err != nil {
		return err
	}
	lev := map[string]int{}
	for _, r := range rows {
		lev[r.Symbol] = r.Leverage
	}
	a.mu.Lock()
	a.leverage[marketUSDM] = lev
	a.mu.Unlock()
	return nil
}

func (a *app) leverageOf(symbol string) int {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.leverage[marketUSDM][symbol]
}

// refreshDual reads whether a futures market is in hedge (dual side) mode.
func (a *app) refreshDual(ctx context.Context, mkt string) error {
	var r struct {
		DualSidePosition bool `json:"dualSidePosition"`
	}
	path := "/fapi/v1/positionSide/dual"
	if mkt == marketCoinM {
		path = "/dapi/v1/positionSide/dual"
	}
	if err := a.client().signed(ctx, marketHost(mkt), "GET", path, nil, &r); err != nil {
		return err
	}
	a.mu.Lock()
	a.dual[mkt] = r.DualSidePosition
	a.mu.Unlock()
	return nil
}

func abs(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}
