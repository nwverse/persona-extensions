package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"
	_ "time/tzdata" // the sandbox may have no zoneinfo

	"github.com/nwverse/persona-extensions/pluginkit"
)

// config is the extension's settings (persona-extension.json "configuration"); the hub fills in the defaults.
type config struct {
	ProxyURL          string   `json:"proxy_url"`
	TradingEnabled    bool     `json:"trading_enabled"`
	PaperTrading      bool     `json:"paper_trading"`
	Markets           []string `json:"markets"`
	SymbolWhitelist   []string `json:"symbol_whitelist"`
	MaxOrderUSDT      float64  `json:"max_order_usdt"`
	MaxDailyUSDT      float64  `json:"max_daily_usdt"`
	MaxOrdersPerDay   int      `json:"max_orders_per_day"`
	MaxLeverage       int      `json:"max_leverage"`
	LimitDeviationPct float64  `json:"limit_deviation_pct"`
	MarketOrders      bool     `json:"market_orders"`
	MarketSlippagePct float64  `json:"market_slippage_pct"`
	Timezone          string   `json:"timezone"`
	PriceMovePct      float64  `json:"price_move_pct"`
	AlertMinUSDT      float64  `json:"alert_min_usdt"`
	LiqWarnPct        float64  `json:"liq_warn_pct"`
	WatchSymbols      []string `json:"watch_symbols"`

	proxy    *url.URL
	loc      *time.Location
	rules    string
	allowed  map[string]bool
	marketOK map[string]bool
}

// defaultConfig mirrors the manifest's defaults, for a hub that hands over an empty configuration (tests, dev).
func defaultConfig() config {
	return config{Markets: []string{"spot"}, MaxOrderUSDT: 200, MaxDailyUSDT: 1000, MaxOrdersPerDay: 20, MaxLeverage: 5,
		LimitDeviationPct: 5, MarketOrders: true, MarketSlippagePct: 1, Timezone: "Asia/Shanghai", PriceMovePct: 5,
		AlertMinUSDT: 100, LiqWarnPct: 15}
}

var symbolRE = regexp.MustCompile(`^[A-Z0-9_]{4,24}$`)

// parseConfig reads raw over the defaults and checks it.
func parseConfig(raw json.RawMessage) (config, error) {
	c := defaultConfig()
	if len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &c); err != nil {
			return c, errors.New("settings are not valid JSON")
		}
	}
	var err error
	if c.proxy, err = parseProxyURL(c.ProxyURL); err != nil {
		return c, err
	}
	if c.Timezone == "" {
		c.Timezone = "UTC"
	}
	if c.loc, err = time.LoadLocation(c.Timezone); err != nil {
		return c, fmt.Errorf("timezone %q is not an IANA time zone", c.Timezone)
	}
	c.allowed = map[string]bool{}
	for _, s := range c.SymbolWhitelist {
		s = strings.ToUpper(strings.TrimSpace(s))
		if !symbolRE.MatchString(s) {
			return c, fmt.Errorf("symbol_whitelist: %q is not a symbol", s)
		}
		c.allowed[s] = true
	}
	c.marketOK = map[string]bool{}
	for _, m := range c.Markets {
		switch m {
		case marketSpot, marketUSDM, marketCoinM:
			c.marketOK[m] = true
		default:
			return c, fmt.Errorf("markets: %q is not spot, usdm or coinm", m)
		}
	}
	switch {
	case c.MaxOrderUSDT < 0, c.MaxDailyUSDT < 0, c.MaxOrdersPerDay < 0:
		return c, errors.New("limits must not be negative")
	case c.MaxLeverage < 1:
		return c, errors.New("max_leverage must be at least 1")
	case c.LimitDeviationPct <= 0, c.MarketSlippagePct <= 0:
		return c, errors.New("limit_deviation_pct and market_slippage_pct must be positive")
	}
	if c.PriceMovePct <= 0 {
		c.PriceMovePct = 5
	}
	if c.LiqWarnPct <= 0 {
		c.LiqWarnPct = 15
	}
	c.rules = pluginkit.TriageRules(raw)
	return c, nil
}

// dayKey is the guard day of t in the configured time zone.
func (c *config) dayKey(t time.Time) string { return t.In(c.loc).Format("2006-01-02") }
