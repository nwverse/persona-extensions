package main

import (
	"context"
	"encoding/json"
	"maps"
	"net/url"
	"slices"
	"strconv"
)

const storeBrowseBytes = 1 << 20

type storeDataRequest struct {
	BestPurchase bool `json:"include_best_purchase_option"`
	Release      bool `json:"include_release,omitempty"`
	Assets       bool `json:"include_assets,omitempty"`
	BasicInfo    bool `json:"include_basic_info,omitempty"`
	Platforms    bool `json:"include_platforms,omitempty"`
	TagCount     int  `json:"include_tag_count,omitempty"`
}

type storeBrowseItem struct {
	ID         uint32           `json:"id"`
	AppID      uint32           `json:"appid"`
	Success    int              `json:"success"`
	Restricted bool             `json:"unvailable_for_country_restriction"`
	Name       string           `json:"name"`
	IsFree     bool             `json:"is_free"`
	ComingSoon *bool            `json:"is_coming_soon"`
	Release    *storeRelease    `json:"release"`
	Assets     *storeAssets     `json:"assets"`
	Type       int              `json:"type"`
	TagIDs     []uint32         `json:"tagids"`
	Categories *storeCategories `json:"categories"`
	BasicInfo  struct {
		Description string `json:"short_description"`
	} `json:"basic_info"`
	Platforms struct {
		Windows bool `json:"windows"`
		Mac     bool `json:"mac"`
		Linux   bool `json:"steamos_linux"`
		Deck    int  `json:"steam_deck_compat_category"`
	} `json:"platforms"`
	Related struct {
		Demos         []storeDemo `json:"demos"`
		Standalone    []storeDemo `json:"standalone_demos"`
		DemoIDs       []uint32    `json:"demo_appid"`
		StandaloneIDs []uint32    `json:"standalone_demo_appid"`
	} `json:"related_items"`
	FreeWeekend *struct {
		Start uint32 `json:"start_time"`
		End   uint32 `json:"end_time"`
	} `json:"free_weekend"`

	BestPurchase  *storePurchaseOption `json:"-"`
	purchaseIssue problem
}

type storeRelease struct {
	ComingSoon        *bool  `json:"is_coming_soon"`
	SteamDate         uint32 `json:"steam_release_date"`
	IsPreload         bool   `json:"is_preload"`
	Limited           bool   `json:"limited_launch_active"`
	CustomDate        string `json:"custom_release_date_message"`
	ComingSoonDisplay string `json:"coming_soon_display"`
}

type storeCategories struct {
	Players     []uint32 `json:"supported_player_categoryids"`
	Features    []uint32 `json:"feature_categoryids"`
	Controllers []uint32 `json:"controller_categoryids"`
}

type storeAssets struct {
	URLFormat    string `json:"asset_url_format"`
	Header       string `json:"header"`
	SmallCapsule string `json:"small_capsule"`
	MainCapsule  string `json:"main_capsule"`
}

type storeDemo struct {
	AppID uint32 `json:"appid"`
}

type storePurchaseOption struct {
	Final         json.Number `json:"final_price_in_cents"`
	Original      json.Number `json:"original_price_in_cents"`
	Formatted     string      `json:"formatted_final_price"`
	Discount      int         `json:"discount_pct"`
	IsFreeToKeep  bool        `json:"is_free_to_keep"`
	FreeToKeepEnd uint32      `json:"free_to_keep_ends"`
	Discounts     []struct {
		End uint32 `json:"discount_end_date"`
	} `json:"active_discounts"`
}

func (c steamClient) browseStore(ctx context.Context, ids []uint32, country, language string, request storeDataRequest) (map[uint32]storeBrowseItem, problem) {
	items := make(map[uint32]storeBrowseItem, len(ids))
	var issue problem
	for _, batch := range runBatches(ctx, ids, storeBatchSize, batchTimeout, func(ctx context.Context, batch []uint32) (map[uint32]storeBrowseItem, problem) {
		return c.browseStoreBatch(ctx, batch, country, language, request)
	}) {
		maps.Copy(items, batch.value)
		if issue == "" {
			issue = batch.issue
		}
	}
	return items, issue
}

func (c steamClient) browseStoreBatch(ctx context.Context, ids []uint32, country, language string, request storeDataRequest) (map[uint32]storeBrowseItem, problem) {
	type appRef struct {
		AppID uint32 `json:"appid"`
	}
	apps := make([]appRef, 0, len(ids))
	for _, id := range ids {
		apps = append(apps, appRef{id})
	}
	input, _ := json.Marshal(struct {
		IDs     []appRef          `json:"ids"`
		Context map[string]string `json:"context"`
		Request storeDataRequest  `json:"data_request"`
	}{apps, map[string]string{"country_code": country, "language": language}, request})
	var reply struct {
		Response struct {
			Items []json.RawMessage `json:"store_items"`
		} `json:"response"`
	}
	query := url.Values{"input_json": {string(input)}}
	if issue := c.readBoundedJSON(ctx, apiBase+storeNamesPath, query, nil, false, storeBrowseBytes, &reply); issue != "" {
		return nil, issue
	}
	items := make(map[uint32]storeBrowseItem, len(ids))
	var issue problem
	for _, raw := range reply.Response.Items {
		var wire struct {
			storeBrowseItem
			Purchase json.RawMessage `json:"best_purchase_option"`
		}
		if json.Unmarshal(raw, &wire) != nil {
			continue
		}
		item := wire.storeBrowseItem
		id := item.AppID
		if id == 0 {
			id = item.ID // Rejected app lookups can omit appid.
		}
		if !slices.Contains(ids, id) {
			continue
		}
		switch item.Success {
		case 1:
			item.AppID, item.Name = id, c.text(item.Name)
			if len(wire.Purchase) > 0 && json.Unmarshal(wire.Purchase, &item.BestPurchase) != nil {
				item.purchaseIssue = problemResponse
			}
			items[id] = item
		case 8, 9, 15: // Invalid parameter, file not found, or access denied for this app.
			if _, found := items[id]; !found {
				items[id] = storeBrowseItem{AppID: id, Success: item.Success}
			}
		case 16:
			if issue == "" {
				issue = problemTimeout
			}
		case 25, 84:
			if issue == "" {
				issue = problemRateLimited
			}
		}
	}
	if len(items) != len(ids) && issue == "" {
		issue = problemResponse
	}
	return items, issue
}

func (c steamClient) browsePrice(item storeBrowseItem, country string) (*storePrice, problem) {
	if item.purchaseIssue != "" {
		return nil, item.purchaseIssue
	}
	p := item.BestPurchase
	if item.Restricted || p == nil || p.Final == "" || p.Final == "-1" {
		return nil, ""
	}
	final, err := strconv.ParseUint(p.Final.String(), 10, 32)
	if err != nil || p.Discount < 0 || p.Discount > 100 {
		return nil, problemResponse
	}
	if p.Original != "" && p.Original != "-1" {
		if _, err := strconv.ParseUint(p.Original.String(), 10, 32); err != nil {
			return nil, problemResponse
		}
	}
	return &storePrice{Currency: storeCurrency(country), Final: uint32(final), FinalFormatted: c.text(p.Formatted), DiscountPercent: p.Discount}, ""
}

// StoreBrowse formats the regional quote but does not send a currency code in
// the purchase option. Steam's published currencies use integer hundredths.
func storeCurrency(country string) string {
	if currency := localStoreCurrency[country]; currency != "" {
		return currency
	}
	switch country {
	case "AD", "AL", "AT", "AX", "BA", "BE", "BG", "CY", "CZ", "DE", "DK", "EE", "ES", "FI", "FO", "FR", "GI", "GR", "HR", "HU", "IE",
		"IT", "LT", "LU", "LV", "MC", "ME", "MK", "MT", "NL", "PT", "RO", "RS", "SE", "SI", "SJ", "SK", "SM", "VA":
		return "EUR"
	default:
		return "USD"
	}
}

var localStoreCurrency = map[string]string{
	"AE": "AED", "AU": "AUD", "BR": "BRL", "CA": "CAD", "CH": "CHF", "CL": "CLP", "CN": "CNY", "CO": "COP",
	"CR": "CRC", "GB": "GBP", "GG": "GBP", "HK": "HKD", "ID": "IDR", "IL": "ILS", "IM": "GBP", "IN": "INR", "JE": "GBP", "JP": "JPY", "KR": "KRW",
	"KW": "KWD", "KZ": "KZT", "LI": "CHF", "MX": "MXN", "MY": "MYR", "NO": "NOK", "NZ": "NZD", "PE": "PEN",
	"PH": "PHP", "PL": "PLN", "QA": "QAR", "RU": "RUB", "SA": "SAR", "SG": "SGD", "TH": "THB", "TW": "TWD",
	"UA": "UAH", "UY": "UYU", "VN": "VND", "ZA": "ZAR",
}
