package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// keyPerms is what a key may do, as the extension accepts it.
type keyPerms struct {
	Checked bool
	Spot    bool // enableSpotAndMarginTrading
	Futures bool // enableFutures
	IP      bool
}

// Permissions a key must not have; and the ones known to be harmless. Any other enable* / permits* field that is
// true refuses the key (a permission Binance adds later is refused until it is reviewed here).
var (
	forbiddenPerms = map[string]string{
		"enableWithdrawals":            "提现",
		"enableInternalTransfer":       "内部划转",
		"permitsUniversalTransfer":     "万向划转",
		"enableMargin":                 "杠杆借贷",
		"enableVanillaOptions":         "期权",
		"enablePortfolioMarginTrading": "统一账户",
		"enableFixApiTrade":            "FIX API 交易",
	}
	acceptedPerms = map[string]bool{
		"enableReading": true, "enableSpotAndMarginTrading": true, "enableFutures": true, "enableFixReadOnly": true,
	}
)

// judgeRestrictions applies the policy to GET /sapi/v1/account/apiRestrictions: reading and at least one trading
// permission, an IP whitelist, nothing that moves money out or borrows.
func judgeRestrictions(raw json.RawMessage) (keyPerms, error) {
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return keyPerms{}, errors.New("币安返回的权限信息读不懂")
	}
	flag := func(k string) bool { b, _ := m[k].(bool); return b }
	var bad []string
	for k, v := range m {
		b, isBool := v.(bool)
		if !isBool || !b || !(strings.HasPrefix(k, "enable") || strings.HasPrefix(k, "permits")) {
			continue
		}
		if name, ok := forbiddenPerms[k]; ok {
			bad = append(bad, name)
		} else if !acceptedPerms[k] {
			bad = append(bad, "未知权限 "+k)
		}
	}
	sort.Strings(bad)
	p := keyPerms{Checked: true, Spot: flag("enableSpotAndMarginTrading"), Futures: flag("enableFutures"), IP: flag("ipRestrict")}
	switch {
	case len(bad) > 0:
		return p, fmt.Errorf("这把 Key 开了 %s 权限，扩展不接受。请在币安 API 管理里关掉后重试", strings.Join(bad, "、"))
	case !p.IP:
		return p, errors.New("这把 Key 没有绑定 IP 白名单，扩展不接受。请在币安 API 管理里选「仅限受信任 IP」")
	case !flag("enableReading"):
		return p, errors.New("这把 Key 没有读取权限")
	case !p.Spot && !p.Futures:
		return p, errors.New("这把 Key 没有任何交易权限（现货或合约），至少要开一项")
	}
	return p, nil
}

// checkKey asks Binance about c's key and judges it.
func checkKey(ctx context.Context, c *client) (keyPerms, error) {
	var raw json.RawMessage
	if err := c.signed(ctx, hostSpot, "GET", "/sapi/v1/account/apiRestrictions", nil, &raw); err != nil {
		var ae *apiError
		if errors.As(err, &ae) {
			switch ae.Code {
			case -2008, -2014, -2015, -1022:
				return keyPerms{}, fmt.Errorf("API Key 或私钥不对，或这台机器的出口 IP 不在 Key 的白名单里（币安 %d）", ae.Code)
			}
			if ae.Status == 451 || ae.Status == 403 {
				return keyPerms{}, fmt.Errorf("币安拒绝了这个出口（HTTP %d），检查代理设置", ae.Status)
			}
		}
		return keyPerms{}, fmt.Errorf("连不上币安：%v", err)
	}
	return judgeRestrictions(raw)
}
