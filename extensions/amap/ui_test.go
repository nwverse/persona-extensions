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

	// Initial render makes zero AMap calls.
	v, err := a.renderPage(context.Background(), &ext.UIRequest{})
	if err != nil {
		t.Fatal(err)
	}
	checkValidView(t, v, false)
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
