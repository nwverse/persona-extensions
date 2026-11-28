package main

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
		TokenConfig:          TokenConfig{InboxEventsEnabled: true},
		WishlistEventTriggers: []string{},
		WishlistTargetPrices: appIDText(firstApp) + ":USD:500," + appIDText(secondApp) + ":USD:600",
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
