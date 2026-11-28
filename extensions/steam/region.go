package main

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"
	"time"
)

const (
	userCountryPath     = "/IUserAccountService/GetUserCountry/v1/"
	mobileSummariesPath = "/ISteamUserOAuth/GetUserSummaries/v1/"

	countryLookupBudget   = 2500 * time.Millisecond
	accountCountryTimeout = 1500 * time.Millisecond
	countryResponseBytes  = 32 << 10
)

func (a *app) storeAccount() *steamData {
	if d := a.currentQRData(); d != nil {
		return d
	}
	return a.steamData
}

// Resolve an account's country within the caller's Steam read budget. An ID
// from an existing account read lets Web API key mode reuse that profile.
func (d *steamData) storeCountry(ctx context.Context, id string) string {
	if country := d.config.CountryCode; validCountryCode(country) {
		return country
	}
	if d.setup != "" {
		return defaultCountry
	}

	ctx, cancel := context.WithTimeout(ctx, countryLookupBudget)
	defer d.cancelOnSessionEnd(cancel)()
	if d.session != nil {
		return d.qrStoreCountry(ctx)
	}

	var profile reading[player]
	if id == "" {
		profile = d.account(ctx, profileTTL, false)
	} else {
		profile = d.readPlayer(ctx, id, profileTTL, false)
	}
	if p := profile.Data; p != nil && p.Public && p.ProfileCountry != nil && validCountryCode(*p.ProfileCountry) {
		return *p.ProfileCountry
	}
	return defaultCountry
}

func (d *steamData) qrStoreCountry(ctx context.Context) string {
	// Leave time for the mobile profile lookup if the account endpoint stalls.
	accountCtx, cancel := context.WithTimeout(ctx, accountCountryTimeout)
	country := d.readQRCountry(accountCtx, d.qrCountry, steamClient.accountStoreCountry)
	cancel()
	if country != nil {
		return *country
	}
	if location := d.readQRCountry(ctx, d.qrLocation, steamClient.mobileProfileCountry); location != nil {
		return *location
	}
	return defaultCountry
}

func (d *steamData) readQRCountry(ctx context.Context, slot *cacheSlot[string], fetch func(steamClient, context.Context, string) (string, problem)) *string {
	id := d.config.SteamID
	r := slot.read(ctx, id, profileTTL, false, func(ctx context.Context) (*string, problem) {
		c, issue := d.readClient(ctx)
		if issue == "" {
			var country string
			country, issue = fetch(c, ctx, id)
			if issue == "" && country != "" {
				return &country, ""
			}
		}
		// Even a rejected optional endpoint may recover. Keep its last country
		// available during backoff and retry on later reads.
		if issue.temporary() || issue == problemCanceled {
			return nil, issue
		}
		return nil, problemResponse
	})
	return r.Data
}

func (c steamClient) accountStoreCountry(ctx context.Context, id string) (string, problem) {
	var reply struct {
		Response struct {
			Country json.RawMessage `json:"country"`
		} `json:"response"`
	}
	form := url.Values{"steamid": {id}}
	issue := c.readBoundedJSON(ctx, apiBase+userCountryPath, nil, form, true, countryResponseBytes, &reply)
	return apiCountryCode(reply.Response.Country), issue
}

func (c steamClient) mobileProfileCountry(ctx context.Context, id string) (string, problem) {
	// GetPlayerLinkDetails omits profile location. The mobile OAuth summary
	// accepts an access token and supplies loccountrycode when available.
	var reply struct {
		Players []struct {
			SteamID string          `json:"steamid"`
			Country json.RawMessage `json:"loccountrycode"`
		} `json:"players"`
	}
	query := url.Values{"steamids": {id}}
	if issue := c.readBoundedJSON(ctx, apiBase+mobileSummariesPath, query, nil, true, countryResponseBytes, &reply); issue != "" {
		return "", issue
	}
	for _, p := range reply.Players {
		if p.SteamID == id {
			return apiCountryCode(p.Country), ""
		}
	}
	return "", ""
}

func apiCountryCode(raw json.RawMessage) string {
	var country string
	if json.Unmarshal(raw, &country) == nil {
		country = strings.ToUpper(strings.TrimSpace(country))
		if validCountryCode(country) {
			return country
		}
	}
	return ""
}
