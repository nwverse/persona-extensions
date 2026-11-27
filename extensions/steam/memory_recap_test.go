package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/sdk/go/ext"
	"github.com/nwverse/persona/sdk/go/exttest"
	"github.com/nwverse/persona/sdk/go/wire"
)

func TestPlaytimeRecapPeriodsFromLocalHistory(t *testing.T) {
	f := newFakeSteam(t, time.Second)
	dir := t.TempDir()
	now := time.Date(2025, time.December, 10, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	a := &app{}
	h := startInboxTestHost(t, f, a, clock, dir, "en", `"discovery_tools_enabled":false`)
	const newApp = thirdApp + 1
	setLibrary := func(games ...game) {
		raw, err := json.Marshal(games)
		if err != nil {
			t.Fatal(err)
		}
		f.reply(ownedPath, 200, fmt.Sprintf(`{"response":{"game_count":%d,"games":%s}}`, len(games), raw))
	}
	setLibrary(game{AppID: firstApp, Name: "Fixture Quest", PlaytimeForever: 100}, game{AppID: secondApp, Name: "Fixture Rally"}, game{AppID: thirdApp, Name: "Fixture Maze"})
	mark := func(appID uint32, status BacklogStatus) {
		t.Helper()
		answer := toolAnswer(t, h, backlogMarkTool, fmt.Sprintf(`{"appid":%d,"status":%q}`, appID, status))
		contains(t, answer, `"available":true`)
	}
	mark(firstApp, BacklogPlaying)
	mark(secondApp, BacklogQueued)
	mark(thirdApp, BacklogUnplayed)
	fallback := toolAnswer(t, h, "steam_playtime_recap", `{}`)
	contains(t, fallback, `"period":"month"`, `"owned_games":3`, `"total_minutes":100`, `"playtime_2weeks":130`,
		`"playing":1`, `"queued":1`, "start snapshot", "automatically")
	assertHidden(t, fallback, `"delta":`)

	now = time.Date(2026, time.January, 1, 12, 0, 0, 0, time.UTC)
	setLibrary(game{AppID: firstApp, Name: "Fixture Quest", PlaytimeForever: 150}, game{AppID: secondApp, Name: "Fixture Rally"}, game{AppID: thirdApp, Name: "Fixture Maze"})
	toolAnswer(t, h, "steam_playtime_recap", `{}`)
	now = time.Date(2026, time.October, 2, 12, 0, 0, 0, time.UTC)
	setLibrary(game{AppID: firstApp, Name: "Fixture Quest", PlaytimeForever: 250}, game{AppID: secondApp, Name: "Fixture Rally"}, game{AppID: thirdApp, Name: "Fixture Maze"})
	toolAnswer(t, h, "steam_playtime_recap", `{}`)
	now = time.Date(2026, time.November, 1, 12, 0, 0, 0, time.UTC)
	setLibrary(game{AppID: firstApp, Name: "Fixture Quest", PlaytimeForever: 300}, game{AppID: secondApp, Name: "Fixture Rally"}, game{AppID: thirdApp, Name: "Fixture Maze"})
	toolAnswer(t, h, "steam_playtime_recap", `{}`)

	now = time.Date(2026, time.November, 27, 12, 0, 0, 0, time.UTC)
	setLibrary(game{AppID: firstApp, Name: "Fixture Quest", PlaytimeForever: 340}, game{AppID: secondApp, Name: "Fixture Rally", PlaytimeForever: 120}, game{AppID: newApp, Name: "Fixture Castle", PlaytimeForever: 10})
	mark(firstApp, BacklogCompleted)
	mark(secondApp, BacklogPlaying)
	mark(newApp, BacklogDropped)
	// Start a fresh process to prove the comparison survives a restart.
	h = startInboxTestHost(t, f, &app{}, clock, dir, "en", `"discovery_tools_enabled":false`)
	for _, tc := range []struct {
		period string
		start  string
		mins   int
	}{
		{"month", "2026-11-01", 170}, {"quarter", "2026-10-02", 220}, {"year", "2026-01-01", 320}, {"all", "2025-12-10", 370},
	} {
		answer := toolAnswer(t, h, "steam_playtime_recap", fmt.Sprintf(`{"period":%q,"top_n":1}`, tc.period))
		contains(t, answer, `"from":"`+tc.start, fmt.Sprintf(`"playtime_minutes":%d`, tc.mins), `"new_games":1`,
			`"top_games":[`, `"backlog_at_start":2`, `"backlog_consumed":1`, `"backlog_consumption_rate":0.5`,
			`"count_changes":{"backlog":-2,"completed":1,"dropped":1,"playing":0}`,
			`"moved_into":{"backlog":0,"completed":1,"dropped":1,"playing":1}`)
		var result playtimeRecap
		if err := json.Unmarshal([]byte(answer), &result); err != nil {
			t.Fatal(err)
		}
		want := firstApp
		if tc.period == "month" || tc.period == "quarter" {
			want = secondApp
		}
		if result.Delta == nil || len(result.Delta.TopGames) != 1 || result.Delta.TopGames[0].AppID != want {
			t.Errorf("%s top game is incorrect: %s", tc.period, answer)
		}
	}
	path := filepath.Join(dir, storeDirectory, "playtime-recap.json")
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Hour)
	toolAnswer(t, h, "steam_playtime_recap", `{}`)
	after, err := os.Stat(path)
	if err != nil || !os.SameFile(before, after) || !before.ModTime().Equal(after.ModTime()) || f.hit(ownedPath) != 5 {
		t.Fatalf("same-day recaps should reuse the library and skip disk replacement: %v, %d library reads", err, f.hit(ownedPath))
	}
	nav := wire.UIParams{Contribution: extensionID, Size: wire.SizeFull, Placement: wire.PlacementNavPage, UIVersion: 12, Locale: "en"}
	view := act(t, h, nav, viewAnswer(t, h, nav), recapAction, nil)
	view = act(t, h, nav, view, recapPeriodAction, map[string]string{"period": "year"})
	page, _ := json.Marshal(view)
	contains(t, string(page), "Observed 2026-01-01 through 2026-11-27", "5 h 20 min", "50%", "Fixture Quest")
}

func TestPlaytimeRecapPageLoadsOnDemand(t *testing.T) {
	f := newFakeSteam(t, time.Second)
	dir := t.TempDir()
	h := startTestHost(t, f, fixtureID, "GB", dir)
	nav := wire.UIParams{Contribution: extensionID, Size: wire.SizeFull, Placement: wire.PlacementNavPage, UIVersion: 12, Locale: "zh-Hans"}
	first := viewAnswer(t, h, nav)
	if _, err := os.Stat(filepath.Join(dir, storeDirectory, "playtime-recap.json")); !os.IsNotExist(err) {
		t.Fatalf("overview should skip recap history: %v", err)
	}
	var action string
	for _, item := range first.Sections[0].Items {
		if item.Title == "游玩复盘" && item.Type == wire.ComponentButton && item.Action != nil {
			action = item.Action.ID
		}
	}
	if action == "" {
		t.Fatal("the overview is missing the recap button")
	}
	filtered := act(t, h, nav, first, "search", map[string]string{"q": "rally"})
	recap := act(t, h, nav, filtered, action, nil)
	if recap.Title != "游玩复盘" || recap.Filter == nil || recap.Filter.Value != "month" || len(recap.Filter.Choices) != 4 {
		t.Fatalf("invalid recap navigation: %+v", recap)
	}
	raw, _ := json.Marshal(recap)
	contains(t, string(raw), "周期起点快照", "自动累计", chinese.recentTime)
	for _, period := range []string{"quarter", "year", "all", "month"} {
		recap = act(t, h, nav, recap, recap.Filter.Action.ID, map[string]string{recap.Filter.Name: period})
		if recap.Filter == nil || recap.Filter.Value != period {
			t.Fatalf("period did not change to %s", period)
		}
	}
	if f.hit(ownedPath) != 1 || f.hit(recentPath) != 1 || f.hit(wishlistPath) != 1 {
		t.Fatal("recap period changes should reuse the library and recent cache")
	}
	if back := act(t, h, nav, recap, "back", nil); back.Search == nil || back.Search.Value != "rally" {
		t.Fatal("returning from recap lost the parent page's search state")
	}
}

func TestPlaytimeRecapQRHistoryIsSessionOnly(t *testing.T) {
	dir := t.TempDir()
	var clock atomic.Int64
	start := time.Date(2026, time.November, 27, 23, 58, 0, 0, time.UTC)
	clock.Store(start.Unix())
	now := func() time.Time { return time.Unix(clock.Load(), 0).UTC() }
	a := &app{}
	f, h := startRecapQRHost(t, a, dir, now, `{"auth_mode":"qr","discovery_tools_enabled":false}`)
	signIn := func() {
		authStep(t, h, "")
		await(t, 5*time.Second, func() bool { return authStep(t, h, qrStep).Done })
	}
	signIn()
	answer := toolAnswer(t, h, playtimeRecapName, `{}`)
	contains(t, answer, `"available":true`, `"hint":`)
	clock.Add(3 * 60)
	f.reply(ownedPath, http.StatusOK, fmt.Sprintf(`{"response":{"game_count":1,"games":[{"appid":%d,"name":"Fixture Quest","playtime_forever":240}]}}`, firstApp))
	answer = toolAnswer(t, h, playtimeRecapName, `{}`)
	contains(t, answer, `"playtime_minutes":40`, `"new_games":0`)
	if f.hit(ownedPath) != 2 {
		t.Fatal("recap should refresh an overnight library cache on demand")
	}
	for _, name := range []string{storeDirectory, "steam-cache-v1"} {
		if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Fatalf("QR recap wrote remote account data under %s: %v", name, err)
		}
	}
	a.qr.mu.Lock()
	previous := a.qr.session.reads
	a.qr.mu.Unlock()
	var status wire.AuthStatus
	if err := h.Call("auth.logout", ext.Empty{}, &status); err != nil || status.State != wire.AuthStateSignedOut {
		t.Fatalf("QR logout failed: %s, %v", status.State, err)
	}
	requireStoreOK(t, previous.stores.playtime.recaps.view(func(entries map[string][]recapSnapshot) {
		if len(entries) != 0 {
			t.Error("the signed-out session retained recap history")
		}
	}))
	answer = toolAnswer(t, h, playtimeRecapName, `{}`)
	contains(t, answer, `"available":false`, string(problemAuthRequired))
	assertHidden(t, answer, `"library":`, `"delta":`)
}

func startRecapQRHost(t *testing.T, a *app, dir string, now func() time.Time, cfg string) (*fakeSteam, *exttest.Host) {
	t.Helper()
	t.Setenv("PERSONA_EXTENSION_DATA_DIR", dir)
	access := fixtureJWT(t, fixtureID, now().Add(time.Hour), "recap-access")
	refresh := fixtureSecret(t, fixtureID, now()).RefreshToken
	f := newFakeSteamMode(t, time.Second, access)
	f.handle(authBasePath+beginQRPath+"/v1/", func(w http.ResponseWriter, _ *http.Request) {
		authFixture(w, "1", beginFixture())
	})
	f.handle(authBasePath+pollQRPath+"/v1/", func(w http.ResponseWriter, _ *http.Request) {
		authFixture(w, "1", grantFixture(refresh, access))
	})
	h := exttest.StartWith(t, manifest, exttest.Serve(t, a.program(f.http, now)), func(p *wire.InitializeParams) {
		p.Config, p.Hub.Locale = json.RawMessage(cfg), "en"
	})
	return f, h
}

func TestPlaytimeRecapRejectsInvalidArgsWithoutReading(t *testing.T) {
	f := newFakeSteam(t, time.Second)
	h := startTestHost(t, f, fixtureID, "GB", t.TempDir())
	for _, args := range []string{`{"period":"week"}`, `{"period":""}`, `{"top_n":0}`, `{"top_n":21}`, `{"other":true}`} {
		var out wire.ConnectorCallResult
		err := h.Call("connector.tools.call", wire.ConnectorCallParams{
			Contribution: extensionID, Name: playtimeRecapName, Args: json.RawMessage(args),
		}, &out)
		var issue *ext.Error
		if !errors.As(err, &issue) || issue.Code != "invalid" || out.Text != "" {
			t.Errorf("invalid recap args %s yielded %q and %v", args, out.Text, err)
		}
	}
	if f.hit(playerPath) != 0 || f.hit(ownedPath) != 0 || f.hit(recentPath) != 0 {
		t.Fatal("invalid recap args contacted Steam")
	}
}
