package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	fixtureCountryPath         = "/IUserAccountService/GetUserCountry/v1/"
	fixtureMobileSummariesPath = "/ISteamUserOAuth/GetUserSummaries/v1/"
	fixtureStoreAccess         = "synthetic-store-access"
)

func regionQRApp(t *testing.T, f *fakeSteam, cfg config, now func() time.Time) *app {
	t.Helper()
	_, ctx, hub := testSecretHub(t)
	cfg.AuthMode = modeQR
	d := newSteamData(cfg, "", "", f.http, now)
	q := newQRAuth(ctx, hub, f.http, now, d.config, "en")
	if !q.waitLoaded(t.Context()) {
		t.Fatal("QR startup did not complete")
	}
	installRegionAccount(t, q, fixtureID)
	return &app{steamData: d, qr: q, discovery: newSteamDiscovery(f.http, now), locale: "en"}
}

func installRegionAccount(t *testing.T, q *qrAuth, id string) *steamData {
	t.Helper()
	now := q.now()
	q.mu.Lock()
	defer q.mu.Unlock()
	q.installLocked(steamTokens{secret: fixtureSecret(t, id, now), refreshExpiry: now.Add(48 * time.Hour),
		access: fixtureStoreAccess, accessExpiry: now.Add(48 * time.Hour)})
	return q.session.reads
}

func TestQRStoreCountryFallback(t *testing.T) {
	for _, test := range []struct {
		name, response, profileCountry, want string
		status                               int
		result                               string
		profileTimeout                       bool
	}{
		{name: "denied", status: http.StatusForbidden, profileCountry: "gb", want: "GB"},
		{name: "rejected result", status: http.StatusOK, result: "15", response: `{"response":{"country":"JP"}}`, profileCountry: "CN", want: "CN"},
		{name: "missing", status: http.StatusOK, response: `{"response":{}}`, profileCountry: "KR", want: "KR"},
		{name: "unassigned", status: http.StatusOK, response: `{"response":{"country":"ZZ"}}`, profileCountry: "GB", want: "GB"},
		{name: "wrong type", status: http.StatusOK, response: `{"response":{"country":123}}`, profileCountry: "GB", want: "GB"},
		{name: "account timeout", profileCountry: "GB", want: "GB"},
		{name: "no matching profile", status: http.StatusServiceUnavailable, want: "US"},
		{name: "invalid profile location", status: http.StatusBadRequest, profileCountry: "ZZ", want: "US"},
		{name: "both routes time out", profileTimeout: true, want: "US"},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newFakeSteamMode(t, 0, fixtureStoreAccess)
			f.handle(fixtureCountryPath, func(w http.ResponseWriter, r *http.Request) {
				if test.status == 0 {
					_, _ = io.Copy(io.Discard, r.Body)
					<-r.Context().Done()
					return
				}
				if test.result != "" {
					w.Header().Set("X-EResult", test.result)
				}
				w.WriteHeader(test.status)
				_, _ = io.WriteString(w, test.response)
			})
			f.handle(fixtureMobileSummariesPath, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Query().Get("steamids") != fixtureID {
					t.Error("profile fallback must request only the signed-in account")
				}
				if test.profileTimeout {
					<-r.Context().Done()
					return
				}
				_, _ = fmt.Fprintf(w, `{"players":[{"steamid":%q,"loccountrycode":"JP"}`, fixtureID2)
				if test.profileCountry != "" {
					_, _ = fmt.Fprintf(w, `,{"steamid":%q,"loccountrycode":%q}`, fixtureID, test.profileCountry)
				}
				_, _ = io.WriteString(w, `]}`)
			})
			a := regionQRApp(t, f, config{}, time.Now)
			start := time.Now()
			out, err := a.callTool(t.Context(), "steam_wishlist", nil)
			if err != nil {
				t.Fatal(err)
			}
			contains(t, out.Text, `"country_code":"`+test.want+`"`, `"available":true`)
			if elapsed := time.Since(start); elapsed > countryLookupBudget+time.Second {
				t.Errorf("region lookup prevented the wishlist from completing promptly: %v", elapsed)
			}
			if f.hit(fixtureCountryPath) != 1 || f.hit(fixtureMobileSummariesPath) != 1 || f.hit(playerLinkPath) != 0 {
				t.Fatal("the region fallback must check the mobile profile exactly once")
			}
		})
	}
}

func TestQRManualStoreCountrySkipsRegionRequests(t *testing.T) {
	f := newFakeSteamMode(t, time.Second, fixtureStoreAccess)
	f.reply(fixtureCountryPath, http.StatusOK, `{"response":{"country":"GB"}}`)
	f.reply(storeSearchPath, http.StatusOK, `{"success":1,"total_count":0,"results_html":""}`)
	a := regionQRApp(t, f, config{CountryCode: " jp "}, time.Now)
	for _, tool := range []string{"steam_wishlist", storeSearchTool} {
		out, err := a.callTool(t.Context(), tool, nil)
		if err != nil {
			t.Fatal(err)
		}
		contains(t, out.Text, `"available":true`, `"country_code":"JP"`)
	}
	if f.hit(fixtureCountryPath) != 0 || f.hit(fixtureMobileSummariesPath) != 0 {
		t.Fatal("an explicit Store country must bypass automatic detection")
	}
}

func TestQRStoreCountryRetriesAndReusesLastSuccess(t *testing.T) {
	f := newFakeSteamMode(t, time.Second, fixtureStoreAccess)
	now, advance := syntheticClock()
	d := regionQRApp(t, f, config{}, now).storeAccount()
	check := func(want string, accountHits, profileHits int) {
		t.Helper()
		if got := d.storeCountry(t.Context(), ""); got != want ||
			f.hit(fixtureCountryPath) != accountHits || f.hit(fixtureMobileSummariesPath) != profileHits {
			t.Fatalf("country = %s, want %s. Requests = %d account and %d profile, want %d and %d",
				got, want, f.hit(fixtureCountryPath), f.hit(fixtureMobileSummariesPath), accountHits, profileHits)
		}
	}
	f.reply(fixtureCountryPath, http.StatusServiceUnavailable, "")
	f.reply(fixtureMobileSummariesPath, http.StatusGatewayTimeout, "")
	check("US", 1, 1)
	f.reply(fixtureMobileSummariesPath, http.StatusOK, fmt.Sprintf(`{"players":[{"steamid":%q,"loccountrycode":"GB"}]}`, fixtureID))
	check("US", 1, 1)
	advance(retryDelay)
	check("GB", 2, 2)

	advance(profileTTL)
	f.reply(fixtureMobileSummariesPath, http.StatusGatewayTimeout, "")
	check("GB", 3, 3)
	f.reply(fixtureCountryPath, http.StatusOK, `{"response":{"country":"JP"}}`)
	check("GB", 3, 3)
	advance(retryDelay)
	check("JP", 4, 3)

	advance(profileTTL)
	f.reply(fixtureCountryPath, http.StatusGatewayTimeout, "")
	check("JP", 5, 3)
	f.reply(fixtureCountryPath, http.StatusOK, `{"response":{"country":"CN"}}`)
	advance(retryDelay)
	check("CN", 6, 3)
}

func TestQRStoreCountryInvalidatedOnLogoutAndAccountChange(t *testing.T) {
	f := newFakeSteamMode(t, 0, fixtureStoreAccess)
	now, advance := syntheticClock()
	a := regionQRApp(t, f, config{}, now)
	old := a.storeAccount()
	f.reply(fixtureCountryPath, http.StatusForbidden, "")
	f.reply(fixtureMobileSummariesPath, http.StatusOK, fmt.Sprintf(`{"players":[{"steamid":%q,"loccountrycode":"GB"}]}`, fixtureID))
	if got := old.storeCountry(t.Context(), ""); got != "GB" {
		t.Fatalf("the old account should cache its profile country, got %s", got)
	}
	advance(retryDelay)
	f.reply(fixtureCountryPath, http.StatusOK, `{"response":{"country":"JP"}}`)
	if got := old.storeCountry(t.Context(), ""); got != "JP" {
		t.Fatalf("the old account should cache its account country, got %s", got)
	}

	advance(profileTTL)
	started := make(chan struct{})
	requestCanceled := make(chan struct{})
	f.handle(fixtureCountryPath, func(w http.ResponseWriter, r *http.Request) {
		switch r.PostFormValue("steamid") {
		case fixtureID:
			close(started)
			<-r.Context().Done()
			close(requestCanceled)
		case fixtureID2:
			_, _ = io.WriteString(w, `{"response":{"country":"CN"}}`)
		default:
			t.Error("Store region queried an unexpected account")
		}
	})
	inFlight := make(chan string, 1)
	go func() { inFlight <- old.storeCountry(t.Context(), "") }()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("old account's region request did not start")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	if issue := a.qr.logout(ctx); issue != "" {
		t.Fatalf("logout failed: %s", issue)
	}
	select {
	case <-requestCanceled:
	case <-ctx.Done():
		t.Fatal("logout did not cancel the old account's request")
	}
	select {
	case country := <-inFlight:
		if country != "US" {
			t.Errorf("an old region read kept a signed-out country's value: %s", country)
		}
	case <-ctx.Done():
		t.Fatal("the old region read did not return after logout")
	}
	a.qr.mu.Lock()
	cleared := old.qrCountry.record.Data == nil && old.qrLocation.record.Data == nil
	a.qr.mu.Unlock()
	if !cleared || a.storeAccount().storeCountry(ctx, "") != "US" {
		t.Fatal("logout retained cached region data")
	}
	installRegionAccount(t, a.qr, fixtureID2)
	if got := a.storeAccount().storeCountry(ctx, ""); got != "CN" || f.hit(fixtureCountryPath) != 4 {
		t.Fatalf("a different QR account must resolve its own Store country, got %s", got)
	}
	if country := old.storeCountry(ctx, ""); country != "US" {
		t.Fatalf("the old generation read a country after the account changed: %s", country)
	}
}

func TestQRStoreCountryFromAccount(t *testing.T) {
	f := newFakeSteamMode(t, time.Second, fixtureStoreAccess)
	f.handle(fixtureCountryPath, func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil || r.PostForm.Get("steamid") != fixtureID || r.URL.Query().Has("steamid") {
			t.Error("account region must send the signed-in SteamID in the POST form")
		}
		_, _ = io.WriteString(w, `{"response":{"country":" jp "}}`)
	})
	a := regionQRApp(t, f, config{}, time.Now)
	var calls sync.WaitGroup
	for range 8 {
		calls.Go(func() {
			out, err := a.callTool(t.Context(), "steam_wishlist", nil)
			if err != nil {
				t.Error(err)
				return
			}
			contains(t, out.Text, `"country_code":"JP"`, `"available":true`)
			assertHidden(t, out.Text, fixtureStoreAccess)
		})
	}
	calls.Wait()
	if f.hit(fixtureCountryPath) != 1 || f.hit(fixtureMobileSummariesPath) != 0 || f.hit(storeBrowsePath) != 1 {
		t.Fatal("QR wishlist reads must coalesce and use the account region before consulting the profile")
	}
}

func TestWebStoreCountryFromProfile(t *testing.T) {
	for _, test := range []struct{ name, location, manual, want string }{
		{"auto", `,"loccountrycode":"gb"`, "", "GB"},
		{"omitted", "", "", "US"},
		{"unassigned", `,"loccountrycode":"ZZ"`, "", "US"},
		{"malformed", `,"loccountrycode":123`, "", "US"},
		{"override", `,"loccountrycode":"GB"`, " cn ", "CN"},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newFakeSteam(t, time.Second)
			f.handle(playerPath, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Query().Get("steamids") != fixtureID {
					t.Error("player summaries must request the configured SteamID")
				}
				_, _ = fmt.Fprintf(w, `{"response":{"players":[{"steamid":%q,"loccountrycode":"JP"},`+
					`{"steamid":%q,"communityvisibilitystate":3%s}]}}`, fixtureID2, fixtureID, test.location)
			})
			h := startTestHost(t, f, fixtureID, test.manual, t.TempDir())
			for range 2 {
				contains(t, toolAnswer(t, h, "steam_wishlist", "{}"), `"available":true`, `"country_code":"`+test.want+`"`)
			}
			assertHidden(t, toolAnswer(t, h, "steam_profile", "{}"), "loccountrycode")
			if f.hit(playerPath) != 1 || f.hit(fixtureCountryPath) != 0 || f.hit(fixtureMobileSummariesPath) != 0 {
				t.Fatal("key mode must reuse its player summary for Store country")
			}
		})
	}
}

func TestWebStoreCountryRefreshesOlderProfileSnapshot(t *testing.T) {
	f := newFakeSteam(t, time.Second)
	now, advance := syntheticClock()
	dir := t.TempDir()
	writeRecord(filepath.Join(dir, "steam-cache-v1", "profile.json"), cacheRecord[player]{
		Scope: fixtureID, At: now(), Data: &player{SteamID: fixtureID, Name: "Cached Pilot", Public: true},
	})
	f.reply(playerPath, http.StatusServiceUnavailable, "")
	d := f.data(dir, fixtureID, "", now)
	if got := d.storeCountry(t.Context(), ""); got != "US" {
		t.Errorf("a cold upgrade during an outage should fall back to US, got %s", got)
	}
	if stale := d.account(t.Context(), profileTTL, false); !stale.Stale || stale.Data == nil || stale.Data.Name != "Cached Pilot" {
		t.Fatal("the older snapshot should remain available for presence")
	}
	advance(retryDelay)
	f.reply(playerPath, http.StatusOK, fmt.Sprintf(`{"response":{"players":[{"steamid":%q,"communityvisibilitystate":3,"loccountrycode":"CA"}]}}`, fixtureID))
	if got := d.storeCountry(t.Context(), ""); got != "CA" || f.hit(playerPath) != 2 {
		t.Fatalf("the older snapshot prevented the regional refresh, got %s", got)
	}
}

func TestStoreCountryPartitionsWishlistAndResearchCaches(t *testing.T) {
	f := newFakeSteamMode(t, time.Second, fixtureStoreAccess)
	now, advance := syntheticClock()
	f.reply(fixtureCountryPath, http.StatusServiceUnavailable, "")
	f.handle(storeBrowsePath, func(w http.ResponseWriter, r *http.Request) {
		input := fixtureBrowseInput(t, r)
		country := input.Context["country_code"]
		if input.DataRequest["include_basic_info"] == true {
			http.Error(w, "synthetic StoreBrowse outage", http.StatusServiceUnavailable)
			return
		}
		_, _ = fmt.Fprintf(w, `{"response":{"store_items":[{"appid":%d,"success":1,"name":%q,"best_purchase_option":{"final_price_in_cents":"399","discount_pct":40}}]}}`, firstApp, "Wishlist "+country)
	})
	f.handle(storeSearchPath, func(w http.ResponseWriter, r *http.Request) {
		country := r.URL.Query().Get("cc")
		writeSyntheticSearch(w, syntheticSearchRow(firstApp, "Search "+country, "", "", "", 0), 1)
	})
	f.handle(appDetailsPath, func(w http.ResponseWriter, r *http.Request) {
		country := r.URL.Query().Get("cc")
		currency := map[string]string{"US": "USD", "GB": "GBP"}[country]
		_, _ = fmt.Fprintf(w, `{"%d":{"success":true,"data":{"steam_appid":%d,"name":%q,"price_overview":{"currency":%q,"final":399}}}}`,
			firstApp, firstApp, "Details "+country, currency)
	})
	f.reply(reviewsAPIPath, http.StatusOK, `{"response":{"total_matching":0}}`)
	a := regionQRApp(t, f, config{}, now)
	check := func(country, currency string) {
		t.Helper()
		for _, tool := range []struct{ name, args, title string }{
			{"steam_wishlist", "{}", "Wishlist "},
			{storeSearchTool, `{"query":"same query"}`, "Search "},
			{gameDetailsTool, fmt.Sprintf(`{"appid":%d}`, firstApp), "Details "},
		} {
			out, err := a.callTool(t.Context(), tool.name, []byte(tool.args))
			if err != nil {
				t.Fatal(err)
			}
			contains(t, out.Text, `"available":true`, `"country_code":"`+country+`"`, tool.title+country)
			if tool.name != storeSearchTool {
				contains(t, out.Text, `"currency":"`+currency+`"`)
			}
		}
	}
	check("US", "USD")
	advance(retryDelay)
	f.reply(fixtureCountryPath, http.StatusOK, `{"response":{"country":"GB"}}`)
	check("GB", "GBP")
	check("GB", "GBP")
	if f.hit(storeSearchPath) != 2 || f.hit(appDetailsPath) != 2 || f.hit(storeBrowsePath) != 5 {
		t.Fatalf("regional data was not isolated and reused. Got %d search, %d legacy details, %d StoreBrowse requests",
			f.hit(storeSearchPath), f.hit(appDetailsPath), f.hit(storeBrowsePath))
	}
	for country, currency := range map[string]string{"US": "USD", "GB": "GBP"} {
		prices, err := a.storeAccount().stores.prices.Histories(fixtureID, country, firstApp)
		if quote := prices[firstApp].Latest; err != nil || quote.Final != 399 || quote.Currency != currency {
			t.Errorf("wishlist history for %s has the wrong quote: %+v", country, quote)
		}
	}
}

func TestAutomaticStoreCountryScopesWishlistInbox(t *testing.T) {
	f := newFakeSteam(t, time.Second)
	now, advance := syntheticClock()
	a := newInboxTestApp(f, t.TempDir(), now, config{TokenConfig: TokenConfig{InboxEventsEnabled: true}})
	games := []inboxFixtureGame{{firstApp, 300, 60, ""}}
	seedInboxWishlist(t, f, games...)
	before, notified := scanTestInbox(t, a, &english)
	if len(before) != 1 || !notified {
		t.Fatal("the first scan should deliver one US price event")
	}
	advance(time.Minute)
	f.reply(playerPath, http.StatusOK, fmt.Sprintf(`{"response":{"players":[{"steamid":%q,"communityvisibilitystate":3,"loccountrycode":"GB"}]}}`, fixtureID))
	f.reply(wishlistPath, http.StatusServiceUnavailable, "")
	if items, changed := scanTestInbox(t, a, &english); len(items) != 0 || changed {
		t.Fatal("a failed GB wishlist read replayed the US queue")
	}
	seedInboxWishlist(t, f, games...)
	after, notified := scanTestInbox(t, a, &english)
	if len(after) != 1 || !notified || after[0].Thread == before[0].Thread {
		t.Fatal("the GB observation reused an alert from the US scope")
	}
	for country, currency := range map[string]string{"US": "USD", "GB": "GBP"} {
		prices, err := a.stores.prices.Histories(fixtureID, country, firstApp)
		if quote := prices[firstApp].Latest; err != nil || quote.Final != 300 || quote.Currency != currency {
			t.Errorf("Inbox wrote the wrong quote for %s: %+v", country, quote)
		}
	}
	out, err := a.callTool(t.Context(), wishlistDealsName, nil)
	if err != nil {
		t.Fatal(err)
	}
	contains(t, out.Text, `"country_code":"GB"`, `"currency":"GBP"`)
	if strings.Contains(out.Text, `"currency":"USD"`) {
		t.Error("the on-demand tool joined a quote from the other region")
	}
}
