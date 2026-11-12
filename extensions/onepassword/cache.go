package main

// The extension's memory of 1Password's metadata (#488): the vault list, each vault's item overviews and each item's
// field structure, as the extension keeps them (vaultInfo, itemInfo, itemDetail: values already dropped, names
// scrubbed), for metaTTL, so repeated describes, searches and item looks do not each spend requests of the service
// account's hourly and daily limits (or ask op on the computer again). Never a value: secrets.resolve always asks.
//
// Only in this process's memory. A new token, computer or setting restarts the program, so it starts empty; the
// page's Check again empties it. While 1Password's rate limit holds (rateWindow after it refused), reads answer from
// what is kept, however old, and ask 1Password nothing; with nothing kept they answer the limit at once.

import (
	"context"
	"slices"
	"time"
)

const (
	// metaTTL is how long kept metadata answers without asking 1Password again.
	metaTTL = 5 * time.Minute
	// missAgain: a vault, item or field named that a kept answer lacks is looked up again once that answer is older
	// than this (it may have just been made); younger, the miss stands.
	missAgain = 30 * time.Second
)

type kept[T any] struct {
	v  T
	at time.Time
}

type metaCache struct {
	vaults  *kept[[]vaultInfo]
	items   map[string]kept[[]itemInfo] // by vault ID
	details map[string]kept[itemDetail] // by vault ID + "/" + item ID
}

// limited says whether 1Password's rate limit holds, and the error it answered then.
func (a *app) limited() (bool, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.lastErr != nil && a.errKind == errRate && a.now().Sub(a.errAt) < rateWindow {
		return true, a.lastErr
	}
	return false, nil
}

// forgetMeta empties the cache; a read that started before it keeps nothing it got.
func (a *app) forgetMeta() {
	a.metaMu.Lock()
	defer a.metaMu.Unlock()
	a.meta = metaCache{}
	a.metaGen++
}

// cachedRead answers what get finds kept while it is fresh, or at any age under the rate limit; otherwise it fetches,
// keeps the answer with put (unless the cache was emptied meanwhile) and answers it.
func cachedRead[T any](a *app, get func(*metaCache) (kept[T], bool), put func(*metaCache, kept[T]), fetch func() (T, error)) (T, error) {
	now := a.now()
	limited, limitErr := a.limited()
	a.metaMu.Lock()
	k, ok := get(&a.meta)
	gen := a.metaGen
	a.metaMu.Unlock()
	if ok && (limited || now.Sub(k.at) < metaTTL) {
		return k.v, nil
	}
	var zero T
	if limited {
		return zero, limitErr
	}
	v, err := fetch()
	if err != nil {
		if ok && kindOf(err) == errRate { // refused for the limit just now: what is kept answers instead
			return k.v, nil
		}
		return zero, err
	}
	a.metaMu.Lock()
	if gen == a.metaGen {
		put(&a.meta, kept[T]{v: v, at: now})
	}
	a.metaMu.Unlock()
	return v, nil
}

// dropIfOlder forgets what get finds kept when it is older than missAgain, so the next read asks again; false when it
// is younger, missing, or the rate limit holds (then nothing is asked anyway).
func dropIfOlder[T any](a *app, get func(*metaCache) (kept[T], bool), drop func(*metaCache)) bool {
	if limited, _ := a.limited(); limited {
		return false
	}
	now := a.now()
	a.metaMu.Lock()
	defer a.metaMu.Unlock()
	k, ok := get(&a.meta)
	if !ok || now.Sub(k.at) < missAgain {
		return false
	}
	drop(&a.meta)
	return true
}

func (c *metaCache) getVaults() (kept[[]vaultInfo], bool) {
	if c.vaults == nil {
		return kept[[]vaultInfo]{}, false
	}
	return *c.vaults, true
}

func (a *app) keptVaults(c *metaCache) (kept[[]vaultInfo], bool) { return c.getVaults() }

// vaultsHaving is the vault list, asked again once when a kept one has no vault has(…) accepts.
func (a *app) vaultsHaving(ctx context.Context, has func(vaultInfo) bool) ([]vaultInfo, error) {
	vaults, err := a.listVaults(ctx)
	if err != nil || slices.ContainsFunc(vaults, has) {
		return vaults, err
	}
	if dropIfOlder(a, a.keptVaults, func(c *metaCache) { c.vaults = nil }) {
		return a.listVaults(ctx)
	}
	return vaults, nil
}

// itemsHaving is a vault's item list, asked again once when a kept one has no item has(…) accepts.
func (a *app) itemsHaving(ctx context.Context, vaultID string, has func(itemInfo) bool) ([]itemInfo, error) {
	items, err := a.listItems(ctx, vaultID)
	if err != nil || slices.ContainsFunc(items, has) {
		return items, err
	}
	get := func(c *metaCache) (kept[[]itemInfo], bool) { k, ok := c.items[vaultID]; return k, ok }
	if dropIfOlder(a, get, func(c *metaCache) { delete(c.items, vaultID) }) {
		return a.listItems(ctx, vaultID)
	}
	return items, nil
}

// itemHaving is an item's structure, asked again once when a kept one has no field has(…) accepts.
func (a *app) itemHaving(ctx context.Context, vaultID, itemID string, has func(fieldInfo) bool) (itemDetail, error) {
	d, err := a.getItem(ctx, vaultID, itemID)
	if err != nil || slices.ContainsFunc(d.Fields, has) {
		return d, err
	}
	key := vaultID + "/" + itemID
	get := func(c *metaCache) (kept[itemDetail], bool) { k, ok := c.details[key]; return k, ok }
	if dropIfOlder(a, get, func(c *metaCache) { delete(c.details, key) }) {
		return a.getItem(ctx, vaultID, itemID)
	}
	return d, nil
}
