package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// marketOfAction maps a write's name to its market and whether it cancels.
func marketOfAction(name string) (mkt string, cancel bool, ok bool) {
	switch name {
	case "binance_spot_order", "binance_paper_spot_order":
		return marketSpot, false, true
	case "binance_spot_cancel":
		return marketSpot, true, true
	case "binance_usdm_order", "binance_paper_usdm_order":
		return marketUSDM, false, true
	case "binance_usdm_cancel":
		return marketUSDM, true, true
	case "binance_coinm_order", "binance_paper_coinm_order":
		return marketCoinM, false, true
	case "binance_coinm_cancel":
		return marketCoinM, true, true
	}
	return "", false, false
}

const paperActionPrefix = "binance_paper_"

func checkActionMode(cfg *config, name string) error {
	if cfg.PaperTrading != strings.HasPrefix(name, paperActionPrefix) {
		return errors.New("下单模式已切换，请重新发起订单或撤单")
	}
	return nil
}

// env is the guards' view of now.
func (a *app) env() *guardEnv {
	now := a.now()
	a.mu.RLock()
	cfg := a.cfg
	locked, perms := a.locked, a.perms
	configErr := a.cfgErr
	ledger := a.ledger
	if cfg.PaperTrading {
		ledger = a.paper
	}
	a.mu.RUnlock()
	used, n := ledger.usage(cfg.dayKey(now))
	return &guardEnv{cfg: &cfg, now: now, usedUSDT: used, ordersDone: n, locked: locked, configErr: configErr,
		perms: perms, market: a.market, ledger: ledger,
		leverage: func(mkt, symbol string) (int, bool) {
			a.mu.RLock()
			defer a.mu.RUnlock()
			l, ok := a.leverage[mkt][symbol]
			return l, ok && l > 0
		},
		dual: func(mkt string) (bool, bool) {
			a.mu.RLock()
			defer a.mu.RUnlock()
			d, ok := a.dual[mkt]
			return d, ok
		},
		position: func(mkt, symbol, side string) (float64, bool) {
			a.mu.RLock()
			defer a.mu.RUnlock()
			if now.Sub(a.posAt[mkt]) > 2*time.Minute {
				return 0, false
			}
			for _, p := range a.positions[mkt] {
				if p.Symbol == symbol && (side == "BOTH" && (p.Side == "BOTH" || p.Side == "") || p.Side == side) {
					return p.Amount, true
				}
			}
			return 0, true
		}}
}

func argsKey(name string, raw json.RawMessage) string {
	var v any
	_ = json.Unmarshal(raw, &v)
	b, _ := json.Marshal(v) // canonical: sorted keys
	h := sha256.Sum256(append([]byte(name+"\x00"), b...))
	return hex.EncodeToString(h[:])
}

// clientOrderID is the order's id derived from the hub's request id: one card, at most one order.
func clientOrderID(requestID string) string {
	h := sha256.Sum256([]byte(requestID))
	return "pa-" + hex.EncodeToString(h[:])[:32]
}

func (a *app) describe(name string, raw json.RawMessage) (map[string]any, error) {
	mkt, cancel, ok := marketOfAction(name)
	if !ok {
		return nil, fmt.Errorf("unknown action %q", name)
	}
	env := a.env()
	if err := checkActionMode(env.cfg, name); err != nil {
		return nil, err
	}
	if cancel {
		c, err := parseCancel(raw)
		if err != nil {
			return nil, err
		}
		if err := checkWritable(env, mkt); err != nil {
			return nil, err
		}
		return map[string]any{"action": map[string]any{"name": name, "visible": "owner"}, "summary": a.cancelSummary(mkt, c)}, nil
	}
	p, err := planOrder(env, mkt, raw)
	if err != nil {
		return nil, err
	}
	summary := orderSummary(env, p)
	if env.cfg.PaperTrading {
		summary = "纸面模拟 · 不向币安发送订单\n" + summary
	}
	a.mu.Lock()
	for k, q := range a.quotes {
		if env.now.Sub(q.At) > 20*time.Minute {
			delete(a.quotes, k)
		}
	}
	a.quotes[argsKey(name, raw)] = quote{Price: p.RefPrice, At: env.now}
	a.mu.Unlock()
	return map[string]any{"action": map[string]any{"name": name, "visible": "owner"}, "summary": summary}, nil
}

func parseCancel(raw json.RawMessage) (cancelArgs, error) {
	var c cancelArgs
	if err := decodeStrict(raw, &c); err != nil {
		return c, err
	}
	c.Symbol = strings.ToUpper(strings.TrimSpace(c.Symbol))
	c.ClientID = strings.TrimSpace(c.ClientID)
	switch {
	case !symbolRE.MatchString(c.Symbol):
		return c, errors.New("symbol 不是币安的交易对代码")
	case (c.OrderID > 0) == (c.ClientID != ""):
		return c, errors.New("撤单需要 order_id 或 client_id 之一")
	case c.OrderID < 0, len(c.ClientID) > 36:
		return c, errors.New("订单号不对")
	}
	return c, nil
}

func (a *app) cancelSummary(mkt string, c cancelArgs) string {
	ref := fmt.Sprintf("#%d", c.OrderID)
	if c.ClientID != "" {
		ref = "client id " + c.ClientID
	}
	s := fmt.Sprintf("币安 · %s · 撤单\n币对　%s\n订单　%s", marketName(mkt), c.Symbol, ref)
	for _, o := range a.ledger.snapshot().Placed {
		if o.Market == mkt && o.Symbol == c.Symbol && (o.OrderID == c.OrderID || (c.ClientID != "" && o.ClientID == c.ClientID)) {
			s += fmt.Sprintf("\n内容　%s %s %s @ %s（Persona 下的单，%s）", sideName(o.Side), o.Type, o.Quantity, o.Price, statusName(o.Status))
		}
	}
	return s
}

func orderSummary(env *guardEnv, p *orderPlan) string {
	a := p.Args
	var b strings.Builder
	fmt.Fprintf(&b, "币安 · %s · %s\n", marketName(p.Market), sideName(a.Side))
	fmt.Fprintf(&b, "币对　%s\n类型　%s", a.Symbol, a.Type)
	if a.Type == "LIMIT" {
		fmt.Fprintf(&b, " %s", a.TimeInForce)
	}
	if q := p.Params.Get("quantity"); q != "" {
		unit := p.Rules.Base
		if p.Market == marketCoinM {
			unit = "张"
		}
		fmt.Fprintf(&b, "\n数量　%s %s", q, unit)
	}
	if q := p.Params.Get("quoteOrderQty"); q != "" {
		fmt.Fprintf(&b, "\n金额　%s %s（按金额市价成交）", q, p.Rules.Quote)
	}
	refName := "现价"
	if p.Market != marketSpot {
		refName = "标记价"
	}
	if pr := p.Params.Get("price"); pr != "" {
		dev := (floatOf(pr) - p.RefPrice) / p.RefPrice * 100
		fmt.Fprintf(&b, "\n价格　%s（%s %s，%+.2f%%）", pr, refName, fmtNum(p.RefPrice), dev)
	} else {
		fmt.Fprintf(&b, "\n价格　市价（%s %s；执行时偏离超过 %g%% 就不下）", refName, fmtNum(p.RefPrice), env.cfg.MarketSlippagePct)
	}
	unit := "USDT"
	if p.Market == marketCoinM {
		unit = "USD"
	}
	fmt.Fprintf(&b, "\n估算金额 ≈ %s %s（手续费另计）", fmtNum(p.Notional), unit)
	if p.Market != marketSpot {
		lev, _ := env.leverage(p.Market, a.Symbol)
		ps := p.Params.Get("positionSide")
		if ps == "" {
			ps = "单向"
		}
		fmt.Fprintf(&b, "\n合约　杠杆 %dx · 只减仓 %s · 持仓方向 %s", lev, yesNo(p.Reducing), ps)
	}
	if p.Counts {
		fmt.Fprintf(&b, "\n护栏　单笔上限 %g · 今天已用 %s / %g · 下单后剩 %s", env.cfg.MaxOrderUSDT, fmtNum(env.usedUSDT),
			env.cfg.MaxDailyUSDT, fmtNum(math.Max(0, env.cfg.MaxDailyUSDT-env.usedUSDT-p.Notional)))
	} else {
		b.WriteString("\n护栏　只减仓，不计入每日额度")
	}
	return b.String()
}

func yesNo(b bool) string {
	if b {
		return "是"
	}
	return "否"
}

// orderStatus is an order as Binance reports it.
type orderStatus struct {
	OrderID       int64  `json:"orderId"`
	ClientOrderID string `json:"clientOrderId"`
	Status        string `json:"status"`
	ExecutedQty   string `json:"executedQty"`
	OrigQty       string `json:"origQty"`
	Price         string `json:"price"`
	Side          string `json:"side"`
	Type          string `json:"type"`
}

func (a *app) queryOrder(ctx context.Context, mkt, symbol string, orderID int64, clientID string) (orderStatus, error) {
	q := url.Values{"symbol": {symbol}}
	if orderID > 0 {
		q.Set("orderId", strconv.FormatInt(orderID, 10))
	} else {
		q.Set("origClientOrderId", clientID)
	}
	var st orderStatus
	err := a.client().signed(ctx, marketHost(mkt), "GET", orderPath(mkt), q, &st)
	return st, err
}
