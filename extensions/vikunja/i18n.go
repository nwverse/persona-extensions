package main

import "strings"

type texts struct {
	tasks, search, noTasks, back, more, missingConfig             string
	projects, noProjects, create, review, title, description, due string
	complete, comments, noComments, user                          string
}

var locales = map[string]*texts{
	"en": {
		tasks: "My Vikunja tasks", search: "Search accessible tasks", noTasks: "No tasks in this view.", back: "Back", more: "Load more", missingConfig: "Set the Vikunja instance URL and API token in Settings › Extensions first.",
		projects: "Choose a project", noProjects: "No active accessible projects.", create: "Create task", review: "Review", title: "Title", description: "Description", due: "Due date (RFC3339)",
		complete: "Mark complete", comments: "Comments", noComments: "No comments.", user: "Vikunja user",
	},
	"ja": {
		tasks: "Vikunja の自分のタスク", search: "アクセス可能なタスクを検索", noTasks: "この表示にタスクはありません。", back: "戻る", more: "さらに読み込む", missingConfig: "設定 › 拡張機能で Vikunja のインスタンス URL と API トークンを設定してください。",
		projects: "プロジェクトを選択", noProjects: "有効なプロジェクトはありません。", create: "タスクを作成", review: "確認", title: "タイトル", description: "説明", due: "期限（RFC3339）",
		complete: "完了にする", comments: "コメント", noComments: "コメントはありません。", user: "Vikunja ユーザー",
	},
	"zh-Hans": {
		tasks: "我的 Vikunja 任务", search: "搜索可访问的任务", noTasks: "这个视图里没有任务。", back: "返回", more: "加载更多", missingConfig: "先在「设置 › 扩展」中填写 Vikunja 实例地址和 API token。",
		projects: "选择项目", noProjects: "没有可访问的有效项目。", create: "创建任务", review: "预览", title: "标题", description: "描述", due: "截止日期（RFC3339）",
		complete: "标记完成", comments: "评论", noComments: "暂无评论。", user: "Vikunja 用户",
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
