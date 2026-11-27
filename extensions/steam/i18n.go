package main

import (
	"fmt"
	"strings"
)

type text struct {
	storeLanguage                                                       string
	invalidPage, invalidAction                                          string
	overview, status, refresh, back, details, store                     string
	recent, recentTime, totalTime, owned, library, wishlist, discounted string
	search, sort, byPlaytime, byName, previous, next, showing, capped   string
	noRecent, noGames, noMatches, missingGame, noWishes                 string
	noPrice, priceUnchecked                                             string
	achievements, noAchievements, unlocked, game, playing, unknown      string
	minutes, hoursMinutes, discount, atLeast, stale                     string
	qrHint, keyMode, keySignOut                                         string
	states                                                              map[string]string
	problems                                                            map[problem]string
}

var english = text{
	storeLanguage: "english",
	invalidPage:   "Invalid Steam page state.", invalidAction: "Invalid Steam page action.",
	overview: "Steam overview", status: "Player status", refresh: "Refresh", back: "Back",
	details: "Game details", store: "Open in Steam Store",
	recent: "Recently played", recentTime: "Last 2 weeks", totalTime: "Total playtime", owned: "Owned games",
	library: "Game library", wishlist: "Wishlist", discounted: "Wishlist discounts",
	search: "Search your library by name or AppID", sort: "Sort library", byPlaytime: "Playtime", byName: "Name",
	previous: "Previous", next: "Next", showing: "%d–%d of %d", capped: "This snapshot includes %d of %d titles.",
	noRecent: "No games played in the last two weeks.", noGames: "No owned games are visible.", noMatches: "No games match this search.",
	missingGame: "This game is unavailable in the current list.",
	noWishes:    "The wishlist is empty.", noPrice: "No price listed in this region.", priceUnchecked: "Price unavailable.",
	achievements: "Achievements", noAchievements: "Steam lists no achievements for this game.", unlocked: "%d of %d unlocked",
	game: "App %d", playing: "Playing %s", unknown: "Status unavailable",
	minutes: "%d min", hoursMinutes: "%d h %d min", discount: "%d%% off", atLeast: "%d+", stale: "Showing cached data.",
	qrHint:     "Open Steam Guard in the Steam Mobile App, scan the QR code, and approve the sign-in on your phone.",
	keyMode:    "Web API key mode is managed in Steam's extension settings. Select Steam Mobile QR there to sign in by scanning.",
	keySignOut: "To disconnect Web API key mode, clear the saved key in Steam's extension settings.",
	states: map[string]string{
		"offline": "Offline", "online": "Online", "busy": "Busy", "away": "Away",
		"snooze": "Snoozing", "looking_to_trade": "Looking to trade", "looking_to_play": "Looking to play", "invisible": "Invisible",
	},
	problems: map[problem]string{
		problemSetup:              "Enter your Steam player ID and Web API key in Settings › Extensions › Steam.",
		problemInvalidID:          "Steam could not find this player. Check the SteamID64 or profile vanity name.",
		problemCountry:            "Enter an assigned two-letter ISO 3166-1 country code for the Steam Store.",
		problemMode:               "Choose Steam Mobile QR or Web API key in Steam's extension settings.",
		problemKey:                "Steam rejected the Web API key. Check the saved secret in Settings › Extensions › Steam.",
		problemPrivate:            "This Steam profile is private. Make the profile public to see its activity.",
		problemProfileUnavailable: "Steam did not share this player's profile.",
		problemGameDetails:        "Steam did not share these games. Availability depends on the sign-in method and Steam privacy settings.",
		problemAchievements:       "Steam did not share achievement progress. Availability depends on Steam privacy settings and whether the game supports achievements.",
		problemWishlist:           "Steam did not share a wishlist. It may be private, empty or unavailable.",
		problemRateLimited:        "Steam is rate limiting requests. Try again in a moment.",
		problemTimeout:            "Steam took too long to respond. Try again in a moment.",
		problemNetwork:            "Steam is temporarily unavailable. Try again in a moment.",
		problemResponse:           "Steam returned an unexpected response. Try again in a moment.",
		problemCanceled:           "The Steam request was canceled.",
		problemAuthRequired:       "Sign in with the Steam Mobile App in Settings › Extensions › Steam.",
		problemAuthPending:        "Scan the QR code and approve the sign-in on your phone.",
		problemAuthRefused:        "Steam ended the sign-in request. Start a new QR sign-in.",
		problemAuthTimeout:        "The Steam QR sign-in expired. Start a new QR sign-in.",
		problemAuthExpired:        "Your Steam sign-in expired. Scan a new QR code to sign in again.",
		problemAuthLoad:           "Persona could not restore the saved Steam sign-in. Sign in with a new QR code.",
		problemAuthSave:           "Persona could not save the Steam sign-in. Try again when the Hub's secret store is available.",
		problemAuthDelete:         "Persona could not delete the saved Steam sign-in. Try signing out again.",
		problemAuthClearing:       "Persona is clearing the saved Steam sign-in. Try again in a moment.",

		problemGlobalAchievements:     "Steam did not share global unlock rates for this game.",
		problemAchievementSchema:      "Steam did not share this game's achievement names and hidden status.",
		problemAchievementDetails:     "Steam did not share complete personal achievement details. Any global rankings are for all players.",
		problemAchievementUnlockTimes: "Steam did not share timestamps for some unlocked achievements.",
	},
}

var chinese = text{
	storeLanguage: "schinese",
	invalidPage:   "Steam 页面状态无效。", invalidAction: "Steam 页面操作无效。",
	overview: "Steam 概况", status: "玩家状态", refresh: "刷新", back: "返回",
	details: "游戏详情", store: "打开 Steam 商店",
	recent: "最近游玩", recentTime: "近两周", totalTime: "总游玩时长", owned: "拥有游戏",
	library: "游戏库", wishlist: "愿望单", discounted: "愿望单打折",
	search: "按名称或 AppID 搜索游戏库", sort: "游戏库排序", byPlaytime: "游玩时长", byName: "名称",
	previous: "上一页", next: "下一页", showing: "第 %d–%d 项，共 %d 项", capped: "此快照包含 %d 款游戏，共 %d 款。",
	noRecent: "近两周没有游玩记录。", noGames: "没有可见的游戏。", noMatches: "没有符合搜索条件的游戏。",
	missingGame: "当前列表中没有这款游戏。",
	noWishes:    "愿望单为空。", noPrice: "此地区没有挂牌价格。", priceUnchecked: "暂时无法获取价格。",
	achievements: "成就", noAchievements: "Steam 没有列出这款游戏的成就。", unlocked: "已解锁 %d / %d",
	game: "应用 %d", playing: "游戏中 · %s", unknown: "状态不可用",
	minutes: "%d 分钟", hoursMinutes: "%d 小时 %d 分钟", discount: "优惠 %d%%", atLeast: "%d+", stale: "当前显示缓存数据。",
	qrHint:     "在 Steam 手机 App 中打开 Steam 令牌，扫描二维码，并在手机上批准登录。",
	keyMode:    "Web API 密钥在 Steam 扩展设置中管理。如需扫码，请在设置中选择 Steam 手机 App 扫码。",
	keySignOut: "如需断开 Web API 密钥模式，请在 Steam 扩展设置中清除已保存的密钥。",
	states: map[string]string{
		"offline": "离线", "online": "在线", "busy": "忙碌", "away": "离开",
		"snooze": "暂离", "looking_to_trade": "想交易", "looking_to_play": "想玩游戏", "invisible": "隐身",
	},
	problems: map[problem]string{
		problemSetup:              "请在「设置 › 扩展 › Steam」填写玩家标识和 Web API 密钥。",
		problemInvalidID:          "Steam 未找到该玩家，请检查 SteamID64 或个人资料自定义名称。",
		problemCountry:            "请填写有效的两位 ISO 3166-1 商店国家或地区代码。",
		problemMode:               "请在 Steam 扩展设置中选择手机 App 扫码或 Web API 密钥。",
		problemKey:                "Steam 拒绝了 Web API 密钥，请在「设置 › 扩展 › Steam」检查已保存的密钥。",
		problemPrivate:            "此 Steam 个人资料为私密状态。公开资料后可查看游戏活动。",
		problemProfileUnavailable: "Steam 未共享该玩家的个人资料。",
		problemGameDetails:        "Steam 未共享这些游戏。可见范围取决于登录方式和 Steam 隐私设置。",
		problemAchievements:       "Steam 未共享成就进度。可见范围取决于 Steam 隐私设置及游戏是否支持成就。",
		problemWishlist:           "Steam 未共享愿望单。愿望单可能为私密、为空或暂不可访问。",
		problemRateLimited:        "Steam 正在限制请求频率，请稍后重试。",
		problemTimeout:            "Steam 响应超时，请稍后重试。",
		problemNetwork:            "Steam 暂时不可用，请稍后重试。",
		problemResponse:           "Steam 返回了无法识别的数据，请稍后重试。",
		problemCanceled:           "Steam 请求已取消。",
		problemAuthRequired:       "请在「设置 › 扩展 › Steam」使用 Steam 手机 App 扫码登录。",
		problemAuthPending:        "请扫描二维码，并在手机上批准登录。",
		problemAuthRefused:        "Steam 已结束此次登录请求，请重新发起扫码登录。",
		problemAuthTimeout:        "Steam 扫码登录已超时，请重新发起扫码登录。",
		problemAuthExpired:        "Steam 登录已失效，请重新扫码登录。",
		problemAuthLoad:           "Persona 无法恢复已保存的 Steam 登录，请重新扫码登录。",
		problemAuthSave:           "Persona 无法保存 Steam 登录，请在 Hub 密钥存储可用后重试。",
		problemAuthDelete:         "Persona 无法删除已保存的 Steam 登录，请重试登出。",
		problemAuthClearing:       "Persona 正在清除已保存的 Steam 登录，请稍后重试。",

		problemGlobalAchievements:     "Steam 未共享这款游戏的全球成就达成率。",
		problemAchievementSchema:      "Steam 未共享这款游戏的成就名称和隐藏状态。",
		problemAchievementDetails:     "Steam 未共享完整的个人成就明细。全球榜单适用于所有玩家。",
		problemAchievementUnlockTimes: "Steam 未共享部分已解锁成就的解锁时间。",
	},
}

func textFor(locale string) *text {
	language, _, _ := strings.Cut(strings.ToLower(strings.ReplaceAll(locale, "_", "-")), "-")
	if language == "" || language == "zh" {
		return &chinese
	}
	return &english
}

func (t *text) message(issue problem, stale bool) string {
	if issue == "" {
		return ""
	}
	message := t.problems[issue]
	if message == "" {
		message = t.problems[problemResponse]
	}
	if stale {
		return t.stale + " " + message
	}
	return message
}

func (t *text) presence(p *player) string {
	if p != nil {
		if p.Playing != "" {
			return fmt.Sprintf(t.playing, p.Playing)
		}
		if p.PlayApp != 0 {
			return fmt.Sprintf(t.playing, t.gameName(p.PlayApp, ""))
		}
		if state := t.states[p.State]; state != "" {
			return state
		}
	}
	return t.unknown
}

func (t *text) gameName(appID uint32, name string) string {
	if name != "" {
		return name
	}
	return fmt.Sprintf(t.game, appID)
}

func (t *text) playtime(n uint64) string {
	if n < 60 {
		return fmt.Sprintf(t.minutes, n)
	}
	return fmt.Sprintf(t.hoursMinutes, n/60, n%60)
}
