package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/sdk/go/ext"
	"github.com/nwverse/persona/sdk/go/wire"
)

func TestWishlistTargetStoreCRUDIsolationPersistenceAndLimits(t *testing.T) {
	dir := t.TempDir()
	store := NewWishlistTargetStore(dir)
	at := time.Date(2026, 11, 28, 12, 0, 0, 0, time.UTC)

	// Missing entry returns ok=false without creating files on disk.
	if _, ok, err := store.Get(fixtureID, firstApp); err != nil || ok {
		t.Fatalf("empty store Get returned ok=%t err=%v", ok, err)
	}
	if _, err := os.Stat(filepath.Join(dir, storeDirectory)); !os.IsNotExist(err) {
		t.Fatalf("read-only Get created store directory: %v", err)
	}

	// Set and read back across two isolated accounts.
	t1 := WishlistTarget{AppID: firstApp, Currency: "CNY", MaxUnits: 9900, UpdatedAt: at}
	t2 := WishlistTarget{AppID: firstApp, Currency: "USD", MaxUnits: 1999, UpdatedAt: at.Add(time.Minute)}
	requireStoreOK(t, store.Set(fixtureID, t1))
	requireStoreOK(t, store.Set(fixtureID2, t2))

	got1, ok, err := store.Get(fixtureID, firstApp)
	if err != nil || !ok || got1 != t1 {
		t.Fatalf("account 1 Get = %+v, ok=%t, err=%v; want %+v", got1, ok, err, t1)
	}
	got2, ok, err := store.Get(fixtureID2, firstApp)
	if err != nil || !ok || got2 != t2 {
		t.Fatalf("account 2 Get = %+v, ok=%t, err=%v; want %+v", got2, ok, err, t2)
	}

	// Persistence across store reopen.
	reopened := NewWishlistTargetStore(dir)
	list1, err := reopened.List(fixtureID)
	if err != nil || len(list1) != 1 || list1[firstApp] != t1 {
		t.Fatalf("reopened List(account 1) = %+v, err=%v", list1, err)
	}

	// Update existing entry in place.
	updated := WishlistTarget{AppID: firstApp, Currency: "CNY", MaxUnits: 4950, UpdatedAt: at.Add(2 * time.Minute)}
	requireStoreOK(t, reopened.Set(fixtureID, updated))
	got1, ok, err = reopened.Get(fixtureID, firstApp)
	if err != nil || !ok || got1 != updated {
		t.Fatalf("updated Get = %+v, ok=%t, err=%v", got1, ok, err)
	}

	// Delete removes only the target entry for that account.
	requireStoreOK(t, reopened.Delete(fixtureID, firstApp))
	if _, ok, err := reopened.Get(fixtureID, firstApp); err != nil || ok {
		t.Fatalf("deleted target still exists: ok=%t err=%v", ok, err)
	}
	if _, ok, err := reopened.Get(fixtureID2, firstApp); err != nil || !ok {
		t.Fatalf("deleting account 1 affected account 2: ok=%t err=%v", ok, err)
	}

	// ClearAccount clears only that account.
	requireStoreOK(t, reopened.Set(fixtureID, t1))
	requireStoreOK(t, reopened.Set(fixtureID, WishlistTarget{AppID: secondApp, Currency: "CNY", MaxUnits: 5000, UpdatedAt: at}))
	requireStoreOK(t, reopened.ClearAccount(fixtureID))
	list1, err = reopened.List(fixtureID)
	if err != nil || len(list1) != 0 {
		t.Fatalf("ClearAccount left entries: %+v, err=%v", list1, err)
	}
	list2, err := reopened.List(fixtureID2)
	if err != nil || len(list2) != 1 {
		t.Fatalf("ClearAccount affected account 2: %+v, err=%v", list2, err)
	}

	// Validation rejections.
	for _, bad := range []struct {
		id     string
		target WishlistTarget
	}{
		{"not-a-steamid", t1},
		{fixtureID, WishlistTarget{AppID: 0, Currency: "USD", MaxUnits: 100, UpdatedAt: at}},
		{fixtureID, WishlistTarget{AppID: firstApp, Currency: "usd", MaxUnits: 100, UpdatedAt: at}},
		{fixtureID, WishlistTarget{AppID: firstApp, Currency: "US", MaxUnits: 100, UpdatedAt: at}},
		{fixtureID, WishlistTarget{AppID: firstApp, Currency: "USD", MaxUnits: 0, UpdatedAt: at}},
		{fixtureID, WishlistTarget{AppID: firstApp, Currency: "USD", MaxUnits: 100}},
	} {
		if err := reopened.Set(bad.id, bad.target); err == nil {
			t.Errorf("Set accepted invalid target (%q, %+v)", bad.id, bad.target)
		}
	}

	// Per-account limit enforcement (200 entries) while still allowing updates to existing entries.
	limitStore := NewWishlistTargetStore("")
	for i := range maxWishlistTargetsPerAccount {
		requireStoreOK(t, limitStore.Set(fixtureID, WishlistTarget{
			AppID: uint32(1000 + i), Currency: "USD", MaxUnits: 999, UpdatedAt: at,
		}))
	}
	if err := limitStore.Set(fixtureID, WishlistTarget{
		AppID: uint32(1000 + maxWishlistTargetsPerAccount), Currency: "USD", MaxUnits: 999, UpdatedAt: at,
	}); err == nil {
		t.Fatal("Set exceeded per-account target limit")
	}
	// Updating an existing entry at the cap still succeeds.
	requireStoreOK(t, limitStore.Set(fixtureID, WishlistTarget{
		AppID: 1000, Currency: "USD", MaxUnits: 599, UpdatedAt: at.Add(time.Minute),
	}))
	// Another account still has its own budget.
	requireStoreOK(t, limitStore.Set(fixtureID2, WishlistTarget{
		AppID: 1000, Currency: "CNY", MaxUnits: 5900, UpdatedAt: at,
	}))

	// Corrupt file rejection.
	badDir := t.TempDir()
	badStore := NewWishlistTargetStore(badDir)
	badPath := filepath.Join(badDir, storeDirectory, "wishlist-targets.json")
	requireStoreOK(t, os.MkdirAll(filepath.Dir(badPath), 0o700))
	requireStoreOK(t, os.WriteFile(badPath, []byte(`{"version":99,"entries":{}}`), 0o600))
	if _, _, err := badStore.Get(fixtureID, firstApp); err == nil {
		t.Fatal("corrupt snapshot version was accepted")
	}
	requireStoreOK(t, os.WriteFile(badPath, []byte(`not-json`), 0o600))
	if _, err := badStore.List(fixtureID); err == nil {
		t.Fatal("corrupt JSON snapshot was accepted")
	}
}

func TestParsePriceUnitsAndFormatting(t *testing.T) {
	valid := []struct {
		input string
		want  uint32
	}{
		{"99", 9900},
		{"19.99", 1999},
		{"19.9", 1990},
		{"0.5", 50},
		{"0.01", 1},
		{" 49.50 ", 4950},
		{"0010.05", 1005},
		{"42949672.95", math.MaxUint32},
	}
	for _, tc := range valid {
		got, ok := parsePriceUnits(tc.input)
		if !ok || got != tc.want {
			t.Errorf("parsePriceUnits(%q) = (%d, %t), want (%d, true)", tc.input, got, ok, tc.want)
		}
	}

	invalid := []string{
		"", "   ", "0", "0.0", "0.00", "-1", "-0.01", "+9.99",
		".99", "19.", "19.999", "1.2.3", "abc", "$9.99", "1,999",
		"1e2", "42949672.96", "999999999999",
	}
	for _, input := range invalid {
		if got, ok := parsePriceUnits(input); ok {
			t.Errorf("parsePriceUnits(%q) unexpectedly accepted -> %d", input, got)
		}
	}

	if got := formatPriceInput(9900); got != "99.00" {
		t.Errorf("formatPriceInput(9900) = %q, want 99.00", got)
	}
	if got := formatPriceInput(1999); got != "19.99" {
		t.Errorf("formatPriceInput(1999) = %q, want 19.99", got)
	}
	for _, tc := range []struct {
		currency string
		units    uint32
		want     string
	}{
		{"CNY", 9900, "¥99.00"},
		{"USD", 999, "$9.99"},
		{"EUR", 1499, "€14.99"},
		{"GBP", 399, "£3.99"},
		{"HKD", 12800, "HK$128.00"},
		{"TWD", 39900, "NT$399.00"},
		{"JPY", 198000, "JPY 1980.00"},
	} {
		if got := formatCurrencyAmount(tc.currency, tc.units); got != tc.want {
			t.Errorf("formatCurrencyAmount(%q, %d) = %q, want %q", tc.currency, tc.units, got, tc.want)
		}
	}
}

func TestWishlistSentinelAndDealsPreferLocalTargetsWithLegacyFallback(t *testing.T) {
	f := newFakeSteam(t, time.Second)
	now := fixtureObservationAt
	dir := t.TempDir()

	// Legacy config sets firstApp <= 500 USD and secondApp <= 600 USD.
	// Local WishlistTargetStore overrides firstApp to <= 200 USD (not met at 400)
	// and adds thirdApp <= 750 USD (met at 700).
	legacyCfg := config{
		TokenConfig:           TokenConfig{InboxEventsEnabled: true},
		WishlistEventTriggers: []string{},
		WishlistTargetPrices:  appIDText(firstApp) + ":USD:500," + appIDText(secondApp) + ":USD:600",
	}
	a := newInboxTestApp(f, dir, func() time.Time { return now }, legacyCfg)
	requireStoreOK(t, a.stores.targets.Set(fixtureID, WishlistTarget{
		AppID: firstApp, Currency: "USD", MaxUnits: 200, UpdatedAt: now,
	}))
	requireStoreOK(t, a.stores.targets.Set(fixtureID, WishlistTarget{
		AppID: thirdApp, Currency: "USD", MaxUnits: 750, UpdatedAt: now,
	}))

	seedInboxWishlist(t, f,
		inboxFixtureGame{firstApp, 400, 0, ""},  // legacy would trigger (<=500), local overrides (<=200 -> NOT met)
		inboxFixtureGame{secondApp, 550, 0, ""}, // legacy fallback triggers (<=600)
		inboxFixtureGame{thirdApp, 700, 0, ""},  // local target triggers (<=750)
	)

	got, changed := scanTestInbox(t, a, &english)
	if !changed || len(got) != 2 {
		t.Fatalf("expected 2 target alerts (legacy fallback + local override), got %+v", got)
	}
	urls := make(map[string]bool, len(got))
	for _, thing := range got {
		urls[thing.URL] = true
		contains(t, thing.Preview, "Target price reached")
	}
	if urls[storeLink(firstApp)] || !urls[storeLink(secondApp)] || !urls[storeLink(thirdApp)] {
		t.Fatalf("unexpected alert URLs: %+v", urls)
	}

	// Verify steam_wishlist_deals AI tool obeys the same precedence.
	h := startInboxTestHost(t, f, &app{}, func() time.Time { return now }, dir, "en",
		fmt.Sprintf(`"inbox_events_enabled":false,"wishlist_target_prices":%q`, legacyCfg.WishlistTargetPrices))
	raw := toolAnswer(t, h, wishlistDealsName, `{}`)
	contains(t, raw, storeLink(secondApp), storeLink(thirdApp), `"target_price_met":true`)
	if strings.Contains(raw, storeLink(firstApp)) {
		t.Fatalf("local override did not supersede legacy config in steam_wishlist_deals: %s", raw)
	}
}

func TestWishlistDetailViewAndTargetPriceActions(t *testing.T) {
	f := newFakeSteam(t, time.Second)
	dir := t.TempDir()
	now := fixtureObservationAt

	// Seed an observed local low for firstApp in CNY and an upcoming unpriced game (thirdApp).
	requireStoreOK(t, NewPriceHistoryStore(dir).Observe(fixtureID, "CN", now.Add(-48*time.Hour), map[uint32]PriceQuote{
		firstApp: {Currency: "CNY", Final: 7900, DiscountPercent: 50},
	}))
	seedInboxWishlist(t, f,
		inboxFixtureGame{firstApp, 9900, 40, `{"name":"Wishlist Alpha","best_purchase_option":{"original_price_in_cents":"16500","formatted_final_price":"¥ 99.00"}}`},
		inboxFixtureGame{secondApp, 6800, 0, `{"name":"Wishlist Beta","best_purchase_option":{"formatted_final_price":"¥ 68.00"}}`},
		inboxFixtureGame{thirdApp, -1, 0, `{"name":"Wishlist Upcoming","release":{"is_coming_soon":true}}`},
	)

	h := startTestHost(t, f, fixtureID, "CN", dir)
	nav := wire.UIParams{Contribution: extensionID, Size: wire.SizeFull, Placement: wire.PlacementNavPage, UIVersion: 12, Locale: "zh-Hans"}

	overview := viewAnswer(t, h, nav)
	wishRows := overview.Sections[3].Items
	if len(wishRows) != 3 || wishRows[0].Action == nil || wishRows[0].Action.ID != "wish-game-"+appIDText(firstApp) {
		t.Fatalf("wishlist rows should be clickable with wish-game-<appid>, got %+v", wishRows)
	}

	// Open wishlist game detail page for firstApp.
	detail := act(t, h, nav, overview, wishRows[0].Action.ID, nil)
	raw, err := json.Marshal(detail)
	requireStoreOK(t, err)
	contains(t, string(raw),
		"Wishlist Alpha", "¥ 99.00", "原价 ¥165.00 · 优惠 40%",
		"¥79.00", "CN · CNY", storeLink(firstApp), communityLink(firstApp),
		"目标提醒价格（CNY）", `"kind":"number"`, `"name":"target_price"`,
		`"id":"wishlist-target-save","submit":true`,
	)
	assertHidden(t, string(raw), wishlistTargetClearAction)

	// Invalid price inputs are rejected with a friendly error.
	for _, bad := range []string{"", "0", "-5", "19.999", "abc", "99999999999"} {
		badReq := nav
		badReq.Action, badReq.State, badReq.Values = wishlistTargetSaveAction, detail.State, map[string]string{wishlistTargetPriceField: bad}
		var e *ext.Error
		if err := h.Call("ui.action", badReq, nil); !errors.As(err, &e) || e.Code != "invalid" {
			t.Errorf("invalid target_price %q was accepted: %v", bad, err)
		}
	}

	// Save a target price that is NOT yet met (¥80.00 < ¥99.00).
	saveReq := nav
	saveReq.Action, saveReq.State, saveReq.Values = wishlistTargetSaveAction, detail.State, map[string]string{wishlistTargetPriceField: "80"}
	var saveRes wire.ActionResult
	requireStoreOK(t, h.Call("ui.action", saveReq, &saveRes))
	requireStoreOK(t, wire.CheckView(saveRes.View))
	contains(t, saveRes.Toast, "¥80.00")
	detail = *saveRes.View
	raw, _ = json.Marshal(detail)
	contains(t, string(raw), `"value":"80.00"`, `"id":"wishlist-target-clear"`)

	// Verify back to overview shows target hint without losing discount badge.
	overview = act(t, h, nav, detail, "back", nil)
	row0 := overview.Sections[3].Items[0]
	if row0.Badge == nil || row0.Badge.Text != "优惠 40%" || !strings.Contains(row0.Subtitle, "目标价 ≤ ¥80.00") {
		t.Fatalf("unmet target row should keep discount badge and show target subtitle: %+v", row0)
	}

	// Save a target price that IS met (¥100.50 >= ¥99.00).
	detail = act(t, h, nav, overview, row0.Action.ID, nil)
	detail = act(t, h, nav, detail, wishlistTargetSaveAction, map[string]string{wishlistTargetPriceField: "100.5"})
	raw, _ = json.Marshal(detail)
	contains(t, string(raw), "已达到目标价", "已达目标价", `"value":"100.50"`)

	overview = act(t, h, nav, detail, "back", nil)
	row0 = overview.Sections[3].Items[0]
	if row0.Badge == nil || row0.Badge.Text != "已达目标价" || row0.Badge.Tone != wire.TonePositive ||
		!strings.Contains(row0.Subtitle, "优惠 40%") || !strings.Contains(row0.Subtitle, "目标价 ≤ ¥100.50") {
		t.Fatalf("met target row should highlight positive badge and retain discount + target info: %+v", row0)
	}

	// Account-change protection rejects stale state from another SteamID.
	other := startTestHost(t, f, fixtureID2, "CN", dir)
	staleReq := nav
	staleReq.Action, staleReq.State = wishlistTargetClearAction, detail.State
	var e *ext.Error
	if err := other.Call("ui.action", staleReq, nil); !errors.As(err, &e) || e.Code != "invalid" {
		t.Fatalf("target clear from another SteamID was accepted: %v", err)
	}

	// Clear target price on firstApp.
	clearReq := nav
	clearReq.Action, clearReq.State = wishlistTargetClearAction, detail.State
	var clearRes wire.ActionResult
	requireStoreOK(t, h.Call("ui.action", clearReq, &clearRes))
	requireStoreOK(t, wire.CheckView(clearRes.View))
	contains(t, clearRes.Toast, "已清除")
	raw, _ = json.Marshal(clearRes.View)
	assertHidden(t, string(raw), wishlistTargetClearAction, "已达到目标价")
	if _, ok, err := NewWishlistTargetStore(dir).Get(fixtureID, firstApp); err != nil || ok {
		t.Fatalf("target still persisted after clear: ok=%t err=%v", ok, err)
	}

	// Upcoming unpriced game allows presetting a target price using the region's currency (CN -> CNY).
	upcomingDetail := act(t, h, nav, overview, "wish-game-"+appIDText(thirdApp), nil)
	raw, _ = json.Marshal(upcomingDetail)
	contains(t, string(raw), "Wishlist Upcoming", "即将推出", "暂无历史记录", "CN · CNY", "目标提醒价格（CNY）")
	upcomingDetail = act(t, h, nav, upcomingDetail, wishlistTargetSaveAction, map[string]string{wishlistTargetPriceField: "128.00"})
	got, ok, err := NewWishlistTargetStore(dir).Get(fixtureID, thirdApp)
	if err != nil || !ok || got.Currency != "CNY" || got.MaxUnits != 12800 {
		t.Fatalf("upcoming target was not saved with regional currency: %+v, ok=%t, err=%v", got, ok, err)
	}
}
