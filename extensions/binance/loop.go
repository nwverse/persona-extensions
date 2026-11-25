package main

import (
	"context"
	"errors"
	"log"
	"sort"
	"time"
)

// loop keeps the caches fresh and watches for inbox events until ctx ends.
func (a *app) loop(ctx context.Context) {
	tick := time.NewTicker(15 * time.Second)
	defer tick.Stop()
	var last = map[string]time.Time{}
	due := func(name string, every time.Duration, now time.Time) bool {
		if now.Sub(last[name]) < every {
			return false
		}
		last[name] = now
		return true
	}
	for {
		now := a.now()
		a.round(ctx, now, due)
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

func (a *app) round(ctx context.Context, now time.Time, due func(string, time.Duration, time.Time) bool) {
	a.mu.RLock()
	cfg := a.cfg
	signed := a.cl.signer != nil
	perms := a.perms
	a.mu.RUnlock()
	rctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	c := a.client()

	if due("prices", 55*time.Second, now) {
		a.record(a.market.refreshPrices(rctx, c, now))
		a.market.track(a.watchSet(), now)
		for _, m := range []string{marketUSDM, marketCoinM} {
			if cfg.marketOK[m] || perms.Futures {
				a.record(a.market.refreshMarks(rctx, c, m, now))
			}
		}
		a.checkPriceMoves(now)
		a.logPublicOnce()
	}
	if due("rules", time.Hour, now) || a.market.rulesAge(marketSpot, now) > 2*time.Hour {
		a.record(a.market.refreshRules(rctx, c, marketSpot, spotSymbols(cfg), now))
	}
	for _, m := range []string{marketUSDM, marketCoinM} {
		if (cfg.marketOK[m] || perms.Futures) && (due("rules-"+m, time.Hour, now) || a.market.rulesAge(m, now) > 2*time.Hour) {
			a.record(a.market.refreshRules(rctx, c, m, nil, now))
		}
	}
	if !signed {
		return
	}
	if due("key", time.Hour, now) || (!perms.Checked && due("key-retry", 2*time.Minute, now)) {
		a.refreshKey(rctx, now)
		a.mu.RLock()
		perms = a.perms
		a.mu.RUnlock()
	}
	if !perms.Checked {
		return
	}
	if perms.Futures {
		if due("futures-mode", 10*time.Minute, now) {
			a.record(a.refreshDual(rctx, marketUSDM))
			a.record(a.refreshDual(rctx, marketCoinM))
			a.record(a.refreshLeverage(rctx))
		}
		if due("positions", 55*time.Second, now) {
			for _, m := range []string{marketUSDM, marketCoinM} {
				ps, err := a.readPositions(rctx, m)
				a.record(err)
				if err == nil {
					a.watchPositions(m, ps, now)
				}
			}
		}
	}
	if due("orders", 30*time.Second, now) {
		a.followOrders(rctx, now)
	}
	if due("deposits", 10*time.Minute, now) {
		a.watchDeposits(rctx, now)
	}
	if due("withdrawals", 30*time.Minute, now) {
		a.watchWithdrawals(rctx, now)
	}
}

// refreshKey checks the key's permissions; a problem locks every write.
func (a *app) refreshKey(ctx context.Context, now time.Time) {
	perms, err := checkKey(ctx, a.client())
	a.mu.Lock()
	prevLocked, prevChecked := a.locked, a.perms.Checked
	switch {
	case err == nil:
		a.perms, a.locked = perms, ""
	case perms.Checked: // Binance answered: the key is not acceptable
		a.perms, a.locked = perms, err.Error()
	default: // not known: reads go on, writes wait
		a.locked = "查不到 API Key 的权限：" + err.Error()
	}
	locked := a.locked
	a.mu.Unlock()
	if err != nil {
		log.Printf("binance: key check: %v", err)
	}
	if locked != "" && perms.Checked && locked != prevLocked {
		a.raise(event{Thread: "key", Kind: "key_problem", Tier: "action", Title: "币安 API Key 有问题，已停止下单",
			Body: locked, Push: "time-sensitive", At: now})
	}
	_ = prevChecked
}

// record keeps the last error and success for status.
func (a *app) record(err error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err == nil {
		a.lastOK, a.lastErr = a.now(), nil
		return
	}
	if errors.Is(err, context.Canceled) {
		return
	}
	a.lastErr = err
}

// spotSymbols are the spot symbols whose rules are kept: the whitelist and the watch list.
func spotSymbols(cfg config) []string {
	seen := map[string]bool{}
	var out []string
	for s := range cfg.allowed {
		if !seen[s] && !isFuturesSymbol(s) {
			seen[s] = true
			out = append(out, s)
		}
	}
	for _, s := range cfg.WatchSymbols {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}

// isFuturesSymbol: COIN-M symbols have an underscore (BTCUSD_PERP); spot ones never do.
func isFuturesSymbol(s string) bool {
	for _, r := range s {
		if r == '_' {
			return true
		}
	}
	return false
}

// logPublicOnce says once, in the hub's log, that public market data came through (no account data).
func (a *app) logPublicOnce() {
	a.market.mu.RLock()
	n, btc := len(a.market.prices), a.market.prices["BTCUSDT"]
	marks := len(a.market.marks[marketUSDM]) + len(a.market.marks[marketCoinM])
	a.market.mu.RUnlock()
	if n == 0 || a.w.loggedPublic.Swap(true) {
		return
	}
	log.Printf("binance: public market data ok: %d spot prices (BTCUSDT %s), %d futures mark prices", n, fmtNum(btc), marks)
}
