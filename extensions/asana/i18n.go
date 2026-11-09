package main

import "strings"

type texts struct {
	tasks, workspaces, noWorkspaces, noTasks, noProjects, create, back, more, missingConfig string
	name, notes, project, none, due, open, complete, review, description, noNotes           string
}

var locales = map[string]*texts{
	"en": {
		tasks: "My Asana tasks", workspaces: "Workspaces", noWorkspaces: "No workspaces are visible to this token.", noTasks: "No incomplete tasks assigned to you.", noProjects: "No active projects; the task will still be assigned to you in this workspace.", create: "Create task", back: "Back", more: "Load more", missingConfig: "Set the Asana personal access token in Settings › Extensions first.",
		name: "Title", notes: "Notes", project: "Project", none: "No project", due: "Due date (YYYY-MM-DD)", open: "Open in Asana", complete: "Mark complete", review: "Review", description: "Notes", noNotes: "No notes.",
	},
	"ja": {
		tasks: "Asana の自分のタスク", workspaces: "ワークスペース", noWorkspaces: "このトークンで表示できるワークスペースはありません。", noTasks: "自分に割り当てられた未完了タスクはありません。", noProjects: "アクティブなプロジェクトはありません。このワークスペースに自分のタスクを作成できます。", create: "タスクを作成", back: "戻る", more: "さらに読み込む", missingConfig: "設定 › 拡張機能で Asana 個人アクセストークンを設定してください。",
		name: "タイトル", notes: "メモ", project: "プロジェクト", none: "プロジェクトなし", due: "期限 (YYYY-MM-DD)", open: "Asana で開く", complete: "完了にする", review: "確認", description: "メモ", noNotes: "メモはありません。",
	},
	"zh-Hans": {
		tasks: "我的 Asana 任务", workspaces: "工作区", noWorkspaces: "这个令牌看不到任何工作区。", noTasks: "没有分配给你的未完成任务。", noProjects: "没有活跃项目；任务仍会在这个工作区中分配给你。", create: "创建任务", back: "返回", more: "加载更多", missingConfig: "先在「设置 › 扩展」中填写 Asana 个人访问令牌。",
		name: "标题", notes: "备注", project: "项目", none: "不属于项目", due: "截止日期 (YYYY-MM-DD)", open: "在 Asana 中打开", complete: "标记完成", review: "预览", description: "备注", noNotes: "暂无备注。",
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
