package main

import "strings"

type texts struct {
	reading, unread, starred, articles, noArticles, more, missingConfig      string
	subscriptions, noSubscriptions, open, markRead, back, content, noContent string
}

var locales = map[string]*texts{
	"en": {
		reading: "FreshRSS reading", unread: "Unread", starred: "Starred", articles: "Articles", noArticles: "No articles in this view.", more: "Load more", missingConfig: "Set the FreshRSS API URL, username and dedicated API password in Settings › Extensions first.",
		subscriptions: "Subscriptions", noSubscriptions: "No subscriptions.", open: "Open original article", markRead: "Mark read", back: "Back", content: "Article text", noContent: "No readable article content is available.",
	},
	"ja": {
		reading: "FreshRSS リーディング", unread: "未読", starred: "スター付き", articles: "記事", noArticles: "この表示に記事はありません。", more: "さらに読み込む", missingConfig: "設定 › 拡張機能で FreshRSS の API URL、ユーザー名、専用 API パスワードを設定してください。",
		subscriptions: "購読フィード", noSubscriptions: "購読フィードはありません。", open: "元の記事を開く", markRead: "既読にする", back: "戻る", content: "記事本文", noContent: "読み取り可能な記事本文はありません。",
	},
	"zh-Hans": {
		reading: "FreshRSS 阅读", unread: "未读", starred: "收藏", articles: "文章", noArticles: "这个视图里没有文章。", more: "加载更多", missingConfig: "先在「设置 › 扩展」中填写 FreshRSS API 地址、用户名和专用 API 密码。",
		subscriptions: "订阅源", noSubscriptions: "暂无订阅源。", open: "打开原文", markRead: "标记已读", back: "返回", content: "文章正文", noContent: "没有可读取的文章正文。",
	},
}

func textsFor(locale string) *texts {
	if t := locales[locale]; t != nil {
		return t
	}
	if i := strings.IndexAny(locale, "-_"); i > 0 {
		if t := locales[locale[:i]]; t != nil {
			return t
		}
	}
	return locales["en"]
}
