package main

// The guards every order passes twice: in actions.describe (no network: the cached prices and rules) before the card
// is shown, and again in actions.do right before it is sent.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"net/url"
	"strings"
	"time"
)

// orderArgs are an order write's arguments (the card shows exactly these).
type orderArgs struct {
	Symbol       string          `json:"symbol"`
	Side         string          `json:"side"`
	Type         string          `json:"type"`
	Quantity     json.RawMessage `json:"quantity,omitempty"`
	QuoteAmount  json.RawMessage `json:"quote_amount,omitempty"`
	Price        json.RawMessage `json:"price,omitempty"`
	TimeInForce  string          `json:"time_in_force,omitempty"`
	ReduceOnly   bool            `json:"reduce_only,omitempty"`
	PositionSide string          `json:"position_side,omitempty"`
}

// cancelArgs are a cancel write's arguments.
type cancelArgs struct {
	Symbol   string `json:"symbol"`
	OrderID  int64  `json:"order_id,omitempty"`
	ClientID string `json:"client_id,omitempty"`
}

// guardEnv is what the guards read: settings, today's use, the market caches and the account's futures state.
type guardEnv struct {
	cfg        *config
	now        time.Time
	usedUSDT   float64
	ordersDone int
	locked     string // non-empty: why every write is refused (key problem)
	configErr  error
	perms      keyPerms
	market     *market
	priceNow   float64 // direct price read for this execution, or zero when describing
	ledger     *ledger
	leverage   func(mkt, symbol string) (int, bool)
	dual       func(mkt string) (bool, bool)
	position   func(mkt, symbol, side string) (float64, bool) // open size (contracts / base), known
}

// orderPlan is a checked order: the parameters to send and what the card says.
type orderPlan struct {
	Market   string
	Args     orderArgs
	Params   url.Values
	Notional float64 // USDT (coinm: USD) estimate
	Counts   bool    // counts against the allowance (not reduce-only)
	RefPrice float64
	Rules    *symbolRules
	Reducing bool
	Lines    []string
}

func decodeStrict(raw json.RawMessage, v any) error {
	if len(bytes.TrimSpace(raw)) == 0 {
		raw = json.RawMessage("{}")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return fmt.Errorf("invalid arguments: %v", err)
	}
	return nil
}

func optionalDec(raw json.RawMessage, name string) (dec, error) {
	if len(raw) == 0 || string(raw) == "null" || string(raw) == `""` {
		return dec{}, nil
	}
	d, err := parseDec(raw)
	if err != nil {
		return dec{}, fmt.Errorf("%s: %v", name, err)
	}
	return d, nil
}

// checkWritable is what every write needs, the cancels too.
func checkWritable(env *guardEnv, mkt string) error {
	switch {
	case !env.cfg.TradingEnabled:
		return errors.New("下单和撤单已关闭：在 设置 › 扩展 › 币安 打开「允许下单和撤单」")
	case env.configErr != nil:
		return fmt.Errorf("设置有误：%w", env.configErr)
	case !env.cfg.marketOK[mkt]:
		return fmt.Errorf("设置里没有允许在 %s 市场下单", marketName(mkt))
	case env.cfg.PaperTrading && mkt == marketSpot:
		return nil // public market data is sufficient for a spot simulation
	case env.locked != "":
		return errors.New("写操作已锁定：" + env.locked)
	case mkt == marketSpot && !env.perms.Spot:
		return errors.New("这把 API Key 没有现货交易权限")
	case mkt != marketSpot && !env.perms.Futures:
		return errors.New("这把 API Key 没有合约交易权限")
	}
	return nil
}

// planOrder runs every guard on an order and builds what to send.
func planOrder(env *guardEnv, mkt string, raw json.RawMessage) (*orderPlan, error) {
	var a orderArgs
	if err := decodeStrict(raw, &a); err != nil {
		return nil, err
	}
	if err := checkWritable(env, mkt); err != nil {
		return nil, err
	}
	a.Symbol = strings.ToUpper(strings.TrimSpace(a.Symbol))
	a.Side, a.Type = strings.ToUpper(a.Side), strings.ToUpper(a.Type)
	a.TimeInForce, a.PositionSide = strings.ToUpper(a.TimeInForce), strings.ToUpper(a.PositionSide)
	cfg := env.cfg
	switch {
	case !symbolRE.MatchString(a.Symbol):
		return nil, errors.New("symbol 不是币安的交易对代码")
	case !cfg.allowed[a.Symbol]:
		return nil, fmt.Errorf("%s 不在允许下单的币对里（设置 › 扩展 › 币安 › 允许下单的币对）", a.Symbol)
	case a.Side != "BUY" && a.Side != "SELL":
		return nil, errors.New("side 只能是 BUY 或 SELL")
	case a.Type != "LIMIT" && a.Type != "MARKET":
		return nil, errors.New("type 只能是 LIMIT 或 MARKET（不支持条件单）")
	case a.Type == "MARKET" && !cfg.MarketOrders:
		return nil, errors.New("设置里没有允许市价单")
	}
	rules := env.market.rule(mkt, a.Symbol)
	if rules == nil || env.market.rulesAge(mkt, env.now) > 3*time.Hour {
		return nil, errors.New("还没有这个币对的交易规则（或已过期），稍后再试")
	}
	if rules.Status != "TRADING" {
		return nil, fmt.Errorf("%s 现在不能交易（%s）", a.Symbol, rules.Status)
	}
	if len(rules.OrderTypes) > 0 && !rules.OrderTypes[a.Type] {
		return nil, fmt.Errorf("%s 不支持 %s 单", a.Symbol, a.Type)
	}
	ref, fresh := env.market.price(mkt, a.Symbol, env.now)
	if env.priceNow > 0 {
		ref, fresh = env.priceNow, true
	}
	if !fresh || math.IsNaN(ref) || math.IsInf(ref, 0) {
		return nil, errors.New("价格数据过期，稍后再试")
	}
	p := &orderPlan{Market: mkt, Args: a, Params: url.Values{}, RefPrice: ref, Rules: rules}
	q := p.Params
	q.Set("symbol", a.Symbol)
	q.Set("side", a.Side)
	q.Set("type", a.Type)

	qty, err := optionalDec(a.Quantity, "quantity")
	if err != nil {
		return nil, err
	}
	quote, err := optionalDec(a.QuoteAmount, "quote_amount")
	if err != nil {
		return nil, err
	}
	price, err := optionalDec(a.Price, "price")
	if err != nil {
		return nil, err
	}
	if err := checkPrice(p, cfg, a, price, ref); err != nil {
		return nil, err
	}
	if err := checkQuantity(p, a, qty, quote); err != nil {
		return nil, err
	}
	if err := checkPosition(env, p, qty); err != nil {
		return nil, err
	}
	if a.Type == "MARKET" && !quote.ok() && mkt != marketCoinM {
		// A market order may fill up to the slippage guard above the price now: count that worst case.
		p.Notional *= 1 + cfg.MarketSlippagePct/100
	}
	if err := checkAllowance(env, p); err != nil {
		return nil, err
	}
	return p, nil
}

// checkPrice: a limit order needs a price on the tick, within the symbol's range and near the market.
func checkPrice(p *orderPlan, cfg *config, a orderArgs, price dec, ref float64) error {
	r := p.Rules
	if a.Type == "MARKET" {
		if price.ok() {
			return errors.New("市价单不能带 price")
		}
		if a.TimeInForce != "" {
			return errors.New("市价单不能带 time_in_force")
		}
		return nil
	}
	if !price.ok() {
		return errors.New("限价单需要 price")
	}
	if !price.multipleOf(r.TickSize) {
		return fmt.Errorf("price %s 不是最小价格单位 %s 的整数倍", price, ratText(r.TickSize))
	}
	if r.MinPrice != nil && r.MinPrice.Sign() > 0 && price.r.Cmp(r.MinPrice) < 0 ||
		r.MaxPrice != nil && r.MaxPrice.Sign() > 0 && price.r.Cmp(r.MaxPrice) > 0 {
		return fmt.Errorf("price %s 超出这个币对允许的价格范围", price)
	}
	dev := math.Abs(price.Float()-ref) / ref * 100
	if dev > cfg.LimitDeviationPct {
		return fmt.Errorf("限价 %s 偏离现价 %s 达 %.2f%%，超过设置的 %g%%", price, fmtNum(ref), dev, cfg.LimitDeviationPct)
	}
	tif := a.TimeInForce
	if tif == "" {
		tif = "GTC"
	}
	if tif != "GTC" && tif != "IOC" && tif != "FOK" {
		return errors.New("time_in_force 只能是 GTC、IOC 或 FOK")
	}
	p.Params.Set("price", price.String())
	p.Params.Set("timeInForce", tif)
	p.Args.TimeInForce = tif
	return nil
}

// checkQuantity: the size on the step and in range; the estimated notional over the minimum.
func checkQuantity(p *orderPlan, a orderArgs, qty, quote dec) error {
	r := p.Rules
	price := ratOf(p.Params.Get("price"))
	priceF := p.RefPrice
	if price != nil {
		priceF = ratFloat(price)
	}
	if quote.ok() {
		if p.Market != marketSpot || a.Type != "MARKET" {
			return errors.New("quote_amount 只能用于现货市价单")
		}
		if qty.ok() {
			return errors.New("quantity 和 quote_amount 只能给一个")
		}
		p.Params.Set("quoteOrderQty", quote.String())
		p.Notional = quote.Float()
		return checkMinNotional(p)
	}
	if !qty.ok() {
		return errors.New("需要 quantity（现货市价单也可以用 quote_amount）")
	}
	step, max := r.StepSize, r.MaxQty
	if a.Type == "MARKET" && r.MarketStep != nil && r.MarketStep.Sign() > 0 {
		step = r.MarketStep
		if r.MarketMaxQty != nil && r.MarketMaxQty.Sign() > 0 {
			max = r.MarketMaxQty
		}
	}
	if !qty.multipleOf(step) {
		return fmt.Errorf("quantity %s 不是最小数量单位 %s 的整数倍", qty, ratText(step))
	}
	if r.MinQty != nil && qty.r.Cmp(r.MinQty) < 0 {
		return fmt.Errorf("quantity %s 小于最小数量 %s", qty, ratText(r.MinQty))
	}
	if max != nil && max.Sign() > 0 && qty.r.Cmp(max) > 0 {
		return fmt.Errorf("quantity %s 大于最大数量 %s", qty, ratText(max))
	}
	p.Params.Set("quantity", qty.String())
	if p.Market == marketCoinM {
		if r.ContractSize <= 0 {
			return errors.New("不知道这个合约的面值，稍后再试")
		}
		p.Notional = qty.Float() * r.ContractSize
		return nil
	}
	p.Notional = qty.Float() * priceF
	return checkMinNotional(p)
}

func checkMinNotional(p *orderPlan) error {
	if m := p.Rules.MinNotional; m != nil && p.Notional < ratFloat(m) {
		return fmt.Errorf("金额约 %s，低于这个币对的最小下单金额 %s", fmtNum(p.Notional), ratText(m))
	}
	return nil
}

// checkPosition: futures position side, reduce-only and leverage.
func checkPosition(env *guardEnv, p *orderPlan, qty dec) error {
	a := p.Args
	if p.Market == marketSpot {
		if a.ReduceOnly || a.PositionSide != "" {
			return errors.New("现货单不能带 reduce_only 或 position_side")
		}
		return nil
	}
	dual, known := env.dual(p.Market)
	if !known {
		return errors.New("还不知道合约的持仓模式（单向 / 双向），稍后再试")
	}
	side := "BOTH"
	if dual {
		if a.PositionSide != "LONG" && a.PositionSide != "SHORT" {
			return errors.New("双向持仓模式下需要 position_side：LONG 或 SHORT")
		}
		if a.ReduceOnly {
			return errors.New("双向持仓模式不能带 reduce_only：平多用 SELL + LONG，平空用 BUY + SHORT")
		}
		side = a.PositionSide
		p.Params.Set("positionSide", side)
		p.Reducing = (a.Side == "SELL" && side == "LONG") || (a.Side == "BUY" && side == "SHORT")
	} else {
		if a.PositionSide != "" && a.PositionSide != "BOTH" {
			return errors.New("单向持仓模式不能带 position_side LONG / SHORT")
		}
		p.Reducing = a.ReduceOnly
		if a.ReduceOnly {
			p.Params.Set("reduceOnly", "true")
		}
	}
	if p.Reducing {
		open, ok := env.position(p.Market, a.Symbol, side)
		if !ok {
			return errors.New("还没有读到当前持仓，稍后再试")
		}
		// one-way: a reduce-only BUY closes a short (negative size), a SELL a long.
		if side == "BOTH" && ((a.Side == "SELL" && open <= 0) || (a.Side == "BUY" && open >= 0)) {
			return errors.New("只减仓单的方向和当前持仓不符")
		}
		if qty.Float() > math.Abs(open)+1e-12 {
			return fmt.Errorf("只减仓数量 %s 超过当前持仓 %s", qty, fmtNum(math.Abs(open)))
		}
		return nil
	}
	lev, ok := env.leverage(p.Market, a.Symbol)
	if !ok {
		return errors.New("还没有读到这个合约的当前杠杆，稍后再试")
	}
	if lev > env.cfg.MaxLeverage {
		return fmt.Errorf("%s 当前杠杆 %dx 超过设置的最大杠杆 %dx（扩展不改杠杆，请在币安里调低）", a.Symbol, lev, env.cfg.MaxLeverage)
	}
	return nil
}

// checkAllowance: per-order and daily limits, and the daily order count.
func checkAllowance(env *guardEnv, p *orderPlan) error {
	cfg := env.cfg
	if cfg.MaxOrdersPerDay >= 0 && env.ordersDone >= cfg.MaxOrdersPerDay {
		return fmt.Errorf("今天已经下了 %d 笔，达到每日笔数上限", env.ordersDone)
	}
	p.Counts = !p.Reducing
	if !p.Counts {
		return nil
	}
	if p.Notional > cfg.MaxOrderUSDT {
		return fmt.Errorf("估算金额 %s USDT 超过单笔上限 %g USDT", fmtNum(p.Notional), cfg.MaxOrderUSDT)
	}
	if env.usedUSDT+p.Notional > cfg.MaxDailyUSDT {
		left := math.Max(0, cfg.MaxDailyUSDT-env.usedUSDT)
		return fmt.Errorf("估算金额 %s USDT 超过今天剩余额度 %s USDT（每日上限 %g）", fmtNum(p.Notional), fmtNum(left), cfg.MaxDailyUSDT)
	}
	return nil
}

func ratText(r *big.Rat) string {
	if r == nil {
		return "?"
	}
	return normalize(r.FloatString(18))
}

// fmtNum writes a price or amount readably: up to 8 significant decimals, no trailing zeros.
func fmtNum(f float64) string {
	var s string
	switch a := math.Abs(f); {
	case a >= 1000:
		s = fmt.Sprintf("%.2f", f)
	case a >= 1:
		s = fmt.Sprintf("%.4f", f)
	default:
		s = fmt.Sprintf("%.8f", f)
	}
	if strings.Contains(s, ".") {
		s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
	}
	return s
}

func marketName(m string) string {
	switch m {
	case marketUSDM:
		return "U 本位合约"
	case marketCoinM:
		return "币本位合约"
	}
	return "现货"
}
