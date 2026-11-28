package main

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// watchState is what the watchers remember between rounds (memory only: a restart starts quietly again).
type watchState struct {
	mu        sync.Mutex
	liqLevel  map[string]int                // position key → warning level reached (1..3)
	prevPos   map[string]map[string]float64 // market → position key → size
	moved     map[string]time.Time          // symbol:dir → last price move told
	holdings  map[string]float64            // asset → USDT value, from the last overview
	deposits  map[string]bool
	withdraws map[string]bool
	seededDep bool
	seededWd  bool

	loggedPublic atomic.Bool
}

func newWatchState() *watchState {
	return &watchState{liqLevel: map[string]int{}, prevPos: map[string]map[string]float64{}, moved: map[string]time.Time{},
		holdings: map[string]float64{}, deposits: map[string]bool{}, withdraws: map[string]bool{}}
}

// watchSet is the spot symbols watched for price moves: holdings worth at least alert_min_usdt, plus watch_symbols.
func (a *app) watchSet() []string {
	a.mu.RLock()
	cfg := a.cfg
	a.mu.RUnlock()
	a.w.mu.Lock()
	defer a.w.mu.Unlock()
	seen := map[string]bool{}
	var out []string
	for asset, v := range a.w.holdings {
		if v >= cfg.AlertMinUSDT && !stableUSD[asset] {
			if s := asset + "USDT"; !seen[s] && a.market.spotPrice(s) > 0 {
				seen[s] = true
				out = append(out, s)
			}
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

// checkPriceMoves raises price_move for a watched symbol that moved price_move_pct within 60 minutes.
func (a *app) checkPriceMoves(now time.Time) {
	a.mu.RLock()
	pct := a.cfg.PriceMovePct
	a.mu.RUnlock()
	for _, s := range a.watchSet() {
		chg, last, ok := a.market.change(s, time.Hour, now)
		if !ok || math.Abs(chg)*100 < pct {
			continue
		}
		dir := "up"
		if chg < 0 {
			dir = "down"
		}
		key := s + ":" + dir
		a.w.mu.Lock()
		recent := now.Sub(a.w.moved[key]) < time.Hour
		if !recent {
			a.w.moved[key] = now
		}
		a.w.mu.Unlock()
		if recent {
			continue
		}
		e := event{Thread: "price:" + s, Kind: "price_move", Tier: "fyi", At: now,
			Title: fmt.Sprintf("%s 1 小时 %+.1f%%", s, chg*100), Body: fmt.Sprintf("现价 %s。", fmtNum(last))}
		if math.Abs(chg)*100 >= 2*pct {
			e.Tier, e.Push = "action", "active"
		}
		a.raise(e)
	}
}

// watchPositions caches positions, warns near liquidation and tells a change that was not the extension's order.
func (a *app) watchPositions(mkt string, ps []position, now time.Time) {
	a.mu.Lock()
	a.positions[mkt], a.posAt[mkt] = ps, now
	for _, p := range ps {
		if mkt == marketCoinM && p.Leverage > 0 {
			if a.leverage[marketCoinM] == nil {
				a.leverage[marketCoinM] = map[string]int{}
			}
			a.leverage[marketCoinM][p.Symbol] = p.Leverage
		}
	}
	warn := a.cfg.LiqWarnPct / 100
	a.mu.Unlock()
	cur := map[string]float64{}
	prefix := mkt + ":"
	for _, p := range ps {
		k := p.Symbol + ":" + p.Direction()
		cur[k] = p.Amount
		d := p.LiqDistance()
		level := 0
		switch {
		case d <= 0:
		case d < warn/3:
			level = 3
		case d < warn*2/3:
			level = 2
		case d < warn:
			level = 1
		}
		a.w.mu.Lock()
		prev := a.w.liqLevel[prefix+k]
		a.w.liqLevel[prefix+k] = level
		a.w.mu.Unlock()
		if level > prev {
			a.raise(event{Thread: "liq:" + mkt + ":" + k, Kind: "liq_risk", Tier: "action", Push: "time-sensitive", At: now,
				Title: fmt.Sprintf("%s %s %s 离强平只剩 %.1f%%", marketName(mkt), p.Symbol, p.Direction(), d*100),
				Body:  fmt.Sprintf("标记价 %s，强平价 %s，数量 %s，杠杆 %dx。", fmtNum(p.Mark), fmtNum(p.Liquidation), fmtNum(p.Amount), p.Leverage)})
		}
	}
	a.w.mu.Lock()
	for lk := range a.w.liqLevel {
		if strings.HasPrefix(lk, prefix) {
			if _, ok := cur[strings.TrimPrefix(lk, prefix)]; !ok {
				delete(a.w.liqLevel, lk)
			}
		}
	}
	prev, had := a.w.prevPos[mkt]
	a.w.prevPos[mkt] = cur
	a.w.mu.Unlock()
	if !had {
		return
	}
	keys := map[string]bool{}
	for k := range cur {
		keys[k] = true
	}
	for k := range prev {
		keys[k] = true
	}
	for k := range keys {
		if cur[k] == prev[k] || a.ownOrderRecently(mkt, symbolOf(k), now) {
			continue
		}
		a.raise(event{Thread: "pos:" + mkt + ":" + k, Kind: "position_change", Tier: "action", Push: "time-sensitive", At: now,
			Title: fmt.Sprintf("%s %s 仓位变了，不是 Persona 下的单", marketName(mkt), k),
			Body:  fmt.Sprintf("数量 %s → %s。如果不是你自己操作的，检查 API Key 是否泄露。", fmtNum(prev[k]), fmtNum(cur[k]))})
	}
}

func symbolOf(posKey string) string {
	for i := 0; i < len(posKey); i++ {
		if posKey[i] == ':' {
			return posKey[:i]
		}
	}
	return posKey
}

// ownOrderRecently: the extension has an order on symbol that is open or was placed in the last 10 minutes.
func (a *app) ownOrderRecently(mkt, symbol string, now time.Time) bool {
	for _, o := range a.ledger.snapshot().Placed {
		if o.Market == mkt && o.Symbol == symbol && (o.open() || now.Sub(o.At) < 10*time.Minute) {
			return true
		}
	}
	return false
}

// followOrders reads the status of the extension's open orders and tells fills and rejections.
func (a *app) followOrders(ctx context.Context, now time.Time) {
	for _, o := range a.ledger.snapshot().Placed {
		if o.Status == "PENDING_SUBMIT" && now.Sub(o.At) < time.Minute {
			continue
		}
		watching := o.Status == "NOT_FOUND" && now.Sub(o.At) < 10*time.Minute
		if (!o.open() && !watching) || now.Sub(o.At) > 30*24*time.Hour {
			continue
		}
		st, err := a.queryOrder(ctx, o.Market, o.Symbol, o.OrderID, o.ClientID)
		var ae *apiError
		if o.OrderID == 0 && errors.As(err, &ae) && ae.Code == -2013 {
			if (o.Status == "UNKNOWN" || o.Status == "PENDING_SUBMIT") && now.Sub(o.At) > 2*time.Minute {
				a.settleAbsent(o, now)
			}
			continue
		}
		if err != nil {
			a.record(err)
			continue
		}
		if o.OrderID == 0 {
			a.raise(event{Thread: "order:unknown:" + o.ClientID, Kind: "order_filled", Tier: "action", Push: "time-sensitive", At: now,
				Title: fmt.Sprintf("币安 %s %s 那笔结果不明的单其实已下", marketName(o.Market), o.Symbol),
				Body:  fmt.Sprintf("订单 #%d（client id %s），状态 %s，成交 %s。", st.OrderID, o.ClientID, statusName(st.Status), st.ExecutedQty)})
			_ = a.ledger.update(func(s *ledgerState) error {
				for i := range s.Placed {
					p := &s.Placed[i]
					if p.ClientID == o.ClientID && p.Market == o.Market {
						wasAbsent := p.Status == "NOT_FOUND"
						p.OrderID, p.Status, p.Executed = st.OrderID, st.Status, st.ExecutedQty
						if wasAbsent {
							if !p.Reducing && !p.Counts && p.Notional > 0 {
								if s.Day == o.dayIn(a) {
									s.UsedUSDT += p.Notional // it counts after all
								}
								p.Counts = true
							}
							if s.Day == o.dayIn(a) {
								s.Orders++
							}
						}
					}
				}
				return nil
			})
			continue
		}
		if st.Status == o.Status && st.ExecutedQty == o.Executed {
			continue
		}
		_ = a.ledger.update(func(s *ledgerState) error {
			for i := range s.Placed {
				if s.Placed[i].key() == o.key() {
					s.Placed[i].Status, s.Placed[i].Executed = st.Status, st.ExecutedQty
				}
			}
			return nil
		})
		title := fmt.Sprintf("%s %s %s %s", marketName(o.Market), o.Symbol, sideName(o.Side), statusName(st.Status))
		body := fmt.Sprintf("订单 #%d，成交 %s / %s。", o.OrderID, st.ExecutedQty, o.Quantity)
		switch st.Status {
		case "FILLED", "PARTIALLY_FILLED":
			a.raise(event{Thread: "order:" + o.key(), Kind: "order_filled", Tier: "fyi", Push: "active", At: now, Title: title, Body: body})
		case "REJECTED", "EXPIRED", "EXPIRED_IN_MATCH":
			a.raise(event{Thread: "order:" + o.key(), Kind: "order_rejected", Tier: "action", Push: "active", At: now, Title: title, Body: body})
		}
	}
}

func (a *app) watchDeposits(ctx context.Context, now time.Time) {
	var rows []struct {
		ID      string `json:"id"`
		TxID    string `json:"txId"`
		Coin    string `json:"coin"`
		Amount  string `json:"amount"`
		Status  int    `json:"status"`
		Network string `json:"network"`
	}
	q := url.Values{"startTime": {strconv.FormatInt(now.Add(-7*24*time.Hour).UnixMilli(), 10)}}
	if err := a.client().signed(ctx, hostSpot, "GET", "/sapi/v1/capital/deposit/hisrec", q, &rows); err != nil {
		a.record(err)
		return
	}
	a.w.mu.Lock()
	seeded := a.w.seededDep
	type transferNotice struct {
		id    string
		label string
	}
	var fresh []transferNotice
	for _, r := range rows {
		// Binance deposit status: 0=pending, 1=success, 6=credited (cannot withdraw yet), 7=wrong deposit, 8=waiting confirm.
		// Only announce completed/credited deposits; leave pending ones unmarked so their later completion is reported.
		if r.Status != 1 && r.Status != 6 {
			continue
		}
		label := fmt.Sprintf("%s %s（%s）", r.Amount, r.Coin, r.Network)
		id := firstOf(r.ID, r.TxID)
		if id == "" {
			id = label
		}
		if !a.w.deposits[id] {
			a.w.deposits[id] = true
			if seeded {
				fresh = append(fresh, transferNotice{id: id, label: label})
			}
		}
	}
	a.w.seededDep = true
	a.w.mu.Unlock()
	for _, f := range fresh {
		a.raise(event{Thread: "deposit:" + f.id, Kind: "deposit", Tier: "fyi", At: now, Title: "币安收到充值 " + f.label, Body: "新的充值记录：" + f.label + "。"})
	}
}

func (a *app) watchWithdrawals(ctx context.Context, now time.Time) {
	var rows []struct {
		ID      string `json:"id"`
		Coin    string `json:"coin"`
		Amount  string `json:"amount"`
		Network string `json:"network"`
	}
	q := url.Values{"startTime": {strconv.FormatInt(now.Add(-7*24*time.Hour).UnixMilli(), 10)}}
	if err := a.client().signed(ctx, hostSpot, "GET", "/sapi/v1/capital/withdraw/history", q, &rows); err != nil {
		a.record(err)
		return
	}
	a.w.mu.Lock()
	seeded := a.w.seededWd
	type transferNotice struct {
		id    string
		label string
	}
	var fresh []transferNotice
	for _, r := range rows {
		label := fmt.Sprintf("%s %s（%s）", r.Amount, r.Coin, r.Network)
		id := firstOf(r.ID, label)
		if !a.w.withdraws[id] {
			a.w.withdraws[id] = true
			if seeded {
				fresh = append(fresh, transferNotice{id: id, label: label})
			}
		}
	}
	a.w.seededWd = true
	a.w.mu.Unlock()
	for _, f := range fresh {
		a.raise(event{Thread: "withdraw:" + f.id, Kind: "withdrawal", Tier: "action", Push: "time-sensitive", At: now,
			Title: "币安账户有一笔提现 " + f.label, Body: "扩展不会提现。如果不是你自己操作的，立刻检查账户安全。"})
	}
}

func sideName(s string) string {
	if s == "SELL" {
		return "卖出"
	}
	return "买入"
}

func statusName(s string) string {
	switch s {
	case "FILLED":
		return "已成交"
	case "PARTIALLY_FILLED":
		return "部分成交"
	case "CANCELED":
		return "已撤销"
	case "REJECTED":
		return "被拒绝"
	case "EXPIRED", "EXPIRED_IN_MATCH":
		return "已过期"
	case "NEW":
		return "挂单中"
	case "PENDING_SUBMIT":
		return "提交中"
	}
	return s
}

// settleAbsent: an order of unknown outcome that Binance still does not have after two minutes is taken as never
// placed; its allowance goes back.
func (a *app) settleAbsent(o placedOrder, now time.Time) {
	_ = a.ledger.update(func(s *ledgerState) error {
		for i := range s.Placed {
			p := &s.Placed[i]
			if p.ClientID == o.ClientID && p.Market == o.Market && (p.Status == "UNKNOWN" || p.Status == "PENDING_SUBMIT") {
				p.Status = "NOT_FOUND"
				if s.Day == o.dayIn(a) {
					if p.Counts {
						s.UsedUSDT = math.Max(0, s.UsedUSDT-p.Notional)
					}
					if s.Orders > 0 {
						s.Orders--
					}
				}
				p.Counts = false
			}
		}
		return nil
	})
	a.raise(event{Thread: "order:unknown:" + o.ClientID, Kind: "order_rejected", Tier: "fyi", At: now,
		Title: fmt.Sprintf("币安 %s %s 那笔结果不明的单确认没下成", marketName(o.Market), o.Symbol),
		Body:  "币安反复确认没有 client id " + o.ClientID + " 的订单，额度已退回。"})
}
