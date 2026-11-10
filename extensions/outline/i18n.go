package main

import "strings"

type texts struct {
	knowledge, search, searchHint, results, noDocuments, back, more, missingConfig string
	collections, noCollections, create, personal, review, title, text, draftNotice string
	updated, document                                                              string
}

var locales = map[string]*texts{
	"en": {
		knowledge: "Outline knowledge", search: "Search accessible documents", searchHint: "Search the knowledge you can access, or create an unpublished draft.", results: "Documents", noDocuments: "No accessible documents matched.", back: "Back", more: "Load more", missingConfig: "Set the Outline instance URL and API key in Settings › Extensions first.",
		collections: "Choose a collection", noCollections: "No active accessible collections.", create: "Create draft", personal: "Personal collection", review: "Review", title: "Title", text: "Markdown content", draftNotice: "This creates an unpublished document (publish: false); it does not deliberately publish to workspace members.",
		updated: "Updated", document: "Markdown document",
	},
	"ja": {
		knowledge: "Outline ナレッジ", search: "アクセス可能なドキュメントを検索", searchHint: "アクセス可能なナレッジを検索するか、非公開の下書きを作成します。", results: "ドキュメント", noDocuments: "一致するドキュメントはありません。", back: "戻る", more: "さらに読み込む", missingConfig: "設定 › 拡張機能で Outline のインスタンス URL と API キーを設定してください。",
		collections: "コレクションを選択", noCollections: "有効なコレクションはありません。", create: "下書きを作成", personal: "個人コレクション", review: "確認", title: "タイトル", text: "Markdown 本文", draftNotice: "非公開ドキュメントを作成します（publish: false）。ワークスペースのメンバーへ意図的に公開しません。",
		updated: "更新日", document: "Markdown ドキュメント",
	},
	"zh-Hans": {
		knowledge: "Outline 知识库", search: "搜索可访问的文档", searchHint: "搜索你能访问的知识，或创建不公开的草稿。", results: "文档", noDocuments: "没有匹配的可访问文档。", back: "返回", more: "加载更多", missingConfig: "先在「设置 › 扩展」中填写 Outline 实例地址和 API key。",
		collections: "选择集合", noCollections: "没有可访问的有效集合。", create: "创建草稿", personal: "个人集合", review: "预览", title: "标题", text: "Markdown 正文", draftNotice: "将创建不公开的文档（publish: false），不会主动向工作区成员发布。",
		updated: "更新时间", document: "Markdown 文档",
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
