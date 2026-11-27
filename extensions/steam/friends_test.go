package main

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/sdk/go/exttest"
	"github.com/nwverse/persona/sdk/go/wire"
)

func TestFriendsRadarOptIn(t *testing.T) {
	var source struct {
		Configuration struct {
			Properties struct {
				Radar struct {
					Type    string `json:"type"`
					Default *bool  `json:"default"`
				} `json:"friends_radar_enabled"`
			} `json:"properties"`
		} `json:"configuration"`
	}
	if err := json.Unmarshal(authorManifest, &source); err != nil {
		t.Fatal(err)
	}
	setting := source.Configuration.Properties.Radar
	if setting.Type != "boolean" || setting.Default == nil || *setting.Default {
		t.Fatal("the manifest must declare the radar as an off-by-default boolean")
	}
	for _, tc := range []struct {
		name, config, locale, hint string
		enabled                    bool
	}{
		{name: "default", locale: "zh-Hans", hint: "好友开黑雷达默认关闭，请在 Steam 扩展设置中开启"},
		{name: "disabled", config: `,"friends_radar_enabled":false`, locale: "en", hint: "Friends Radar is off by default. Enable it in the Steam extension settings."},
		{name: "enabled", config: `,"friends_radar_enabled":true`, locale: "en", enabled: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeSteam(t, time.Second)
			var requests atomic.Int32
			client := steamHTTP(roundTripFunc(func(r *http.Request) (*http.Response, error) {
				requests.Add(1)
				return f.http.Transport.RoundTrip(r)
			}), time.Second)
			h := startFriendsHost(t, client, f.key, tc.locale, fmt.Sprintf(`{"steam_id":%q%s}`, fixtureID, tc.config), t.TempDir(), time.Now)
			var listed wire.ConnectorTools
			if err := h.Call("connector.tools.list", wire.ConnectorCallParams{Contribution: extensionID}, &listed); err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{achievementHunterName, gameNewsName} {
				if !slices.ContainsFunc(listed.Tools, func(tool wire.ConnectorTool) bool { return tool.Name == name }) {
					t.Fatalf("%s must be listed independently of the friends radar setting", name)
				}
			}
			for _, name := range []string{"steam_friends_radar"} {
				if shown := slices.ContainsFunc(listed.Tools, func(tool wire.ConnectorTool) bool { return tool.Name == name }); shown != tc.enabled {
					t.Fatalf("%s listed = %v, want %v", name, shown, tc.enabled)
				}
				if !tc.enabled {
					err := h.Call("connector.tools.call", wire.ConnectorCallParams{
						Contribution: extensionID, Name: name, Args: json.RawMessage(`{"unexpected":true}`),
					}, nil)
					if err == nil || !strings.Contains(err.Error(), tc.hint) {
						t.Fatalf("disabled %s: %v", name, err)
					}
				}
			}
			if n := requests.Load(); n != 0 {
				t.Fatalf("listing or calling disabled friends tools made %d Steam requests", n)
			}
			if !tc.enabled {
				v := viewAnswer(t, h, wire.UIParams{Contribution: extensionID, Locale: tc.locale})
				if slices.ContainsFunc(v.Sections, func(section wire.Section) bool {
					return section.Title == textFor(tc.locale).friendsPlaying
				}) || f.hit(playerPath) != 1 {
					t.Fatal("disabled radar changed the Steam navigation page")
				}
				// The synthetic transport rejects all routes outside the original navigation reads.
				if n := requests.Load(); n != 6 {
					t.Fatalf("disabled navigation made %d Steam requests, want the six baseline reads", n)
				}
			}
		})
	}
}

func startFriendsHost(t *testing.T, client *http.Client, key, locale, configJSON, dir string, now func() time.Time) *exttest.Host {
	t.Helper()
	t.Setenv("PERSONA_EXTENSION_DATA_DIR", dir)
	return exttest.StartWith(t, manifest, exttest.Serve(t, program(client, now)), func(p *wire.InitializeParams) {
		p.Config = json.RawMessage(configJSON)
		p.Secrets = map[string]string{keyName: key}
		p.Hub.Locale = locale
	})
}

func newFriendsClient(t *testing.T, key, access string, handler http.HandlerFunc) *http.Client {
	t.Helper()
	server := httptest.NewTLSServer(handler)
	t.Cleanup(server.Close)
	u, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	return steamHTTP(roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodGet || req.URL.Scheme != "https" || req.URL.Host != apiHost {
			t.Error("friends read used an unexpected method or host")
			return nil, fmt.Errorf("unexpected Steam route")
		}
		wantKey, wantAccess := key, access
		if req.URL.Path == storeNamesPath || (req.URL.Path == wishlistPath && access == "") {
			wantKey, wantAccess = "", ""
		} else if access != "" {
			wantKey = ""
		}
		q := req.URL.Query()
		if q.Get("access_token") != wantAccess || req.Header.Get("X-WebAPI-Key") != wantKey ||
			q.Has("key") || q.Has("refresh_token") || req.Header.Get("Authorization") != "" ||
			(wantAccess == "" && q.Has("access_token")) || (key != "" && strings.Contains(req.URL.String(), key)) {
			t.Error("friends read used an unexpected credential")
			return nil, fmt.Errorf("unexpected Steam credential placement")
		}
		clone := req.Clone(req.Context())
		clone.URL.Scheme, clone.URL.Host = u.Scheme, u.Host
		return server.Client().Transport.RoundTrip(clone)
	}), time.Second)
}

func syntheticFriendID(n int) string {
	return strconv.FormatUint(steamIDBase+(1<<32)-100-uint64(n), 10)
}

func sendFriendList(t *testing.T, w http.ResponseWriter, r *http.Request, ids []string) {
	t.Helper()
	if q := r.URL.Query(); q.Get("steamid") != fixtureID || q.Get("relationship") != "friend" {
		t.Error("friends list did not use the current account and friend relationship filter")
	}
	friends := make([]map[string]string, 0, len(ids))
	for _, id := range ids {
		friends = append(friends, map[string]string{"steamid": id, "relationship": "friend"})
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"friendslist": map[string]any{"friends": friends}})
}

func TestFriendsRadarPresence(t *testing.T) {
	key := rand.Text()
	var clock atomic.Int64
	clock.Store(time.Now().Unix())
	now := func() time.Time { return time.Unix(clock.Load(), 0) }
	ids := make([]string, 104)
	for i := range ids {
		ids[i] = syntheticFriendID(i)
	}
	var listHits, presenceHits atomic.Int32
	var failList atomic.Bool
	client := newFriendsClient(t, key, "", func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case vanityPath:
			if r.URL.Query().Get("vanityurl") != fixtureVanity {
				t.Error("friends radar tried to resolve an unexpected vanity ID")
			}
			_, _ = fmt.Fprintf(w, `{"response":{"success":1,"steamid":%q}}`, fixtureID)
		case friendListPath:
			listHits.Add(1)
			if failList.Load() {
				http.Error(w, key+ids[0], http.StatusServiceUnavailable)
				return
			}
			sendFriendList(t, w, r, append(slices.Clone(ids), ids[0], "invalid", fixtureID))
		case playerPath:
			if r.URL.Query().Get("steamids") == fixtureID {
				_, _ = fmt.Fprintf(w, `{"response":{"players":[{"steamid":%q,"communityvisibilitystate":3}]}}`, fixtureID)
				return
			}
			presenceHits.Add(1)
			batch := strings.Split(r.URL.Query().Get("steamids"), ",")
			if (len(batch) != 100 && len(batch) != 4) || slices.Contains(batch, fixtureID) {
				t.Error("player summaries received an unexpected batch")
			}
			players := make([]map[string]any, 0)
			for _, p := range []struct {
				index, visibility, state int
				name, game               string
			}{
				{10, 3, 0, "Offline Fixture", ""}, {7, 3, 3, "Away Fixture", ""}, {5, 3, 2, "Busy Fixture", ""},
				{3, 3, 1, "Online Fixture", ""}, {2, 1, 1, "Private Fixture", "Withheld fixture game"},
				{1, 3, 7, "Invisible Fixture", "Invisible fixture game"},
				{50, 3, 2, "B Playing Fixture", ""}, {0, 3, 1, "A Playing Fixture " + key, "Synthetic co-op game"},
			} {
				if !slices.Contains(batch, ids[p.index]) {
					continue
				}
				entry := map[string]any{"steamid": ids[p.index], "communityvisibilitystate": p.visibility, "personastate": p.state, "personaname": p.name}
				if p.game != "" || p.index == 50 {
					entry["gameid"], entry["gameextrainfo"] = appIDText(firstApp), p.game
				}
				players = append(players, entry)
			}
			players = append(players, map[string]any{"steamid": syntheticFriendID(900), "personaname": "Unrequested Fixture", "communityvisibilitystate": 3, "personastate": 1})
			_ = json.NewEncoder(w).Encode(map[string]any{"response": map[string]any{"players": players}})
		case recentPath:
			_, _ = fmt.Fprint(w, `{"response":{"total_count":0}}`)
		case ownedPath:
			_, _ = fmt.Fprint(w, `{"response":{"game_count":0}}`)
		case wishlistPath:
			_, _ = fmt.Fprint(w, `{"response":{"items":[]}}`)
		default:
			t.Error("unexpected friends radar route")
			http.NotFound(w, r)
		}
	})
	dir := t.TempDir()
	h := startFriendsHost(t, client, key, "en", fmt.Sprintf(`{"steam_id":%q,"friends_radar_enabled":true}`, fixtureVanity), dir, now)

	first := toolAnswer(t, h, friendsRadarTool, `{"limit":2}`)
	var radar struct {
		Friends      []player            `json:"friends"`
		InGame       []currentFriendGame `json:"in_game_now"`
		InGameCount  int                 `json:"in_game_count"`
		MatchedCount int                 `json:"matched_count"`
		FriendCount  int                 `json:"friend_count"`
		UnknownCount int                 `json:"presence_unknown_count"`
		NextOffset   int                 `json:"next_offset"`
	}
	if json.Unmarshal([]byte(first), &radar) != nil || len(radar.Friends) != 2 || radar.Friends[0].SteamID != ids[0] || radar.Friends[1].SteamID != ids[50] ||
		radar.InGameCount != 2 || radar.MatchedCount != 5 || radar.NextOffset != 2 || radar.FriendCount != len(ids) || radar.UnknownCount != len(ids)-7 ||
		len(radar.InGame) != 1 || radar.InGame[0].Count != 2 || radar.InGame[0].Game != "Synthetic co-op game" || len(radar.InGame[0].Friends) != 2 {
		t.Fatal("unexpected default friends ordering, pagination or in-game aggregation")
	}
	assertHidden(t, first, key, "Offline Fixture", "Withheld fixture game", "Unrequested Fixture", "Invisible fixture game")
	second := toolAnswer(t, h, friendsRadarTool, `{"offset":2,"online_only":false}`)
	if json.Unmarshal([]byte(second), &radar) != nil || len(radar.InGame) != 0 {
		t.Fatal("the in-game groups should cover the current page only")
	}
	var names []string
	for _, p := range radar.Friends {
		names = append(names, p.Name)
	}
	if !slices.Equal(names, []string{"Online Fixture", "Away Fixture", "Busy Fixture", "Invisible Fixture", "Offline Fixture", "Private Fixture"}) {
		t.Fatal("presence ordering did not place online, away/busy, offline and unknown friends in that order")
	}
	assertHidden(t, second, "Withheld fixture game", "Unrequested Fixture", "Invisible fixture game")
	contains(t, toolAnswer(t, h, friendsRadarTool, `{"in_game_only":true}`), `"matched_count":2`, `"limit":20`)

	if listHits.Load() != 1 || presenceHits.Load() != 2 {
		t.Fatal("friends reads were not batched and shared across tool calls")
	}
	// The vanity mapping may be cached, but no friend data belongs on disk.
	files, err := os.ReadDir(dir + "/steam-cache-v1")
	if err != nil || len(files) != 1 || files[0].Name() != "vanity.json" {
		t.Fatal("friends tool wrote more than the existing account vanity mapping")
	}
	nav := wire.UIParams{Contribution: extensionID, Locale: "zh-Hans"}
	view := viewAnswer(t, h, nav)
	section := view.Sections[len(view.Sections)-1]
	if section.Title != "好友在玩" || len(section.Items) != 2 || section.Items[0].Subtitle != "Synthetic co-op game" || listHits.Load() != 1 || presenceHits.Load() != 2 {
		t.Fatal("the navigation summary did not use the cached, sorted in-game results")
	}
	if !slices.ContainsFunc(view.Sections[0].Items, func(item wire.Component) bool {
		return item.Type == wire.ComponentButton && item.Title == "近期官方更新" && item.Action != nil && item.Action.ID == gameNewsAction
	}) {
		t.Fatal("the enabled radar hid the official updates entry")
	}
	clock.Add(int64(friendTTL/time.Second) + 1)
	news := act(t, h, nav, view, gameNewsAction, nil)
	if news.Title != "近期官方更新" || listHits.Load() != 1 || presenceHits.Load() != 2 {
		t.Fatal("the official updates page did not open independently of expired friend data")
	}
	_ = viewAnswer(t, h, wire.UIParams{Contribution: summaryID, Size: wire.SizeCompact, Locale: "zh-Hans"})
	if listHits.Load() != 1 || presenceHits.Load() != 2 {
		t.Fatal("the overview card requested friend data after the cache expired")
	}
	failList.Store(true)
	failed := toolAnswer(t, h, friendsRadarTool, `{}`)
	contains(t, failed, `"available":false`, `"steam_unavailable"`)
	assertHidden(t, failed, key, ids[0], "Synthetic co-op game")
	if listHits.Load() != 2 || presenceHits.Load() != 2 {
		t.Fatal("an expired friends list was reused during a failure")
	}
}

func TestFriendsRadarPrivateList(t *testing.T) {
	key := rand.Text()
	client := newFriendsClient(t, key, "", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != friendListPath {
			t.Error("a private list triggered another upstream read")
		}
		http.Error(w, key+syntheticFriendID(0), http.StatusUnauthorized)
	})
	h := startFriendsHost(t, client, key, "en", fmt.Sprintf(`{"steam_id":%q,"friends_radar_enabled":true}`, fixtureID), t.TempDir(), time.Now)
	got := toolAnswer(t, h, friendsRadarTool, `{}`)
	contains(t, got, `"available":false`, `"friends_list_unavailable"`, "Friends List visibility")
	assertHidden(t, got, key, syntheticFriendID(0))
}

func newFriendsQRApp(t *testing.T, client *http.Client, access string, saved steamSecret, now time.Time, locale string) (*app, *qrSession) {
	t.Helper()
	h, root, hub := testSecretHub(t)
	answerSaved(t, h, saved)
	cfg := config{AuthMode: modeQR, CountryCode: "US", FriendsRadarEnabled: true}
	q := newQRAuth(root, hub, client, func() time.Time { return now }, cfg)
	if !q.waitLoaded(t.Context()) {
		t.Fatal("could not initialize the synthetic QR session")
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	q.installLocked(steamTokens{secret: saved, refreshExpiry: now.Add(time.Hour), access: access, accessExpiry: now.Add(time.Hour)})
	return &app{steamData: &steamData{config: cfg}, qr: q, locale: locale}, q.session
}

func callFriendsFixture(t *testing.T, a *app, name, args string) string {
	t.Helper()
	result, err := a.callTool(t.Context(), name, json.RawMessage(args))
	if err != nil || !json.Valid([]byte(result.Text)) {
		t.Fatalf("%s did not yield a structured result: %v", name, err)
	}
	return result.Text
}

func TestFriendsRadarQRPresenceAndLogout(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	access := fixtureJWT(t, fixtureID, now.Add(time.Hour), "friends-access")
	saved := fixtureSecret(t, fixtureID, now)
	ids := []string{syntheticFriendID(1), syntheticFriendID(0)}
	var hits atomic.Int32
	client := newFriendsClient(t, rand.Text(), access, func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		switch r.URL.Path {
		case friendListPath:
			sendFriendList(t, w, r, ids)
		case playerLinkPath:
			q := r.URL.Query()
			if q.Get("steamids[0]") != ids[0] || q.Get("steamids[1]") != ids[1] {
				t.Error("QR friend presence did not send one indexed batch")
			}
			_, _ = fmt.Fprintf(w, `{"response":{"accounts":[
				{"public_data":{"steamid":%s,"persona_name":%q},"private_data":{"persona_state":2,"game_id":%d,"game_extra_info":%q}},
				{"public_data":{"steamid":%q,"persona_name":"Private Game Fixture"},"private_data":{"persona_state":1,"game_id":%d,"game_is_private":true,"game_extra_info":"Hidden Game Fixture"}}
			]}}`, ids[0], "QR Playing "+access, firstApp, "Fixture Space "+saved.RefreshToken, ids[1], secondApp)
		default:
			t.Error("QR presence attempted an unexpected read")
			http.NotFound(w, r)
		}
	})
	a, session := newFriendsQRApp(t, client, access, saved, now, "en")
	got := callFriendsFixture(t, a, friendsRadarTool, `{}`)
	contains(t, got, `"in_game_count":1`, "QR Playing [redacted]", "Fixture Space [redacted]", "Private Game Fixture")
	assertHidden(t, got, access, saved.RefreshToken, "Hidden Game Fixture", appIDText(secondApp))
	if hits.Load() != 2 {
		t.Fatal("QR presence should use just the friend list and an indexed presence batch")
	}
	if st, err := a.Logout(t.Context()); err != nil || st.State != wire.AuthStateSignedOut {
		t.Fatal("synthetic QR logout failed")
	}
	if session.reads.friendList.record.Data != nil || session.reads.friendState.record.Data != nil {
		t.Fatal("QR log-out retained friend data")
	}
	signedOut := callFriendsFixture(t, a, friendsRadarTool, `{}`)
	contains(t, signedOut, `"available":false`, `"steam_sign_in_required"`)
	assertHidden(t, signedOut, "QR Playing", ids[0], access)
	if hits.Load() != 2 {
		t.Fatal("signed-out friends radar made a Steam request")
	}
}

func TestFriendsRadarQRPermission(t *testing.T) {
	for _, path := range []string{friendListPath, playerLinkPath} {
		for _, status := range []int{http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden} {
			t.Run(fmt.Sprintf("%s%d", path, status), func(t *testing.T) {
				now := time.Now().Truncate(time.Second)
				access := fixtureJWT(t, fixtureID, now.Add(time.Hour), "friends-denied")
				saved := fixtureSecret(t, fixtureID, now)
				key, friend := rand.Text(), syntheticFriendID(0)
				client := newFriendsClient(t, key, access, func(w http.ResponseWriter, r *http.Request) {
					switch {
					case r.URL.Path == path:
						http.Error(w, r.URL.String()+key+friend, status)
					case r.URL.Path == friendListPath && path == playerLinkPath:
						sendFriendList(t, w, r, []string{friend})
					default:
						t.Error("QR friends path fell back to an unexpected endpoint")
						http.NotFound(w, r)
					}
				})
				a, _ := newFriendsQRApp(t, client, access, saved, now, "zh-Hans")
				got := callFriendsFixture(t, a, friendsRadarTool, `{}`)
				if path == friendListPath {
					contains(t, got, `"qr_friends_list_unavailable"`, "好友列表隐私设置")
				} else {
					contains(t, got, `"qr_friends_presence_unavailable"`, "未授权此次手机扫码会话读取好友状态")
				}
				contains(t, got, `"available":false`, "Web API 密钥模式")
				assertHidden(t, got, key, access, saved.RefreshToken, friend)
			})
		}
	}
}
