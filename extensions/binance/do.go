package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// do runs a write the user approved on the phone. The guards run again first (the settings may have changed since
// the card), with a fresh price read now.
func (a *app) do(ctx context.Context, requestID, name string, raw json.RawMessage) (map[string]any, error) {
	mkt, cancel, ok := marketOfAction(name)
	if !ok {
		return nil, fmt.Errorf("unknown action %q", name)
	}
	a.writeMu.Lock()
	defer a.writeMu.Unlock()
	env := a.env()
	// Live actions always check idempotency against the live ledger a.ledger,
	// never paper-ledger.json, regardless of the current PaperTrading mode.
	if !isPaperAction(name) {
		if r, ok := a.ledger.result(requestID); ok {
			return answer(r), nil
		}
		if prev, ok := a.ledger.placedByRequest(requestID); ok {
			return a.recoverInFlightOrder(ctx, env, requestID, prev)
		}
	} else if a.paper != nil {
		if r, ok := a.paper.result(requestID); ok {
			return answer(r), nil
		}
	}
	if err := checkActionMode(env.cfg, name); err != nil {
		return nil, err
	}
	if cancel {
		return a.doCancel(ctx, requestID, mkt, raw)
	}
	return a.doOrder(ctx, requestID, name, mkt, raw)
}

func (a *app) recoverInFlightOrder(ctx context.Context, env *guardEnv, requestID string, prev placedOrder) (map[string]any, error) {
	if env.cfg.PaperTrading {
		return nil, fmt.Errorf("该实盘请求已预留额度但尚未确认终态（client id %s），当前已切换到纸面模式，不会重发；请在币安核对订单状态", prev.ClientID)
	}
	if prev.Status != "PENDING_SUBMIT" {
		return nil, fmt.Errorf("该实盘请求已记录在台账中（状态 %s，client id %s），不会重发", statusName(prev.Status), prev.ClientID)
	}
	if a.client().signer == nil || env.locked != "" {
		return nil, fmt.Errorf("该实盘请求已预留额度但尚未确认终态（client id %s），当前无法向币安核对，不会重发", prev.ClientID)
	}
	found, verdict := a.resolveUnknown(ctx, prev.Market, prev.Symbol, prev.ClientID)
	switch verdict {
	case verdictFound:
		r := writeResult{Text: fmt.Sprintf("已下单 %s %s %s %s #%d，状态 %s（client id %s）", marketName(prev.Market), prev.Symbol,
			sideName(prev.Side), prev.Type, found.OrderID, statusName(found.Status), prev.ClientID),
			MessageRef: strconv.FormatInt(found.OrderID, 10), At: a.now()}
		_ = a.ledger.update(func(s *ledgerState) error {
			s.Results[requestID] = r
			prev.OrderID, prev.Status, prev.Executed, prev.At = found.OrderID, found.Status, found.ExecutedQty, r.At
			savePlacedOrder(s, prev)
			return nil
		})
		return answer(r), nil
	case verdictAbsent:
		msg := fmt.Sprintf("没下成：币安确认没有这笔单（client id %s）。额度已退回；扩展还会盯 10 分钟，万一出现会提醒你", prev.ClientID)
		now := a.now()
		prevDay := prev.dayIn(a)
		_ = a.ledger.update(func(s *ledgerState) error {
			for i := range s.Placed {
				if s.Placed[i].RequestID == requestID && s.Placed[i].Status == "PENDING_SUBMIT" {
					if s.Day == prevDay {
						if s.Placed[i].Counts {
							s.UsedUSDT = math.Max(0, s.UsedUSDT-s.Placed[i].Notional)
						}
						if s.Orders > 0 {
							s.Orders--
						}
					}
					s.Placed[i].Status, s.Placed[i].Counts, s.Placed[i].At = "NOT_FOUND", false, now
				}
			}
			s.Results[requestID] = writeResult{Text: msg, At: now}
			return nil
		})
		return nil, fmt.Errorf("没下成：币安确认没有这笔单（client id %s），额度已退回；不会自动重发", prev.ClientID)
	default:
		msg := fmt.Sprintf("下单结果不明（上次提交未确认），扩展没有重发。请在币安里核对 client id %s；在核对清楚之前，额度按已下单占用", prev.ClientID)
		now := a.now()
		_ = a.ledger.update(func(s *ledgerState) error {
			s.Results[requestID] = writeResult{Text: msg, At: now}
			prev.Status, prev.Counts, prev.At = "UNKNOWN", !prev.Reducing, now
			savePlacedOrder(s, prev)
			return nil
		})
		a.raise(event{Thread: "order:unknown:" + prev.ClientID, Kind: "order_rejected", Tier: "action", Push: "time-sensitive", At: now,
			Title: fmt.Sprintf("币安 %s %s %s 下单结果不明", marketName(prev.Market), prev.Symbol, sideName(prev.Side)), Body: msg})
		return answer(writeResult{Text: msg}), nil
	}
}

func answer(r writeResult) map[string]any {
	out := map[string]any{"text": r.Text}
	if r.MessageRef != "" {
		out["message_ref"] = r.MessageRef
	}
	return out
}

func (a *app) doCancel(ctx context.Context, requestID, mkt string, raw json.RawMessage) (map[string]any, error) {
	c, err := parseCancel(raw)
	if err != nil {
		return nil, err
	}
	if err := checkWritable(a.env(), mkt); err != nil {
		return nil, err
	}
	q := url.Values{"symbol": {c.Symbol}}
	if c.OrderID > 0 {
		q.Set("orderId", strconv.FormatInt(c.OrderID, 10))
	} else {
		q.Set("origClientOrderId", c.ClientID)
	}
	var st orderStatus
	if err := a.client().signed(ctx, marketHost(mkt), "DELETE", orderPath(mkt), q, &st); err != nil {
		return nil, fmt.Errorf("撤单失败：%v", err)
	}
	r := writeResult{Text: fmt.Sprintf("已撤销 %s %s 订单 #%d（状态 %s，已成交 %s）", marketName(mkt), c.Symbol, st.OrderID,
		statusName(st.Status), st.ExecutedQty), MessageRef: strconv.FormatInt(st.OrderID, 10), At: a.now()}
	_ = a.ledger.update(func(s *ledgerState) error {
		s.Results[requestID] = r
		for i := range s.Placed {
			if s.Placed[i].Market == mkt && s.Placed[i].OrderID == st.OrderID {
				s.Placed[i].Status, s.Placed[i].Executed = st.Status, st.ExecutedQty
			}
		}
		return nil
	})
	return answer(r), nil
}

func (a *app) doOrder(ctx context.Context, requestID, name, mkt string, raw json.RawMessage) (map[string]any, error) {
	var peek orderArgs
	if err := decodeStrict(raw, &peek); err != nil {
		return nil, err
	}
	price, err := a.readOrderPrice(ctx, mkt, strings.ToUpper(strings.TrimSpace(peek.Symbol)))
	if err != nil {
		return nil, fmt.Errorf("下单前取不到现价，没有下单：%v", err)
	}
	env := a.env()
	if err := checkActionMode(env.cfg, name); err != nil {
		return nil, err
	}
	env.priceNow = price
	p, err := planOrder(env, mkt, raw)
	if err != nil {
		return nil, fmt.Errorf("没有下单：%v", err)
	}
	if p.Args.Type == "MARKET" {
		a.mu.RLock()
		card := a.quotes[argsKey(name, raw)]
		a.mu.RUnlock()
		if card.Price <= 0 || env.now.Sub(card.At) > 20*time.Minute {
			return nil, errors.New("没有下单：找不到有效的确认卡片价格，请重新下单")
		}
		if dev := math.Abs(p.RefPrice-card.Price) / card.Price * 100; dev > env.cfg.MarketSlippagePct {
			return nil, fmt.Errorf("没有下单：现价 %s 和卡片上的 %s 相差 %.2f%%，超过滑点保护 %g%%，请重新下单",
				fmtNum(p.RefPrice), fmtNum(card.Price), dev, env.cfg.MarketSlippagePct)
		}
	}
	if isPaperAction(name) || env.cfg.PaperTrading {
		return recordPaperOrder(env, requestID, p)
	}
	day := env.cfg.dayKey(env.now)
	cid := clientOrderID(requestID)
	order := plannedOrder(requestID, cid, p, env.now)
	order.Status = "PENDING_SUBMIT"
	if err := a.ledger.update(func(s *ledgerState) error {
		a.ledger.roll(day)
		env.usedUSDT, env.ordersDone = s.UsedUSDT, s.Orders
		if err := checkAllowance(env, p); err != nil {
			return err
		}
		if p.Counts {
			s.UsedUSDT += p.Notional
		}
		s.Orders++
		savePlacedOrder(s, order)
		return nil
	}); err != nil {
		return nil, fmt.Errorf("没有下单：无法预留额度：%w", err)
	}
	release := func() {
		_ = a.ledger.update(func(s *ledgerState) error {
			if s.Day == day {
				if p.Counts {
					s.UsedUSDT = math.Max(0, s.UsedUSDT-p.Notional)
				}
				if s.Orders > 0 {
					s.Orders--
				}
			}
			removePlacedOrder(s, requestID)
			return nil
		})
	}
	q := url.Values{}
	for k, v := range p.Params {
		q[k] = v
	}
	q.Set("newClientOrderId", cid)
	q.Set("newOrderRespType", "RESULT")
	var st orderStatus
	err = a.client().signed(ctx, marketHost(mkt), "POST", orderPath(mkt), q, &st)
	if err != nil && orderOutcomeUnknown(err) {
		// The order may or may not be at Binance: never send it again; ask by the client id.
		found, verdict := a.resolveUnknown(ctx, mkt, p.Args.Symbol, cid)
		switch verdict {
		case verdictFound:
			st, err = found, nil
		case verdictAbsent:
			release()
			a.rememberOrderOutcome(requestID, p, cid, "NOT_FOUND", fmt.Sprintf("没下成：币安确认没有这笔单（%v）。额度已退回；扩展还会盯 10 分钟，万一出现会提醒你", err))
			return nil, fmt.Errorf("没下成：币安确认没有这笔单（%v），额度已退回；不会自动重发", err)
		default:
			msg := fmt.Sprintf("下单结果不明（%v），扩展没有重发。请在币安里核对 client id %s；在核对清楚之前，额度按已下单占用", err, cid)
			a.rememberOrderOutcome(requestID, p, cid, "UNKNOWN", msg)
			a.raise(event{Thread: "order:unknown:" + cid, Kind: "order_rejected", Tier: "action", Push: "time-sensitive", At: a.now(),
				Title: fmt.Sprintf("币安 %s %s %s 下单结果不明", marketName(mkt), p.Args.Symbol, sideName(p.Args.Side)), Body: msg})
			return answer(writeResult{Text: msg}), nil
		}
	}
	if err != nil {
		release()
		return nil, fmt.Errorf("币安拒绝了这笔单：%v", err)
	}
	r := writeResult{Text: fmt.Sprintf("已下单 %s %s %s %s #%d，状态 %s（client id %s）", marketName(mkt), p.Args.Symbol,
		sideName(p.Args.Side), p.Args.Type, st.OrderID, statusName(st.Status), cid), MessageRef: strconv.FormatInt(st.OrderID, 10), At: a.now()}
	_ = a.ledger.update(func(s *ledgerState) error {
		s.Results[requestID] = r
		order.OrderID, order.Status, order.Executed, order.At = st.OrderID, st.Status, st.ExecutedQty, r.At
		savePlacedOrder(s, order)
		return nil
	})
	return answer(r), nil
}

func plannedOrder(requestID, cid string, p *orderPlan, now time.Time) placedOrder {
	return placedOrder{Market: p.Market, Symbol: p.Args.Symbol, ClientID: cid, Side: p.Args.Side, Type: p.Args.Type,
		Quantity: firstOf(p.Params.Get("quantity"), p.Params.Get("quoteOrderQty")), Price: p.Params.Get("price"),
		Notional: p.Notional, Reducing: p.Reducing, Counts: p.Counts, At: now, RequestID: requestID}
}

func savePlacedOrder(s *ledgerState, order placedOrder) {
	for i := range s.Placed {
		if s.Placed[i].RequestID == order.RequestID {
			s.Placed[i] = order
			return
		}
	}
	s.Placed = append(s.Placed, order)
}

func removePlacedOrder(s *ledgerState, requestID string) {
	for i := range s.Placed {
		if s.Placed[i].RequestID == requestID {
			s.Placed = append(s.Placed[:i], s.Placed[i+1:]...)
			return
		}
	}
}

func recordPaperOrder(env *guardEnv, requestID string, p *orderPlan) (map[string]any, error) {
	o := plannedOrder(requestID, "paper-"+clientOrderID(requestID), p, env.now)
	fillPrice := p.RefPrice
	o.Status = "FILLED"
	if o.Type == "LIMIT" {
		fillPrice = floatOf(o.Price)
		if o.Side == "BUY" && fillPrice < p.RefPrice || o.Side == "SELL" && fillPrice > p.RefPrice {
			o.Status = "EXPIRED" // this simulator only evaluates the instant of execution
		}
	}
	text := fmt.Sprintf("纸面模拟 · %s %s %s %s · %s", marketName(o.Market), o.Symbol, sideName(o.Side), o.Type, o.ClientID)
	if o.Status == "FILLED" {
		o.FillPrice = strconv.FormatFloat(fillPrice, 'f', -1, 64)
		o.Executed = p.Params.Get("quantity")
		if o.Executed == "" {
			o.Executed = strconv.FormatFloat(floatOf(p.Params.Get("quoteOrderQty"))/fillPrice, 'f', -1, 64)
		}
		text += fmt.Sprintf(" · 模拟成交 %s @ %s", o.Executed, o.FillPrice)
	} else {
		text += " · 现价未达到限价，模拟未成交"
	}
	r := writeResult{Text: text, At: env.now}
	if err := env.ledger.update(func(s *ledgerState) error {
		env.ledger.roll(env.cfg.dayKey(env.now))
		env.usedUSDT, env.ordersDone = s.UsedUSDT, s.Orders
		if err := checkAllowance(env, p); err != nil {
			return err
		}
		if p.Counts {
			s.UsedUSDT += p.Notional
		}
		s.Orders++
		s.Results[requestID] = r
		s.Placed = append(s.Placed, o)
		return nil
	}); err != nil {
		return nil, fmt.Errorf("纸面台账写入失败，模拟结果未记录：%w", err)
	}
	return answer(r), nil
}

func firstOf(s ...string) string {
	for _, v := range s {
		if v != "" {
			return v
		}
	}
	return ""
}

// readOrderPrice fetches a fresh spot last price or futures mark price for this order.
func (a *app) readOrderPrice(ctx context.Context, mkt, symbol string) (float64, error) {
	if !symbolRE.MatchString(symbol) {
		return 0, errors.New("symbol 不对")
	}
	if mkt == marketSpot {
		var r struct {
			Price string `json:"price"`
		}
		if err := a.client().public(ctx, hostPublic, "/api/v3/ticker/price", url.Values{"symbol": {symbol}}, &r); err != nil {
			return 0, err
		}
		return validOrderPrice(r.Price)
	}
	path := "/fapi/v1/premiumIndex"
	if mkt == marketCoinM {
		path = "/dapi/v1/premiumIndex"
	}
	var raw json.RawMessage
	if err := a.client().public(ctx, marketHost(mkt), path, url.Values{"symbol": {symbol}}, &raw); err != nil {
		return 0, err
	}
	var one struct {
		MarkPrice string `json:"markPrice"`
	}
	var many []struct {
		MarkPrice string `json:"markPrice"`
	}
	if json.Unmarshal(raw, &one) != nil {
		if json.Unmarshal(raw, &many) != nil || len(many) == 0 {
			return 0, errors.New("标记价读不懂")
		}
		one.MarkPrice = many[0].MarkPrice
	}
	return validOrderPrice(one.MarkPrice)
}

func validOrderPrice(raw string) (float64, error) {
	price := floatOf(raw)
	if price <= 0 || math.IsInf(price, 0) || math.IsNaN(price) {
		return 0, errors.New("币安返回无效价格")
	}
	return price, nil
}

// orderOutcomeUnknown: the answer does not say Binance refused the order. A lost connection, a timeout, any 5xx (from
// Binance or a proxy on the way) and Binance's own "unknown" codes leave it open whether the order exists.
func orderOutcomeUnknown(err error) bool {
	var ne *netError
	if errors.As(err, &ne) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return true
	}
	var ae *apiError
	if errors.As(err, &ae) {
		return ae.Status >= 500 || ae.Status == 0 || ae.Code == -1007 || ae.Code == -1006 || ae.Code == -1001
	}
	return true // anything else (an answer that cannot be read): not known either
}

type verdict int

const (
	verdictUnknown verdict = iota
	verdictFound
	verdictAbsent
)

// resolveUnknown asks Binance for the order by client id, a few times. Only Binance answering "no such order"
// (-2013) twice, two seconds apart, counts as absent; a query that fails on the way says nothing.
var resolveTries, resolveGap = 4, 2 * time.Second

func (a *app) resolveUnknown(ctx context.Context, mkt, symbol, cid string) (orderStatus, verdict) {
	absent := 0
	for i := 0; i < resolveTries; i++ {
		if ctx.Err() != nil {
			return orderStatus{}, verdictUnknown
		}
		if i > 0 {
			timer := time.NewTimer(resolveGap)
			select {
			case <-ctx.Done():
				timer.Stop()
				return orderStatus{}, verdictUnknown
			case <-timer.C:
			}
		}
		qctx, cancel := context.WithTimeout(ctx, 8*time.Second)
		st, err := a.queryOrder(qctx, mkt, symbol, 0, cid)
		cancel()
		var ae *apiError
		switch {
		case err == nil:
			return st, verdictFound
		case errors.As(err, &ae) && ae.Code == -2013:
			absent++
			if absent >= 2 {
				return orderStatus{}, verdictAbsent
			}
		default:
			absent = 0
		}
	}
	return orderStatus{}, verdictUnknown
}

// rememberOrderOutcome saves the result for retries and keeps the client id for reconciliation.
func (a *app) rememberOrderOutcome(requestID string, p *orderPlan, cid, status, text string) {
	now := a.now()
	_ = a.ledger.update(func(s *ledgerState) error {
		s.Results[requestID] = writeResult{Text: text, At: now}
		order := plannedOrder(requestID, cid, p, now)
		order.Counts, order.Status = p.Counts && status == "UNKNOWN", status
		savePlacedOrder(s, order)
		return nil
	})
}
