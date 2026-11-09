package main

import "strings"

type texts struct {
	links, recent, search, results, collections, all, read, back, open, save, review, confirm, archive string
	note, highlights, noLinks, missingConfig, more                                                     string
}

var locales = map[string]*texts{
	"en": {
		links: "Linkwarden bookmarks", recent: "Recent", search: "Search bookmarks", results: "%d bookmarks", collections: "Collections", all: "All collections", read: "Read", back: "Back", open: "Open original", save: "Save a link", review: "Review and save", confirm: "Confirm save", archive: "Archive",
		note: "Note", highlights: "Highlights", noLinks: "No bookmarks found.", missingConfig: "Set the Linkwarden instance URL and API key in Settings › Extensions first.", more: "Load more",
	},
	"ja": {
		links: "Linkwarden ブックマーク", recent: "最近", search: "ブックマークを検索", results: "%d 件", collections: "コレクション", all: "すべてのコレクション", read: "読む", back: "戻る", open: "元のページを開く", save: "リンクを保存", review: "確認して保存", confirm: "保存を確認", archive: "アーカイブ",
		note: "メモ", highlights: "ハイライト", noLinks: "ブックマークが見つかりません。", missingConfig: "設定 › 拡張機能で Linkwarden インスタンス URL と API キーを設定してください。", more: "さらに読み込む",
	},
	"zh-Hans": {
		links: "Linkwarden 书签", recent: "最近", search: "搜索书签", results: "%d 条书签", collections: "集合", all: "全部集合", read: "阅读", back: "返回", open: "打开原文", save: "收藏链接", review: "复核并收藏", confirm: "确认收藏", archive: "归档",
		note: "笔记", highlights: "划线", noLinks: "没有找到书签。", missingConfig: "先在「设置 › 扩展」中填写 Linkwarden 实例地址和 API Key。", more: "加载更多",
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
