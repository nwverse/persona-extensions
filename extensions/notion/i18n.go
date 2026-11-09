package main

import "strings"

type texts struct {
	pages, recent, search, results, read, back, open, create, review, confirm, append, save string
	title, content, parent, noPages, missingToken, noContent, todo, paragraph               string
}

var locales = map[string]*texts{
	"en": {
		pages: "Notion pages", recent: "Recently edited", search: "Search pages", results: "%d pages", read: "Read", back: "Back", open: "Open in Notion",
		create: "New page", review: "Review and create", confirm: "Confirm create", append: "Append a note", save: "Save note",
		title: "Title", content: "Content", parent: "Parent page", noPages: "No shared pages found.", missingToken: "Add your Notion integration secret in Settings › Extensions first.", noContent: "This page has no supported text blocks yet.", todo: "To-do", paragraph: "Paragraph",
	},
	"ja": {
		pages: "Notion ページ", recent: "最近の編集", search: "ページを検索", results: "%d ページ", read: "読む", back: "戻る", open: "Notion で開く",
		create: "新しいページ", review: "確認して作成", confirm: "作成を確認", append: "メモを追加", save: "メモを保存",
		title: "タイトル", content: "内容", parent: "親ページ", noPages: "共有ページが見つかりません。", missingToken: "設定 › 拡張機能で Notion integration secret を追加してください。", noContent: "表示できるテキストブロックはまだありません。", todo: "To-do", paragraph: "段落",
	},
	"zh-Hans": {
		pages: "Notion 笔记", recent: "最近编辑", search: "搜索页面", results: "%d 个页面", read: "阅读", back: "返回", open: "在 Notion 中打开",
		create: "新建页面", review: "复核并创建", confirm: "确认创建", append: "追加笔记", save: "保存笔记",
		title: "标题", content: "内容", parent: "父页面", noPages: "没有找到共享页面。", missingToken: "先在「设置 › 扩展」中填写 Notion integration secret。", noContent: "这个页面还没有可显示的文本块。", todo: "待办", paragraph: "段落",
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
