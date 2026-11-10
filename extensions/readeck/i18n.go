package main

import "strings"

type texts struct {
	reading, unread, favorites, search, results, noBookmarks, back, more, missingConfig string
	open, save, review, url, title, labels, update, progress, marked                    string
	article, highlights, noHighlights, noContent, note, minutes                         string
}

var locales = map[string]*texts{
	"en": {
		reading: "Readeck reading", unread: "Unread", favorites: "Favorites", search: "Search saved articles", results: "Saved reading", noBookmarks: "No bookmarks in this reading view.", back: "Back", more: "Load more", missingConfig: "Set the Readeck instance URL and API token in Settings › Extensions first.",
		open: "Open original article", save: "Save webpage", review: "Review", url: "Webpage URL", title: "Optional title", labels: "Labels (comma-separated)", update: "Update reading", progress: "Reading progress (0–100)", marked: "Favorite (true or false)",
		article: "Saved article", highlights: "Highlights", noHighlights: "No highlights.", noContent: "Readeck has not saved a readable Markdown article yet. Saving a URL does not guarantee extraction has finished.", note: "Personal note", minutes: "min",
	},
	"ja": {
		reading: "Readeck リーディング", unread: "未読", favorites: "お気に入り", search: "保存した記事を検索", results: "保存したリーディング", noBookmarks: "この表示にブックマークはありません。", back: "戻る", more: "さらに読み込む", missingConfig: "設定 › 拡張機能で Readeck のインスタンス URL と API トークンを設定してください。",
		open: "元の記事を開く", save: "ウェブページを保存", review: "確認", url: "ウェブページ URL", title: "タイトル（任意）", labels: "ラベル（カンマ区切り）", update: "リーディングを更新", progress: "読書の進捗（0〜100）", marked: "お気に入り（true または false）",
		article: "保存した記事", highlights: "ハイライト", noHighlights: "ハイライトはありません。", noContent: "読み取り可能な Markdown 記事はまだ保存されていません。URL の保存は抽出の完了を意味しません。", note: "個人メモ", minutes: "分",
	},
	"zh-Hans": {
		reading: "Readeck 阅读", unread: "未读", favorites: "收藏", search: "搜索已保存的文章", results: "已保存的阅读", noBookmarks: "这个阅读视图里没有书签。", back: "返回", more: "加载更多", missingConfig: "先在「设置 › 扩展」中填写 Readeck 实例地址和 API token。",
		open: "打开原文", save: "保存网页", review: "预览", url: "网页 URL", title: "标题（可选）", labels: "标签（用逗号分隔）", update: "更新阅读状态", progress: "阅读进度（0–100）", marked: "收藏（true 或 false）",
		article: "已保存的正文", highlights: "划线", noHighlights: "暂无划线。", noContent: "Readeck 还没有保存可读取的 Markdown 正文。URL 已保存不代表正文已抓取完成。", note: "个人笔记", minutes: "分钟",
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
