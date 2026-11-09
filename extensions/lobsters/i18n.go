package main

import "strings"

type texts struct {
	hot, newest, search, results, read, back, open, discussion, description, comments, noStories string
}

var locales = map[string]*texts{
	"en":      {hot: "Hot", newest: "New", search: "Search stories", results: "%d stories", read: "Read discussion", back: "Back", open: "Open article", discussion: "Discussion", description: "Description", comments: "Comments", noStories: "No stories match your interests."},
	"ja":      {hot: "人気", newest: "新着", search: "記事を検索", results: "%d 件", read: "ディスカッションを読む", back: "戻る", open: "記事を開く", discussion: "ディスカッション", description: "説明", comments: "コメント", noStories: "条件に合う記事はありません。"},
	"zh-Hans": {hot: "热门", newest: "最新", search: "搜索帖子", results: "%d 篇", read: "阅读讨论", back: "返回", open: "打开原文", discussion: "讨论", description: "简介", comments: "评论", noStories: "没有符合你关注条件的帖子。"},
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
