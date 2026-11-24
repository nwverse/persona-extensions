package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"

	"github.com/nwverse/persona/sdk/go/wire"
)

// The connector's tools, for the assistant. Descriptions and answers are in Chinese like the service; every point
// in and out is WGS-84 "纬度,经度".

// schema is a JSON Schema object for a tool's input.
type schema = map[string]any

func prop(typ, desc string) schema { return schema{"type": typ, "description": desc} }

func intProp(desc string, lo, hi int) schema {
	return schema{"type": "integer", "description": desc, "minimum": lo, "maximum": hi}
}

func enumProp(desc string, values ...string) schema {
	return schema{"type": "string", "description": desc, "enum": values}
}

func stringArrayProp(desc string) schema {
	return schema{"type": "array", "description": desc, "items": schema{"type": "string"}}
}

func latProp(what string) schema {
	return schema{"type": "number", "description": what + "纬度（WGS-84，和手机定位、常用地点相同）", "minimum": -90, "maximum": 90}
}

func lngProp(what string) schema {
	return schema{"type": "number", "description": what + "经度（WGS-84）", "minimum": -180, "maximum": 180}
}

func object(required []string, props schema) json.RawMessage {
	s := schema{"type": "object", "properties": props}
	if len(required) > 0 {
		s["required"] = required
	}
	b, _ := json.Marshal(s)
	return b
}

type toolSpec struct {
	name, description string
	input             json.RawMessage
}

var tools = []toolSpec{
	{"amap_search",
		"用高德地图按名字或类别搜中国境内的地点（店铺、景点、车站、小区、公司……）：名称、类别、地址、坐标、电话、今天的营业时间、评分、人均。找「附近的」用 amap_nearby。坐标是 WGS-84「纬度,经度」，可以直接交给 amap_route、amap_nearby 或 phone_maps_*。搜索额度少，不要换着说法反复搜。",
		object([]string{"keywords"}, schema{
			"keywords":  prop("string", "搜什么：地点名、品牌或类别，如「北京南站」「星巴克」「火锅」"),
			"city":      prop("string", "在哪个城市搜，如「上海」；只是优先这个城市，不写就搜全国"),
			"city_only": prop("boolean", "true 时只要 city 里的结果"),
			"limit":     intProp("最多几条，默认 5", 1, 10),
		})},
	{"amap_nearby",
		"用高德地图搜一个点周围的地点，由近到远：名称、类别、距离（米）、地址、坐标、电话、营业时间、评分、人均。用户说「附近」「我这儿」时先用 phone_location_current 拿当前位置再传坐标；说「家附近」「公司附近」可直接把「家」「公司」或常用地点名传给 place，或先用 amap_places / list_places。",
		object(nil, schema{
			"latitude":      latProp("中心点"),
			"longitude":     lngProp("中心点"),
			"place":         prop("string", "没有坐标时，中心点的地址、地名或常用地点名（如「家」「公司」「北京市朝阳区三里屯」）"),
			"city":          prop("string", "place 在哪个城市，如「上海」；同名地点多时写上"),
			"keywords":      prop("string", "找什么，如「咖啡」「停车场」「药店」；不写就是餐饮、购物和生活服务"),
			"radius_meters": intProp("搜索半径（米），默认 1000", 1, 50000),
			"sort":          enumProp("distance（默认）由近到远；weight 按高德的综合排序", "distance", "weight"),
			"limit":         intProp("最多几条，默认 5", 1, 10),
		})},
	{"amap_geocode",
		"用高德地图把中国境内的地址换成坐标（WGS-84「纬度,经度」），并给出规范地址、所在城市和区、匹配到的精度。找店名、景点名用 amap_search 更准。",
		object([]string{"address"}, schema{
			"address": prop("string", "地址，越完整越准，如「北京市朝阳区阜通东大街6号」"),
			"city":    prop("string", "在哪个城市，如「北京」；不写就查全国"),
		})},
	{"amap_reverse_geocode",
		"用高德地图查一个坐标（WGS-84）在哪儿：地址、街道门牌、所在的小区或商圈、附近的几个地点（距离、方向）。",
		object([]string{"latitude", "longitude"}, schema{
			"latitude":  latProp(""),
			"longitude": lngProp(""),
		})},
	{"amap_route",
		"用高德地图规划中国境内的路线：驾车（默认，支持途经点 waypoints）、步行、骑行、电动车、公交地铁。给距离（公里）和时长；驾车另给过路费、红绿灯、主要道路和打车约多少钱；公交给每个方案坐哪条线、几站、票价、步行多远。起终点给坐标（WGS-84）最准，也可以直接给常用地点名（「家」「公司」等）、地址或地名。用户说「从这儿」「我现在」时先用 phone_location_current；不确定「家」「公司」有没有配时可用 amap_places 或 list_places。只算路线，不打开导航（导航用 phone_maps_navigate）。",
		object(nil, schema{
			"mode":           enumProp("出行方式：driving 驾车（默认），walking 步行，cycling 骑自行车，ebike 电动车，transit 公交地铁", "driving", "walking", "cycling", "ebike", "transit"),
			"from":           prop("string", "起点的地名、地址或常用地点名（如「家」「公司」「北京南站」）；给了坐标时只当作起点的名字"),
			"from_latitude":  latProp("起点"),
			"from_longitude": lngProp("起点"),
			"to":             prop("string", "终点的地名、地址或常用地点名（如「家」「公司」）；给了坐标时只当作终点的名字"),
			"to_latitude":    latProp("终点"),
			"to_longitude":   lngProp("终点"),
			"waypoints":      stringArrayProp("驾车途经点列表（最多 16 个），每项可以是地名、地址、常用地点名或 WGS-84「纬度,经度」；仅 driving 模式支持"),
			"city":           prop("string", "用名字查起终点和途经点时优先在哪个城市找，如「上海」；不写就按另一端所在的城市找"),
			"prefer":         enumProp("驾车偏好：recommended 高德推荐（默认），fastest 最快，avoid_congestion 躲避拥堵，avoid_tolls 不走收费路，avoid_highways 不走高速", "recommended", "fastest", "avoid_congestion", "avoid_tolls", "avoid_highways"),
			"depart_at":      prop("string", "公交的出发时间，北京时间，如「2026-11-16 08:30」；不写就是现在"),
			"alternatives":   intProp("给几个方案，默认 1", 1, 3),
			"steps":          prop("boolean", "true 时附上逐段指引（驾车、步行、骑行、电动车）"),
		})},
	{"amap_distance",
		"用高德地图批量测多个起点到同一个终点的距离和耗时（驾车、步行或直线，最多 20 个起点），适合比较「哪家店 / 哪个酒店离目的地最近或最快到」。起点和终点都支持地名、地址、常用地点名（「家」「公司」等）或 WGS-84「纬度,经度」。",
		object([]string{"origins"}, schema{
			"origins":               stringArrayProp("起点列表（1–20 个），每项可以是地名、地址、常用地点名或 WGS-84「纬度,经度」"),
			"destination":           prop("string", "终点的地名、地址、常用地点名或 WGS-84「纬度,经度」；给了终点坐标时只当作终点名字"),
			"destination_latitude":  latProp("终点"),
			"destination_longitude": lngProp("终点"),
			"mode":                  enumProp("测量方式：driving 驾车（默认），walking 步行，straight 直线距离", "driving", "walking", "straight"),
			"city":                  prop("string", "用名字查起终点时优先在哪个城市找，如「北京」"),
			"sort":                  enumProp("结果排序：input 按输入顺序（默认），distance 由近到远，duration 用时最短优先", "input", "distance", "duration"),
		})},
	{"amap_district",
		"用高德地图查中国行政区划（省、市、区县、街道）：返回行政区编码（adcode）、电话区号（citycode）、WGS-84 中心坐标和下辖区划列表。不填 keywords 时返回全国省级区划。",
		object(nil, schema{
			"keywords":    prop("string", "省、市、区县名或 adcode，如「浙江」「杭州市」「330100」；不填查全国"),
			"subdistrict": intProp("往下查几级下辖区划（0 不查下级，1 下一级，2 下两级），默认 1", 0, 2),
		})},
	{"amap_input_tips",
		"用高德地图做地名输入提示/联想补全：当用户只记得简称、局部名字或不确定完整名称时，快速列出候选地名、所在区、地址、adcode 和 WGS-84 坐标。",
		object([]string{"keywords"}, schema{
			"keywords":  prop("string", "要补全或联想的关键词，如「国贸」「协和」"),
			"city":      prop("string", "在哪个城市联想，如「北京」"),
			"city_only": prop("boolean", "true 时只返回 city 里的结果"),
			"latitude":  latProp("偏好位置"),
			"longitude": lngProp("偏好位置"),
			"limit":     intProp("最多几条，默认 5", 1, 10),
		})},
	{"amap_static_map",
		"用高德地图生成一张中国境内的静态地图图片（PNG），可标中心点、多个标注点（自动标 A、B、C…）、连线路径和实时路况。中心、markers、path 也都支持常用地点名（如「家」「公司」）。适合直观展示位置、多个候选地点分布或大致路线。",
		object(nil, schema{
			"latitude":  latProp("地图中心"),
			"longitude": lngProp("地图中心"),
			"place":     prop("string", "地图中心的地名、地址或常用地点名（未给经纬度时使用）"),
			"city":      prop("string", "用名字解析地点时优先在哪个城市找"),
			"markers":   stringArrayProp("要在图上标注的地点（最多 10 个，按顺序标为 A、B、C…），每项可以是地名、地址、常用地点名或 WGS-84「纬度,经度」"),
			"path":      stringArrayProp("要在图上按顺序连成蓝线的点（2–20 个），每项可以是地名、地址、常用地点名或 WGS-84「纬度,经度」"),
			"zoom":      intProp("缩放级别 1–17（越大越细）；给了 markers 或 path 时可不填，高德会自动适配视野", 1, 17),
			"width":     intProp("图片宽（像素），默认 600", 100, 1024),
			"height":    intProp("图片高（像素），默认 400", 100, 1024),
			"traffic":   prop("boolean", "true 时叠加实时路况"),
		})},
	{"amap_weather",
		"用高德地图查中国城市（含区县）的天气，数据来自中国气象局：实况的天气、温度、湿度、风，以及从今天起几天的白天和夜间天气、最高最低温。city 也可直接填常用地点名（如「家」「公司」）或 WGS-84 坐标。中国城市优先用这个；境外城市用别的天气工具。",
		object([]string{"city"}, schema{
			"city": prop("string", "城市/区县名（如「杭州」「北京市朝阳区」）、6 位 adcode、常用地点名（如「家」「公司」）或 WGS-84「纬度,经度」"),
			"days": intProp("预报几天（含今天），默认 3；0 只看实况", 0, 4),
		})},
	{"amap_places",
		"读取用户在高德地图扩展里配置或页面保存的常用地点（「家」「公司」等），不花高德额度。其实 amap_route、amap_nearby、amap_distance、amap_static_map、amap_weather 都能直接传常用地点名或别名（「家 / home / 我家」「公司 / company / work / office」）；想先确认已存地点、坐标或详情时用它，没配到时再看 Persona 的 list_places。",
		object(nil, schema{
			"name": prop("string", "可选：要查的常用地点名或别名，如「家」「公司」；不填就列出全部"),
		})},
}

func toolList() []wire.ConnectorTool {
	out := make([]wire.ConnectorTool, len(tools))
	for i, t := range tools {
		out[i] = wire.ConnectorTool{Name: t.name, Description: t.description, InputSchema: t.input}
	}
	return out
}

// args are every tool's arguments; each tool reads its own.
type args struct {
	Keywords             string   `json:"keywords"`
	City                 string   `json:"city"`
	CityOnly             bool     `json:"city_only"`
	Limit                int      `json:"limit"`
	Latitude             *float64 `json:"latitude"`
	Longitude            *float64 `json:"longitude"`
	Place                string   `json:"place"`
	RadiusMeters         int      `json:"radius_meters"`
	Sort                 string   `json:"sort"`
	Address              string   `json:"address"`
	Mode                 string   `json:"mode"`
	From                 string   `json:"from"`
	FromLatitude         *float64 `json:"from_latitude"`
	FromLongitude        *float64 `json:"from_longitude"`
	To                   string   `json:"to"`
	ToLatitude           *float64 `json:"to_latitude"`
	ToLongitude          *float64 `json:"to_longitude"`
	Waypoints            []string `json:"waypoints"`
	Prefer               string   `json:"prefer"`
	DepartAt             string   `json:"depart_at"`
	Alternatives         int      `json:"alternatives"`
	Steps                bool     `json:"steps"`
	Days                 *int     `json:"days"`
	Origins              []string `json:"origins"`
	Destination          string   `json:"destination"`
	DestinationLatitude  *float64 `json:"destination_latitude"`
	DestinationLongitude *float64 `json:"destination_longitude"`
	Subdistrict          *int     `json:"subdistrict"`
	Markers              []string `json:"markers"`
	Path                 []string `json:"path"`
	Zoom                 *int     `json:"zoom"`
	Width                int      `json:"width"`
	Height               int      `json:"height"`
	Traffic              bool     `json:"traffic"`
	Name                 string   `json:"name"`
}

// limitOr is n clamped to 1..most, def when n is not set.
func limitOr(n, def, most int) int {
	if n <= 0 {
		return def
	}
	return min(n, most)
}

// pointArg reads a latitude and longitude pair: ok false when neither is given, an error when only one is or it is
// out of range.
func pointArg(lat, lng *float64, what string) (point, bool, error) {
	if lat == nil && lng == nil {
		return point{}, false, nil
	}
	if lat == nil || lng == nil {
		return point{}, false, fmt.Errorf("%s的纬度和经度要一起给", what)
	}
	p := point{*lat, *lng}
	if math.IsNaN(p.Lat) || math.IsNaN(p.Lng) || math.Abs(p.Lat) > 90 || math.Abs(p.Lng) > 180 {
		return point{}, false, fmt.Errorf("%s的坐标不对：纬度在 -90 到 90 之间，经度在 -180 到 180 之间", what)
	}
	return p, true, nil
}

func (a *app) callTool(ctx context.Context, name string, raw json.RawMessage) (wire.ConnectorCallResult, error) {
	var in args
	if len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &in); err != nil {
			return wire.ConnectorCallResult{}, fmt.Errorf("参数不对：%v", err)
		}
	}
	trim := func(s *string) { *s = strings.TrimSpace(*s) }
	for _, s := range []*string{&in.Keywords, &in.City, &in.Place, &in.Sort, &in.Address, &in.Mode, &in.From, &in.To, &in.Prefer, &in.DepartAt, &in.Destination, &in.Name} {
		trim(s)
	}
	var text string
	var err error
	switch name {
	case "amap_search":
		text, err = a.toolSearch(ctx, in)
	case "amap_nearby":
		text, err = a.toolNearby(ctx, in)
	case "amap_geocode":
		text, err = a.toolGeocode(ctx, in)
	case "amap_reverse_geocode":
		text, err = a.toolReverse(ctx, in)
	case "amap_route":
		text, err = a.toolRoute(ctx, in)
	case "amap_distance":
		text, err = a.toolDistance(ctx, in)
	case "amap_district":
		text, err = a.toolDistrict(ctx, in)
	case "amap_input_tips":
		text, err = a.toolInputTips(ctx, in)
	case "amap_static_map":
		return a.toolStaticMap(ctx, in)
	case "amap_weather":
		text, err = a.toolWeather(ctx, in)
	case "amap_places":
		text, err = a.toolPlaces(ctx, in)
	default:
		return wire.ConnectorCallResult{}, fmt.Errorf("没有工具 %s", name)
	}
	if err != nil {
		return wire.ConnectorCallResult{}, err
	}
	return wire.ConnectorCallResult{Text: text}, nil
}
