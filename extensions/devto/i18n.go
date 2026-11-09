package main

import "strings"

type texts struct {
	articles, recent, search, results, read, back, open, summary, comments, noArticles, more string
}

var locales = map[string]*texts{
	"en":      {articles: "DEV articles", recent: "Recent", search: "Search articles", results: "%d articles", read: "Read", back: "Back", open: "Open article", summary: "Summary", comments: "Comments", noArticles: "No articles match your interests.", more: "Load more"},
	"ja":      {articles: "DEV 記事", recent: "新着", search: "記事を検索", results: "%d 件", read: "読む", back: "戻る", open: "記事を開く", summary: "概要", comments: "コメント", noArticles: "条件に合う記事はありません。", more: "さらに読み込む"},
	"zh-Hans": {articles: "DEV 技术文章", recent: "最新", search: "搜索文章", results: "%d 篇", read: "阅读", back: "返回", open: "打开原文", summary: "摘要", comments: "评论", noArticles: "没有符合你关注条件的文章。", more: "加载更多"},
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
