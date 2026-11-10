package main

import "strings"

type texts struct {
	inventory, search, results, noItems, back, more, missingConfig string
	create, noCreateChoices, review, name, description, quantity   string
	serial, model, manufacturer, warranty, notes                   string
}

var locales = map[string]*texts{
	"en": {
		inventory: "Homebox inventory", search: "Search items, serial numbers and locations", results: "%d entities", noItems: "No entities matched.", back: "Back", more: "Load more", missingConfig: "Set the Homebox instance URL and API token in Settings › Extensions first.",
		create: "Register an item in this location", noCreateChoices: "Create a non-location entity type and a location in Homebox before registering items here.", review: "Review", name: "Item name", description: "Description", quantity: "Quantity",
		serial: "Serial number", model: "Model", manufacturer: "Manufacturer", warranty: "Warranty", notes: "Notes",
	},
	"ja": {
		inventory: "Homebox 物品", search: "物品・シリアル番号・保管場所を検索", results: "%d 件のエンティティ", noItems: "一致するエンティティはありません。", back: "戻る", more: "さらに読み込む", missingConfig: "設定 › 拡張機能で Homebox のインスタンス URL と API トークンを設定してください。",
		create: "この保管場所に物品を登録", noCreateChoices: "先に Homebox でロケーション以外のエンティティタイプと保管場所を作成してください。", review: "確認", name: "物品名", description: "説明", quantity: "数量",
		serial: "シリアル番号", model: "型番", manufacturer: "メーカー", warranty: "保証", notes: "ノート",
	},
	"zh-Hans": {
		inventory: "Homebox 物品库", search: "搜索物品、序列号和存放位置", results: "%d 个实体", noItems: "没有匹配的实体。", back: "返回", more: "加载更多", missingConfig: "先在「设置 › 扩展」中填写 Homebox 实例地址和 API token。",
		create: "在此位置登记物品", noCreateChoices: "请先在 Homebox 创建非位置类型和存放位置，再在这里登记物品。", review: "预览", name: "物品名称", description: "描述", quantity: "数量",
		serial: "序列号", model: "型号", manufacturer: "制造商", warranty: "保修", notes: "备注",
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
