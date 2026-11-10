package main

import "strings"

type texts struct {
	recipes, search, results, noRecipes, back, more, missingConfig        string
	importRecipe, review, url, importNotice, source, servings             string
	description, ingredients, noIngredients, instructions, noInstructions string
}

var locales = map[string]*texts{
	"en": {
		recipes: "Mealie recipes", search: "Search your recipe library", results: "%d recipes", noRecipes: "No recipes matched.", back: "Back", more: "Load more", missingConfig: "Set the Mealie instance URL and API token in Settings › Extensions first.",
		importRecipe: "Import recipe", review: "Review", url: "Public recipe webpage URL", importNotice: "Mealie will fetch and import this public webpage. Local/private addresses and URL credentials are refused.", source: "Open original recipe", servings: "servings",
		description: "Description", ingredients: "Ingredients", noIngredients: "No ingredients saved.", instructions: "Instructions", noInstructions: "No instructions saved.",
	},
	"ja": {
		recipes: "Mealie レシピ", search: "レシピライブラリを検索", results: "%d 件のレシピ", noRecipes: "一致するレシピはありません。", back: "戻る", more: "さらに読み込む", missingConfig: "設定 › 拡張機能で Mealie のインスタンス URL と API トークンを設定してください。",
		importRecipe: "レシピをインポート", review: "確認", url: "公開レシピページの URL", importNotice: "Mealie がこの公開ページを取得してインポートします。ローカル／プライベートアドレスと URL 認証情報は拒否されます。", source: "元のレシピを開く", servings: "人分",
		description: "説明", ingredients: "材料", noIngredients: "材料は保存されていません。", instructions: "手順", noInstructions: "手順は保存されていません。",
	},
	"zh-Hans": {
		recipes: "Mealie 菜谱", search: "搜索自己的菜谱库", results: "%d 个菜谱", noRecipes: "没有匹配的菜谱。", back: "返回", more: "加载更多", missingConfig: "先在「设置 › 扩展」中填写 Mealie 实例地址和 API token。",
		importRecipe: "导入菜谱", review: "预览", url: "公开菜谱网页 URL", importNotice: "Mealie 将抓取并导入这个公开网页。拒绝本机、内网地址和 URL 中的凭据。", source: "打开原始菜谱", servings: "人份",
		description: "简介", ingredients: "食材", noIngredients: "没有保存食材。", instructions: "步骤", noInstructions: "没有保存步骤。",
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
