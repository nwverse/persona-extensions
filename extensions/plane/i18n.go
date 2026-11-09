package main

import "strings"

type texts struct {
	work, projects, noProjects, items, noItems, open, back, more, missingConfig string
	state, priority, due, description, create, edit, review, save, name, body   string
	urgent, high, medium, low, none, noDescription                              string
}

var locales = map[string]*texts{
	"en": {
		work: "Plane work items", projects: "Projects", noProjects: "No projects in this workspace.", items: "%d work items", noItems: "No work items in this project.", open: "Open in Plane", back: "Back", more: "Load more", missingConfig: "Set the Plane workspace slug and API key in Settings › Extensions first.",
		state: "State", priority: "Priority", due: "Due", description: "Description", create: "Create work item", edit: "Edit state and priority", review: "Review", save: "Save changes", name: "Title", body: "Description",
		urgent: "Urgent", high: "High", medium: "Medium", low: "Low", none: "None", noDescription: "No description.",
	},
	"ja": {
		work: "Plane 作業項目", projects: "プロジェクト", noProjects: "このワークスペースにプロジェクトはありません。", items: "%d 件の作業項目", noItems: "このプロジェクトに作業項目はありません。", open: "Plane で開く", back: "戻る", more: "さらに読み込む", missingConfig: "設定 › 拡張機能で Plane ワークスペース slug と API キーを設定してください。",
		state: "状態", priority: "優先度", due: "期限", description: "説明", create: "作業項目を作成", edit: "状態と優先度を編集", review: "確認", save: "変更を保存", name: "タイトル", body: "説明",
		urgent: "緊急", high: "高", medium: "中", low: "低", none: "なし", noDescription: "説明はありません。",
	},
	"zh-Hans": {
		work: "Plane 工作项", projects: "项目", noProjects: "这个工作区里没有项目。", items: "%d 个工作项", noItems: "这个项目里没有工作项。", open: "在 Plane 中打开", back: "返回", more: "加载更多", missingConfig: "先在「设置 › 扩展」中填写 Plane 工作区 slug 和 API key。",
		state: "状态", priority: "优先级", due: "截止日期", description: "描述", create: "创建工作项", edit: "编辑状态和优先级", review: "预览", save: "保存更改", name: "标题", body: "描述",
		urgent: "紧急", high: "高", medium: "中", low: "低", none: "无", noDescription: "暂无描述。",
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
