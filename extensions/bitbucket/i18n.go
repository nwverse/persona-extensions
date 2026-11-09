package main

import "strings"

type texts struct {
	reviews, open, search, results, read, back, openPR, approve, unapprove string
	author, reviewers, description, comments, noPRs, missingConfig, more   string
}

var locales = map[string]*texts{
	"en": {
		reviews: "Pull request reviews", open: "Open PRs", search: "Search PRs", results: "%d open PRs", read: "Read", back: "Back", openPR: "Open in Bitbucket", approve: "Approve", unapprove: "Remove approval",
		author: "Author", reviewers: "Reviewers", description: "Description", comments: "Discussion", noPRs: "No open pull requests.", missingConfig: "Set the Bitbucket instance URL and personal access token in Settings › Extensions first.", more: "Load more",
	},
	"ja": {
		reviews: "プルリクエストレビュー", open: "オープン PR", search: "PR を検索", results: "%d 件のオープン PR", read: "読む", back: "戻る", openPR: "Bitbucket で開く", approve: "承認", unapprove: "承認を取り消す",
		author: "作成者", reviewers: "レビュアー", description: "説明", comments: "ディスカッション", noPRs: "オープンなプルリクエストはありません。", missingConfig: "設定 › 拡張機能で Bitbucket インスタンス URL と個人アクセストークンを設定してください。", more: "さらに読み込む",
	},
	"zh-Hans": {
		reviews: "代码评审", open: "打开的 PR", search: "搜索 PR", results: "%d 个打开的 PR", read: "阅读", back: "返回", openPR: "在 Bitbucket 中打开", approve: "批准", unapprove: "取消批准",
		author: "作者", reviewers: "审查人", description: "描述", comments: "讨论", noPRs: "没有打开的 PR。", missingConfig: "先在「设置 › 扩展」中填写 Bitbucket 实例地址和个人访问令牌。", more: "加载更多",
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
