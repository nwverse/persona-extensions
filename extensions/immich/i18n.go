package main

import "strings"

type texts struct {
	library, favorites, filename, smart, search, results, noAssets, more, missingConfig string
	asset, favorite, unfavorite, back, description, people                              string
}

var locales = map[string]*texts{
	"en": {
		library: "Immich library", favorites: "Favorites", filename: "Filename", smart: "Natural language", search: "Search filename or natural-language description", results: "Assets", noAssets: "No assets matched.", more: "Load more", missingConfig: "Set the Immich instance URL and API key in Settings › Extensions first.",
		asset: "Asset details", favorite: "Add to favorites", unfavorite: "Remove from favorites", back: "Back", description: "Description", people: "People",
	},
	"ja": {
		library: "Immich ライブラリ", favorites: "お気に入り", filename: "ファイル名", smart: "自然言語", search: "ファイル名または自然言語で検索", results: "アセット", noAssets: "一致するアセットはありません。", more: "さらに読み込む", missingConfig: "設定 › 拡張機能で Immich のインスタンス URL と API キーを設定してください。",
		asset: "アセットの詳細", favorite: "お気に入りに追加", unfavorite: "お気に入りから削除", back: "戻る", description: "説明", people: "人物",
	},
	"zh-Hans": {
		library: "Immich 照片库", favorites: "收藏", filename: "文件名", smart: "自然语言", search: "按文件名或自然语言搜索", results: "资产", noAssets: "没有匹配的资产。", more: "加载更多", missingConfig: "先在「设置 › 扩展」中填写 Immich 实例地址和 API key。",
		asset: "资产详情", favorite: "添加收藏", unfavorite: "取消收藏", back: "返回", description: "描述", people: "人物",
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
