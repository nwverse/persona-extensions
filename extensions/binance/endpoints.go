package main

// Every Binance REST endpoint the extension may call, and nothing else: the client refuses any method and path that
// is not in this table (endpointFor), so a withdrawal, transfer, loan or leverage change cannot be sent even by a bug.
// endpoints_test.go holds the table (and every path literal in the source) to that.

const (
	marketSpot  = "spot"
	marketUSDM  = "usdm"
	marketCoinM = "coinm"
)

const (
	hostSpot   = "https://api.binance.com"
	hostUSDM   = "https://fapi.binance.com"
	hostCoinM  = "https://dapi.binance.com"
	hostPublic = "https://data-api.binance.vision"
)

// access says what an endpoint does: read (public or signed account data), or one of the two writes.
type access int

const (
	accessPublic access = iota // no key
	accessRead                 // signed, USER_DATA
	accessOrder                // signed, places one order
	accessCancel               // signed, cancels one order
)

type endpoint struct {
	Host   string
	Method string
	Path   string
	Access access
}

// endpoints is the whole list. Keep it short; every addition needs a reason in review.
var endpoints = []endpoint{
	// Public market data.
	{hostPublic, "GET", "/api/v3/ping", accessPublic},
	{hostPublic, "GET", "/api/v3/ticker/price", accessPublic},
	{hostPublic, "GET", "/api/v3/ticker/24hr", accessPublic},
	{hostPublic, "GET", "/api/v3/exchangeInfo", accessPublic},
	{hostSpot, "GET", "/api/v3/time", accessPublic},
	{hostUSDM, "GET", "/fapi/v1/exchangeInfo", accessPublic},
	{hostUSDM, "GET", "/fapi/v1/premiumIndex", accessPublic},
	{hostCoinM, "GET", "/dapi/v1/exchangeInfo", accessPublic},
	{hostCoinM, "GET", "/dapi/v1/premiumIndex", accessPublic},

	// Account reads.
	{hostSpot, "GET", "/sapi/v1/account/apiRestrictions", accessRead},
	{hostSpot, "GET", "/api/v3/account", accessRead},
	{hostSpot, "GET", "/api/v3/openOrders", accessRead},
	{hostSpot, "GET", "/api/v3/order", accessRead},
	{hostSpot, "GET", "/api/v3/myTrades", accessRead},
	{hostSpot, "POST", "/sapi/v1/asset/get-funding-asset", accessRead}, // a POST that only reads the funding wallet
	{hostSpot, "GET", "/sapi/v1/simple-earn/flexible/position", accessRead},
	{hostSpot, "GET", "/sapi/v1/simple-earn/locked/position", accessRead},
	{hostSpot, "GET", "/sapi/v1/capital/deposit/hisrec", accessRead},
	{hostSpot, "GET", "/sapi/v1/capital/withdraw/history", accessRead}, // history only: there is no way to withdraw
	{hostUSDM, "GET", "/fapi/v3/account", accessRead},
	{hostUSDM, "GET", "/fapi/v3/positionRisk", accessRead},
	{hostUSDM, "GET", "/fapi/v1/symbolConfig", accessRead},
	{hostUSDM, "GET", "/fapi/v1/positionSide/dual", accessRead},
	{hostUSDM, "GET", "/fapi/v1/openOrders", accessRead},
	{hostUSDM, "GET", "/fapi/v1/order", accessRead},
	{hostUSDM, "GET", "/fapi/v1/userTrades", accessRead},
	{hostUSDM, "GET", "/fapi/v1/income", accessRead},
	{hostCoinM, "GET", "/dapi/v1/account", accessRead},
	{hostCoinM, "GET", "/dapi/v1/positionRisk", accessRead},
	{hostCoinM, "GET", "/dapi/v1/positionSide/dual", accessRead},
	{hostCoinM, "GET", "/dapi/v1/openOrders", accessRead},
	{hostCoinM, "GET", "/dapi/v1/order", accessRead},
	{hostCoinM, "GET", "/dapi/v1/userTrades", accessRead},

	// The only writes: one order, one cancel, per market.
	{hostSpot, "POST", "/api/v3/order", accessOrder},
	{hostSpot, "DELETE", "/api/v3/order", accessCancel},
	{hostUSDM, "POST", "/fapi/v1/order", accessOrder},
	{hostUSDM, "DELETE", "/fapi/v1/order", accessCancel},
	{hostCoinM, "POST", "/dapi/v1/order", accessOrder},
	{hostCoinM, "DELETE", "/dapi/v1/order", accessCancel},
}

// endpointFor finds host, method and path in the table.
func endpointFor(host, method, path string) (endpoint, bool) {
	for _, e := range endpoints {
		if e.Host == host && e.Method == method && e.Path == path {
			return e, true
		}
	}
	return endpoint{}, false
}

// marketHost is a market's REST host.
func marketHost(market string) string {
	switch market {
	case marketUSDM:
		return hostUSDM
	case marketCoinM:
		return hostCoinM
	}
	return hostSpot
}

// orderPath is the order endpoint's path of a market.
func orderPath(market string) string {
	switch market {
	case marketUSDM:
		return "/fapi/v1/order"
	case marketCoinM:
		return "/dapi/v1/order"
	}
	return "/api/v3/order"
}
