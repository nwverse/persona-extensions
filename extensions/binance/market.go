package main

import (
	"context"
	"encoding/json"
	"math/big"
	"net/url"
	"sort"
	"sync"
	"time"
)

// symbolRules are an order's exchange rules for one symbol (exchangeInfo filters).
type symbolRules struct {
	Market       string
	Symbol       string
	Status       string
	Base, Quote  string
	TickSize     *big.Rat
	MinPrice     *big.Rat
	MaxPrice     *big.Rat
	StepSize     *big.Rat
	MinQty       *big.Rat
	MaxQty       *big.Rat
	MarketStep   *big.Rat
	MarketMaxQty *big.Rat
	MinNotional  *big.Rat
	ContractSize float64 // coinm: USD per contract
	OrderTypes   map[string]bool
}

// market is what the extension knows of prices and symbols, refreshed in the background; actions.describe reads only
// this (it must not reach the network).
type market struct {
	mu       sync.RWMutex
	prices   map[string]float64 // spot last price by symbol
	pricesAt time.Time
	marks    map[string]map[string]float64 // usdm / coinm mark price by symbol
	marksAt  map[string]time.Time
	rules    map[string]map[string]*symbolRules // by market, symbol
	rulesAt  map[string]time.Time
	history  map[string][]pricePoint // spot symbol → last two hours
}

type pricePoint struct {
	At    time.Time
	Price float64
}

func newMarket() *market {
	return &market{prices: map[string]float64{}, marks: map[string]map[string]float64{}, marksAt: map[string]time.Time{},
		rules: map[string]map[string]*symbolRules{}, rulesAt: map[string]time.Time{}, history: map[string][]pricePoint{}}
}

// staleAfter: describe refuses an order on prices older than this.
const staleAfter = 2 * time.Minute

// price is a market's current reference price of symbol: spot last price, futures mark price.
func (m *market) price(mkt, symbol string, now time.Time) (float64, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if mkt == marketSpot {
		p, ok := m.prices[symbol]
		return p, ok && p > 0 && now.Sub(m.pricesAt) < staleAfter
	}
	p, ok := m.marks[mkt][symbol]
	return p, ok && p > 0 && now.Sub(m.marksAt[mkt]) < staleAfter
}

func (m *market) spotPrice(symbol string) float64 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.prices[symbol]
}

func (m *market) rule(mkt, symbol string) *symbolRules {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.rules[mkt][symbol]
}

// refreshPrices reads every spot price (one call) and keeps the history for price moves.
func (m *market) refreshPrices(ctx context.Context, c *client, now time.Time) error {
	var rows []struct {
		Symbol string `json:"symbol"`
		Price  string `json:"price"`
	}
	if err := c.public(ctx, hostPublic, "/api/v3/ticker/price", nil, &rows); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, r := range rows {
		p := floatOf(r.Price)
		m.prices[r.Symbol] = p
	}
	m.pricesAt = now
	return nil
}

// track adds now's price of symbols to the history (two hours kept).
func (m *market) track(symbols []string, now time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, s := range symbols {
		p := m.prices[s]
		if p <= 0 {
			continue
		}
		h := append(m.history[s], pricePoint{now, p})
		for len(h) > 0 && now.Sub(h[0].At) > 2*time.Hour {
			h = h[1:]
		}
		m.history[s] = h
	}
}

// change is symbol's move over the last window: (now - oldest in window) / oldest; ok false without enough history.
func (m *market) change(symbol string, window time.Duration, now time.Time) (float64, float64, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	h := m.history[symbol]
	if len(h) < 2 {
		return 0, 0, false
	}
	i := sort.Search(len(h), func(i int) bool { return now.Sub(h[i].At) <= window })
	if i >= len(h)-1 || now.Sub(h[0].At) < window*3/4 {
		return 0, 0, false
	}
	first, last := h[i].Price, h[len(h)-1].Price
	return (last - first) / first, last, true
}

// refreshMarks reads every mark price of a futures market.
func (m *market) refreshMarks(ctx context.Context, c *client, mkt string, now time.Time) error {
	path := "/fapi/v1/premiumIndex"
	if mkt == marketCoinM {
		path = "/dapi/v1/premiumIndex"
	}
	var rows []struct {
		Symbol    string `json:"symbol"`
		MarkPrice string `json:"markPrice"`
	}
	if err := c.public(ctx, marketHost(mkt), path, nil, &rows); err != nil {
		return err
	}
	marks := map[string]float64{}
	for _, r := range rows {
		marks[r.Symbol] = floatOf(r.MarkPrice)
	}
	m.mu.Lock()
	m.marks[mkt], m.marksAt[mkt] = marks, now
	m.mu.Unlock()
	return nil
}

type exchangeInfo struct {
	Symbols []struct {
		Symbol         string            `json:"symbol"`
		Status         string            `json:"status"`
		ContractStatus string            `json:"contractStatus"`
		BaseAsset      string            `json:"baseAsset"`
		QuoteAsset     string            `json:"quoteAsset"`
		ContractSize   float64           `json:"contractSize"`
		OrderTypes     []string          `json:"orderTypes"`
		Filters        []json.RawMessage `json:"filters"`
	} `json:"symbols"`
}

type filter struct {
	FilterType  string `json:"filterType"`
	TickSize    string `json:"tickSize"`
	MinPrice    string `json:"minPrice"`
	MaxPrice    string `json:"maxPrice"`
	StepSize    string `json:"stepSize"`
	MinQty      string `json:"minQty"`
	MaxQty      string `json:"maxQty"`
	MinNotional string `json:"minNotional"`
	Notional    string `json:"notional"`
}

// refreshRules reads a market's exchangeInfo (spot: only symbols, as the full list is large).
func (m *market) refreshRules(ctx context.Context, c *client, mkt string, symbols []string, now time.Time) error {
	var info exchangeInfo
	var err error
	switch mkt {
	case marketSpot:
		if len(symbols) == 0 {
			return nil
		}
		b, _ := json.Marshal(symbols)
		err = c.public(ctx, hostPublic, "/api/v3/exchangeInfo", url.Values{"symbols": {string(b)}}, &info)
	case marketUSDM:
		err = c.public(ctx, hostUSDM, "/fapi/v1/exchangeInfo", nil, &info)
	case marketCoinM:
		err = c.public(ctx, hostCoinM, "/dapi/v1/exchangeInfo", nil, &info)
	}
	if err != nil {
		return err
	}
	out := map[string]*symbolRules{}
	for _, s := range info.Symbols {
		r := &symbolRules{Market: mkt, Symbol: s.Symbol, Status: s.Status, Base: s.BaseAsset, Quote: s.QuoteAsset,
			ContractSize: s.ContractSize, OrderTypes: map[string]bool{}}
		if r.Status == "" {
			r.Status = s.ContractStatus
		}
		for _, t := range s.OrderTypes {
			r.OrderTypes[t] = true
		}
		for _, raw := range s.Filters {
			var f filter
			if json.Unmarshal(raw, &f) != nil {
				continue
			}
			switch f.FilterType {
			case "PRICE_FILTER":
				r.TickSize, r.MinPrice, r.MaxPrice = ratOf(f.TickSize), ratOf(f.MinPrice), ratOf(f.MaxPrice)
			case "LOT_SIZE":
				r.StepSize, r.MinQty, r.MaxQty = ratOf(f.StepSize), ratOf(f.MinQty), ratOf(f.MaxQty)
			case "MARKET_LOT_SIZE":
				r.MarketStep, r.MarketMaxQty = ratOf(f.StepSize), ratOf(f.MaxQty)
			case "MIN_NOTIONAL", "NOTIONAL":
				if v := ratOf(f.MinNotional); v != nil {
					r.MinNotional = v
				} else if v := ratOf(f.Notional); v != nil {
					r.MinNotional = v
				}
			}
		}
		out[s.Symbol] = r
	}
	m.mu.Lock()
	m.rules[mkt], m.rulesAt[mkt] = out, now
	m.mu.Unlock()
	return nil
}

func (m *market) rulesAge(mkt string, now time.Time) time.Duration {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.rulesAt[mkt].IsZero() {
		return time.Duration(1 << 62)
	}
	return now.Sub(m.rulesAt[mkt])
}
