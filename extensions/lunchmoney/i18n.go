package main

import "strings"

type texts struct {
	finance, transactions, noTransactions, more, missingConfig    string
	filter, start, end, category, apply, categories, noCategories string
	accounts, noAccounts, manual, synced, back                    string
}

var locales = map[string]*texts{
	"en": {
		finance: "Lunch Money finance", transactions: "Transactions", noTransactions: "No transactions matched.", more: "Load more", missingConfig: "Set the Lunch Money API token in Settings › Extensions first.",
		filter: "Filter transactions", start: "Start date (YYYY-MM-DD)", end: "End date (YYYY-MM-DD)", category: "Category ID (optional)", apply: "Apply filter", categories: "Categories", noCategories: "No categories.",
		accounts: "Accounts", noAccounts: "No accounts.", manual: "Manual accounts", synced: "Synced accounts", back: "Back",
	},
	"ja": {
		finance: "Lunch Money 家計", transactions: "取引", noTransactions: "一致する取引はありません。", more: "さらに読み込む", missingConfig: "設定 › 拡張機能で Lunch Money API トークンを設定してください。",
		filter: "取引を絞り込む", start: "開始日（YYYY-MM-DD）", end: "終了日（YYYY-MM-DD）", category: "カテゴリ ID（任意）", apply: "適用", categories: "カテゴリ", noCategories: "カテゴリはありません。",
		accounts: "アカウント", noAccounts: "アカウントはありません。", manual: "手動アカウント", synced: "同期アカウント", back: "戻る",
	},
	"zh-Hans": {
		finance: "Lunch Money 账务", transactions: "交易", noTransactions: "没有匹配的交易。", more: "加载更多", missingConfig: "先在「设置 › 扩展」中填写 Lunch Money API token。",
		filter: "筛选交易", start: "开始日期（YYYY-MM-DD）", end: "结束日期（YYYY-MM-DD）", category: "分类 ID（可选）", apply: "应用筛选", categories: "分类", noCategories: "暂无分类。",
		accounts: "账户", noAccounts: "暂无账户。", manual: "手动账户", synced: "同步账户", back: "返回",
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
