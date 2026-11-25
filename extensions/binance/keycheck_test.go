package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func perms(over map[string]any) json.RawMessage {
	m := map[string]any{"ipRestrict": true, "createTime": 1, "enableReading": true, "enableWithdrawals": false,
		"enableInternalTransfer": false, "enableMargin": false, "enableFutures": false, "permitsUniversalTransfer": false,
		"enableVanillaOptions": false, "enableFixApiTrade": false, "enableFixReadOnly": false,
		"enableSpotAndMarginTrading": true, "enablePortfolioMarginTrading": false}
	for k, v := range over {
		m[k] = v
	}
	b, _ := json.Marshal(m)
	return b
}

func TestKeyPolicy(t *testing.T) {
	for _, c := range []struct {
		name string
		over map[string]any
		want string // "" accepted, else part of the refusal
	}{
		{"read and spot trade, IP bound", nil, ""},
		{"read and futures", map[string]any{"enableSpotAndMarginTrading": false, "enableFutures": true}, ""},
		{"FIX read-only is harmless", map[string]any{"enableFixReadOnly": true}, ""},
		{"withdrawals", map[string]any{"enableWithdrawals": true}, "提现"},
		{"internal transfer", map[string]any{"enableInternalTransfer": true}, "内部划转"},
		{"universal transfer", map[string]any{"permitsUniversalTransfer": true}, "万向划转"},
		{"margin loans", map[string]any{"enableMargin": true}, "杠杆借贷"},
		{"options", map[string]any{"enableVanillaOptions": true}, "期权"},
		{"portfolio margin", map[string]any{"enablePortfolioMarginTrading": true}, "统一账户"},
		{"FIX trading", map[string]any{"enableFixApiTrade": true}, "FIX"},
		{"a permission Binance adds later", map[string]any{"permitsSomethingNew": true}, "未知权限 permitsSomethingNew"},
		{"no IP whitelist", map[string]any{"ipRestrict": false}, "IP 白名单"},
		{"IP field missing", map[string]any{"ipRestrict": nil}, "IP 白名单"},
		{"no reading", map[string]any{"enableReading": false}, "读取"},
		{"no trading at all", map[string]any{"enableSpotAndMarginTrading": false}, "交易权限"},
	} {
		t.Run(c.name, func(t *testing.T) {
			p, err := judgeRestrictions(perms(c.over))
			if c.want == "" {
				if err != nil {
					t.Fatalf("refused: %v", err)
				}
				if !p.Checked || !p.IP {
					t.Fatalf("perms %+v", p)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("got %v, want a refusal naming %q", err, c.want)
			}
		})
	}
}
