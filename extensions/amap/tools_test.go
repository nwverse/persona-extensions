package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/nwverse/persona/sdk/go/wire"
)

func TestToolsAreListedWithSchemas(t *testing.T) {
	list := toolList()
	if len(list) != 11 {
		t.Fatalf("%d tools", len(list))
	}
	seen := map[string]bool{}
	for _, tool := range list {
		if !strings.HasPrefix(tool.Name, connectorID+"_") || seen[tool.Name] {
			t.Errorf("tool name %q", tool.Name)
		}
		seen[tool.Name] = true
		var s struct {
			Type       string                     `json:"type"`
			Properties map[string]json.RawMessage `json:"properties"`
			Required   []string                   `json:"required"`
		}
		if err := json.Unmarshal(tool.InputSchema, &s); err != nil || s.Type != "object" || len(s.Properties) == 0 {
			t.Errorf("%s: schema %s", tool.Name, tool.InputSchema)
		}
		for _, r := range s.Required {
			if _, ok := s.Properties[r]; !ok {
				t.Errorf("%s: required %q is not a property", tool.Name, r)
			}
		}
		// Every argument a schema names is one callTool reads.
		for name := range s.Properties {
			if !strings.Contains(argFields, `json:"`+name+`"`) {
				t.Errorf("%s: %q is not read", tool.Name, name)
			}
		}
		if err := wire.ValidateJSON("ConnectorTool", mustJSON(t, tool)); err != nil {
			t.Errorf("%s: %v", tool.Name, err)
		}
	}
}

// argFields are args' JSON tags.
var argFields = func() string {
	b, _ := json.Marshal(args{})
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	var out []string
	for k := range m {
		out = append(out, `json:"`+k+`"`)
	}
	return strings.Join(out, " ")
}()

func TestSearch(t *testing.T) {
	f := newFake(t)
	a, _ := testApp(t, f)
	text := mustCall(t, a, "amap_search", `{"keywords":"北京南站","city":"北京","city_only":true,"limit":2}`)
	q := f.last("/v5/place/text")
	if q.Get("keywords") != "北京南站" || q.Get("region") != "北京" || q.Get("city_limit") != "true" || q.Get("page_size") != "2" || q.Get("show_fields") != "business" {
		t.Fatalf("query %v", q)
	}
	wantIn(t, text, "WGS-84",
		"1. 北京南站（火车站） · 北京市丰台区永外大街车站路12号 · ",
		"电话 010-10000001 · 今天 05:30-24:00",
		"2. 北京南站(地铁站) · ", "电话 010-10000002、010-10000003")
	// AMap's point (GCJ-02) comes back as WGS-84: about 500 m south-west of it in Beijing.
	at := toWGS(point{39.865246, 116.378517})
	wantIn(t, text, at.String())

	f.queue("/v5/place/text", `{"status":"1","info":"OK","infocode":"10000","count":"0","pois":[]}`)
	if text := mustCall(t, a, "amap_search", `{"keywords":"不存在的地方"}`); !strings.Contains(text, "没搜到「不存在的地方」") {
		t.Fatalf("text %q", text)
	}
	if _, err := call(t, a, "amap_search", `{}`); err == nil {
		t.Fatal("a search without keywords ran")
	}
}

func TestNearbyConvertsTheCenterAndSortsByDistance(t *testing.T) {
	f := newFake(t)
	a, _ := testApp(t, f)
	text := mustCall(t, a, "amap_nearby", `{"latitude":39.9055,"longitude":116.3976,"keywords":"咖啡","radius_meters":800,"limit":3}`)
	q := f.last("/v5/place/around")
	if q.Get("location") != amapLocation(point{39.9055, 116.3976}) || q.Get("radius") != "800" || q.Get("sortrule") != "distance" || q.Get("keywords") != "咖啡" {
		t.Fatalf("query %v", q)
	}
	first := strings.Index(text, "国家博物馆咖啡厅")
	second := strings.Index(text, "北京饭店阳光咖啡厅")
	third := strings.Index(text, "书局coffee")
	if first < 0 || !(first < second && second < third) {
		t.Fatalf("not nearest first:\n%s", text)
	}
	wantIn(t, text, "800 米 内找「咖啡」", "1. 国家博物馆咖啡厅（餐饮相关场所） · 218 米", "评分 3.9 · 人均 ¥59", "人均 ¥107")

	// A place's name is looked up first.
	text = mustCall(t, a, "amap_nearby", `{"place":"天安门"}`)
	wantIn(t, text, "天安门（北京市东城区） 周围 1 公里 内找吃饭、购物和生活服务的地方")
	if q := f.last("/v5/place/around"); q.Get("location") != "116.397755,39.903182" || q.Get("keywords") != "" {
		t.Fatalf("query %v", q)
	}

	_, err := call(t, a, "amap_nearby", `{"keywords":"咖啡"}`)
	if err == nil || !strings.Contains(err.Error(), "phone_location_current") {
		t.Fatalf("err %v", err)
	}
	if _, err := call(t, a, "amap_nearby", `{"latitude":39.9}`); err == nil || !strings.Contains(err.Error(), "一起给") {
		t.Fatalf("err %v", err)
	}
}

func TestGeocode(t *testing.T) {
	f := newFake(t)
	a, _ := testApp(t, f)
	text := mustCall(t, a, "amap_geocode", `{"address":"北京市天安门","city":"北京"}`)
	if q := f.last("/v3/geocode/geo"); q.Get("city") != "北京" {
		t.Fatalf("query %v", q)
	}
	wantIn(t, text, "「北京市天安门」的坐标（WGS-84 纬度,经度）", "1. 北京市东城区天安门广场 · ", "精度：兴趣点 · 北京市 东城区 · adcode 110101",
		toWGS(point{39.903182, 116.397755}).String())
	if text := mustCall(t, a, "amap_geocode", `{"address":"无此地址"}`); !strings.Contains(text, "没找到「无此地址」") {
		t.Fatalf("text %q", text)
	}
}

func TestReverseGeocode(t *testing.T) {
	f := newFake(t)
	a, _ := testApp(t, f)
	text := mustCall(t, a, "amap_reverse_geocode", `{"latitude":39.9042,"longitude":116.3912}`)
	q := f.last("/v3/geocode/regeo")
	if q.Get("location") != amapLocation(point{39.9042, 116.3912}) || q.Get("extensions") != "all" {
		t.Fatalf("query %v", q)
	}
	wantIn(t, text, "39.904200,116.391200（WGS-84）在：北京市东城区东华门街道毛主席纪念堂",
		"门牌：东交民巷39号（东210 米）", "在：毛主席纪念堂、天安门广场 里", "商圈：大栅栏", "附近：")
}

func TestDrivingRoute(t *testing.T) {
	f := newFake(t)
	a, _ := testApp(t, f)
	// Names are searched for; the second in the first one's city.
	text := mustCall(t, a, "amap_route", `{"from":"天安门","to":"北京南站"}`)
	q := f.last("/v5/direction/driving")
	if q.Get("origin") != "116.397755,39.903182" || q.Get("destination") != "116.378517,39.865246" || q.Get("strategy") != "32" || q.Get("show_fields") != "cost" {
		t.Fatalf("query %v", q)
	}
	if f.count("/v5/place/text") != 2 || f.last("/v5/place/text").Get("region") != "010" || f.count("/v3/geocode/geo") != 0 {
		t.Fatalf("searches %d (last %v), geocodes %d", f.count("/v5/place/text"), f.last("/v5/place/text"), f.count("/v3/geocode/geo"))
	}
	wantIn(t, text, "驾车 天安门（北京市东城区） → 北京南站（北京市丰台区）（高德推荐）：", "9.2 公里，约 34 分钟，红绿灯 20 个，无过路费；主要走 ", "西单北大街", "打车约 ¥26（高德估算）")
	if strings.Contains(text, "方案") {
		t.Fatalf("one plan was asked for:\n%s", text)
	}

	text = mustCall(t, a, "amap_route", `{"from_latitude":39.9055,"from_longitude":116.3976,"from":"我这儿","to":"北京南站","prefer":"avoid_tolls","alternatives":3,"steps":true}`)
	if q := f.last("/v5/direction/driving"); q.Get("strategy") != "1" || q.Get("origin") != amapLocation(point{39.9055, 116.3976}) {
		t.Fatalf("query %v", q)
	}
	// From a point, the name is looked up in the point's city.
	if q := f.last("/v5/place/text"); q.Get("region") != "010" {
		t.Fatalf("search %v", q)
	}
	wantIn(t, text, "驾车 我这儿 → 北京南站（北京市丰台区）（不走收费路）", "方案 1：9.2 公里", "方案 2：13.7 公里", "方案 3：8.9 公里", "  1. 沿广场东侧路向北行驶475米左转")
}

func TestAddressesAreGeocodedAndNamesSearched(t *testing.T) {
	f := newFake(t)
	a, _ := testApp(t, f)
	pl, err := a.locate(context.Background(), "北京市东城区天安门广场1号", "北京")
	if err != nil {
		t.Fatal(err)
	}
	if f.count("/v3/geocode/geo") != 1 || f.count("/v5/place/text") != 0 || f.last("/v3/geocode/geo").Get("city") != "北京" {
		t.Fatalf("geocodes %d, searches %d", f.count("/v3/geocode/geo"), f.count("/v5/place/text"))
	}
	if pl.where != "北京市东城区" || pl.citycode != "010" {
		t.Fatalf("place %+v", pl)
	}
	// A name the search does not know falls back on geocoding; a coarse address match on a search in its city.
	if _, err := a.locate(context.Background(), "北京南站东广场", ""); err == nil {
		t.Fatal("found a place nothing knows")
	}
	if _, err := a.locate(context.Background(), "北京南站", ""); err != nil {
		t.Fatal(err)
	}
	for in, want := range map[string]bool{"北京市朝阳区阜通东大街6号": true, "人民广场": false, "1号线": true, "东方明珠": false, "南京路100弄": true} {
		if addressLike(in) != want {
			t.Errorf("addressLike(%q) != %v", in, want)
		}
	}
}

func TestRouteNeedsBothEnds(t *testing.T) {
	f := newFake(t)
	a, _ := testApp(t, f)
	_, err := call(t, a, "amap_route", `{"to":"北京南站"}`)
	if err == nil {
		t.Fatal("no error")
	}
	wantIn(t, err.Error(), "要给起点", "phone_location_current", "list_places")
	if _, err := call(t, a, "amap_route", `{"from":"天安门","to":"北京南站","mode":"plane"}`); err == nil {
		t.Fatal("an unknown mode ran")
	}
	if f.count("") != 0 {
		t.Fatalf("%d requests for refused arguments", f.count(""))
	}
	text := mustCall(t, a, "amap_route", `{"from_latitude":39.9,"from_longitude":116.4,"to_latitude":39.90001,"to_longitude":116.40001}`)
	wantIn(t, text, "几乎在同一个地方")
}

func TestWalkingRoute(t *testing.T) {
	f := newFake(t)
	a, _ := testApp(t, f)
	text := mustCall(t, a, "amap_route", `{"mode":"walking","from_latitude":39.9055,"from_longitude":116.3976,"to":"天安门","steps":true}`)
	wantIn(t, text, "步行 起点 → 天安门（北京市东城区）：", "2.9 公里，约 39 分钟", "  1. 向西步行26米左转", "  4. 向北步行148米左转")
	if strings.Contains(text, "红绿灯") || strings.Contains(text, "过路费") {
		t.Fatalf("driving words on a walk:\n%s", text)
	}
	text = mustCall(t, a, "amap_route", `{"mode":"ebike","from_latitude":39.9055,"from_longitude":116.3976,"to":"天安门"}`)
	if f.count("/v5/direction/electrobike") != 1 {
		t.Fatal("ebike did not ask the e-bike route")
	}
	// Cycling and e-bike answers carry the time on the path and step lengths as numbers.
	wantIn(t, text, "电动车 起点 → 天安门（北京市东城区）：", "8 公里，约 48 分钟")
}

func TestTransitRoute(t *testing.T) {
	f := newFake(t)
	a, _ := testApp(t, f)
	text := mustCall(t, a, "amap_route", `{"mode":"transit","from":"天安门","to_latitude":39.8634,"to_longitude":116.3722,"to":"北京南站","alternatives":2,"depart_at":"2026-11-16 08:30"}`)
	q := f.last("/v5/direction/transit/integrated")
	// The named start has its city from geocoding; the point's city is looked up.
	if q.Get("city1") != "010" || q.Get("city2") != "010" || q.Get("AlternativeRoute") != "2" || q.Get("date") != "2026-11-16" || q.Get("time") != "8-30" {
		t.Fatalf("query %v", q)
	}
	if f.count("/v3/geocode/regeo") != 1 {
		t.Fatalf("%d reverse lookups", f.count("/v3/geocode/regeo"))
	}
	wantIn(t, text, "公交地铁 天安门（北京市东城区） → 北京南站（2026-11-16 08:30 出发）：",
		"方案 1：约 40 分钟，票价 ¥3，步行共 1.1 公里",
		"步行 880 米 → 地铁2号线内环 前门→宣武门（2 站） → 步行 157 米 → 地铁4号线大兴线（往天宫院）宣武门→北京南站（3 站） → 步行 56 米")
	if _, err := call(t, a, "amap_route", `{"mode":"transit","from":"天安门","to":"北京南站","depart_at":"明天早上"}`); err == nil {
		t.Fatal("a wrong depart_at was taken")
	}
}

func TestWeather(t *testing.T) {
	f := newFake(t)
	a, _ := testApp(t, f)
	text := mustCall(t, a, "amap_weather", `{"city":"杭州","days":2}`)
	if f.count("/v3/geocode/geo") != 1 || f.last("/v3/weather/weatherInfo").Get("city") != "330100" {
		t.Fatalf("the city was not looked up: %v", f.last("/v3/weather/weatherInfo"))
	}
	wantIn(t, text, "北京东城区 天气（中国气象局，经高德）：", "实况（11-15 19:05 发布）：霾，7°C，湿度 76%，西南风 ≤3 级",
		"- 11-15 周日：白天多云，夜间小雨，4~12°C，东北风 1-3 级", "- 11-16 周一：白天阴，夜间多云，2~8°C")
	if strings.Contains(text, "11-17") {
		t.Fatalf("more days than asked:\n%s", text)
	}

	before := f.count("/v3/weather/weatherInfo")
	text = mustCall(t, a, "amap_weather", `{"city":"110105","days":0}`)
	if f.count("/v3/weather/weatherInfo") != before+1 || strings.Contains(text, "周") {
		t.Fatalf("days 0 read a forecast:\n%s", text)
	}
	if _, err := call(t, a, "amap_weather", `{}`); err == nil || !strings.Contains(err.Error(), "list_places") {
		t.Fatalf("err %v", err)
	}
}

func TestWords(t *testing.T) {
	for in, want := range map[float64]string{0: "0 米", 999: "999 米", 1000: "1 公里", 9183: "9.2 公里", 13679: "13.7 公里"} {
		if got := distanceWords(in); got != want {
			t.Errorf("distanceWords(%v) = %q", in, got)
		}
	}
	for in, want := range map[float64]string{20: "1 分钟", 2057: "34 分钟", 3600: "1 小时", 4080: "1 小时 8 分钟"} {
		if got := durationWords(in); got != want {
			t.Errorf("durationWords(%v) = %q", in, got)
		}
	}
	for in, want := range map[string]string{
		"地铁4号线大兴线(安河桥北--天宫院)":       "地铁4号线大兴线（往天宫院）",
		"地铁2号线内环(积水潭--积水潭)":         "地铁2号线内环",
		"快速直达专线117":                 "快速直达专线117",
		"轨道交通11号线(花桥--唯亭(苏州新区火车站))": "轨道交通11号线（往唯亭(苏州新区火车站)）",
	} {
		if got := lineName(in); got != want {
			t.Errorf("lineName(%q) = %q", in, got)
		}
	}
	for in, want := range map[string]string{
		"餐饮服务;咖啡厅;星巴克咖啡":   "咖啡厅",
		"风景名胜;风景名胜;国家级景点":  "国家级景点",
		"交通设施服务;火车站;火车站|x": "火车站",
		"购物服务": "购物服务",
	} {
		if got := category(in); got != want {
			t.Errorf("category(%q) = %q", in, got)
		}
	}
	if got := address("北京市", "东城区", "北京市东城区东长安街33号"); got != "北京市东城区东长安街33号" {
		t.Errorf("address %q", got)
	}
	if got := placeName("北京", "北京市"); got != "北京市" {
		t.Errorf("placeName %q", got)
	}
	if got := wind("旋转不定", "≤3"); got != "风向不定 ≤3 级" {
		t.Errorf("wind %q", got)
	}
}

func TestDrivingWaypoints(t *testing.T) {
	f := newFake(t)
	a, _ := testApp(t, f)

	// Mix a place name and a WGS-84 "lat,lng" coordinate as waypoints.
	wpCoord := point{39.9080, 116.3900}
	text := mustCall(t, a, "amap_route", `{"from":"天安门","to":"北京南站","waypoints":["西单","39.9080,116.3900"]}`)
	q := f.last("/v5/direction/driving")
	wantWP := "116.374128,39.907439;" + amapLocation(wpCoord)
	if q.Get("waypoints") != wantWP {
		t.Fatalf("waypoints %q, want %q", q.Get("waypoints"), wantWP)
	}
	wantIn(t, text, "驾车 天安门（北京市东城区） → 经 西单（北京市西城区）、39.908000,116.390000 → 北京南站（北京市丰台区）（高德推荐）：")

	// Non-driving modes refuse waypoints cleanly without hitting AMap.
	before := f.count("")
	if _, err := call(t, a, "amap_route", `{"mode":"transit","from":"天安门","to":"北京南站","waypoints":["西单"]}`); err == nil || !strings.Contains(err.Error(), "只支持驾车") {
		t.Fatalf("expected non-driving waypoint refusal, got %v", err)
	}
	if _, err := call(t, a, "amap_route", `{"mode":"walking","from":"天安门","to":"北京南站","waypoints":["西单"]}`); err == nil || !strings.Contains(err.Error(), "只支持驾车") {
		t.Fatalf("expected walking waypoint refusal, got %v", err)
	}
	// More than 16 waypoints refused.
	many := `{"from":"天安门","to":"北京南站","waypoints":[` + strings.Repeat(`"39.91,116.40",`, 16) + `"39.91,116.40"]}`
	if _, err := call(t, a, "amap_route", many); err == nil || !strings.Contains(err.Error(), "最多支持 16") {
		t.Fatalf("expected >16 waypoint refusal, got %v", err)
	}
	if f.count("") != before {
		t.Fatalf("refused waypoint calls still reached AMap")
	}
}

func TestDistance(t *testing.T) {
	f := newFake(t)
	a, _ := testApp(t, f)

	// Sort by distance: origin 2 (西单, 4.2 km) comes before origin 1 (天安门, 9.2 km).
	text := mustCall(t, a, "amap_distance", `{"origins":["天安门","西单"],"destination":"北京南站","mode":"driving","sort":"distance"}`)
	q := f.last("/v3/distance")
	if q.Get("origins") != "116.397755,39.903182|116.374128,39.907439" || q.Get("destination") != "116.378517,39.865246" || q.Get("type") != "1" {
		t.Fatalf("query %v", q)
	}
	first := strings.Index(text, "1. 西单（北京市西城区）：4.2 公里，约 15 分钟")
	second := strings.Index(text, "2. 天安门（北京市东城区）：9.2 公里，约 34 分钟")
	if first < 0 || second < 0 || !(first < second) {
		t.Fatalf("not sorted by distance:\n%s", text)
	}

	// Straight-line mode hides duration and uses type=0.
	text = mustCall(t, a, "amap_distance", `{"origins":["39.9042,116.3912","39.9100,116.3800"],"destination_latitude":39.8634,"destination_longitude":116.3722,"destination":"南站","mode":"straight"}`)
	if q := f.last("/v3/distance"); q.Get("type") != "0" {
		t.Fatalf("query %v", q)
	}
	wantIn(t, text, "高德直线距离测量（到 南站）：", "1. 39.904200,116.391200：9.2 公里")
	if strings.Contains(text, "分钟") {
		t.Fatalf("straight-line should not print duration:\n%s", text)
	}

	// Per-item failure does not fail the whole batch.
	f.queue("/v3/distance", `{"status":"1","info":"OK","infocode":"10000","results":[{"origin_id":"1","dest_id":"1","distance":"0","duration":"0","info":"距离过远","code":"3"}]}`)
	text = mustCall(t, a, "amap_distance", `{"origins":["39.9042,116.3912"],"destination":"39.8634,116.3722","mode":"walking"}`)
	wantIn(t, text, "无法计算（距离过远）")

	// Validation checks.
	if _, err := call(t, a, "amap_distance", `{"destination":"北京南站"}`); err == nil {
		t.Fatal("distance without origins ran")
	}
	if _, err := call(t, a, "amap_distance", `{"origins":["天安门"]}`); err == nil {
		t.Fatal("distance without destination ran")
	}
	if _, err := call(t, a, "amap_distance", `{"origins":["天安门"],"destination":"北京南站","mode":"fly"}`); err == nil {
		t.Fatal("distance with invalid mode ran")
	}
}

func TestDistrict(t *testing.T) {
	f := newFake(t)
	a, _ := testApp(t, f)

	text := mustCall(t, a, "amap_district", `{"keywords":"杭州市","subdistrict":2}`)
	q := f.last("/v3/config/district")
	if q.Get("keywords") != "杭州市" || q.Get("subdistrict") != "2" || q.Get("extensions") != "base" {
		t.Fatalf("query %v", q)
	}
	centerWGS := toWGS(point{30.245853, 120.209947}).String()
	wantIn(t, text,
		"杭州市（市） · adcode 330100 · 区号 0571 · 中心 "+centerWGS,
		"- 西湖区（区县） · adcode 330106",
		"下辖：北山街道(330106)",
		"- 上城区（区县） · adcode 330102",
	)

	// Empty keywords queries top-level China districts.
	_ = mustCall(t, a, "amap_district", `{}`)
	if q := f.last("/v3/config/district"); q.Get("keywords") != "" || q.Get("subdistrict") != "1" {
		t.Fatalf("query %v", q)
	}
}

func TestInputTips(t *testing.T) {
	f := newFake(t)
	a, _ := testApp(t, f)

	text := mustCall(t, a, "amap_input_tips", `{"keywords":"天安门","city":"北京","city_only":true,"latitude":39.9055,"longitude":116.3976,"limit":2}`)
	q := f.last("/v3/assistant/inputtips")
	if q.Get("keywords") != "天安门" || q.Get("city") != "北京" || q.Get("citylimit") != "true" || q.Get("location") != amapLocation(point{39.9055, 116.3976}) {
		t.Fatalf("query %v", q)
	}
	at := toWGS(point{39.903182, 116.397755}).String()
	wantIn(t, text, "1. 天安门广场 · 北京市东城区 · 东长安街 · "+at+" · adcode 110101", "2. 天安门东(地铁站)")
	if strings.Contains(text, "天安门地区") {
		t.Fatalf("limit 2 should omit 3rd tip:\n%s", text)
	}
	if _, err := call(t, a, "amap_input_tips", `{}`); err == nil {
		t.Fatal("input_tips without keywords ran")
	}
}

func TestStaticMap(t *testing.T) {
	f := newFake(t)
	a, _ := testApp(t, f)

	res := mustCallResult(t, a, "amap_static_map", `{"markers":["天安门","北京南站"],"path":["天安门","西单","北京南站"],"width":640,"height":480,"traffic":true}`)
	q := f.last("/v3/staticmap")
	if q.Get("size") != "640*480" || q.Get("scale") != "2" || q.Get("traffic") != "1" || q.Get("zoom") != "" {
		t.Fatalf("query %v", q)
	}
	if want := "mid,0xE53935,A:116.397755,39.903182|mid,0xE53935,B:116.378517,39.865246"; q.Get("markers") != want {
		t.Fatalf("markers %q, want %q", q.Get("markers"), want)
	}
	if want := "5,0x1976D2,1,,:116.397755,39.903182;116.374128,39.907439;116.378517,39.865246"; q.Get("paths") != want {
		t.Fatalf("paths %q, want %q", q.Get("paths"), want)
	}
	wantIn(t, res.Text, "高德静态地图（640×480，含实时路况）：", "- A. 天安门（北京市东城区）", "- B. 北京南站（北京市丰台区）", "连线：天安门（北京市东城区） → 西单（北京市西城区） → 北京南站（北京市丰台区）")
	if len(res.Images) != 1 || res.Images[0].Mime != "image/png" || res.Images[0].Caption == "" {
		t.Fatalf("images %+v", res.Images)
	}
	rawImg, err := base64.StdEncoding.DecodeString(res.Images[0].Data)
	if err != nil || string(rawImg) != string(tinyPNG) {
		t.Fatalf("decoded image mismatch: err=%v len=%d", err, len(rawImg))
	}
	if err := wire.ValidateJSON("ConnectorCallResult", mustJSON(t, res)); err != nil {
		t.Fatalf("ConnectorCallResult schema: %v", err)
	}

	// Center-only request defaults zoom to 14.
	res = mustCallResult(t, a, "amap_static_map", `{"latitude":39.9042,"longitude":116.3912}`)
	if q := f.last("/v3/staticmap"); q.Get("zoom") != "14" || q.Get("location") != amapLocation(point{39.9042, 116.3912}) {
		t.Fatalf("query %v", q)
	}

	// If scale=2 exceeds 1 MiB, falls back to scale=1.
	bigPNG := append(append([]byte(nil), tinyPNG...), make([]byte, staticMapMaxBytes+10)...)
	f.queue("/v3/staticmap", string(bigPNG), string(tinyPNG))
	res = mustCallResult(t, a, "amap_static_map", `{"place":"天安门","zoom":15}`)
	if q := f.last("/v3/staticmap"); q.Get("scale") != "1" {
		t.Fatalf("expected scale=1 fallback, got %v", q)
	}
	if len(res.Images) != 1 {
		t.Fatalf("expected fallback image, got %+v", res.Images)
	}

	// JSON error from /v3/staticmap is parsed and handled (including key/quota errors).
	f.queue("/v3/staticmap", refusal("10001", "INVALID_USER_KEY"))
	if _, err := callResult(t, a, "amap_static_map", `{"latitude":31.23,"longitude":121.47,"zoom":12}`); err == nil || !strings.Contains(err.Error(), "不对或已经过期") {
		t.Fatalf("expected key error from staticmap JSON refusal, got %v", err)
	}

	// Validation checks.
	if _, err := callResult(t, a, "amap_static_map", `{}`); err == nil {
		t.Fatal("static_map with no location/markers/path ran")
	}
	if _, err := callResult(t, a, "amap_static_map", `{"path":["天安门"]}`); err == nil || !strings.Contains(err.Error(), "至少要给 2 个点") {
		t.Fatalf("expected 1-point path error, got %v", err)
	}
}

func TestParseLatLng(t *testing.T) {
	if p, ok := parseLatLng("39.9042, 116.3912"); !ok || p.Lat != 39.9042 || p.Lng != 116.3912 {
		t.Fatalf("parseLatLng valid failed: %+v %v", p, ok)
	}
	for _, bad := range []string{"", "天安门", "0,0", "91,116", "39,181", "39.9,abc", "39.9"} {
		if _, ok := parseLatLng(bad); ok {
			t.Errorf("parseLatLng(%q) unexpectedly succeeded", bad)
		}
	}
}

func TestFixedPlacesInTools(t *testing.T) {
	f := newFake(t)
	a, _ := testApp(t, f)

	// Empty amap_places points to Settings, in-app page, and Persona list_places without calling AMap.
	emptyRes := mustCall(t, a, "amap_places", `{}`)
	wantIn(t, emptyRes, "还没有配置常用地点", "设置 › 扩展 › 高德地图", "常用地点", "list_places")
	if f.count("") != 0 {
		t.Fatalf("empty amap_places made %d AMap requests", f.count(""))
	}

	dir := t.TempDir()
	a.initPlaces(placeConfig{
		Home:    "天安门广场 39.9032,116.3978",
		Company: "北京南站",
		Places:  []string{"西湖边: 杭州"},
	}, dir)
	if _, _, err := a.saveUIPlace("西单集合点", "西单"); err != nil {
		t.Fatal(err)
	}

	// Listing and inspecting places uses 0 AMap requests.
	listText := mustCall(t, a, "amap_places", `{}`)
	wantIn(t, listText, "共 4 个", "1. 家", "2. 公司 · 北京南站", "3. 西湖边 · 杭州", "4. 西单集合点 · 西单 · 来源：页面保存")
	aliasText := mustCall(t, a, "amap_places", `{"name":"work"}`)
	wantIn(t, aliasText, "高德地图常用地点「公司」", "来源：扩展设置", "work")
	missText := mustCall(t, a, "amap_places", `{"name":"老家"}`)
	wantIn(t, missText, "没有「老家」", "家", "公司", "list_places")
	if f.count("") != 0 {
		t.Fatalf("amap_places queries made %d AMap requests", f.count(""))
	}

	// Route directly using configured places and aliases ("回家" / "home" -> "work"):
	// Pinned "家" requires 0 location lookups; "公司" is searched globally without needing home's city.
	beforeSearch := f.count("/v5/place/text")
	text := mustCall(t, a, "amap_route", `{"from":"home","to":"work"}`)
	if f.count("/v5/place/text") != beforeSearch+1 {
		t.Fatalf("expected 1 search (for company) and 0 for pinned home, got %d", f.count("/v5/place/text")-beforeSearch)
	}
	if q := f.last("/v5/place/text"); q.Get("keywords") != "北京南站" || q.Get("region") != "" {
		t.Fatalf("company fixed search query = %v", q)
	}
	if q := f.last("/v5/direction/driving"); q.Get("origin") != amapLocation(point{39.9032, 116.3978}) {
		t.Fatalf("driving origin = %q", q.Get("origin"))
	}
	wantIn(t, text, "驾车 家（天安门广场） → 公司（北京市丰台区）")

	// When an unconfigured origin goes to a pinned fixed destination ("天安门" -> "家"),
	// destination resolves first and supplies the city hint (010) to the unconfigured origin.
	text = mustCall(t, a, "amap_route", `{"from":"天安门","to":"我家"}`)
	if q := f.last("/v5/place/text"); q.Get("keywords") != "天安门" || q.Get("region") != "010" {
		t.Fatalf("origin should get destination city hint 010: %v", q)
	}
	wantIn(t, text, "驾车 天安门（北京市东城区） → 家（天安门广场）")

	// Nearby around fixed place ("家") uses pinned WGS-84 coordinates directly.
	beforeSearch = f.count("/v5/place/text")
	text = mustCall(t, a, "amap_nearby", `{"place":"家里","keywords":"咖啡"}`)
	if f.count("/v5/place/text") != beforeSearch {
		t.Fatalf("nearby on pinned place should not call text search")
	}
	if q := f.last("/v5/place/around"); q.Get("location") != amapLocation(point{39.9032, 116.3978}) {
		t.Fatalf("around location = %q", q.Get("location"))
	}
	wantIn(t, text, "家（天安门广场） 周围 1 公里 内找「咖啡」")

	// Distance batch accepts fixed places in origins and destination.
	text = mustCall(t, a, "amap_distance", `{"origins":["家","西单集合点"],"destination":"company","sort":"distance"}`)
	wantIn(t, text, "高德驾车距离测量（到 公司（北京市丰台区））", "1. 西单集合点（北京市西城区）", "2. 家（天安门广场）")

	// Static map accepts fixed places in place, markers, and path.
	res := mustCallResult(t, a, "amap_static_map", `{"place":"家","markers":["家","公司"],"path":["home","西单集合点","company"]}`)
	wantIn(t, res.Text, "中心：家（天安门广场）", "- A. 家（天安门广场）", "- B. 公司（北京市丰台区）", "连线：家（天安门广场） → 西单集合点（北京市西城区） → 公司（北京市丰台区）")
	if len(res.Images) != 1 {
		t.Fatalf("expected static map image for fixed places")
	}

	// Weather on a fixed place with coords uses reverse geocoding to get district adcode;
	// fixed place with city name resolves its target globally ("杭州" -> 330100).
	text = mustCall(t, a, "amap_weather", `{"city":"我家","days":1}`)
	if q := f.last("/v3/weather/weatherInfo"); q.Get("city") != "110101" {
		t.Fatalf("weather adcode from home coords = %q, want 110101", q.Get("city"))
	}
	wantIn(t, text, "家（北京东城区） 天气（中国气象局，经高德）：")

	text = mustCall(t, a, "amap_weather", `{"city":"西湖边","days":0}`)
	if q := f.last("/v3/weather/weatherInfo"); q.Get("city") != "330100" {
		t.Fatalf("weather adcode from 西湖边 = %q, want 330100", q.Get("city"))
	}
	wantIn(t, text, "西湖边（")
}
