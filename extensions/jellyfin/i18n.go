package main

import "strings"

type texts struct {
	watching, resume, favorites, searchView, search, items, noItems, more, missingConfig string
	item, favorite, unfavorite, back, overview                                           string
}

var locales = map[string]*texts{
	"en": {
		watching: "Jellyfin watching", resume: "Resume", favorites: "Favorites", searchView: "Search", search: "Search movies and shows", items: "Media", noItems: "No media items in this view.", more: "Load more", missingConfig: "Set the Jellyfin instance URL and API key in Settings › Extensions first.",
		item: "Media details", favorite: "Add to favorites", unfavorite: "Remove from favorites", back: "Back", overview: "Overview",
	},
	"ja": {
		watching: "Jellyfin 視聴", resume: "続きを見る", favorites: "お気に入り", searchView: "検索", search: "映画と番組を検索", items: "メディア", noItems: "この表示にメディアはありません。", more: "さらに読み込む", missingConfig: "設定 › 拡張機能で Jellyfin のインスタンス URL と API キーを設定してください。",
		item: "メディアの詳細", favorite: "お気に入りに追加", unfavorite: "お気に入りから削除", back: "戻る", overview: "概要",
	},
	"zh-Hans": {
		watching: "Jellyfin 继续观看", resume: "继续观看", favorites: "收藏", searchView: "搜索", search: "搜索电影和剧集", items: "媒体", noItems: "这个视图里没有媒体。", more: "加载更多", missingConfig: "先在「设置 › 扩展」中填写 Jellyfin 实例地址和 API key。",
		item: "媒体详情", favorite: "添加收藏", unfavorite: "取消收藏", back: "返回", overview: "简介",
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
