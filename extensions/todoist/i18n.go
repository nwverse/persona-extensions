package main

import "strings"

type texts struct {
	today, overdue, upcoming, all, search, tasks, projects, create, edit, complete                       string
	back, open, detail, description, noTasks, missingToken, project, section, labels                     string
	due, priority, recurring, confirm, created, updated, completed, newTask, titleOptional, more, review string
}

var locales = map[string]*texts{
	"en": {
		today: "Today", overdue: "Overdue", upcoming: "Next 7 days", all: "All tasks", search: "Search tasks",
		tasks: "%d tasks", projects: "Projects", create: "New task", edit: "Edit task", complete: "Complete",
		back: "Back", open: "Open in Todoist", detail: "Task", description: "Description", noTasks: "No tasks here.",
		missingToken: "Add your Todoist API token in Settings › Extensions first.", project: "Project", section: "Section", labels: "Labels",
		due: "Due", priority: "Priority (1 highest)", recurring: "Recurring", confirm: "Confirm create", created: "Created", updated: "Updated", completed: "Completed", newTask: "Create", titleOptional: "Title (optional)", more: "Load more", review: "Review and create",
	},
	"ja": {
		today: "今日", overdue: "期限切れ", upcoming: "今後7日間", all: "すべてのタスク", search: "タスクを検索",
		tasks: "%d 件のタスク", projects: "プロジェクト", create: "新しいタスク", edit: "タスクを編集", complete: "完了",
		back: "戻る", open: "Todoist で開く", detail: "タスク", description: "説明", noTasks: "タスクはありません。",
		missingToken: "設定 › 拡張機能で Todoist API トークンを追加してください。", project: "プロジェクト", section: "セクション", labels: "ラベル",
		due: "期限", priority: "優先度（1 が最高）", recurring: "繰り返し", confirm: "作成を確認", created: "作成", updated: "更新", completed: "完了", newTask: "作成", titleOptional: "タイトル（任意）", more: "さらに読み込む", review: "確認して作成",
	},
	"zh-Hans": {
		today: "今天", overdue: "已逾期", upcoming: "近七天", all: "全部任务", search: "搜索任务",
		tasks: "%d 项任务", projects: "项目", create: "新建任务", edit: "编辑任务", complete: "完成",
		back: "返回", open: "在 Todoist 中打开", detail: "任务", description: "描述", noTasks: "这里没有任务。",
		missingToken: "先在「设置 › 扩展」中填写 Todoist API Token。", project: "项目", section: "分区", labels: "标签",
		due: "截止", priority: "优先级（1 最高）", recurring: "循环", confirm: "确认创建", created: "已创建", updated: "已修改", completed: "已完成", newTask: "创建", titleOptional: "标题（可选）", more: "加载更多", review: "复核并创建",
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
