package page

import "strings"

// text is the page's words in one language (the app's locale; Chinese when it says none).
type text struct {
	signInTitle, signInDetail                             string
	account, budget, lastSync, state, never, feedOff      string
	stateOK, stateRateLimited, stateError, syncing        string
	window, w24h, w3d, current                            string
	top, links, threads, active, none                     string
	repostedBy, sharedBy, threadOf, postsN, counts        string
	openOnX, moreOnX, totals, refresh, refreshing, footer string
}

func texts(locale string) text {
	switch {
	case strings.HasPrefix(locale, "en"):
		return text{
			signInTitle:  "Not signed in to X",
			signInDetail: "Sign in with your own X developer app's keys under Settings › Connectors › X. Reading uses your pay-per-use credits, within the daily post budget in this extension's settings.",
			account:      "Account", budget: "Posts read today (UTC)", lastSync: "Last sync", state: "State", never: "Not yet", feedOff: "feed off",
			stateOK: "OK", stateRateLimited: "Rate limited", stateError: "Needs attention", syncing: "Syncing…",
			window: "Window", w24h: "24 hours", w3d: "3 days", current: "Showing",
			top: "Top posts", links: "Most shared links", threads: "Threads", active: "Most active accounts you follow", none: "Nothing in this window yet.",
			repostedBy: "reposted by %d", sharedBy: "shared by %d", threadOf: "thread of %d posts", postsN: "%d posts",
			counts:  "%d likes · %d reposts · %d replies · %d quotes",
			openOnX: "Open on X", moreOnX: "The rest of this post is on X.",
			totals: "%d posts seen in this window · budget %d / %d today (UTC)", refresh: "Refresh", refreshing: "Syncing with X…",
			footer: "A digest of what was read, not a timeline. Posts are kept a few days, then deleted.",
		}
	case strings.HasPrefix(locale, "ja"):
		return text{
			signInTitle:  "X にサインインしていません",
			signInDetail: "設定 › コネクタ › X で、自分の X 開発者アプリのキーでサインインしてください。読み込みには従量課金のクレジットを使い、この拡張の設定の 1 日の投稿予算内に収めます。",
			account:      "アカウント", budget: "今日読んだ投稿（UTC）", lastSync: "最終同期", state: "状態", never: "まだ", feedOff: "フィード オフ",
			stateOK: "正常", stateRateLimited: "レート制限中", stateError: "要対応", syncing: "同期中…",
			window: "期間", w24h: "24 時間", w3d: "3 日", current: "表示中",
			top: "注目の投稿", links: "よく共有されたリンク", threads: "スレッド", active: "よく投稿したフォロー中のアカウント", none: "この期間にはまだありません。",
			repostedBy: "%d 人がリポスト", sharedBy: "%d アカウントが共有", threadOf: "%d 件のスレッド", postsN: "%d 件",
			counts:  "いいね %d · リポスト %d · 返信 %d · 引用 %d",
			openOnX: "X で開く", moreOnX: "この投稿の続きは X で読めます。",
			totals: "この期間に読んだ投稿 %d 件 · 今日（UTC）の予算 %d / %d", refresh: "更新", refreshing: "X と同期しています…",
			footer: "読んだ投稿のまとめで、タイムラインではありません。投稿は数日保存したあと削除します。",
		}
	}
	return text{
		signInTitle:  "还没有登录 X",
		signInDetail: "在 设置 › 连接器 › X 里用你自己的 X 开发者应用的密钥登录。读取用的是你按量付费的额度，不超过这个扩展设置里的每日帖子预算。",
		account:      "账号", budget: "今天读的帖子（UTC）", lastSync: "上次同步", state: "状态", never: "还没有", feedOff: "动态已关",
		stateOK: "正常", stateRateLimited: "请求频率受限", stateError: "需要处理", syncing: "正在同步…",
		window: "时间范围", w24h: "24 小时", w3d: "3 天", current: "正在看",
		top: "热门帖子", links: "分享最多的链接", threads: "长串", active: "发得最多的关注账号", none: "这段时间里还没有。",
		repostedBy: "%d 人转发", sharedBy: "%d 个账号分享", threadOf: "%d 条的长串", postsN: "%d 条",
		counts:  "%d 赞 · %d 转发 · %d 回复 · %d 引用",
		openOnX: "在 X 上打开", moreOnX: "这条帖子剩下的内容在 X 上看。",
		totals: "这段时间读到 %d 条 · 今天（UTC）预算 %d / %d", refresh: "刷新", refreshing: "正在和 X 同步…",
		footer: "读到的帖子整理出的摘要，不是时间线。帖子只保留几天，之后删除。",
	}
}
