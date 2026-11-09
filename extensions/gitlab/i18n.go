package main

import "strings"

type texts struct {
	reviews, open, search, results, back, openMR, approve, unapprove, comment string
	author, reviewers, description, comments, noMRs, missingConfig, more      string
	assigned, review, body, reviewComment, confirmComment, noComments         string
}

var locales = map[string]*texts{
	"en": {
		reviews: "Merge request reviews", open: "Open MRs", search: "Search MR titles", results: "%d open MRs", back: "Back", openMR: "Open in GitLab", approve: "Approve", unapprove: "Remove approval", comment: "Add comment",
		author: "Author", reviewers: "Reviewers", description: "Description", comments: "Discussion", noMRs: "No open merge requests in this scope.", missingConfig: "Set the GitLab instance URL and personal access token in Settings › Extensions first.", more: "Load more",
		assigned: "Assigned to me", review: "Awaiting my review", body: "Comment", reviewComment: "Review comment", confirmComment: "Post comment", noComments: "No user comments yet.",
	},
	"ja": {
		reviews: "マージリクエストレビュー", open: "オープン MR", search: "MR タイトルを検索", results: "%d 件のオープン MR", back: "戻る", openMR: "GitLab で開く", approve: "承認", unapprove: "承認を取り消す", comment: "コメントを追加",
		author: "作成者", reviewers: "レビュアー", description: "説明", comments: "ディスカッション", noMRs: "この範囲にオープンなマージリクエストはありません。", missingConfig: "設定 › 拡張機能で GitLab インスタンス URL と個人アクセストークンを設定してください。", more: "さらに読み込む",
		assigned: "自分に割り当て", review: "自分のレビュー待ち", body: "コメント", reviewComment: "コメントの確認", confirmComment: "コメントを投稿", noComments: "ユーザーコメントはまだありません。",
	},
	"zh-Hans": {
		reviews: "合并请求评审", open: "打开的 MR", search: "搜索 MR 标题", results: "%d 个打开的 MR", back: "返回", openMR: "在 GitLab 中打开", approve: "批准", unapprove: "取消批准", comment: "添加评论",
		author: "作者", reviewers: "评审人", description: "描述", comments: "讨论", noMRs: "这个范围内没有打开的合并请求。", missingConfig: "先在「设置 › 扩展」中填写 GitLab 实例地址和个人访问令牌。", more: "加载更多",
		assigned: "分配给我", review: "等待我评审", body: "评论", reviewComment: "确认评论", confirmComment: "发表评论", noComments: "还没有用户评论。",
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
