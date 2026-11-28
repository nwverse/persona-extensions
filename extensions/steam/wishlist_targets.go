package main

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"
)

const (
	maxWishlistTargetsPerAccount = 200
	maxWishlistTargetsTotal      = 2_000

	problemWishlistTargetStore problem = "wishlist_target_store_unavailable"
)

// WishlistTarget stores an explicit per-game price alert threshold in Steam minor currency units.
type WishlistTarget struct {
	AppID     uint32    `json:"appid"`
	Currency  string    `json:"currency"`
	MaxUnits  uint32    `json:"max_units"`
	UpdatedAt time.Time `json:"updated_at"`
}

// WishlistTargetStore persists per-account wishlist target prices atomically under steam-store-v1.
type WishlistTargetStore struct {
	file *snapshotFile[WishlistTarget]
}

func NewWishlistTargetStore(dataDir string) *WishlistTargetStore {
	return &WishlistTargetStore{file: newSnapshotFile[WishlistTarget](dataDir, "wishlist-targets")}
}

func wishlistTargetStoreKey(steamID string, appID uint32) (string, error) {
	if !validSteamID(steamID) || appID == 0 {
		return "", errors.New("wishlist target requires a SteamID64 and AppID")
	}
	return steamID + "/" + appIDText(appID), nil
}

func validCurrencyCode(code string) bool {
	if len(code) != 3 {
		return false
	}
	for i := range 3 {
		if code[i] < 'A' || code[i] > 'Z' {
			return false
		}
	}
	return true
}

func validateWishlistTarget(target WishlistTarget) error {
	if target.AppID == 0 {
		return errors.New("wishlist target requires a positive AppID")
	}
	if !validCurrencyCode(target.Currency) {
		return errors.New("wishlist target requires a three-letter uppercase currency code")
	}
	if target.MaxUnits == 0 {
		return errors.New("wishlist target price must be positive")
	}
	if !validSnapshotTime(target.UpdatedAt) {
		return errors.New("wishlist target requires an updated timestamp")
	}
	return nil
}

func (s *WishlistTargetStore) Get(steamID string, appID uint32) (WishlistTarget, bool, error) {
	key, err := wishlistTargetStoreKey(steamID, appID)
	if err != nil {
		return WishlistTarget{}, false, err
	}
	var target WishlistTarget
	var exists bool
	err = s.file.view(func(entries map[string]WishlistTarget) {
		target, exists = entries[key]
	})
	if err != nil || !exists {
		return WishlistTarget{}, false, err
	}
	if target.AppID == 0 {
		target.AppID = appID
	}
	if err := validateWishlistTarget(target); err != nil {
		return WishlistTarget{}, false, err
	}
	return target, true, nil
}

func (s *WishlistTargetStore) List(steamID string) (map[uint32]WishlistTarget, error) {
	if !validSteamID(steamID) {
		return nil, errors.New("wishlist targets require a SteamID64")
	}
	prefix := steamID + "/"
	out := make(map[uint32]WishlistTarget)
	var invalidErr error
	err := s.file.view(func(entries map[string]WishlistTarget) {
		for key, target := range entries {
			idText, ok := strings.CutPrefix(key, prefix)
			if !ok {
				continue
			}
			appID, parseErr := strconv.ParseUint(idText, 10, 32)
			if parseErr != nil || appID == 0 {
				continue
			}
			if target.AppID == 0 {
				target.AppID = uint32(appID)
			}
			if validateWishlistTarget(target) != nil || target.AppID != uint32(appID) {
				invalidErr = errors.New("invalid Steam wishlist target record")
				return
			}
			out[uint32(appID)] = target
		}
	})
	if err != nil {
		return nil, err
	}
	if invalidErr != nil {
		return nil, invalidErr
	}
	return out, nil
}

func (s *WishlistTargetStore) Set(steamID string, target WishlistTarget) error {
	target.UpdatedAt = target.UpdatedAt.UTC()
	if err := validateWishlistTarget(target); err != nil {
		return err
	}
	return s.edit(steamID, target.AppID, func(WishlistTarget) (*WishlistTarget, error) {
		next := target
		return &next, nil
	})
}

func (s *WishlistTargetStore) Delete(steamID string, appID uint32) error {
	return s.edit(steamID, appID, func(WishlistTarget) (*WishlistTarget, error) {
		return nil, nil
	})
}

func (s *WishlistTargetStore) ClearAccount(steamID string) error {
	if !validSteamID(steamID) {
		return errors.New("wishlist targets require a SteamID64")
	}
	prefix := steamID + "/"
	return s.file.update(func(entries map[string]WishlistTarget) (bool, error) {
		changed := false
		for key := range entries {
			if strings.HasPrefix(key, prefix) {
				delete(entries, key)
				changed = true
			}
		}
		return changed, nil
	})
}

func (s *WishlistTargetStore) edit(steamID string, appID uint32, change func(WishlistTarget) (*WishlistTarget, error)) error {
	key, err := wishlistTargetStoreKey(steamID, appID)
	if err != nil {
		return err
	}
	prefix := steamID + "/"
	return s.file.update(func(entries map[string]WishlistTarget) (bool, error) {
		current, exists := entries[key]
		next, err := change(current)
		if err != nil {
			return false, err
		}
		if next == nil {
			delete(entries, key)
			return exists, nil
		}
		target := *next
		target.AppID = appID
		target.UpdatedAt = target.UpdatedAt.UTC()
		if err := validateWishlistTarget(target); err != nil {
			return false, err
		}
		if !exists {
			if len(entries) >= maxWishlistTargetsTotal {
				return false, errors.New("Steam wishlist target store is full")
			}
			accountCount := 0
			for existingKey := range entries {
				if strings.HasPrefix(existingKey, prefix) {
					accountCount++
				}
			}
			if accountCount >= maxWishlistTargetsPerAccount {
				return false, fmt.Errorf("Steam wishlist target limit of %d per account reached", maxWishlistTargetsPerAccount)
			}
		}
		entries[key] = target
		return true, nil
	})
}

// parsePriceUnits parses a positive decimal amount with up to two fractional
// digits into Steam's hundredths currency units using exact integer arithmetic.
func parsePriceUnits(input string) (uint32, bool) {
	s := strings.TrimSpace(input)
	if s == "" {
		return 0, false
	}
	wholePart, fracPart, hasDot := strings.Cut(s, ".")
	if wholePart == "" || (hasDot && (fracPart == "" || len(fracPart) > 2)) {
		return 0, false
	}
	if strings.ContainsRune(fracPart, '.') {
		return 0, false
	}
	for i := range len(wholePart) {
		if wholePart[i] < '0' || wholePart[i] > '9' {
			return 0, false
		}
	}
	for i := range len(fracPart) {
		if fracPart[i] < '0' || fracPart[i] > '9' {
			return 0, false
		}
	}
	whole, err := strconv.ParseUint(wholePart, 10, 64)
	if err != nil || whole > math.MaxUint32/100 {
		return 0, false
	}
	var frac uint64
	switch len(fracPart) {
	case 1:
		frac = uint64(fracPart[0]-'0') * 10
	case 2:
		frac = uint64(fracPart[0]-'0')*10 + uint64(fracPart[1]-'0')
	}
	total := whole*100 + frac
	if total == 0 || total > math.MaxUint32 {
		return 0, false
	}
	return uint32(total), true
}

func formatPriceInput(units uint32) string {
	return fmt.Sprintf("%d.%02d", units/100, units%100)
}

func formatCurrencyAmount(currency string, units uint32) string {
	amount := formatPriceInput(units)
	switch currency {
	case "CNY":
		return "¥" + amount
	case "USD":
		return "$" + amount
	case "EUR":
		return "€" + amount
	case "GBP":
		return "£" + amount
	case "HKD":
		return "HK$" + amount
	case "TWD":
		return "NT$" + amount
	case "KRW":
		return "₩" + amount
	default:
		if currency == "" {
			return amount
		}
		return currency + " " + amount
	}
}

func wishlistTargetCurrency(item *wishItem, saved *WishlistTarget, country string) string {
	if item != nil && item.Price != nil && validCurrencyCode(item.Price.Currency) {
		return item.Price.Currency
	}
	if saved != nil && validCurrencyCode(saved.Currency) {
		return saved.Currency
	}
	return storeCurrency(country)
}

func wishlistTargetMet(price *storePrice, target WishlistTarget, ok bool) bool {
	return ok && price != nil && price.Currency == target.Currency && price.Final <= target.MaxUnits
}

// resolvedWishlistTargets combines per-account local targets with any legacy
// wishlist_target_prices config rules. Local targets take precedence per AppID.
func (d *steamData) resolvedWishlistTargets(steamID string) map[wishlistTargetKey]uint32 {
	merged := make(map[wishlistTargetKey]uint32)
	for key, amount := range d.config.wishlistTargets() {
		merged[key] = amount
	}
	if d.stores.targets == nil || !validSteamID(steamID) {
		return merged
	}
	local, err := d.stores.targets.List(steamID)
	if err != nil {
		return merged
	}
	for appID, target := range local {
		for key := range merged {
			if key.AppID == appID {
				delete(merged, key)
			}
		}
		merged[wishlistTargetKey{AppID: appID, Currency: target.Currency}] = target.MaxUnits
	}
	return merged
}

func (d *steamData) legacyWishlistTarget(appID uint32, preferredCurrency string) (WishlistTarget, bool) {
	legacy := d.config.wishlistTargets()
	if len(legacy) == 0 {
		return WishlistTarget{}, false
	}
	if preferredCurrency != "" {
		if amount, ok := legacy[wishlistTargetKey{AppID: appID, Currency: preferredCurrency}]; ok && amount > 0 {
			return WishlistTarget{AppID: appID, Currency: preferredCurrency, MaxUnits: amount}, true
		}
	}
	var currencies []string
	for key, amount := range legacy {
		if key.AppID == appID && amount > 0 {
			currencies = append(currencies, key.Currency)
		}
	}
	if len(currencies) == 0 {
		return WishlistTarget{}, false
	}
	slices.SortFunc(currencies, cmp.Compare[string])
	currency := currencies[0]
	return WishlistTarget{AppID: appID, Currency: currency, MaxUnits: legacy[wishlistTargetKey{AppID: appID, Currency: currency}]}, true
}

func (d *steamData) effectiveWishlistTarget(steamID string, item *wishItem) (target WishlistTarget, ok, isLocal bool, err error) {
	if item == nil || item.AppID == 0 {
		return WishlistTarget{}, false, false, nil
	}
	if d.stores.targets != nil && validSteamID(steamID) {
		local, found, getErr := d.stores.targets.Get(steamID, item.AppID)
		if getErr != nil {
			return WishlistTarget{}, false, false, getErr
		}
		if found {
			return local, true, true, nil
		}
	}
	preferred := wishlistTargetCurrency(item, nil, d.config.CountryCode)
	if legacy, found := d.legacyWishlistTarget(item.AppID, preferred); found {
		return legacy, true, false, nil
	}
	return WishlistTarget{}, false, false, nil
}

func (d *steamData) effectiveWishlistTargetsForView(steamID string, items []wishItem) (map[uint32]WishlistTarget, problem) {
	out := make(map[uint32]WishlistTarget)
	var local map[uint32]WishlistTarget
	if d.stores.targets != nil && validSteamID(steamID) {
		var err error
		local, err = d.stores.targets.List(steamID)
		if err != nil {
			return nil, problemWishlistTargetStore
		}
	}
	for i := range items {
		item := &items[i]
		if target, ok := local[item.AppID]; ok {
			out[item.AppID] = target
			continue
		}
		preferred := wishlistTargetCurrency(item, nil, d.config.CountryCode)
		if legacy, ok := d.legacyWishlistTarget(item.AppID, preferred); ok {
			out[item.AppID] = legacy
		}
	}
	return out, ""
}

func (a *app) saveWishlistTarget(ctx context.Context, d *steamData, steamID string, target WishlistTarget) problem {
	store := d.stores.targets
	if store == nil {
		store = a.targets
	}
	if store == nil {
		return problemWishlistTargetStore
	}
	var issue problem
	err := store.edit(steamID, target.AppID, func(WishlistTarget) (*WishlistTarget, error) {
		if issue = d.sessionIssue(); issue != "" {
			return nil, context.Canceled
		}
		if err := ctx.Err(); err != nil {
			issue = sanitizeError(err)
			return nil, err
		}
		next := target
		return &next, nil
	})
	if issue != "" {
		return issue
	}
	if err != nil {
		return problemWishlistTargetStore
	}
	return ""
}

func (a *app) clearWishlistTarget(ctx context.Context, d *steamData, steamID string, appID uint32) problem {
	store := d.stores.targets
	if store == nil {
		store = a.targets
	}
	if store == nil {
		return problemWishlistTargetStore
	}
	var issue problem
	err := store.edit(steamID, appID, func(WishlistTarget) (*WishlistTarget, error) {
		if issue = d.sessionIssue(); issue != "" {
			return nil, context.Canceled
		}
		if err := ctx.Err(); err != nil {
			issue = sanitizeError(err)
			return nil, err
		}
		return nil, nil
	})
	if issue != "" {
		return issue
	}
	if err != nil {
		return problemWishlistTargetStore
	}
	return ""
}
