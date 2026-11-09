package main

import "strings"

type texts struct {
	bookmarks, recent, search, results, read, back, open, save, review, confirm, favourite, archive string
	note, text, highlights, noBookmarks, missingConfig, noContent, more                             string
}

var locales = map[string]*texts{
	"en": {
		bookmarks: "Karakeep bookmarks", recent: "Recent", search: "Search bookmarks", results: "%d bookmarks", read: "Read", back: "Back", open: "Open original", save: "Save a link", review: "Review and save", confirm: "Confirm save", favourite: "Favourite", archive: "Archive",
		note: "Note", text: "Text", highlights: "Highlights", noBookmarks: "No bookmarks found.", missingConfig: "Set the Karakeep instance URL and API key in Settings › Extensions first.", noContent: "This bookmark has no text yet.", more: "Load more",
	},
	"ja": {
		bookmarks: "Karakeep ブックマーク", recent: "最近", search: "ブックマークを検索", results: "%d 件", read: "読む", back: "戻る", open: "元のページを開く", save: "リンクを保存", review: "確認して保存", confirm: "保存を確認", favourite: "お気に入り", archive: "アーカイブ",
		note: "メモ", text: "本文", highlights: "ハイライト", noBookmarks: "ブックマークが見つかりません。", missingConfig: "設定 › 拡張機能で Karakeep インスタンス URL と API キーを設定してください。", noContent: "このブックマークにはまだ本文がありません。", more: "さらに読み込む",
	},
	"zh-Hans": {
		bookmarks: "Karakeep 书签", recent: "最近", search: "搜索书签", results: "%d 条书签", read: "阅读", back: "返回", open: "打开原文", save: "收藏链接", review: "复核并收藏", confirm: "确认收藏", favourite: "收藏", archive: "归档",
		note: "笔记", text: "正文", highlights: "划线", noBookmarks: "没有找到书签。", missingConfig: "先在「设置 › 扩展」中填写 Karakeep 实例地址和 API Key。", noContent: "这条书签还没有正文。", more: "加载更多",
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
