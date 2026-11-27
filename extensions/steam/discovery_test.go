package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/sdk/go/ext"
	"github.com/nwverse/persona/sdk/go/exttest"
	"github.com/nwverse/persona/sdk/go/wire"
)

func startDiscoveryHost(t *testing.T, f *fakeSteam, cfg, locale string, now func() time.Time) *exttest.Host {
	t.Helper()
	t.Setenv("PERSONA_EXTENSION_DATA_DIR", t.TempDir())
	return exttest.StartWith(t, manifest, exttest.Serve(t, program(f.http, now)), func(p *wire.InitializeParams) {
		p.Config = json.RawMessage(cfg)
		p.Secrets = map[string]string{keyName: f.key}
		p.Hub.Locale = locale
	})
}

func syntheticClock() (func() time.Time, func(time.Duration)) {
	start := time.Unix(1_800_000_000, 0).UTC()
	var elapsed atomic.Int64
	return func() time.Time { return start.Add(time.Duration(elapsed.Load())) },
		func(d time.Duration) { elapsed.Add(int64(d)) }
}

func readDiscoveryResult[T any](t *testing.T, h *exttest.Host, name, args string) T {
	t.Helper()
	var out T
	if err := json.Unmarshal([]byte(toolAnswer(t, h, name, args)), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestDiscoveryLocalizedTagCatalogAndFallback(t *testing.T) {
	f := newFakeSteam(t, 300*time.Millisecond)
	now, advance := syntheticClock()
	h := startDiscoveryHost(t, f, `{"discovery_max_results":2}`, "zh-Hans-CN", now)
	zhPath := tagCatalogPath + "schinese"
	f.reply(zhPath, http.StatusOK, `[
		{"tagid":63456,"name":"虚构解谜"},
		{"tagid":1664,"name":"解谜"},
		{"tagid":63457,"name":"解谜冒险"},
		{"tagid":1664,"name":"重复"},
		{"tagid":0,"name":"无效"}
	]`)
	cached := toolAnswer(t, h, storeTagsTool, `{"query":"解谜","limit":50}`)
	contains(t, cached, `"language":"schinese"`, `"source":"steam_store"`, `"matched_count":3`, "虚构解谜", `"tagid":1664`)
	assertHidden(t, cached, "解谜冒险")
	contains(t, toolAnswer(t, h, storeTagsTool, `{"query":"63457","language":"zh-CN"}`), "解谜冒险")
	if f.hit(zhPath) != 1 {
		t.Fatalf("multiple queries did not reuse the localized catalog")
	}
	advance(tagCatalogTTL)
	f.reply(zhPath, http.StatusServiceUnavailable, f.key)
	cached = toolAnswer(t, h, storeTagsTool, `{"query":"虚构"}`)
	contains(t, cached, "虚构解谜", `"stale":true`, "当前显示缓存数据")
	assertHidden(t, cached, f.key)

	dePath := tagCatalogPath + "german"
	f.handle(dePath, func(_ http.ResponseWriter, r *http.Request) { <-r.Context().Done() })
	for range 2 {
		fallback := toolAnswer(t, h, storeTagsTool, `{"query":"pUzZlE","language":"german"}`)
		contains(t, fallback, `"tagid":1664`, `"language":"english"`, `"source":"built_in"`, `"code":"timeout"`, "内置")
	}
	if f.hit(dePath) != 1 {
		t.Fatal("tag failure was retried before its backoff expired")
	}
}

func syntheticSearchRow(id uint32, title, osMarkup, amount, price string, percent int) string {
	return fmt.Sprintf(`<a class="search_result_row" data-ds-appid="%d" data-ds-itemkey="App_%d">
		<span class="title">%s</span>%s
		<div class="search_price_discount_combined" data-price-final="%s">
			<div class="discount_block search_discount_block" data-discount="%d">
				<div class="discount_final_price">%s</div>
			</div>
		</div></a>`, id, id, title, osMarkup, amount, percent, price)
}

func writeSyntheticSearch(w http.ResponseWriter, rows string, total int) {
	_ = json.NewEncoder(w).Encode(map[string]any{"success": 1, "results_html": rows, "total_count": total})
}

type discoverySearchResult struct {
	Available bool
	Truncated bool
	Limit     int
	Games     []discoveryCandidate
	Warnings  []toolWarning
}

func TestDiscoverySearchFiltersAndAccount(t *testing.T) {
	const (
		wanted = firstApp + 10
		win    = `<span class="platform_img win"></span>`
		linux  = `<span class="platform_img linux"></span>`
	)
	f := newFakeSteam(t, time.Second)
	h := startDiscoveryHost(t, f, fmt.Sprintf(`{"steam_id":%q,"country_code":"GB","discovery_max_results":6}`, fixtureID), "en", time.Now)
	f.reply(tagCatalogPath+"schinese", http.StatusOK, `[{"tagid":63456,"name":"合成模拟"}]`)
	f.reply(wishlistPath, http.StatusOK, fmt.Sprintf(`{"response":{"items":[{"appid":%d,"priority":1}]}}`, wanted))
	f.handle(storeSearchPath, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		for key, want := range map[string]string{
			"category1": "998", "term": "space & sea", "tags": "1664,1685,63456", "maxprice": "19.99", "specials": "1",
			"cc": "GB", "l": "schinese", "supportedlang": "schinese", "os": "linux", "deck_compatibility": "2,3", "sort_by": "Reviews_DESC",
			"count": "6", "start": "0", "infinite": "1",
		} {
			if q.Get(key) != want {
				t.Errorf("search %s = %q, want %q", key, q.Get(key), want)
			}
		}
		rows := fmt.Sprintf(`<a class="search_result_row" data-ds-appid="%d" data-ds-itemkey="Sub_123"><span class="title">Bundle</span></a>`, wanted)
		rows += syntheticSearchRow(firstApp, "Owned Fixture", win+linux, "999", "£9.99", 20)
		rows += syntheticSearchRow(wanted, "Wish &amp; Wave", win+linux, "1999", "£19.99", 25)
		rows += syntheticSearchRow(wanted+1, "Over Budget", win+linux, "2000", "£20.00", 25)
		rows += syntheticSearchRow(wanted+2, "Full Price", win+linux, "299", "£2.99", 0)
		rows += syntheticSearchRow(wanted+3, "Linux Only", linux, "299", "£2.99", 30)
		rows += syntheticSearchRow(wanted+4, "Unavailable Price", win+linux, "0", "", 0)
		rows += syntheticSearchRow(wanted+5, "Beyond Candidate Cap", win+linux, "100", "£1.00", 25)
		writeSyntheticSearch(w, rows, 50)
	})
	const args = `"query":" space & sea ", "tags":[1664,"co-OP","合成模拟","1664"],"max_price":19.99,
		"discounted_only":true,"platforms":["steam_deck","linux","windows"],"language":"zh-CN","sort":"reviews","limit":50`
	withOwned := readDiscoveryResult[discoverySearchResult](t, h, storeSearchTool, "{"+args+"}")
	if !withOwned.Available || !withOwned.Truncated || withOwned.Limit != 6 || len(withOwned.Games) != 2 || len(withOwned.Warnings) != 0 {
		t.Fatalf("search ignored a constraint: %+v", withOwned)
	}
	owned, wish := withOwned.Games[0], withOwned.Games[1]
	if owned.AppID != firstApp || owned.Owned == nil || !*owned.Owned || owned.InWishlist == nil || *owned.InWishlist ||
		wish.AppID != wanted || wish.Name != "Wish & Wave" || wish.Owned == nil || *wish.Owned || wish.InWishlist == nil || !*wish.InWishlist ||
		wish.Price == nil || wish.Price.Final != 1999 || wish.Price.DiscountPercent != 25 || wish.URL != storeLink(wanted) {
		t.Fatalf("incorrect prices or account flags: %+v, %+v", owned, wish)
	}
	onlyNew := readDiscoveryResult[discoverySearchResult](t, h, storeSearchTool, "{"+args+`,"exclude_owned":true}`)
	if len(onlyNew.Games) != 1 || onlyNew.Games[0].AppID != wanted {
		t.Fatalf("exclude_owned result: %+v", onlyNew)
	}
	for _, path := range []string{storeSearchPath, tagCatalogPath + "schinese", ownedPath, wishlistPath} {
		if f.hit(path) != 1 {
			t.Errorf("%s missed its cache", path)
		}
	}
	if f.hit(storePricesPath) != 0 || f.hit(storeNamesPath) != 0 {
		t.Fatal("membership lookup performed unnecessary wishlist enrichment")
	}
}

func TestDiscoverySearchSortLimitAndUnknownMembership(t *testing.T) {
	const mac = `<span class="platform_img mac"></span>`
	f := newFakeSteam(t, time.Second)
	h := startDiscoveryHost(t, f, fmt.Sprintf(`{"steam_id":%q,"discovery_max_results":2}`, fixtureID), "en", time.Now)
	f.reply(ownedPath, http.StatusForbidden, "")
	f.reply(wishlistPath, http.StatusForbidden, "")
	f.handle(storeSearchPath, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		sort := map[string]string{"relevance": "", "price_asc": "Price_ASC", "release_date": "Released_DESC"}
		if q.Get("count") != "2" || q.Get("sort_by") != sort[q.Get("term")] || q.Get("os") != "mac" || q.Has("supportedlang") {
			t.Errorf("incorrect search options: %v", q)
		}
		rows := syntheticSearchRow(firstApp, "Fixture 1", mac, "0", "Free", 0) +
			syntheticSearchRow(secondApp, "Fixture 2", mac, "100", "$1.00", 0) +
			syntheticSearchRow(thirdApp, "Fixture 3", mac, "200", "$2.00", 0)
		writeSyntheticSearch(w, rows, 3)
	})
	for _, sort := range []string{"relevance", "price_asc", "release_date"} {
		args := fmt.Sprintf(`{"query":%q,"sort":%q,"platforms":["macos"],"exclude_owned":true}`, sort, sort)
		r := readDiscoveryResult[discoverySearchResult](t, h, storeSearchTool, args)
		if !r.Available || !r.Truncated || len(r.Games) != 2 || len(r.Warnings) != 2 {
			t.Fatalf("incorrect candidate bound or private dataset handling: %+v", r)
		}
		for _, g := range r.Games {
			if g.Owned != nil || g.InWishlist != nil {
				t.Fatalf("unavailable personal data was reported as known: %+v", g)
			}
		}
	}
}

func TestDiscoverySearchQRAccountAndSignOut(t *testing.T) {
	now, _ := syntheticClock()
	access := fixtureJWT(t, fixtureID, now().Add(time.Hour), "discovery-access")
	f := newFakeSteamMode(t, time.Second, access)
	f.handle(storeSearchPath, func(w http.ResponseWriter, _ *http.Request) {
		writeSyntheticSearch(w, syntheticSearchRow(firstApp, "Fixture app", "", "0", "Free", 0), 1)
	})
	a := &app{}
	h := exttest.Start(t, manifest, exttest.Serve(t, a.program(f.http, now)))
	if !a.qr.waitLoaded(t.Context()) {
		t.Fatal("synthetic QR state did not initialize")
	}
	a.qr.mu.Lock()
	a.qr.installLocked(steamTokens{secret: fixtureSecret(t, fixtureID, now()), refreshExpiry: now().Add(time.Hour),
		access: access, accessExpiry: now().Add(time.Hour)})
	a.qr.mu.Unlock()

	const args = `{"query":"fixture","max_price":0}`
	raw := toolAnswer(t, h, storeSearchTool, args)
	contains(t, raw, `"owned":true`, `"in_wishlist":true`, `"final":0`)
	assertHidden(t, raw, access, fixtureID)
	var state wire.AuthStatus
	if err := h.Call("auth.logout", ext.Empty{}, &state); err != nil || state.State != wire.AuthStateSignedOut {
		t.Fatalf("failed to sign out of the synthetic account: %+v, %v", state, err)
	}
	raw = toolAnswer(t, h, storeSearchTool, args)
	contains(t, raw, `"available":true`, `"owned":null`, `"in_wishlist":null`, `"code":"steam_sign_in_required"`)
	if f.hit(storeSearchPath) != 1 || f.hit(wishlistPath) != 1 || f.hit(ownedPath) != 1 {
		t.Fatal("sign-out failed to clear private memberships independently of the public search cache")
	}
}

func TestDiscoverySearchDegradesWithinBudget(t *testing.T) {
	f := newFakeSteam(t, 300*time.Millisecond)
	now, advance := syntheticClock()
	h := startDiscoveryHost(t, f, `{"auth_mode":"web_api_key"}`, "en", now)
	f.handle(storeSearchPath, func(w http.ResponseWriter, _ *http.Request) {
		writeSyntheticSearch(w, syntheticSearchRow(firstApp, "Fixture result", "", "0", "Free", 0), 1)
	})
	contains(t, toolAnswer(t, h, storeSearchTool, `{"query":"fixture"}`), `"available":true`, "Fixture result")
	advance(searchTTL)
	f.handle(storeSearchPath, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, strings.Repeat(" ", searchResponseBytes)+`{"success":1,"total_count":0,"results_html":""}`)
	})
	stale := toolAnswer(t, h, storeSearchTool, `{"query":"fixture"}`)
	contains(t, stale, "Fixture result", `"code":"unexpected_steam_response"`, `"stale":true`, `"as_of"`)
	advance(searchMaxStaleAge)
	f.handle(storeSearchPath, func(_ http.ResponseWriter, r *http.Request) { <-r.Context().Done() })
	unavailable := toolAnswer(t, h, storeSearchTool, `{"query":"fixture"}`)
	contains(t, unavailable, `"available":false`, `"games":[]`, `"code":"timeout"`, `"code":"store_search_unavailable"`)
	assertHidden(t, unavailable, "Fixture result")
}

func TestDiscoveryDisabled(t *testing.T) {
	f := newFakeSteam(t, time.Second)
	h := startDiscoveryHost(t, f, `{"discovery_tools_enabled":false}`, "zh-Hans", time.Now)
	var list wire.ConnectorTools
	if err := h.Call("connector.tools.list", wire.ConnectorParams{Contribution: extensionID}, &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Tools) != len(tools) {
		t.Fatalf("disabled discovery changed the account tools: %v", list.Tools)
	}
	for _, name := range []string{"steam_store_tags", "steam_store_search"} {
		var out wire.ConnectorCallResult
		err := h.Call("connector.tools.call", wire.ConnectorCallParams{
			Contribution: extensionID, Name: name, Args: json.RawMessage(`{"unexpected":true}`),
		}, &out)
		var e *ext.Error
		if !errors.As(err, &e) || e.Code != "discovery_disabled" || out.Text != "" {
			t.Fatalf("%s returned data or failed to identify the disabled setting: %+v, %v", name, out, err)
		}
		contains(t, e.Message, "discovery_tools_enabled", "已关闭")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.hits) != 0 {
		t.Fatal("disabled discovery contacted Steam")
	}
}
