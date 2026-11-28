package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

func (a *app) callTool(ctx context.Context, name string, raw json.RawMessage) (string, error) {
	switch name {
	case "binance_price":
		return a.toolPrice(ctx, raw)
	case "binance_limits":
		return a.toolLimits(raw)
	case "binance_status":
		return a.toolStatus(raw)
	}
	if a.client().signer == nil {
		return "", errNoKey
	}
	switch name {
	case "binance_overview":
		return a.toolOverview(ctx, raw)
	case "binance_balances":
		return a.toolBalances(ctx, raw)
	case "binance_positions":
		return a.toolPositions(ctx, raw)
	case "binance_open_orders":
		return a.toolOpenOrders(ctx, raw)
	case "binance_order":
		return a.toolOrder(ctx, raw)
	case "binance_trades":
		return a.toolTrades(ctx, raw)
	case "binance_income":
		return a.toolIncome(ctx, raw)
	case "binance_transfers":
		return a.toolTransfers(ctx, raw)
	}
	return "", fmt.Errorf("unknown tool %q", name)
}

func (a *app) toolPrice(ctx context.Context, raw json.RawMessage) (string, error) {
	var in struct {
		Symbols []string `json:"symbols"`
	}
	if err := decodeArgs(raw, &in); err != nil {
		return "", err
	}
	if len(in.Symbols) == 0 || len(in.Symbols) > 20 {
		return "", errors.New("symbols: 1 to 20")
	}
	for i, s := range in.Symbols {
		in.Symbols[i] = strings.ToUpper(strings.TrimSpace(s))
		if !symbolRE.MatchString(in.Symbols[i]) {
			return "", fmt.Errorf("%q is not a symbol", s)
		}
	}
	b, _ := json.Marshal(in.Symbols)
	var rows []struct {
		Symbol             string `json:"symbol"`
		LastPrice          string `json:"lastPrice"`
		PriceChangePercent string `json:"priceChangePercent"`
		HighPrice          string `json:"highPrice"`
		LowPrice           string `json:"lowPrice"`
		QuoteVolume        string `json:"quoteVolume"`
	}
	if err := a.client().public(ctx, hostPublic, "/api/v3/ticker/24hr", url.Values{"symbols": {string(b)}}, &rows); err != nil {
		return "", err
	}
	var out strings.Builder
	out.WriteString("symbol | last | 24h change | 24h high | 24h low | 24h quote volume\n")
	for _, r := range rows {
		fmt.Fprintf(&out, "%s | %s | %s%% | %s | %s | %s\n", r.Symbol, fmtNum(floatOf(r.LastPrice)), r.PriceChangePercent,
			fmtNum(floatOf(r.HighPrice)), fmtNum(floatOf(r.LowPrice)), fmtNum(floatOf(r.QuoteVolume)))
	}
	return out.String(), nil
}

func (a *app) toolLimits(raw json.RawMessage) (string, error) {
	if err := decodeArgs(raw, &struct{}{}); err != nil {
		return "", err
	}
	env := a.env()
	c := env.cfg
	wl := make([]string, 0, len(c.allowed))
	for s := range c.allowed {
		wl = append(wl, s)
	}
	sort.Strings(wl)
	var b strings.Builder
	fmt.Fprintf(&b, "trading_enabled: %v\npaper_trading: %v\nmarkets: %s\nsymbol_whitelist: %s\n", c.TradingEnabled, c.PaperTrading, strings.Join(c.Markets, ", "), orNone(strings.Join(wl, ", ")))
	fmt.Fprintf(&b, "max_order_usdt: %g\nmax_daily_usdt: %g (used today %s, left %s; day %s %s)\n", c.MaxOrderUSDT, c.MaxDailyUSDT,
		fmtNum(env.usedUSDT), fmtNum(math.Max(0, c.MaxDailyUSDT-env.usedUSDT)), c.dayKey(env.now), c.Timezone)
	fmt.Fprintf(&b, "max_orders_per_day: %d (placed today %d)\nmax_leverage: %dx\nlimit_deviation_pct: %g\nmarket_orders: %v\nmarket_slippage_pct: %g\n",
		c.MaxOrdersPerDay, env.ordersDone, c.MaxLeverage, c.LimitDeviationPct, c.MarketOrders, c.MarketSlippagePct)
	if env.configErr != nil {
		fmt.Fprintf(&b, "invalid settings: %s\n", env.configErr)
	} else if env.locked != "" && (!c.PaperTrading || c.marketOK[marketUSDM] || c.marketOK[marketCoinM]) {
		if c.PaperTrading {
			fmt.Fprintf(&b, "futures writes locked: %s\n", env.locked)
		} else {
			fmt.Fprintf(&b, "writes locked: %s\n", env.locked)
		}
	}
	b.WriteString("Change these only in Settings › Extensions › 币安.\n")
	return b.String(), nil
}

func orNone(s string) string {
	if s == "" {
		return "(none: no symbol may be traded)"
	}
	return s
}

func (a *app) toolStatus(raw json.RawMessage) (string, error) {
	if err := decodeArgs(raw, &struct{}{}); err != nil {
		return "", err
	}
	a.mu.RLock()
	p, locked, signed, keyErr, cfgErr := a.perms, a.locked, a.cl.signer != nil, a.keyErr, a.cfgErr
	proxy := "none (hub egress)"
	if a.cfg.proxy != nil {
		proxy = a.cfg.proxy.String()
	}
	a.mu.RUnlock()
	var b strings.Builder
	fmt.Fprintf(&b, "key set: %v\n", signed)
	if keyErr != nil {
		fmt.Fprintf(&b, "key error: %v\n", keyErr)
	}
	if cfgErr != nil {
		fmt.Fprintf(&b, "settings error: %v\n", cfgErr)
	}
	fmt.Fprintf(&b, "permissions checked: %v; spot trading: %v; futures: %v; IP whitelist: %v\n", p.Checked, p.Spot, p.Futures, p.IP)
	if locked != "" {
		fmt.Fprintf(&b, "writes locked: %s\n", locked)
	} else {
		b.WriteString("writes: allowed by the key (the settings' guards still apply)\n")
	}
	fmt.Fprintf(&b, "proxy: %s\n", proxy)
	for host, w := range a.client().usedWeight() {
		fmt.Fprintf(&b, "used weight 1m %s: %s\n", strings.TrimPrefix(host, "https://"), w)
	}
	return b.String(), nil
}

// allHoldings reads every account; an account that fails is reported, the rest still count.
func (a *app) allHoldings(ctx context.Context) ([]holding, []string) {
	var out []holding
	var problems []string
	add := func(name string, hs []holding, err error) {
		if err != nil {
			problems = append(problems, name+": "+clip(err.Error(), 120))
		}
		out = append(out, hs...)
	}
	hs, err := a.spotHoldings(ctx)
	add("spot", hs, err)
	hs, err = a.fundingHoldings(ctx)
	add("funding", hs, err)
	hs, err = a.earnHoldings(ctx)
	add("earn", hs, err)
	a.mu.RLock()
	futures := a.perms.Futures || !a.perms.Checked
	a.mu.RUnlock()
	if futures {
		hs, err = a.futuresHoldings(ctx, marketUSDM)
		add("usdm", hs, err)
		hs, err = a.futuresHoldings(ctx, marketCoinM)
		add("coinm", hs, err)
	}
	return out, problems
}

func (a *app) toolOverview(ctx context.Context, raw json.RawMessage) (string, error) {
	if err := decodeArgs(raw, &struct{}{}); err != nil {
		return "", err
	}
	if err := a.ensurePrices(ctx); err != nil {
		return "", err
	}
	hs, problems := a.allHoldings(ctx)
	if len(hs) == 0 && len(problems) > 0 {
		return "", errors.New(strings.Join(problems, "; "))
	}
	byAcc := map[string]float64{}
	byAsset := map[string]float64{}
	total := 0.0
	for _, h := range hs {
		byAcc[h.Account] += h.USDT
		byAsset[h.Asset] += h.USDT
		total += h.USDT
	}
	a.w.mu.Lock()
	a.w.holdings = byAsset
	a.w.mu.Unlock()
	now := a.now()
	env := a.env()
	today, yesterday := env.cfg.dayKey(now), env.cfg.dayKey(now.Add(-24*time.Hour))
	snap := a.ledger.snapshot().Snap
	_ = a.ledger.update(func(s *ledgerState) error {
		if s.Snap == nil {
			s.Snap = map[string]float64{}
		}
		if _, ok := s.Snap[today]; !ok {
			s.Snap[today] = total
		}
		return nil
	})
	var b strings.Builder
	fmt.Fprintf(&b, "Total ≈ %s USDT", fmtNum(total))
	if y, ok := snap[yesterday]; ok && y > 0 {
		fmt.Fprintf(&b, " (%+.2f%% vs first reading yesterday, %s)", (total-y)/y*100, fmtNum(y))
	}
	b.WriteString("\nBy account: ")
	var parts []string
	for _, acc := range []string{"spot", "funding", "earn", "usdm", "coinm"} {
		if v, ok := byAcc[acc]; ok {
			parts = append(parts, fmt.Sprintf("%s %s", acc, fmtNum(v)))
		}
	}
	b.WriteString(strings.Join(parts, " · "))
	type kv struct {
		k string
		v float64
	}
	var top []kv
	for k, v := range byAsset {
		top = append(top, kv{k, v})
	}
	sort.Slice(top, func(i, j int) bool { return top[i].v > top[j].v })
	b.WriteString("\nLargest: ")
	parts = nil
	for i, t := range top {
		if i == 8 || t.v < 1 {
			break
		}
		pct := 0.0
		if total > 0 {
			pct = t.v / total * 100
		}
		parts = append(parts, fmt.Sprintf("%s %s (%.1f%%)", t.k, fmtNum(t.v), pct))
	}
	b.WriteString(strings.Join(parts, " · "))
	fmt.Fprintf(&b, "\nOrders through Persona today: %d, allowance used %s / %g USDT", env.ordersDone, fmtNum(env.usedUSDT), env.cfg.MaxDailyUSDT)
	if len(problems) > 0 {
		b.WriteString("\nNot read: " + strings.Join(problems, "; "))
	}
	return b.String(), nil
}

// ensurePrices reads the spot prices when the cache is older than a minute.
func (a *app) ensurePrices(ctx context.Context) error {
	a.market.mu.RLock()
	fresh := a.now().Sub(a.market.pricesAt) < time.Minute
	a.market.mu.RUnlock()
	if fresh {
		return nil
	}
	return a.market.refreshPrices(ctx, a.client(), a.now())
}

func (a *app) toolBalances(ctx context.Context, raw json.RawMessage) (string, error) {
	var in struct {
		Account string   `json:"account"`
		MinUSDT *float64 `json:"min_usdt"`
	}
	if err := decodeArgs(raw, &in); err != nil {
		return "", err
	}
	min := 1.0
	if in.MinUSDT != nil {
		min = *in.MinUSDT
	}
	if err := a.ensurePrices(ctx); err != nil {
		return "", err
	}
	var hs []holding
	var problems []string
	var err error
	switch in.Account {
	case "":
		hs, problems = a.allHoldings(ctx)
	case "spot":
		hs, err = a.spotHoldings(ctx)
	case "funding":
		hs, err = a.fundingHoldings(ctx)
	case "earn":
		hs, err = a.earnHoldings(ctx)
	case marketUSDM, marketCoinM:
		hs, err = a.futuresHoldings(ctx, in.Account)
	default:
		return "", errors.New("account: spot, funding, earn, usdm or coinm")
	}
	if err != nil {
		return "", err
	}
	sort.Slice(hs, func(i, j int) bool { return hs[i].USDT > hs[j].USDT })
	var b strings.Builder
	b.WriteString("account | asset | free | locked | ≈USDT\n")
	hidden := 0
	for _, h := range hs {
		if h.USDT < min && !(h.USDT == 0 && h.Total() > 0 && min == 0) {
			hidden++
			continue
		}
		fmt.Fprintf(&b, "%s | %s | %s | %s | %s\n", h.Account, h.Asset, fmtNum(h.Free), fmtNum(h.Locked), fmtNum(h.USDT))
	}
	if hidden > 0 {
		fmt.Fprintf(&b, "(%d assets under %g USDT hidden)\n", hidden, min)
	}
	if len(problems) > 0 {
		b.WriteString("Not read: " + strings.Join(problems, "; ") + "\n")
	}
	return b.String(), nil
}

func (a *app) toolPositions(ctx context.Context, raw json.RawMessage) (string, error) {
	var in struct {
		Market string `json:"market"`
	}
	if err := decodeArgs(raw, &in); err != nil {
		return "", err
	}
	markets := []string{marketUSDM, marketCoinM}
	if in.Market != "" {
		if in.Market != marketUSDM && in.Market != marketCoinM {
			return "", errors.New("market: usdm or coinm")
		}
		markets = []string{in.Market}
	}
	var b strings.Builder
	b.WriteString("market | symbol | side | size | entry | mark | unrealized PnL | leverage | liquidation | distance\n")
	n := 0
	for _, m := range markets {
		ps, err := a.readPositions(ctx, m)
		if err != nil {
			fmt.Fprintf(&b, "%s: not read: %s\n", m, clip(err.Error(), 160))
			continue
		}
		a.watchPositions(m, ps, a.now())
		for _, p := range ps {
			n++
			dist := "-"
			if d := p.LiqDistance(); d > 0 {
				dist = fmt.Sprintf("%.1f%%", d*100)
			}
			fmt.Fprintf(&b, "%s | %s | %s | %s | %s | %s | %s | %dx | %s | %s\n", m, p.Symbol, p.Direction(), fmtNum(p.Amount),
				fmtNum(p.Entry), fmtNum(p.Mark), fmtNum(p.PnL), p.Leverage, fmtNum(p.Liquidation), dist)
		}
	}
	if n == 0 {
		b.WriteString("(no open positions)\n")
	}
	return b.String(), nil
}

func marketArg(m string) (string, error) {
	switch m {
	case marketSpot, marketUSDM, marketCoinM:
		return m, nil
	}
	return "", errors.New("market: spot, usdm or coinm")
}

func (a *app) toolOpenOrders(ctx context.Context, raw json.RawMessage) (string, error) {
	var in struct {
		Market string `json:"market"`
		Symbol string `json:"symbol"`
	}
	if err := decodeArgs(raw, &in); err != nil {
		return "", err
	}
	mkt, err := marketArg(in.Market)
	if err != nil {
		return "", err
	}
	q := url.Values{}
	if in.Symbol != "" {
		s := strings.ToUpper(in.Symbol)
		if !symbolRE.MatchString(s) {
			return "", errors.New("symbol is not a symbol")
		}
		q.Set("symbol", s)
	}
	path := map[string]string{marketSpot: "/api/v3/openOrders", marketUSDM: "/fapi/v1/openOrders", marketCoinM: "/dapi/v1/openOrders"}[mkt]
	var rows []struct {
		orderStatus
		Symbol string `json:"symbol"`
		Time   int64  `json:"time"`
		TIF    string `json:"timeInForce"`
		PSide  string `json:"positionSide"`
	}
	if err := a.client().signed(ctx, marketHost(mkt), "GET", path, q, &rows); err != nil {
		return "", err
	}
	if len(rows) == 0 {
		return "(no open orders)", nil
	}
	var b strings.Builder
	b.WriteString("symbol | order id | client id | side | type | price | qty | filled | status | placed\n")
	for _, o := range rows {
		fmt.Fprintf(&b, "%s | %d | %s | %s | %s | %s | %s | %s | %s | %s\n", o.Symbol, o.OrderID, o.ClientOrderID, o.Side, o.Type,
			o.Price, o.OrigQty, o.ExecutedQty, o.Status, time.UnixMilli(o.Time).UTC().Format(time.RFC3339))
	}
	return b.String(), nil
}

func (a *app) toolOrder(ctx context.Context, raw json.RawMessage) (string, error) {
	var in struct {
		Market   string `json:"market"`
		Symbol   string `json:"symbol"`
		OrderID  int64  `json:"order_id"`
		ClientID string `json:"client_id"`
	}
	if err := decodeArgs(raw, &in); err != nil {
		return "", err
	}
	mkt, err := marketArg(in.Market)
	if err != nil {
		return "", err
	}
	in.Symbol = strings.ToUpper(in.Symbol)
	if !symbolRE.MatchString(in.Symbol) || (in.OrderID > 0) == (in.ClientID != "") || len(in.ClientID) > 36 {
		return "", errors.New("symbol, and order_id or client_id")
	}
	st, err := a.queryOrder(ctx, mkt, in.Symbol, in.OrderID, in.ClientID)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s %s order %d (client %s): %s %s, price %s, qty %s, filled %s, status %s", mkt, in.Symbol, st.OrderID,
		st.ClientOrderID, st.Side, st.Type, st.Price, st.OrigQty, st.ExecutedQty, st.Status), nil
}

func daysArg(d, def, max int) int {
	if d <= 0 {
		return def
	}
	if d > max {
		return max
	}
	return d
}

func ms(t time.Time) string { return strconv.FormatInt(t.UnixMilli(), 10) }
