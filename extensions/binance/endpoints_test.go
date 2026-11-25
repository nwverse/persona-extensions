package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Words in a Binance path that move money out, borrow, or change leverage, margin or position mode. None may be in
// the endpoint table; the only exception is reading the withdrawal history (GET, a record, nothing is moved).
var forbiddenPathWords = []string{
	"withdraw", "transfer", "loan", "borrow", "repay", "lending", "margin", "leverage", "marginType", "positionMargin",
	"dual", "batchOrders", "allOpenOrders", "countdownCancelAll", "sub-account", "convert", "dust", "pay/", "giftcard",
	"algo", "oco", "oto", "orderList", "orderAmendment", "multiAssetsMargin", "portfolio", "papi", "eapi", "staking",
	"purchase", "redeem", "subscribe", "capital/contract", "futures/transfer", "asset/transfer", "vip",
}

// allowedExceptions are table entries that contain a forbidden word but only read.
var allowedExceptions = map[string]bool{
	"GET https://api.binance.com /sapi/v1/capital/withdraw/history": true, // records only
	"GET https://fapi.binance.com /fapi/v1/positionSide/dual":       true, // reads the position mode
	"GET https://dapi.binance.com /dapi/v1/positionSide/dual":       true,
}

func TestEndpointTableHasNoWithdrawalTransferOrLeverageWrite(t *testing.T) {
	for _, e := range endpoints {
		key := e.Method + " " + e.Host + " " + e.Path
		lower := strings.ToLower(e.Path)
		for _, w := range forbiddenPathWords {
			if strings.Contains(lower, strings.ToLower(w)) && !allowedExceptions[key] {
				t.Errorf("endpoint %s contains %q", key, w)
			}
		}
		if allowedExceptions[key] && e.Method != "GET" {
			t.Errorf("exception %s must be a GET", key)
		}
		// Only the order endpoints write.
		isOrder := strings.HasSuffix(e.Path, "/order") && (e.Path == "/api/v3/order" || e.Path == "/fapi/v1/order" || e.Path == "/dapi/v1/order")
		switch e.Access {
		case accessOrder:
			if e.Method != "POST" || !isOrder {
				t.Errorf("%s is marked as placing an order", key)
			}
		case accessCancel:
			if e.Method != "DELETE" || !isOrder {
				t.Errorf("%s is marked as cancelling an order", key)
			}
		default:
			if e.Method != "GET" && key != "POST https://api.binance.com /sapi/v1/asset/get-funding-asset" {
				t.Errorf("%s: a read endpoint that is not GET", key)
			}
		}
	}
}

// Every Binance path written anywhere in the program's source is in the table: nothing can be called around it.
func TestEveryPathInTheSourceIsInTheTable(t *testing.T) {
	inTable := map[string]bool{}
	for _, e := range endpoints {
		inTable[e.Path] = true
	}
	pathRE := regexp.MustCompile(`"(/(?:api|sapi|fapi|dapi|papi|eapi|wapi)/[^"]*)"`)
	files, _ := filepath.Glob("*.go")
	found := 0
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range pathRE.FindAllStringSubmatch(string(b), -1) {
			p := m[1]
			if p == "/api/v1/notify" { // the hub's own API (push), not Binance
				continue
			}
			found++
			if strings.HasSuffix(p, "/") { // a prefix joined with a fixed word (simple-earn/<kind>/position)
				p += "flexible/position"
			}
			if !inTable[p] {
				t.Errorf("%s: path %s is not in the endpoint table", f, m[1])
			}
			for _, w := range []string{"withdraw/apply", "transfer", "loan", "borrow", "repay", "leverage\"", "marginType"} {
				if strings.Contains(strings.ToLower(m[1]), strings.ToLower(w)) {
					t.Errorf("%s: path %s looks like a withdrawal / transfer / loan / leverage write", f, m[1])
				}
			}
		}
	}
	if found < len(endpoints)/2 {
		t.Fatalf("found only %d path literals: the scan is broken", found)
	}
}

// The client refuses anything outside the table before reaching the network.
func TestClientRefusesUnlistedEndpoints(t *testing.T) {
	f := newFakeBinance(t)
	s, err := newSigner(f.apiKey, f.privPEM)
	if err != nil {
		t.Fatal(err)
	}
	c := newClient(f.srv.Client(), s)
	c.hostURL = f.hostURL
	ctx := context.Background()
	for _, call := range []struct{ host, method, path string }{
		{hostSpot, "POST", "/sapi/v1/capital/withdraw/apply"},
		{hostSpot, "POST", "/sapi/v1/asset/transfer"},
		{hostSpot, "POST", "/sapi/v1/futures/transfer"},
		{hostSpot, "POST", "/sapi/v1/margin/loan"},
		{hostUSDM, "POST", "/fapi/v1/leverage"},
		{hostUSDM, "POST", "/fapi/v1/marginType"},
		{hostUSDM, "POST", "/fapi/v1/positionSide/dual"},
		{hostUSDM, "POST", "/fapi/v1/batchOrders"},
		{hostUSDM, "DELETE", "/fapi/v1/allOpenOrders"},
		{hostSpot, "GET", "/sapi/v1/capital/withdraw/history/../apply"},
		{hostSpot, "POST", "/api/v3/order/oco"},
		{hostCoinM, "POST", "/dapi/v1/leverage"},
	} {
		err := c.signed(ctx, call.host, call.method, call.path, nil, nil)
		if !errors.Is(err, errNotListed) {
			t.Errorf("%s %s%s: got %v, want refused", call.method, call.host, call.path, err)
		}
	}
	// A public call cannot reach a signed endpoint and the other way round.
	if err := c.public(ctx, hostSpot, "/api/v3/order", nil, nil); !errors.Is(err, errNotListed) {
		t.Errorf("public order call: %v", err)
	}
	if n := len(f.requests); n != 0 {
		t.Fatalf("refused calls reached the server %d times", n)
	}
}
