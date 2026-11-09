package main

import "strings"

type texts struct {
	new, later, shortlist, archive, feed, search, results, progress, read, open, back             string
	move, note, save, review, confirm, noDocuments, missingToken, notes, highlights, noText, more string
}

var locales = map[string]*texts{
	"en": {
		new: "New", later: "Later", shortlist: "Shortlist", archive: "Archive", feed: "Feed", search: "Search reading",
		results: "%d documents", progress: "Progress %.0f%%", read: "Read", open: "Open original", back: "Back",
		move: "Move to Shortlist", note: "Note", save: "Save a link", review: "Review and save", confirm: "Confirm save",
		noDocuments: "Nothing in this queue.", missingToken: "Add your Readwise access token in Settings › Extensions first.", notes: "Notes", highlights: "Highlights", noText: "Reader has no article text for this document yet.", more: "Load more",
	},
	"ja": {
		new: "新規", later: "あとで", shortlist: "ショートリスト", archive: "アーカイブ", feed: "フィード", search: "読書を検索",
		results: "%d 件", progress: "進捗 %.0f%%", read: "読む", open: "元のページを開く", back: "戻る",
		move: "ショートリストへ移動", note: "メモ", save: "リンクを保存", review: "確認して保存", confirm: "保存を確認",
		noDocuments: "このキューには何もありません。", missingToken: "設定 › 拡張機能で Readwise アクセストークンを追加してください。", notes: "メモ", highlights: "ハイライト", noText: "このドキュメントにはまだ本文がありません。", more: "さらに読み込む",
	},
	"zh-Hans": {
		new: "待读", later: "稍后读", shortlist: "短名单", archive: "归档", feed: "订阅", search: "搜索阅读",
		results: "%d 篇", progress: "进度 %.0f%%", read: "阅读", open: "打开原文", back: "返回",
		move: "移入短名单", note: "笔记", save: "收藏链接", review: "复核并收藏", confirm: "确认收藏",
		noDocuments: "这个队列还没有内容。", missingToken: "先在「设置 › 扩展」中填写 Readwise 访问令牌。", notes: "笔记", highlights: "划线", noText: "这篇文章还没有可显示的正文。", more: "加载更多",
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
