package main

import "strings"

type texts struct {
	documents, search, results, noDocuments, back, more, missingConfig string
	tags, correspondents, noLookup, download, ocr, notes, pages        string
}

func (t *texts) lookup(kind string) string {
	if kind == "tags" {
		return t.tags
	}
	return t.correspondents
}

var locales = map[string]*texts{
	"en": {
		documents: "Paperless documents", search: "Search title and OCR content", results: "%d accessible documents", noDocuments: "No accessible documents matched.", back: "Back", more: "Load more", missingConfig: "Set the Paperless-ngx instance URL and API token in Settings › Extensions first.",
		tags: "Tags", correspondents: "Correspondents", noLookup: "No accessible entries.", download: "Download original attachment", ocr: "OCR text", notes: "Notes", pages: "pages",
	},
	"ja": {
		documents: "Paperless ドキュメント", search: "タイトルと OCR 本文を検索", results: "アクセス可能なドキュメント %d 件", noDocuments: "一致するドキュメントはありません。", back: "戻る", more: "さらに読み込む", missingConfig: "設定 › 拡張機能で Paperless-ngx のインスタンス URL と API トークンを設定してください。",
		tags: "タグ", correspondents: "差出人", noLookup: "アクセス可能な項目はありません。", download: "元の添付ファイルをダウンロード", ocr: "OCR テキスト", notes: "ノート", pages: "ページ",
	},
	"zh-Hans": {
		documents: "Paperless 档案", search: "搜索标题和 OCR 正文", results: "%d 份可访问档案", noDocuments: "没有匹配的可访问档案。", back: "返回", more: "加载更多", missingConfig: "先在「设置 › 扩展」中填写 Paperless-ngx 实例地址和 API token。",
		tags: "标签", correspondents: "往来方", noLookup: "没有可访问的条目。", download: "下载原始附件", ocr: "OCR 正文", notes: "笔记", pages: "页",
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
