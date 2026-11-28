package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"
)

type fixtureStoreBrowseRequest struct {
	IDs []struct {
		AppID uint32 `json:"appid"`
	} `json:"ids"`
	Context     map[string]string `json:"context"`
	DataRequest map[string]any    `json:"data_request"`
}

func fixtureBrowseInput(t *testing.T, r *http.Request) fixtureStoreBrowseRequest {
	t.Helper()
	var input fixtureStoreBrowseRequest
	if err := json.Unmarshal([]byte(r.URL.Query().Get("input_json")), &input); err != nil || len(input.IDs) == 0 {
		t.Errorf("invalid StoreBrowse request: %v", err)
	}
	return input
}

func seedWishlistIDs(t *testing.T, f *fakeSteam, items []wishItem) {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"response": map[string]any{"items": items}})
	if err != nil {
		t.Fatal(err)
	}
	f.reply(wishlistPath, http.StatusOK, string(raw))
}

func TestWishlistPricesUseOnlyStoreBrowse(t *testing.T) {
	f := newFakeSteam(t, time.Second)
	seedWishlistIDs(t, f, []wishItem{
		{AppID: firstApp + 3, Priority: 3}, {AppID: firstApp, Priority: 1}, {AppID: secondApp, Priority: 2}, {AppID: thirdApp, Priority: 4},
		{AppID: firstApp + 4, Priority: 5},
	})
	f.handle(storeNamesPath, func(w http.ResponseWriter, r *http.Request) {
		input := fixtureBrowseInput(t, r)
		if len(input.IDs) != 5 || input.IDs[0].AppID != firstApp || input.Context["country_code"] != "JP" || input.Context["language"] != "schinese" {
			t.Errorf("incorrect regional wishlist batch: %+v", input)
		}
		for _, flag := range []string{"include_best_purchase_option", "include_release", "include_assets"} {
			if input.DataRequest[flag] != true {
				t.Errorf("wishlist did not request %s in the same StoreBrowse call", flag)
			}
		}
		_, _ = fmt.Fprintf(w, `{"response":{"store_items":[
			{"appid":%d,"success":1,"name":"Fixture Sale","release":{"steam_release_date":1700000000},
			 "assets":{"asset_url_format":"steam/apps/fixture/${FILENAME}","header":"header.jpg","small_capsule":"small.jpg","main_capsule":"main.jpg"},
			 "best_purchase_option":{"final_price_in_cents":"70000","original_price_in_cents":280000,"discount_pct":75,"formatted_final_price":"¥700"}},
			{"appid":%d,"success":1,"name":"Fixture Free","is_free":true},
			{"appid":%d,"success":1,"name":"Fixture Upcoming","is_coming_soon":true},
			{"appid":%d,"success":1,"name":"Fixture Giveaway","best_purchase_option":{"final_price_in_cents":0,"original_price_in_cents":"100000","discount_pct":100,"is_free_to_keep":true,"free_to_keep_ends":2100000000}},
			{"id":%d,"appid":0,"success":15}
		]}}`, firstApp, secondApp, firstApp+3, thirdApp, firstApp+4)
	})
	c := steamClient{http: f.http, key: f.key}
	list, issue := c.wishlist(t.Context(), fixtureID, "JP", "schinese")
	if f.hit(storeNamesPath) != 1 || f.hit(storePricesPath) != 0 || issue != "" || list == nil || list.Checked != 5 || list.Discounted != 2 {
		t.Fatalf("expected five checked prices in one StoreBrowse call, got %+v, %s", list, issue)
	}
	price := list.Items[0].Price
	if price == nil || price.Currency != "JPY" || price.Final != 70000 || price.FinalFormatted != "¥700" || price.DiscountPercent != 75 {
		t.Fatalf("incorrect JPY sale quote: %+v", price)
	}
	for _, item := range []wishItem{list.Items[1], list.Items[2], list.Items[4]} {
		if !item.PriceChecked || item.Price != nil {
			t.Fatalf("unpriced item should be checked with no quote: %+v", item)
		}
	}
	if price := list.Items[3].Price; price == nil || price.Final != 0 || price.DiscountPercent != 100 {
		t.Fatalf("a zero final price was lost: %+v", price)
	}
	meta := list.store[firstApp]
	if meta.Assets == nil || *meta.Assets != (storeAssets{"steam/apps/fixture/${FILENAME}", "header.jpg", "small.jpg", "main.jpg"}) ||
		meta.BestPurchase.Original.String() != "280000" || meta.Release == nil || meta.Release.SteamDate != 1700000000 {
		t.Fatalf("the wishlist discarded StoreBrowse metadata: %+v", meta)
	}
	if giveaway, end := list.store[thirdApp].promotion(list.Items[3].Price, fixtureObservationAt); !giveaway || end != 2100000000 {
		t.Fatalf("the Inbox lost the promotion or its expiry: %t, %d", giveaway, end)
	}
	for _, locale := range []*text{&english, &chinese} {
		section := wishlistSection(locale, reading[wishList]{Data: list}, &pageState{})
		if section.Items[1].Value != locale.freeToPlay || section.Items[2].Subtitle != locale.noPrice {
			t.Fatalf("wishlist UI lost the free and unpriced states: %+v", section.Items)
		}
	}
}

func TestWishlistExpiredSnapshotAfterSteamTimeout(t *testing.T) {
	f := newFakeSteam(t, 250*time.Millisecond)
	now := fixtureObservationAt
	d := f.data(t.TempDir(), fixtureID, "US", func() time.Time { return now })
	p := d.account(t.Context(), profileTTL, false)
	first := d.wishlist(t.Context(), p, "english", false)
	if first.Data == nil || first.Data.Checked != 3 {
		t.Fatalf("the first wishlist did not populate the cache: %+v", first)
	}
	now = now.Add(wishlistTTL + time.Minute)
	newApp := firstApp + 10
	seedWishlistIDs(t, f, []wishItem{{AppID: firstApp, Priority: 1}, {AppID: newApp, Priority: 2}})
	f.handle(storeNamesPath, func(_ http.ResponseWriter, r *http.Request) { <-r.Context().Done() })
	got := d.wishlist(t.Context(), p, "english", true)
	if got.Data == nil || !got.Stale || got.Issue != problemTimeout || got.At != first.At || got.Data.Checked != 1 || got.Data.Total != 2 {
		t.Fatalf("the wishlist did not return its expired quotes with a stale warning: %+v", got)
	}
	if got.Data.Items[0].Price == nil || got.Data.Items[0].Price.Final != 399 || got.Data.Items[1].AppID != newApp || got.Data.Items[1].PriceChecked {
		t.Fatalf("stale quotes must only fill matching current wishlist entries: %+v", got.Data.Items)
	}
	section := wishlistSection(&chinese, got, &pageState{})
	if section.Items[0].Value != "$3.99" || section.Footer != chinese.message(problemTimeout, true) {
		t.Fatalf("the page did not display the stale price and warning: %+v", section)
	}
}

func TestWishlistSlowBatchKeepsOtherPriorityBatches(t *testing.T) {
	f := newFakeSteam(t, 8*time.Second)
	items := make([]wishItem, (steamConcurrency+1)*storeBatchSize)
	for i := range items {
		// Return the wishlist in reverse order to verify the output uses priority.
		priority := len(items) - 1 - i
		items[i] = wishItem{AppID: firstApp + uint32(priority), Priority: uint32(priority)}
	}
	seedWishlistIDs(t, f, items)
	started := make(chan uint32, steamConcurrency+1)
	release := make(chan struct{})
	f.handle(storeNamesPath, func(w http.ResponseWriter, r *http.Request) {
		input := fixtureBrowseInput(t, r)
		if len(input.IDs) != storeBatchSize {
			t.Errorf("wishlist sent a batch of %d", len(input.IDs))
			return
		}
		id := input.IDs[0].AppID
		started <- id
		if id == firstApp {
			<-r.Context().Done()
			return
		}
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		rows := make([]map[string]any, 0, len(input.IDs))
		for _, app := range input.IDs {
			rows = append(rows, map[string]any{"appid": app.AppID, "success": 1, "name": "Fixture name",
				"best_purchase_option": map[string]any{"final_price_in_cents": "100"}})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"response": map[string]any{"store_items": rows}})
	})
	ctx, cancel := context.WithTimeout(t.Context(), 7*time.Second)
	defer cancel()
	result := make(chan reading[wishList], 1)
	start := time.Now()
	go func() {
		list, issue := (steamClient{http: f.http}).wishlist(ctx, fixtureID, "US", "english")
		result <- reading[wishList]{Data: list, Issue: issue}
	}()
	for range steamConcurrency {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("wishlist batches did not run concurrently")
		}
	}
	close(release)
	got := <-result
	if time.Since(start) > batchTimeout+1500*time.Millisecond || got.Issue != problemTimeout || got.Data == nil || got.Data.Checked != len(items)-storeBatchSize {
		t.Fatalf("a single hung batch lost completed results or consumed the outer budget: elapsed=%s, issue=%s, data=%v", time.Since(start), got.Issue, got.Data != nil)
	}
	if f.hit(storeNamesPath) != steamConcurrency+1 || f.hit(storePricesPath) != 0 {
		t.Fatal("wishlist did not finish the remaining StoreBrowse batches")
	}
	for i, item := range got.Data.Items {
		if item.AppID != firstApp+uint32(i) || item.PriceChecked != (i >= storeBatchSize) || (item.Price != nil) != item.PriceChecked {
			t.Fatalf("wrong result at priority %d: %+v", i, item)
		}
	}
}
