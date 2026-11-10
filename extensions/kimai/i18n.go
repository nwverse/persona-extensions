package main

import "strings"

type texts struct {
	time, recent, active, history, entries, noEntries, more, back, missingConfig string
	projects, noProjects, activities, noActivities, create, review               string
	begin, end, description, timerNotice, stop, noActive                         string
}

var locales = map[string]*texts{
	"en": {
		time: "Kimai time tracking", recent: "Recent", active: "Running", history: "History", entries: "Time entries", noEntries: "No time entries in this view.", more: "Load more", back: "Back", missingConfig: "Set the Kimai instance URL and API token in Settings › Extensions first.",
		projects: "Choose a project", noProjects: "No visible projects.", activities: "Choose an activity", noActivities: "No visible activities in this project.", create: "Create time entry", review: "Review",
		begin: "Begin (RFC3339 with timezone)", end: "End (optional, starts a timer if empty)", description: "Description", timerNotice: "Use timestamps with an explicit timezone. Leave end empty to start a running timer.", stop: "Stop timer", noActive: "No running timers.",
	},
	"ja": {
		time: "Kimai 時間記録", recent: "最近", active: "実行中", history: "履歴", entries: "時間エントリ", noEntries: "この表示に時間エントリはありません。", more: "さらに読み込む", back: "戻る", missingConfig: "設定 › 拡張機能で Kimai のインスタンス URL と API トークンを設定してください。",
		projects: "プロジェクトを選択", noProjects: "表示可能なプロジェクトはありません。", activities: "アクティビティを選択", noActivities: "このプロジェクトに表示可能なアクティビティはありません。", create: "時間エントリを作成", review: "確認",
		begin: "開始（タイムゾーン付き RFC3339）", end: "終了（空の場合はタイマーを開始）", description: "説明", timerNotice: "タイムゾーンを明示してください。終了を空にすると実行中のタイマーが始まります。", stop: "タイマーを停止", noActive: "実行中のタイマーはありません。",
	},
	"zh-Hans": {
		time: "Kimai 时间记录", recent: "近期", active: "进行中", history: "历史", entries: "工时记录", noEntries: "这个视图里没有工时记录。", more: "加载更多", back: "返回", missingConfig: "先在「设置 › 扩展」中填写 Kimai 实例地址和 API token。",
		projects: "选择项目", noProjects: "没有可见项目。", activities: "选择活动", noActivities: "这个项目没有可见活动。", create: "创建工时", review: "预览",
		begin: "开始时间（含时区的 RFC3339）", end: "结束时间（留空则启动计时）", description: "描述", timerNotice: "请使用带时区的时间。结束时间留空会启动一个进行中的计时器。", stop: "停止计时", noActive: "没有进行中的计时器。",
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
