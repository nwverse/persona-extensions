package main

import "strings"

type texts struct {
	assigned, active, all, search, results, create, review, confirm, complete, back, open string
	state, team, assignee, priority, due, description, comments, noIssues, missingToken   string
}

var locales = map[string]*texts{
	"en": {
		assigned: "My issues", active: "Active", all: "All active", search: "Search issues", results: "%d active issues",
		create: "New issue", review: "Review and create", confirm: "Confirm create", complete: "Complete", back: "Back", open: "Open in Linear",
		state: "State", team: "Team", assignee: "Assignee", priority: "Priority", due: "Due", description: "Description", comments: "Comments", noIssues: "No active assigned issues.", missingToken: "Add your Linear personal API key in Settings › Extensions first.",
	},
	"ja": {
		assigned: "自分の課題", active: "進行中", all: "すべての進行中", search: "課題を検索", results: "%d 件の進行中の課題",
		create: "新しい課題", review: "確認して作成", confirm: "作成を確認", complete: "完了", back: "戻る", open: "Linear で開く",
		state: "ステータス", team: "チーム", assignee: "担当者", priority: "優先度", due: "期限", description: "説明", comments: "コメント", noIssues: "進行中の担当課題はありません。", missingToken: "設定 › 拡張機能で Linear 個人 API キーを追加してください。",
	},
	"zh-Hans": {
		assigned: "我的工作项", active: "进行中", all: "全部进行中", search: "搜索工作项", results: "%d 个进行中工作项",
		create: "新建工作项", review: "复核并创建", confirm: "确认创建", complete: "完成", back: "返回", open: "在 Linear 中打开",
		state: "状态", team: "团队", assignee: "负责人", priority: "优先级", due: "截止", description: "描述", comments: "讨论", noIssues: "没有分配给你的进行中工作项。", missingToken: "先在「设置 › 扩展」中填写 Linear 个人 API Key。",
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
