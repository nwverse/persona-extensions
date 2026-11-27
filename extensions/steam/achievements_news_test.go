package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/sdk/go/exttest"
	"github.com/nwverse/persona/sdk/go/wire"
)

func seedAchievementRanking(t *testing.T, f *fakeSteam) {
	f.handle("/ISteamUserStats/GetGlobalAchievementPercentagesForApp/v2/", func(w http.ResponseWriter, r *http.Request) {
		if q := r.URL.Query(); len(q) != 1 || q.Get("gameid") != appIDText(firstApp) {
			t.Error("global rates must be requested anonymously by gameid")
		}
		_, _ = io.WriteString(w, `{"achievementpercentages":{"achievements":[
			{"name":"hard","percent":"2.5"},{"name":"old","percent":95},{"name":"secret_ending","percent":"40.2"},
			{"name":"easy","percent":"80.1"},{"name":"new","percent":90}]}}`)
	})
}

func TestAchievementHunterRanksLockedAndDatedUnlocks(t *testing.T) {
	f := newFakeSteam(t, time.Second)
	seedAchievementRanking(t, f)
	f.reply(achievementsPath, http.StatusOK, `{"playerstats":{"success":true,"gameName":"Synthetic Adventure","achievements":[
		{"apiname":"hard","achieved":0},{"apiname":"old","achieved":1,"unlocktime":100},
		{"apiname":"new","achieved":1,"unlocktime":200},{"apiname":"easy","achieved":0},
		{"apiname":"secret_ending","achieved":0}]}}`)
	f.reply("/ISteamUserStats/GetSchemaForGame/v2/", http.StatusOK, `{"game":{"gameName":"Synthetic Adventure","availableGameStats":{"achievements":[
		{"name":"hard","displayName":"Steep Climb","hidden":0},{"name":"old","displayName":"First Win","hidden":0},
		{"name":"new","displayName":"Second Win","hidden":0},{"name":"easy","displayName":"Getting Started","hidden":0,"description":"Finish the tutorial"},
		{"name":"secret_ending","displayName":"Ending Spoiler","hidden":1,"description":"Spoiler text"}]}}}`)
	h := startTestHost(t, f, fixtureID, "GB", "")
	answer := toolAnswer(t, h, "steam_achievement_hunter", fmt.Sprintf(`{"appid":%d,"limit":2}`, firstApp))
	var got struct {
		Available       bool `json:"available"`
		PersonalDetails bool `json:"personal_details"`
		NextOffset      int  `json:"next_offset"`
		Progress        achievementProgress
		Locked          []struct {
			ID      string  `json:"api_name"`
			Hidden  *bool   `json:"hidden"`
			Percent float64 `json:"global_percent"`
		}
		Timeline []struct {
			Time int64 `json:"unlock_time"`
		}
	}
	if err := json.Unmarshal([]byte(answer), &got); err != nil {
		t.Fatal(err)
	}
	if !got.Available || !got.PersonalDetails || got.Progress.Total != 5 || got.Progress.Unlocked != 2 ||
		len(got.Locked) != 2 || got.Locked[0].ID != "easy" || got.Locked[0].Percent != 80.1 || got.Locked[0].Hidden == nil || *got.Locked[0].Hidden ||
		got.Locked[1].Hidden == nil || !*got.Locked[1].Hidden || got.Locked[1].Percent != 40.2 || got.NextOffset != 2 {
		t.Fatalf("incorrect achievement sprint: %s", answer)
	}
	if len(got.Timeline) != 2 || got.Timeline[0].Time != 200 || got.Timeline[1].Time != 100 {
		t.Fatalf("incorrect unlock timeline: %s", answer)
	}
	assertHidden(t, answer, "secret_ending", "Ending Spoiler", "Spoiler text")
	page := toolAnswer(t, h, "steam_achievement_hunter", fmt.Sprintf(`{"appid":%d,"limit":2,"offset":2}`, firstApp))
	contains(t, page, `"api_name":"hard"`, `"timeline":[]`)
	if hits := f.hit("/ISteamUserStats/GetGlobalAchievementPercentagesForApp/v2/"); hits != 1 {
		t.Fatalf("pagination used %d global requests, want 1", hits)
	}
}

func TestAchievementHunterPrivateProfileUsesGlobalRankings(t *testing.T) {
	f := newFakeSteam(t, time.Second)
	seedAchievementRanking(t, f)
	f.reply(playerPath, http.StatusOK, fmt.Sprintf(`{"response":{"players":[{"steamid":%q,"communityvisibilitystate":1}]}}`, fixtureID))
	f.reply("/ISteamUserStats/GetSchemaForGame/v2/", http.StatusOK, `{"game":{"availableGameStats":{"achievements":[
		{"name":"old","displayName":"First Win","hidden":0},{"name":"hard","displayName":"Steep Climb","hidden":0},
		{"name":"secret_ending","displayName":"Ending Spoiler","hidden":1}]}}}`)
	h := startTestHost(t, f, fixtureID, "GB", "")
	answer := toolAnswer(t, h, "steam_achievement_hunter", fmt.Sprintf(`{"appid":%d,"limit":1}`, firstApp))
	contains(t, answer, `"available":true`, `"personal_details":false`, `"code":"private_profile"`, `"unlocked":null`, `"total":5`,
		`"global_easiest":[{"api_name":"old"`, `"global_rarest":[{"api_name":"hard"`)
	assertHidden(t, answer, "secret_ending", "Ending Spoiler", `"locked":`, `"timeline":`)
	if f.hit(achievementsPath) != 0 {
		t.Fatal("the hunter attempted to read achievements for a private Key-mode profile")
	}
}

func TestAchievementHunterQRDetailsAndCountOnly(t *testing.T) {
	now := time.Now()
	access := fixtureJWT(t, fixtureID, now.Add(time.Hour), "hunter-access")
	saved := fixtureSecret(t, fixtureID, now)
	f := newFakeSteamMode(t, time.Second, access)
	f.handle(authBasePath+beginQRPath+"/v1/", func(w http.ResponseWriter, _ *http.Request) {
		authFixture(w, "1", beginFixture())
	})
	f.handle(authBasePath+pollQRPath+"/v1/", func(w http.ResponseWriter, _ *http.Request) {
		authFixture(w, "1", grantFixture(saved.RefreshToken, access))
	})
	seedAchievementRanking(t, f)
	f.handle(gameAchievementsPath, func(w http.ResponseWriter, r *http.Request) {
		if q := r.URL.Query(); q.Get("appid") != appIDText(firstApp) || q.Get("language") != "english" {
			t.Error("the public schema did not receive the requested app and language")
		}
		_, _ = io.WriteString(w, `{"response":{"schema_hash":15,"achievements":[
			{"internal_name":"easy","internal_key":81,"localized_name":"Getting Started"},
			{"internal_name":"hard","internal_key":82,"localized_name":"Steep Climb"},
			{"internal_name":"old","internal_key":83,"localized_name":"First Win"},
			{"internal_name":"new","internal_key":84,"localized_name":"Second Win"},
			{"internal_name":"secret_ending","internal_key":85,"localized_name":"Ending Spoiler","hidden":true},
			{"internal_name":"retired","internal_key":86,"archived":true}]}}`)
	})
	f.handle(userAchievementsPath, func(w http.ResponseWriter, r *http.Request) {
		if q := r.URL.Query(); q.Get("appid") != appIDText(firstApp) || q.Get("steamid") != fixtureID {
			t.Error("the QR detail call did not target the signed-in account and requested app")
		}
		_, _ = io.WriteString(w, `{"response":{"schema_hash":15,"achievements":[
			{"internal_key":84,"unlocked":true,"unlock_time":200},{"internal_key":83,"unlocked":true,"unlock_time":100},
			{"internal_key":81,"unlocked":false}]}}`)
	})
	f.reply(progressPath, http.StatusOK, fmt.Sprintf(`{"response":{"achievement_progress":[{"appid":%d,"unlocked":2,"total":5}]}}`, firstApp))
	h := exttest.StartWith(t, manifest, exttest.Serve(t, program(f.http, func() time.Time { return now })), func(p *wire.InitializeParams) {
		p.Config = json.RawMessage(`{"auth_mode":"qr"}`)
		p.Hub.Locale = "en"
	})
	authStep(t, h, "")
	await(t, 5*time.Second, func() bool { return authStep(t, h, qrStep).Done })
	args := fmt.Sprintf(`{"appid":%d}`, firstApp)
	answer := toolAnswer(t, h, achievementHunterName, args)
	contains(t, answer, `"personal_details":true`, `"limit":20`, `"timeline":[{"api_name":"new"`,
		`"locked":[{"api_name":"easy"`, `"api_name":"hard"`, `"hidden":true`)
	assertHidden(t, answer, "retired", "secret_ending", "Ending Spoiler", access, saved.RefreshToken)

	f.reply(userAchievementsPath, http.StatusForbidden, "private")
	answer = toolAnswer(t, h, achievementHunterName, args)
	contains(t, answer, `"personal_details":false`, `"total":5`, `"unlocked":2`, string(problemAchievementDetails),
		`"global_easiest":[{"api_name":"old"`, `"global_rarest":[{"api_name":"hard"`)
	assertHidden(t, answer, `"locked":`, `"timeline":`, "secret_ending", access, saved.RefreshToken)
	if f.hit(playerLinkPath) != 0 || f.hit(achievementsPath) != 0 || f.hit(globalAchievementsPath) != 1 {
		t.Fatal("QR achievement calls should use the signed-in SteamID and reuse cached public rates")
	}
}

func TestGlobalAchievementRequestRejectsRedirectsAndOversizedBodies(t *testing.T) {
	for _, name := range []string{"redirect", "oversized"} {
		t.Run(name, func(t *testing.T) {
			f := newFakeSteam(t, 2*time.Second)
			f.handle(globalAchievementsPath, func(w http.ResponseWriter, r *http.Request) {
				if name == "redirect" {
					http.Redirect(w, r, apiBase+globalAchievementsPath+"?gameid="+appIDText(secondApp), http.StatusFound)
					return
				}
				_, _ = io.WriteString(w, `{"achievementpercentages":{"achievements":[{"name":"easy","percent":85}]}}`)
				_, _ = io.WriteString(w, strings.Repeat(" ", maxAPIBytes))
			})
			c := steamClient{http: f.http}
			if data, issue := c.globalAchievementRates(t.Context(), firstApp); data != nil || issue != problemResponse || f.hit(globalAchievementsPath) != 1 {
				t.Fatalf("the %s global request should fail without following a redirect: %v, %s", name, data, issue)
			}
		})
	}
}
