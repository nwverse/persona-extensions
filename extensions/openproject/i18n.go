package main

import "strings"

type texts struct {
	work, noWork, projects, noProjects, create, back, more, missingConfig string
	subject, description, start, due, review, project, typeLabel          string
}

var locales = map[string]*texts{
	"en": {
		work: "My OpenProject work packages", noWork: "No assigned work packages in this view.", projects: "Projects", noProjects: "No active projects are visible.", create: "Create work package", back: "Back", more: "Load more", missingConfig: "Set the OpenProject instance URL and opapi token in Settings › Extensions first.",
		subject: "Subject", description: "Description", start: "Start date (YYYY-MM-DD)", due: "Due date (YYYY-MM-DD)", review: "Review", project: "Project", typeLabel: "Type",
	},
	"ja": {
		work: "自分の OpenProject 作業パッケージ", noWork: "この表示に自分の作業パッケージはありません。", projects: "プロジェクト", noProjects: "アクティブなプロジェクトはありません。", create: "作業パッケージを作成", back: "戻る", more: "さらに読み込む", missingConfig: "設定 › 拡張機能で OpenProject インスタンス URL と opapi トークンを設定してください。",
		subject: "件名", description: "説明", start: "開始日 (YYYY-MM-DD)", due: "期限 (YYYY-MM-DD)", review: "確認", project: "プロジェクト", typeLabel: "種類",
	},
	"zh-Hans": {
		work: "我的 OpenProject 工作包", noWork: "这个视图里没有分配给你的工作包。", projects: "项目", noProjects: "没有可见的活跃项目。", create: "创建工作包", back: "返回", more: "加载更多", missingConfig: "先在「设置 › 扩展」中填写 OpenProject 实例地址和 opapi token。",
		subject: "主题", description: "描述", start: "开始日期 (YYYY-MM-DD)", due: "截止日期 (YYYY-MM-DD)", review: "预览", project: "项目", typeLabel: "类型",
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
