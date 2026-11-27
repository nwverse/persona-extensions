package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/nwverse/persona/sdk/go/ext"
	"github.com/nwverse/persona/sdk/go/exttest"
	"github.com/nwverse/persona/sdk/go/wire"
)

func checkValidView(t *testing.T, v *wire.View, compact bool) {
	t.Helper()
	if err := exttest.CheckView(mustJSON(t, v), compact); err != nil {
		t.Fatalf("invalid view (compact=%v): %v\n%s", compact, err, mustJSON(t, v))
	}
}

func TestOverviewCard(t *testing.T) {
	f := newFake(t)

	// Unconfigured key renders an empty state without touching AMap.
	unconf := newApp()
	unconf.probe = false
	v, err := unconf.renderCard(context.Background(), &ext.UIRequest{})
	if err != nil {
		t.Fatal(err)
	}
	checkValidView(t, v, true)
	if v.Sections[0].Items[0].Type != wire.ComponentEmpty {
		t.Fatalf("expected empty state, got %+v", v.Sections)
	}

	// Configured key shows monthly usage stats and zero network calls on render.
	a, _ := testApp(t, f)
	mustCall(t, a, "amap_search", `{"keywords":"北京南站"}`)
	before := f.count("")
	v, err = a.renderCard(context.Background(), &ext.UIRequest{})
	if err != nil {
		t.Fatal(err)
	}
	checkValidView(t, v, true)
	if f.count("") != before {
		t.Fatalf("overview card made network requests")
	}
	if len(v.Sections) != 1 || v.Sections[0].Items[0].Type != wire.ComponentStatGrid || v.Sections[0].Items[0].Stats[0].Value != "1 次" {
		t.Fatalf("unexpected card view: %+v", v.Sections)
	}

	// Paused quota group surfaces a warning row on the card.
	f.queue("/v5/place/text", refusal("10003", "DAILY_QUERY_OVER_LIMIT"))
	_, _ = call(t, a, "amap_search", `{"keywords":"火锅"}`)
	v, err = a.renderCard(context.Background(), &ext.UIRequest{})
	if err != nil {
		t.Fatal(err)
	}
	checkValidView(t, v, true)
	if len(v.Sections[0].Items) < 2 || v.Sections[0].Items[1].Badge.Tone != wire.ToneCaution {
		t.Fatalf("expected pause warning on card: %+v", v.Sections[0].Items)
	}
}

func TestUISearchAndOpenPOIImage(t *testing.T) {
	f := newFake(t)
	a, _ := testApp(t, f)

	// Initial render makes zero AMap calls and includes the 5 expected tabs.
	v, err := a.renderPage(context.Background(), &ext.UIRequest{})
	if err != nil {
		t.Fatal(err)
	}
	checkValidView(t, v, false)
	if v.Tabs == nil || len(v.Tabs.Items) != 5 {
		t.Fatalf("expected 5 tabs, got %+v", v.Tabs)
	}
	if f.count("") != 0 {
		t.Fatalf("initial render made %d requests", f.count(""))
	}

	// Search action lists POIs and embeds a static map preview via ui.image ref.
	res, err := a.actionPage(context.Background(), &ext.UIRequest{UIParams: wire.UIParams{
		Action: "search",
		Values: map[string]string{"keywords": "北京南站", "city": "北京", "city_only": "true"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	checkValidView(t, res.View, false)
	refs := wire.ImageRefs(res.View)
	if len(refs) != 1 || !strings.HasPrefix(refs[0], "map:") {
		t.Fatalf("expected 1 map ref, got %v", refs)
	}

	// ui.image serves a valid PNG for the ref and passes wire.CheckImage.
	img, err := a.imagePage(context.Background(), &ext.ImageRequest{UIImageParams: wire.UIImageParams{
		Contribution: pageUI, Ref: refs[0], Width: 1280,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := wire.CheckImage(img); err != nil {
		t.Fatalf("CheckImage: %v", err)
	}

	// Tapping a POI row jumps to the map tab with zoom=15 and serves its map image.
	var poiAction *wire.Action
	for _, it := range res.View.Sections[1].Items {
		if it.Type == wire.ComponentRow && it.Action != nil && it.Action.ID == "open_poi" {
			poiAction = it.Action
			break
		}
	}
	if poiAction == nil {
		t.Fatal("expected open_poi action on search result row")
	}
	mapRes, err := a.actionPage(context.Background(), &ext.UIRequest{UIParams: wire.UIParams{
		Action: poiAction.ID, State: res.View.State, Values: poiAction.Values,
	}})
	if err != nil {
		t.Fatal(err)
	}
	checkValidView(t, mapRes.View, false)
	if mapRes.View.Tabs.Value != tabMap {
		t.Fatalf("expected map tab, got %q", mapRes.View.Tabs.Value)
	}

	// Zoom in and toggle traffic on the map tab.
	zoomed, err := a.actionPage(context.Background(), &ext.UIRequest{UIParams: wire.UIParams{
		Action: "zoom_in", State: mapRes.View.State,
	}})
	if err != nil {
		t.Fatal(err)
	}
	checkValidView(t, zoomed.View, false)
	toggled, err := a.actionPage(context.Background(), &ext.UIRequest{UIParams: wire.UIParams{
		Action: "toggle_traffic", State: zoomed.View.State,
	}})
	if err != nil {
		t.Fatal(err)
	}
	checkValidView(t, toggled.View, false)
	if !strings.Contains(string(mustJSON(t, toggled.View)), "含实时路况") {
		t.Fatalf("expected traffic label after toggle")
	}

	// Unknown image ref returns not_found.
	if _, err := a.imagePage(context.Background(), &ext.ImageRequest{UIImageParams: wire.UIImageParams{
		Contribution: pageUI, Ref: "map:deadbeef",
	}}); err == nil {
		t.Fatal("expected error for unknown ref")
	}
}

func TestUISearchRendersPOICostWithoutPanic(t *testing.T) {
	f := newFake(t)
	a, _ := testApp(t, f)
	// Queue recorded coffee fixture whose POIs contain business.cost ("59.00", "107.00").
	f.queue("/v5/place/text", fixture(t, "around-coffee"))

	res, err := a.actionPage(context.Background(), &ext.UIRequest{UIParams: wire.UIParams{
		Action: "search",
		Values: map[string]string{"keywords": "咖啡", "city": "北京"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	checkValidView(t, res.View, false)
	wantIn(t, string(mustJSON(t, res.View)), "¥59/人", "¥107/人")
}

func TestUIRoutePreviewKeepsDestinationMarkerBeyondLimit(t *testing.T) {
	f := newFake(t)
	a, _ := testApp(t, f)

	// Start + 10 waypoints + destination = 12 stops (> staticMapMarkersMost = 10).
	wps := []string{
		"39.910,116.380", "39.911,116.381", "39.912,116.382", "39.913,116.383", "39.914,116.384",
		"39.915,116.385", "39.916,116.386", "39.917,116.387", "39.918,116.388", "39.919,116.389",
	}
	res, err := a.actionPage(context.Background(), &ext.UIRequest{UIParams: wire.UIParams{
		Action: "route",
		Values: map[string]string{
			"from":      "39.900,116.370",
			"to":        "39.930,116.410",
			"waypoints": strings.Join(wps, ";"),
			"mode":      "driving",
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	checkValidView(t, res.View, false)
	refs := wire.ImageRefs(res.View)
	if len(refs) != 1 {
		t.Fatalf("expected 1 route map ref, got %v", refs)
	}
	if _, err := a.imagePage(context.Background(), &ext.ImageRequest{UIImageParams: wire.UIImageParams{
		Contribution: pageUI, Ref: refs[0], Width: 640,
	}}); err != nil {
		t.Fatal(err)
	}
	q := f.last("/v3/staticmap")
	markers := strings.Split(q.Get("markers"), "|")
	if len(markers) != staticMapMarkersMost {
		t.Fatalf("expected %d markers, got %d: %v", staticMapMarkersMost, len(markers), markers)
	}
	if !strings.HasPrefix(markers[0], "mid,0x2E7D32,A:") {
		t.Fatalf("expected green A start marker, got %q", markers[0])
	}
	wantDest := "mid,0xE53935,J:" + amapLocation(point{39.930, 116.410})
	if markers[len(markers)-1] != wantDest {
		t.Fatalf("last marker = %q, want red J destination %q", markers[len(markers)-1], wantDest)
	}
	pathStops := strings.Split(strings.TrimPrefix(q.Get("paths"), "5,0x1976D2,1,,:"), ";")
	if len(pathStops) != 12 {
		t.Fatalf("expected all 12 stops on route path, got %d", len(pathStops))
	}
}

func TestUIRouteWithWaypointsAndUsageTab(t *testing.T) {
	f := newFake(t)
	a, _ := testApp(t, f)

	// Plan a driving route with a waypoint in UI.
	res, err := a.actionPage(context.Background(), &ext.UIRequest{UIParams: wire.UIParams{
		Action: "route",
		Values: map[string]string{
			"from":      "天安门",
			"to":        "北京南站",
			"waypoints": "西单",
			"mode":      "driving",
			"prefer":    "recommended",
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	checkValidView(t, res.View, false)
	raw := string(mustJSON(t, res.View))
	wantIn(t, raw, "经 西单（北京市西城区）", "https://uri.amap.com/navigation?")
	refs := wire.ImageRefs(res.View)
	if len(refs) != 1 {
		t.Fatalf("expected route static map preview ref, got %v", refs)
	}
	img, err := a.imagePage(context.Background(), &ext.ImageRequest{UIImageParams: wire.UIImageParams{
		Contribution: pageUI, Ref: refs[0], Width: 640,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := wire.CheckImage(img); err != nil {
		t.Fatalf("CheckImage: %v", err)
	}

	// Weather & usage tab.
	wRes, err := a.actionPage(context.Background(), &ext.UIRequest{UIParams: wire.UIParams{
		Action: "weather",
		Values: map[string]string{"weather_city": "杭州"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	checkValidView(t, wRes.View, false)
	wantIn(t, string(mustJSON(t, wRes.View)), "天气（中国气象局，经高德）", "本月 API 用量")

	// End-to-end through the SDK protocol with a configured key.
	b := &app{logf: t.Logf, tune: tuneFor(f, newClock())}
	h := startWithKey(t, b, testKey)
	var rendered json.RawMessage
	if err := h.Call("ui.render", wire.UIParams{Contribution: pageUI, Placement: wire.PlacementNavPage, Size: wire.SizeFull, UIVersion: 11}, &rendered); err != nil {
		t.Fatal(err)
	}
	if err := exttest.CheckView(rendered, false); err != nil {
		t.Fatal(err)
	}
}

func TestUISavedPlacesAndCommuteFlow(t *testing.T) {
	f := newFake(t)
	a, _ := testApp(t, f)
	dir := t.TempDir()
	// Pre-configure one place from Extension Settings ("西单商圈"), while Home & Company will be added in UI.
	a.initPlaces(placeConfig{Places: []string{"西单商圈: 西单"}}, dir)

	// Switching to 常用地点 tab spends 0 AMap quota and shows Settings entry without a delete action.
	res, err := a.actionPage(context.Background(), &ext.UIRequest{UIParams: wire.UIParams{
		Action: "tab", Values: map[string]string{"tab": tabPlaces},
	}})
	if err != nil {
		t.Fatal(err)
	}
	checkValidView(t, res.View, false)
	if res.View.Tabs.Value != tabPlaces || res.View.Tabs.Items[3].Badge != "1" {
		t.Fatalf("unexpected places tab view: %+v", res.View.Tabs)
	}
	if f.count("") != 0 {
		t.Fatalf("places tab render called AMap %d times", f.count(""))
	}

	// Refused save (name colliding with Settings "西单商圈") keeps draft and renders inline error.
	badRes, err := a.actionPage(context.Background(), &ext.UIRequest{UIParams: wire.UIParams{
		Action: "save_place",
		State:  res.View.State,
		Values: map[string]string{"place_name": "西单商圈", "place_target": "天安门"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	checkValidView(t, badRes.View, false)
	wantIn(t, string(mustJSON(t, badRes.View)), "保存失败", "已在扩展设置里配置")

	// Save Home (pinned WGS-84) and Company in UI -> unlocks one-tap commute section.
	res, err = a.actionPage(context.Background(), &ext.UIRequest{UIParams: wire.UIParams{
		Action: "save_place",
		State:  badRes.View.State,
		Values: map[string]string{"place_name": "家", "place_target": "天安门 39.9032,116.3978"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	checkValidView(t, res.View, false)
	wantIn(t, res.Toast, "已保存常用地点「家」")

	res, err = a.actionPage(context.Background(), &ext.UIRequest{UIParams: wire.UIParams{
		Action: "save_place",
		State:  res.View.State,
		Values: map[string]string{"place_name": "公司", "place_target": "北京南站"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	checkValidView(t, res.View, false)
	if res.View.Tabs.Items[3].Badge != "3" {
		t.Fatalf("expected badge 3, got %q", res.View.Tabs.Items[3].Badge)
	}
	wantIn(t, string(mustJSON(t, res.View)), "一键通勤", "家 → 公司（规划路线）", "公司 → 家（规划路线）", "扩展设置", "页面保存")

	// One-tap commute triggers quick_route into tabRoute with static map preview.
	routeRes, err := a.actionPage(context.Background(), &ext.UIRequest{UIParams: wire.UIParams{
		Action: "quick_route",
		State:  res.View.State,
		Values: map[string]string{"from": "家", "to": "公司"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	checkValidView(t, routeRes.View, false)
	if routeRes.View.Tabs.Value != tabRoute || len(wire.ImageRefs(routeRes.View)) != 1 {
		t.Fatalf("quick_route result invalid: tab=%q refs=%v", routeRes.View.Tabs.Value, wire.ImageRefs(routeRes.View))
	}
	wantIn(t, string(mustJSON(t, routeRes.View)), "驾车 家（天安门） → 公司（北京市丰台区）")

	// Swap route reverses endpoints cleanly.
	swapRes, err := a.actionPage(context.Background(), &ext.UIRequest{UIParams: wire.UIParams{
		Action: "swap_route",
		State:  routeRes.View.State,
		Values: map[string]string{"from": "家", "to": "公司", "mode": "driving", "prefer": "recommended"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	checkValidView(t, swapRes.View, false)
	wantIn(t, string(mustJSON(t, swapRes.View)), "驾车 公司（北京市丰台区） → 家（天安门）")

	// Tapping a UI-saved place row opens it in tabMap, offering shortcuts + destructive Delete button.
	mapRes, err := a.actionPage(context.Background(), &ext.UIRequest{UIParams: wire.UIParams{
		Action: "view_place",
		State:  res.View.State,
		Values: map[string]string{"name": "家"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	checkValidView(t, mapRes.View, false)
	mapJSON := string(mustJSON(t, mapRes.View))
	wantIn(t, mapJSON, "设为算路起点", "设为算路终点", "查当地天气", "删除常用地点")

	// Deleting a UI-saved place removes it and returns to tabPlaces.
	delRes, err := a.actionPage(context.Background(), &ext.UIRequest{UIParams: wire.UIParams{
		Action: "delete_place",
		State:  mapRes.View.State,
		Values: map[string]string{"name": "家"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	checkValidView(t, delRes.View, false)
	if delRes.View.Tabs.Value != tabPlaces || delRes.View.Tabs.Items[3].Badge != "2" {
		t.Fatalf("after delete: tab=%q badge=%q", delRes.View.Tabs.Value, delRes.View.Tabs.Items[3].Badge)
	}
	wantIn(t, delRes.Toast, "已删除常用地点「家」")

	// Searching and saving a POI from open_poi -> prepare_save prefills DraftName & DraftTarget.
	searchRes, err := a.actionPage(context.Background(), &ext.UIRequest{UIParams: wire.UIParams{
		Action: "open_poi",
		State:  delRes.View.State,
		Values: map[string]string{"lat": "39.9032", "lng": "116.3978", "name": "天安门"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	checkValidView(t, searchRes.View, false)
	wantIn(t, string(mustJSON(t, searchRes.View)), "在高德地图中打开 天安门", "存为常用地点")

	prepRes, err := a.actionPage(context.Background(), &ext.UIRequest{UIParams: wire.UIParams{
		Action: "prepare_save",
		State:  searchRes.View.State,
		Values: map[string]string{"name": "天安门", "target": "天安门 39.903200,116.397800"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	checkValidView(t, prepRes.View, false)
	wantIn(t, string(mustJSON(t, prepRes.View)), "天安门 39.903200,116.397800")

	// Quick weather shortcut on fixed place resolves cleanly.
	wRes, err := a.actionPage(context.Background(), &ext.UIRequest{UIParams: wire.UIParams{
		Action: "quick_weather",
		State:  prepRes.View.State,
		Values: map[string]string{"weather_city": "西单商圈"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	checkValidView(t, wRes.View, false)
	wantIn(t, string(mustJSON(t, wRes.View)), "西单商圈（")
}

func findComponentByType(v *wire.View, typ string) *wire.Component {
	for si := range v.Sections {
		for ii := range v.Sections[si].Items {
			if v.Sections[si].Items[ii].Type == typ {
				return &v.Sections[si].Items[ii]
			}
		}
	}
	return nil
}

func TestUIv12InteractiveMapViews(t *testing.T) {
	f := newFake(t)
	a, _ := testApp(t, f)

	// 1. UI v12 search renders interactive ComponentMap with POI open_poi actions; v11 keeps static ui.image.
	v11Res, err := a.actionPage(context.Background(), &ext.UIRequest{UIParams: wire.UIParams{
		Action:    "search",
		UIVersion: 11,
		Values:    map[string]string{"keywords": "北京南站", "city": "北京"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	checkValidView(t, v11Res.View, false)
	if findComponentByType(v11Res.View, wire.ComponentMap) != nil || len(wire.ImageRefs(v11Res.View)) != 1 {
		t.Fatalf("UI v11 search should keep static image fallback, got refs=%v", wire.ImageRefs(v11Res.View))
	}

	sRes, err := a.actionPage(context.Background(), &ext.UIRequest{UIParams: wire.UIParams{
		Action:    "search",
		UIVersion: 12,
		Values:    map[string]string{"keywords": "北京南站", "city": "北京"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	checkValidView(t, sRes.View, false)
	sMap := findComponentByType(sRes.View, wire.ComponentMap)
	if sMap == nil || sMap.Key != "search-map" || !sMap.ShowList || len(sMap.Markers) != 2 {
		t.Fatalf("expected interactive search ComponentMap, got %+v", sMap)
	}
	if sMap.Markers[0].Action == nil || sMap.Markers[0].Action.ID != "open_poi" || sMap.Markers[0].Detail == "" {
		t.Fatalf("expected open_poi action + detail on search marker: %+v", sMap.Markers[0])
	}
	if len(wire.ImageRefs(sRes.View)) != 0 {
		t.Fatalf("UI v12 interactive search should not emit static image ref, got %v", wire.ImageRefs(sRes.View))
	}

	// 2. Tapping open_poi in v12 opens tabMap with centered ComponentMap; toggling traffic falls back to ui.image.
	poiAct := sMap.Markers[0].Action
	mRes, err := a.actionPage(context.Background(), &ext.UIRequest{UIParams: wire.UIParams{
		Action:    poiAct.ID,
		UIVersion: 12,
		State:     sRes.View.State,
		Values:    poiAct.Values,
	}})
	if err != nil {
		t.Fatal(err)
	}
	checkValidView(t, mRes.View, false)
	mMap := findComponentByType(mRes.View, wire.ComponentMap)
	if mMap == nil || mMap.Key != "map-viewer" || mMap.ShowList || mMap.Viewport == nil {
		t.Fatalf("expected centered ComponentMap in tabMap, got %+v", mMap)
	}

	mTraffic, err := a.actionPage(context.Background(), &ext.UIRequest{UIParams: wire.UIParams{
		Action:    "toggle_traffic",
		UIVersion: 12,
		State:     mRes.View.State,
	}})
	if err != nil {
		t.Fatal(err)
	}
	checkValidView(t, mTraffic.View, false)
	if findComponentByType(mTraffic.View, wire.ComponentMap) != nil || len(wire.ImageRefs(mTraffic.View)) != 1 {
		t.Fatalf("traffic enabled should switch to ui.image PNG preview")
	}
	wantIn(t, string(mustJSON(t, mTraffic.View)), "含实时路况")

	mBack, err := a.actionPage(context.Background(), &ext.UIRequest{UIParams: wire.UIParams{
		Action:    "toggle_traffic",
		UIVersion: 12,
		State:     mTraffic.View.State,
	}})
	if err != nil {
		t.Fatal(err)
	}
	checkValidView(t, mBack.View, false)
	if findComponentByType(mBack.View, wire.ComponentMap) == nil {
		t.Fatalf("toggling traffic off should return interactive ComponentMap")
	}

	// 3. UI v12 route tab renders multi-alternative route ComponentMap + traffic toggle + navigation link.
	rRes, err := a.actionPage(context.Background(), &ext.UIRequest{UIParams: wire.UIParams{
		Action:    "route",
		UIVersion: 12,
		Values: map[string]string{
			"from":      "天安门",
			"to":        "北京南站",
			"waypoints": "西单",
			"mode":      "driving",
			"prefer":    "recommended",
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	checkValidView(t, rRes.View, false)
	rMap := findComponentByType(rRes.View, wire.ComponentMap)
	if rMap == nil || rMap.Key != "route-map" || !rMap.ShowList || len(rMap.Routes) != 2 || len(rMap.Routes[0].Points) < 5 {
		t.Fatalf("expected interactive route ComponentMap with 2 decoded routes, got %+v", rMap)
	}
	if len(rMap.Markers) != 3 || len(rMap.Links) != 1 {
		t.Fatalf("expected origin + waypoint + destination and nav link, got markers=%d links=%d", len(rMap.Markers), len(rMap.Links))
	}

	// Traffic toggle on route tab switches to static ui.image, then restores interactive ComponentMap.
	rTraffic, err := a.actionPage(context.Background(), &ext.UIRequest{UIParams: wire.UIParams{
		Action:    "toggle_traffic",
		UIVersion: 12,
		State:     rRes.View.State,
	}})
	if err != nil {
		t.Fatal(err)
	}
	checkValidView(t, rTraffic.View, false)
	if findComponentByType(rTraffic.View, wire.ComponentMap) != nil || len(wire.ImageRefs(rTraffic.View)) != 1 {
		t.Fatalf("route traffic toggle should switch to static image preview")
	}
	rRestore, err := a.actionPage(context.Background(), &ext.UIRequest{UIParams: wire.UIParams{
		Action:    "toggle_traffic",
		UIVersion: 12,
		State:     rTraffic.View.State,
	}})
	if err != nil {
		t.Fatal(err)
	}
	checkValidView(t, rRestore.View, false)
	if findComponentByType(rRestore.View, wire.ComponentMap) == nil {
		t.Fatalf("disabling route traffic should restore interactive ComponentMap")
	}

	// Transit route in UI v12 includes multi-plan routes and transfer stops.
	tRes, err := a.actionPage(context.Background(), &ext.UIRequest{UIParams: wire.UIParams{
		Action:    "route",
		UIVersion: 12,
		Values:    map[string]string{"from": "天安门", "to": "北京南站", "mode": "transit"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	checkValidView(t, tRes.View, false)
	tMap := findComponentByType(tRes.View, wire.ComponentMap)
	if tMap == nil || len(tMap.Routes) != 2 || tMap.Routes[0].Mode != wire.MapTravelModeTransit {
		t.Fatalf("expected interactive transit ComponentMap, got %+v", tMap)
	}

	// End-to-end SDK protocol check at UIVersion 12 and 11.
	b := &app{logf: t.Logf, tune: tuneFor(f, newClock())}
	h := startWithKey(t, b, testKey)
	for _, ver := range []int{11, 12} {
		var actionRes wire.ActionResult
		if err := h.Call("ui.action", wire.UIParams{
			Contribution: pageUI,
			Placement:    wire.PlacementNavPage,
			Size:         wire.SizeFull,
			UIVersion:    ver,
			Action:       "route",
			Values:       map[string]string{"from": "天安门", "to": "北京南站", "mode": "driving"},
		}, &actionRes); err != nil {
			t.Fatalf("ui.action UIVersion %d: %v", ver, err)
		}
		if err := exttest.CheckView(mustJSON(t, actionRes.View), false); err != nil {
			t.Fatalf("CheckView UIVersion %d: %v", ver, err)
		}
	}
}
