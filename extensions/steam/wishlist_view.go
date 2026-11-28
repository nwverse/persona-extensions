package main

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"time"

	"github.com/nwverse/persona/sdk/go/ext"
	"github.com/nwverse/persona/sdk/go/wire"
)

const (
	wishlistTargetPriceField  = "target_price"
	wishlistTargetSaveAction  = "wishlist-target-save"
	wishlistTargetClearAction = "wishlist-target-clear"
)

func wishlistRow(t *text, item wishItem, target WishlistTarget, hasTarget bool) wire.Component {
	id := appIDText(item.AppID)
	row := wire.Component{
		Type:   wire.ComponentRow,
		Key:    "wish-" + id,
		Title:  t.gameName(item.AppID, item.Name),
		Icon:   wire.NavIconStar,
		Action: &wire.Action{ID: "wish-game-" + id},
	}
	switch {
	case item.Price != nil:
		row.Value = item.Price.FinalFormatted
		if row.Value == "" {
			row.Value = formatCurrencyAmount(item.Price.Currency, item.Price.Final)
		}
		if discount := item.Price.DiscountPercent; discount > 0 {
			row.Badge = &wire.Badge{Text: fmt.Sprintf(t.discount, discount), Tone: wire.TonePositive}
		}
	case item.IsFree:
		row.Value = t.freeToPlay
	case item.PriceChecked:
		row.Subtitle = t.noPrice
	default:
		row.Subtitle = t.priceUnchecked
	}

	if hasTarget {
		targetHint := fmt.Sprintf(t.wishTarget.rowHint, formatCurrencyAmount(target.Currency, target.MaxUnits))
		if wishlistTargetMet(item.Price, target, true) {
			row.Icon = wire.NavIconBell
			row.IconColor = "green"
			row.Badge = &wire.Badge{Text: t.wishTarget.metBadge, Tone: wire.TonePositive}
			if item.Price.DiscountPercent > 0 {
				row.Subtitle = fmt.Sprintf(t.discount, item.Price.DiscountPercent) + " · " + targetHint
			} else {
				row.Subtitle = targetHint
			}
		} else {
			row.Icon = wire.NavIconBell
			if row.Subtitle == "" {
				row.Subtitle = targetHint
			} else {
				row.Subtitle += " · " + targetHint
			}
		}
	}
	return row
}

func (a *app) wishlistDetail(ctx context.Context, d *steamData, t *text, st pageState) *wire.View {
	view := &wire.View{Title: t.wishTarget.detailTitle, State: ext.EncodeState(st)}
	overview := wire.Section{Title: t.wishTarget.marketOverview, Icon: wire.NavIconStar, Items: []wire.Component{button(t.back, "back")}}

	var p reading[player]
	if d.session == nil {
		p = d.account(ctx, profileTTL, false)
	}
	wishes := d.wishlist(ctx, p, t.storeLanguage, false)
	if wishes.Data == nil {
		overview.Items = append(overview.Items, emptyWithIcon(t.message(wishes.Issue, wishes.Stale), wire.NavIconInfo))
		view.Sections = []wire.Section{overview}
		return view
	}
	idx := slices.IndexFunc(wishes.Data.Items, func(item wishItem) bool { return item.AppID == st.AppID })
	if idx < 0 {
		overview.Items = append(overview.Items, emptyWithIcon(t.missingGame, wire.NavIconInfo))
		view.Sections = []wire.Section{overview}
		return view
	}

	item := wishes.Data.Items[idx]
	meta := wishes.Data.store[item.AppID]
	view.Title = t.gameName(item.AppID, item.Name)
	overview.Footer = t.message(wishes.Issue, wishes.Stale)

	steamID, _ := d.readID(p)
	st.TargetAccount = steamID
	view.State = ext.EncodeState(st)

	target, hasTarget, isLocal, targetErr := d.effectiveWishlistTarget(steamID, &item)
	var savedPtr *WishlistTarget
	if hasTarget {
		savedPtr = &target
	}
	currency := wishlistTargetCurrency(&item, savedPtr, d.config.CountryCode)
	met := hasTarget && wishlistTargetMet(item.Price, target, true)

	if met {
		overview.Items = append(overview.Items, wire.Component{
			Type:      wire.ComponentRow,
			Key:       "wish-target-met-" + appIDText(item.AppID),
			Title:     t.wishTarget.metBannerTitle,
			Subtitle:  fmt.Sprintf(t.wishTarget.metBannerDetail, formatCurrencyAmount(target.Currency, target.MaxUnits)),
			Icon:      wire.NavIconBell,
			IconColor: "green",
			Badge:     &wire.Badge{Text: t.wishTarget.metBadge, Tone: wire.TonePositive},
		})
	}

	now := time.Now().UTC()
	if d.wishes != nil && d.wishes.now != nil {
		now = d.wishes.now().UTC()
	}

	// Build current price stat aligned with Steam Store terminology.
	priceStat := wire.Stat{Label: t.wishTarget.currentPrice}
	switch {
	case item.Price != nil:
		priceStat.Value = item.Price.FinalFormatted
		if priceStat.Value == "" {
			priceStat.Value = formatCurrencyAmount(item.Price.Currency, item.Price.Final)
		}
		if item.Price.DiscountPercent > 0 {
			priceStat.Tone = wire.TonePositive
			if orig, ok := originalPriceUnits(meta, item.Price.Final); ok {
				priceStat.Detail = fmt.Sprintf(t.wishTarget.wasPriceDiscount, formatCurrencyAmount(item.Price.Currency, orig), item.Price.DiscountPercent)
			} else {
				priceStat.Detail = fmt.Sprintf(t.discount, item.Price.DiscountPercent)
			}
		} else if met {
			priceStat.Tone = wire.TonePositive
		}
	case item.IsFree:
		priceStat.Value = t.freeToPlay
		priceStat.Tone = wire.TonePositive
	default:
		if state, _ := meta.releaseState(now); state == "upcoming" {
			priceStat.Value = t.wishTarget.comingSoon
			priceStat.Detail = t.wishTarget.unreleasedHint
		} else if item.PriceChecked {
			priceStat.Value = t.wishTarget.unpriced
			priceStat.Detail = t.noPrice
		} else {
			priceStat.Value = "—"
			priceStat.Detail = t.priceUnchecked
		}
	}

	// Read locally observed lowest price from PriceHistoryStore.
	lowStat := wire.Stat{Label: t.wishTarget.observedLow, Value: t.wishTarget.noHistory}
	if validSteamID(steamID) && d.stores.prices != nil {
		if histories, err := d.stores.prices.Histories(steamID, d.config.CountryCode, item.AppID); err == nil {
			if hist, ok := histories[item.AppID]; ok && !hist.ObservedLowestAt.IsZero() &&
				(item.Price == nil || hist.Latest.Currency == currency) {
				lowStat.Value = formatCurrencyAmount(hist.Latest.Currency, hist.ObservedLowestFinal)
				detail := hist.ObservedLowestAt.UTC().Format(time.DateOnly)
				if hist.ObservedLowestDiscount > 0 {
					detail += " · " + fmt.Sprintf(t.discount, hist.ObservedLowestDiscount)
				}
				lowStat.Detail = detail
			}
		}
	}

	regionStat := wire.Stat{
		Label: t.wishTarget.regionCurrency,
		Value: d.config.CountryCode + " · " + currency,
	}

	overview.Items = append(overview.Items,
		wire.Component{Type: wire.ComponentStatGrid, Stats: []wire.Stat{priceStat, lowStat, regionStat}},
		wire.Component{Type: wire.ComponentLink, Title: t.wishTarget.viewInStore, URL: storeLink(item.AppID)},
		wire.Component{Type: wire.ComponentLink, Title: t.communityHub, URL: communityLink(item.AppID)},
	)

	// Target price alert form section.
	targetSection := wire.Section{Title: t.wishTarget.sectionTitle, Icon: wire.NavIconBell}
	if targetErr != nil {
		targetSection.Items = []wire.Component{emptyWithIcon(t.message(problemWishlistTargetStore, false), wire.NavIconWarning)}
	} else {
		desc := t.wishTarget.description
		if hasTarget && !isLocal {
			desc = joinText(desc, t.wishTarget.legacyHint)
		}
		prefill := ""
		if hasTarget {
			prefill = formatPriceInput(target.MaxUnits)
		}
		targetSection.Items = append(targetSection.Items,
			wire.Component{Type: wire.ComponentText, Text: desc, Style: "secondary"},
			wire.Component{
				Type:        wire.ComponentField,
				Kind:        wire.FieldKindNumber,
				Name:        wishlistTargetPriceField,
				Label:       fmt.Sprintf(t.wishTarget.priceLabel, currency),
				Value:       prefill,
				Placeholder: t.wishTarget.placeholder,
				Detail:      fmt.Sprintf(t.wishTarget.inputDetail, currency),
			},
			wire.Component{
				Type:   wire.ComponentButton,
				Title:  t.wishTarget.save,
				Action: &wire.Action{ID: wishlistTargetSaveAction, Submit: true},
			},
		)
		if isLocal {
			targetSection.Items = append(targetSection.Items, wire.Component{
				Type:   wire.ComponentButton,
				Title:  t.wishTarget.clear,
				Role:   "destructive",
				Action: &wire.Action{ID: wishlistTargetClearAction, Confirm: t.wishTarget.confirmClear},
			})
		}
	}

	view.Sections = []wire.Section{overview, targetSection}
	return view
}

func originalPriceUnits(meta storeBrowseItem, final uint32) (uint32, bool) {
	if meta.BestPurchase == nil || meta.BestPurchase.Original == "" || meta.BestPurchase.Original == "-1" {
		return 0, false
	}
	orig, err := strconv.ParseUint(meta.BestPurchase.Original.String(), 10, 32)
	if err != nil || uint32(orig) <= final {
		return 0, false
	}
	return uint32(orig), true
}

func (a *app) wishlistTargetAction(ctx context.Context, r *ext.UIRequest, t *text, st pageState) (*wire.ActionResult, error) {
	if st.AppID == 0 || st.From != wishSection || !validSteamID(st.TargetAccount) {
		return nil, invalid(t.invalidAction)
	}
	d, ctx, cancel := a.readContext(ctx)
	defer cancel()

	id, issue := d.backlogMarkID(ctx)
	if issue != "" {
		return nil, &ext.Error{Code: string(issue), Message: t.message(issue, false)}
	}
	if id != st.TargetAccount {
		return nil, invalid(t.wishTarget.accountChanged)
	}

	if r.Action == wishlistTargetClearAction {
		if issue := a.clearWishlistTarget(ctx, d, id, st.AppID); issue != "" {
			return nil, &ext.Error{Code: string(issue), Message: t.message(issue, false)}
		}
		return &wire.ActionResult{View: a.accountPage(ctx, d, t, st, false), Toast: t.wishTarget.cleared}, nil
	}

	units, ok := parsePriceUnits(r.Values[wishlistTargetPriceField])
	if !ok {
		return nil, invalid(t.wishTarget.invalidPrice)
	}

	// Resolve currency from the current wishlist item if available, then existing target, then Store country.
	var itemPtr *wishItem
	var p reading[player]
	if d.session == nil {
		p = d.account(ctx, profileTTL, false)
	}
	if wishes := d.wishlist(ctx, p, t.storeLanguage, false); wishes.Data != nil {
		if idx := slices.IndexFunc(wishes.Data.Items, func(item wishItem) bool { return item.AppID == st.AppID }); idx >= 0 {
			item := wishes.Data.Items[idx]
			itemPtr = &item
		}
	}
	var savedPtr *WishlistTarget
	if existing, found, _, err := d.effectiveWishlistTarget(id, &wishItem{AppID: st.AppID}); err == nil && found {
		savedPtr = &existing
	}
	currency := wishlistTargetCurrency(itemPtr, savedPtr, d.config.CountryCode)

	now := time.Now().UTC()
	if d.wishes != nil && d.wishes.now != nil {
		now = d.wishes.now().UTC()
	}
	target := WishlistTarget{AppID: st.AppID, Currency: currency, MaxUnits: units, UpdatedAt: now}
	if issue := a.saveWishlistTarget(ctx, d, id, target); issue != "" {
		return nil, &ext.Error{Code: string(issue), Message: t.message(issue, false)}
	}
	toast := fmt.Sprintf(t.wishTarget.savedToast, formatCurrencyAmount(currency, units))
	return &wire.ActionResult{View: a.accountPage(ctx, d, t, st, false), Toast: toast}, nil
}
