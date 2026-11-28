package main

import (
	"cmp"
	"context"
	"net/url"
	"slices"
)

const maxWishlistItems = 1_000

func (c steamClient) wishlistItems(ctx context.Context, id string) ([]wishItem, problem) {
	var reply struct {
		Response *struct {
			Items []wishItem `json:"items"`
		} `json:"response"`
	}
	switch p := c.get(ctx, apiBase+wishlistPath, url.Values{"steamid": {id}}, c.access != "", &reply); p {
	case "":
	case problemDenied, problemMissing, problemBadCall:
		return nil, problemWishlist
	default:
		return nil, p
	}
	if reply.Response == nil || reply.Response.Items == nil {
		return nil, problemWishlist
	}

	// The wishlist endpoint supplies app IDs and priority. Store data is read separately without credentials.
	items := make([]wishItem, 0, len(reply.Response.Items))
	seen := make(map[uint32]struct{}, len(reply.Response.Items))
	for _, item := range reply.Response.Items {
		if _, ok := seen[item.AppID]; item.AppID == 0 || ok {
			continue
		}
		seen[item.AppID] = struct{}{}
		items = append(items, wishItem{AppID: item.AppID, Priority: item.Priority})
	}
	return items, ""
}

func (c steamClient) wishlist(ctx context.Context, id, country, language string) (*wishList, problem) {
	items, issue := c.wishlistItems(ctx, id)
	if issue != "" {
		return nil, issue
	}
	slices.SortFunc(items, func(a, b wishItem) int {
		if order := cmp.Compare(a.Priority, b.Priority); order != 0 {
			return order
		}
		return cmp.Compare(a.AppID, b.AppID)
	})
	list := &wishList{Total: len(items), Items: slices.Clone(items[:min(len(items), maxWishlistItems)])}
	ids := make([]uint32, len(list.Items))
	for i, item := range list.Items {
		ids[i] = item.AppID
	}

	list.store, issue = c.browseStore(ctx, ids, country, language, storeDataRequest{BestPurchase: true, Release: true, Assets: true})
	for i := range list.Items {
		item := &list.Items[i]
		meta, found := list.store[item.AppID]
		if !found {
			continue
		}
		item.Name = meta.Name
		item.IsFree = meta.IsFree && !meta.Restricted && (meta.ComingSoon == nil || !*meta.ComingSoon)
		if art := resolveStoreArtwork(meta.Assets); art != nil {
			if list.Artwork == nil {
				list.Artwork = make(map[uint32]appArtwork)
			}
			list.Artwork[item.AppID] = *art
		}
		price, priceIssue := c.browsePrice(meta, country)
		if priceIssue != "" {
			if issue == "" {
				issue = priceIssue
			}
			continue
		}
		item.Price, item.PriceChecked = price, true
		list.Checked++
		if price != nil && price.DiscountPercent > 0 {
			list.Discounted++
		}
	}
	return list, issue
}

// Reuse prior names and quotes only for missing fields in the current wishlist.
// Price history and alerts only see the fresh upstream results.
func mergeStaleWishlist(current, previous *wishList) *wishList {
	old := make(map[uint32]wishItem, len(previous.Items))
	for _, item := range previous.Items {
		old[item.AppID] = item
	}
	merged := *current
	merged.Items = slices.Clone(current.Items)
	if len(current.Artwork) > 0 {
		merged.Artwork = make(map[uint32]appArtwork, len(current.Artwork))
		for k, v := range current.Artwork {
			merged.Artwork[k] = v
		}
	}
	used := false
	for i := range merged.Items {
		item := &merged.Items[i]
		prior := old[item.AppID]
		if item.Name == "" && prior.Name != "" {
			item.Name = prior.Name
			used = true
		}
		if _, hasArt := merged.Artwork[item.AppID]; !hasArt {
			if priorArt, ok := previous.Artwork[item.AppID]; ok {
				if merged.Artwork == nil {
					merged.Artwork = make(map[uint32]appArtwork)
				}
				merged.Artwork[item.AppID] = priorArt
				used = true
			}
		}
		if !item.PriceChecked && prior.PriceChecked {
			item.Price, item.PriceChecked = prior.Price, true
			if current.store[item.AppID].Success == 0 {
				item.IsFree = prior.IsFree
			}
			merged.Checked++
			if item.Price != nil && item.Price.DiscountPercent > 0 {
				merged.Discounted++
			}
			used = true
		}
	}
	if !used {
		return nil
	}
	return &merged
}
