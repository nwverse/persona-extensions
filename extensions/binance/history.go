package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"
)

type trade struct {
	Time     int64  `json:"time"`
	Price    string `json:"price"`
	Qty      string `json:"qty"`
	Quote    string `json:"quoteQty"`
	Fee      string `json:"commission"`
	FeeAsset string `json:"commissionAsset"`
	IsBuyer  *bool  `json:"isBuyer"`
	Side     string `json:"side"`
	PnL      string `json:"realizedPnl"`
	OrderID  int64  `json:"orderId"`
}

func (a *app) toolTrades(ctx context.Context, raw json.RawMessage) (string, error) {
	var in struct {
		Market string `json:"market"`
		Symbol string `json:"symbol"`
		Days   int    `json:"days"`
	}
	if err := decodeArgs(raw, &in); err != nil {
		return "", err
	}
	mkt, err := marketArg(in.Market)
	if err != nil {
		return "", err
	}
	in.Symbol = strings.ToUpper(in.Symbol)
	if !symbolRE.MatchString(in.Symbol) {
		return "", errors.New("symbol is not a symbol")
	}
	days := daysArg(in.Days, 7, 30)
	window := 24 * time.Hour // spot: at most 24 h per request
	path := "/api/v3/myTrades"
	if mkt != marketSpot {
		window = 7 * 24 * time.Hour
		path = map[string]string{marketUSDM: "/fapi/v1/userTrades", marketCoinM: "/dapi/v1/userTrades"}[mkt]
	}
	end := a.now()
	start := end.Add(-time.Duration(days) * 24 * time.Hour)
	var all []trade
	for from := start; from.Before(end); from = from.Add(window) {
		to := from.Add(window)
		if to.After(end) {
			to = end
		}
		q := url.Values{"symbol": {in.Symbol}, "startTime": {ms(from)}, "endTime": {ms(to.Add(-time.Millisecond))}, "limit": {"1000"}}
		var rows []trade
		if err := a.client().signed(ctx, marketHost(mkt), "GET", path, q, &rows); err != nil {
			return "", err
		}
		all = append(all, rows...)
	}
	if len(all) == 0 {
		return fmt.Sprintf("(no %s trades of %s in %d days)", mkt, in.Symbol, days), nil
	}
	sort.Slice(all, func(i, j int) bool { return all[i].Time > all[j].Time })
	var b strings.Builder
	b.WriteString("time (UTC) | side | price | qty | quote | fee | realized PnL | order id\n")
	for i, t := range all {
		if i == 200 {
			fmt.Fprintf(&b, "(%d more not shown)\n", len(all)-200)
			break
		}
		side := t.Side
		if t.IsBuyer != nil {
			side = map[bool]string{true: "BUY", false: "SELL"}[*t.IsBuyer]
		}
		fmt.Fprintf(&b, "%s | %s | %s | %s | %s | %s %s | %s | %d\n", time.UnixMilli(t.Time).UTC().Format("2006-01-02 15:04"), side,
			t.Price, t.Qty, t.Quote, t.Fee, t.FeeAsset, orDash(t.PnL), t.OrderID)
	}
	return b.String(), nil
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func (a *app) toolIncome(ctx context.Context, raw json.RawMessage) (string, error) {
	var in struct {
		Days int `json:"days"`
	}
	if err := decodeArgs(raw, &in); err != nil {
		return "", err
	}
	days := daysArg(in.Days, 7, 30)
	end := a.now()
	q := url.Values{"startTime": {ms(end.Add(-time.Duration(days) * 24 * time.Hour))}, "endTime": {ms(end)}, "limit": {"1000"}}
	var rows []struct {
		Symbol     string `json:"symbol"`
		IncomeType string `json:"incomeType"`
		Income     string `json:"income"`
		Asset      string `json:"asset"`
	}
	if err := a.client().signed(ctx, hostUSDM, "GET", "/fapi/v1/income", q, &rows); err != nil {
		return "", err
	}
	sum := map[string]float64{}
	for _, r := range rows {
		sum[r.IncomeType+" "+r.Asset] += floatOf(r.Income)
	}
	if len(sum) == 0 {
		return fmt.Sprintf("(no USDⓈ-M income in %d days)", days), nil
	}
	keys := make([]string, 0, len(sum))
	for k := range sum {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	fmt.Fprintf(&b, "USDⓈ-M income, last %d days (%d records):\n", days, len(rows))
	for _, k := range keys {
		fmt.Fprintf(&b, "%s: %s\n", k, fmtNum(sum[k]))
	}
	return b.String(), nil
}

func (a *app) toolTransfers(ctx context.Context, raw json.RawMessage) (string, error) {
	var in struct {
		Days int `json:"days"`
	}
	if err := decodeArgs(raw, &in); err != nil {
		return "", err
	}
	days := daysArg(in.Days, 30, 90)
	end := a.now()
	q := url.Values{"startTime": {ms(end.Add(-time.Duration(days) * 24 * time.Hour))}, "endTime": {ms(end)}}
	var deps []struct {
		Coin       string `json:"coin"`
		Amount     string `json:"amount"`
		Network    string `json:"network"`
		Status     int    `json:"status"`
		InsertTime int64  `json:"insertTime"`
	}
	if err := a.client().signed(ctx, hostSpot, "GET", "/sapi/v1/capital/deposit/hisrec", q, &deps); err != nil {
		return "", err
	}
	var wds []struct {
		Coin      string `json:"coin"`
		Amount    string `json:"amount"`
		Network   string `json:"network"`
		Status    int    `json:"status"`
		ApplyTime string `json:"applyTime"`
	}
	if err := a.client().signed(ctx, hostSpot, "GET", "/sapi/v1/capital/withdraw/history", q, &wds); err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Deposits, last %d days: %d\n", days, len(deps))
	for _, d := range deps {
		fmt.Fprintf(&b, "%s | %s %s | %s | status %d\n", time.UnixMilli(d.InsertTime).UTC().Format("2006-01-02 15:04"), d.Amount, d.Coin, d.Network, d.Status)
	}
	fmt.Fprintf(&b, "Withdrawals, last %d days: %d (records only; this extension cannot withdraw)\n", days, len(wds))
	for _, w := range wds {
		fmt.Fprintf(&b, "%s | %s %s | %s | status %d\n", w.ApplyTime, w.Amount, w.Coin, w.Network, w.Status)
	}
	return b.String(), nil
}
