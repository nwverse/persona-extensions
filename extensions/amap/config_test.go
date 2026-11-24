package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nwverse/persona/sdk/go/wire"
)

func TestParseTargetAndPlaceEntry(t *testing.T) {
	if p, isCoord, err := parseCoordOnly("39.9042， 116.3912"); err != nil || !isCoord || p.Lat != 39.9042 || p.Lng != 116.3912 {
		t.Fatalf("parseCoordOnly Chinese comma: %+v %v %v", p, isCoord, err)
	}
	for _, bad := range []string{"0,0", "91,116.4", "39.9,-181"} {
		if _, isCoord, err := parseCoordOnly(bad); !isCoord || err == nil {
			t.Errorf("parseCoordOnly(%q): expected invalid coord error, got isCoord=%v err=%v", bad, isCoord, err)
		}
	}

	target, pt, hasCoord, err := parseTarget("朝阳门 39.924,116.433")
	if err != nil || !hasCoord || target != "朝阳门" || pt.Lat != 39.924 || pt.Lng != 116.433 {
		t.Fatalf("parseTarget pinned: target=%q pt=%+v hasCoord=%v err=%v", target, pt, hasCoord, err)
	}
	if _, _, _, err := parseTarget("朝阳门 95,116.4"); err == nil {
		t.Fatal("parseTarget accepted out-of-range pinned coordinate")
	}

	validEntries := []struct {
		raw      string
		wantName string
		wantTgt  string
		wantCrd  bool
	}{
		{"父母家: 天安门", "父母家", "天安门", false},
		{"父母家：北京市东城区天安门广场1号", "父母家", "北京市东城区天安门广场1号", false},
		{"外滩 31.2335,121.4905", "外滩", "", true},
		{"老地方: 西单 39.9074，116.3741", "老地方", "西单", true},
		{"北京南站", "北京南站", "北京南站", false},
	}
	for _, tc := range validEntries {
		fp, err := parsePlaceEntry(tc.raw)
		if err != nil {
			t.Fatalf("parsePlaceEntry(%q): %v", tc.raw, err)
		}
		if fp.Name != tc.wantName || fp.Target != tc.wantTgt || fp.HasCoord != tc.wantCrd {
			t.Errorf("parsePlaceEntry(%q) = %+v", tc.raw, fp)
		}
	}

	invalidEntries := []string{
		"",
		"   ",
		": 天安门",
		"父母家:",
		"父母家：",
		"39.9042,116.3912",
		"39.9042,116.3912: 家",
		"坏点: 95,116.4",
	}
	for _, raw := range invalidEntries {
		if fp, err := parsePlaceEntry(raw); err == nil {
			t.Errorf("parsePlaceEntry(%q) unexpectedly succeeded: %+v", raw, fp)
		}
	}
}

func TestConfigPrecedenceAliasesAndUIStore(t *testing.T) {
	dir := t.TempDir()
	a := newApp()
	a.logf = t.Logf
	a.probe = false
	a.initPlaces(placeConfig{
		Home:    "天安门广场 39.9032,116.3978",
		Company: "北京南站",
		Places:  []string{"西单商圈: 西单"},
	}, dir)

	// Canonical home/company slots owned by Extension Settings cannot be duplicated or shadowed from UI.
	for _, blocked := range []string{"家", "home", "我家", "家里", "my home", "公司", "work", "office", "单位", "西单商圈"} {
		if _, _, err := a.saveUIPlace(blocked, "天安门"); err == nil {
			t.Errorf("saveUIPlace(%q) should have been refused when owned by Settings", blocked)
		}
	}

	// Saving a custom UI place persists to places.json with 0600 permissions.
	fp, updated, err := a.saveUIPlace("机场", "40.0799,116.6031")
	if err != nil || updated || !fp.HasCoord || fp.Source != "ui" {
		t.Fatalf("saveUIPlace insert: fp=%+v updated=%v err=%v", fp, updated, err)
	}
	info, err := os.Stat(filepath.Join(dir, "places.json"))
	if err != nil {
		t.Fatalf("places.json missing: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("places.json perm = %o, want 0600", perm)
	}

	// Update existing UI place.
	fp2, updated, err := a.saveUIPlace("机场", "北京首都国际机场 40.0799,116.6031")
	if err != nil || !updated || fp2.Target != "北京首都国际机场" {
		t.Fatalf("saveUIPlace update: fp=%+v updated=%v err=%v", fp2, updated, err)
	}

	// Alias lookups match case-insensitively and canonical slots pair cleanly.
	for query, wantName := range map[string]string{
		"HOME": "家",
		"我家":   "家",
		"Work": "公司",
		"单位":   "公司",
		"西单商圈": "西单商圈",
		"机场":   "机场",
	} {
		got, ok := a.matchFixedPlace(query)
		if !ok || got.Name != wantName {
			t.Errorf("matchFixedPlace(%q) = (%+v, %v), want %q", query, got, ok, wantName)
		}
	}
	h, c, ok := a.findCommutePair()
	if !ok || h.Name != "家" || c.Name != "公司" {
		t.Fatalf("findCommutePair = (%+v, %+v, %v)", h, c, ok)
	}

	// Simulate an older places.json that also contained "home" before Settings added Home: Settings wins.
	if err := os.WriteFile(filepath.Join(dir, "places.json"), mustJSON(t, storedPlaces{Places: []fixedPlace{
		{Name: "home", Target: "旧家", HasCoord: true, Lat: 31.23, Lng: 121.47},
		{Name: "机场", Target: "北京首都国际机场", HasCoord: true, Lat: 40.0799, Lng: 116.6031},
	}}), 0o600); err != nil {
		t.Fatal(err)
	}
	b := newApp()
	b.logf = t.Logf
	b.probe = false
	b.initPlaces(placeConfig{
		Home:   "天安门广场 39.9032,116.3978",
		Places: []string{"西单商圈: 西单"},
	}, dir)
	all := b.fixedPlaces()
	if len(all) != 3 {
		t.Fatalf("expected 3 places (Settings home + 西单商圈 + UI 机场), got %+v", all)
	}
	if all[0].Name != "家" || all[0].Source != "config" {
		t.Fatalf("expected Settings home first, got %+v", all[0])
	}
	if all[2].Name != "机场" || all[2].Source != "ui" {
		t.Fatalf("expected UI 机场 after Settings places, got %+v", all[2])
	}

	// Clearing Settings later allows the fallback UI place to surface again.
	b.initPlaces(placeConfig{}, dir)
	if got, ok := b.matchFixedPlace("家"); !ok || got.Source != "ui" || got.Name != "home" {
		t.Fatalf("after clearing Settings home, expected persisted UI home: %+v %v", got, ok)
	}

	// Removing UI place updates places.json, while config places cannot be removed by removeUIPlace.
	if !b.removeUIPlace("机场") {
		t.Fatal("removeUIPlace failed for UI entry")
	}
	if b.removeUIPlace("不存在") {
		t.Fatal("removeUIPlace returned true for missing entry")
	}
	reloaded := newApp()
	reloaded.initPlaces(placeConfig{}, dir)
	if _, ok := reloaded.matchFixedPlace("机场"); ok {
		t.Fatal("deleted UI place returned after reload")
	}

	// Enforce strict uniqueness on config validation.
	if _, err := parseConfigPlaces(placeConfig{
		Home:   "天安门",
		Places: []string{"家: 西单"},
	}, true, nil); err == nil || !strings.Contains(err.Error(), "重名") {
		t.Fatalf("expected duplicate name error, got %v", err)
	}
}

func TestSettingsCheckValidatesConfigurationOffline(t *testing.T) {
	f := newFake(t)
	a := &app{logf: t.Logf, tune: tuneFor(f, newClock())}
	h := startWithKey(t, a, "")

	callCheck := func(cfg any, key string) (bool, string) {
		t.Helper()
		var res struct {
			OK    bool   `json:"ok"`
			Error string `json:"error"`
		}
		if err := h.Call("settings.check", map[string]any{
			"config":  cfg,
			"secrets": map[string]string{keySecret: key},
		}, &res); err != nil {
			t.Fatal(err)
		}
		return res.OK, res.Error
	}

	badConfigs := []struct {
		cfg  any
		want string
	}{
		{map[string]any{"home": "95,116"}, "「家」"},
		{map[string]any{"company": "0,0"}, "「公司」"},
		{map[string]any{"places": []string{"39.9042,116.3912"}}, "没有名称"},
		{map[string]any{"places": []string{""}}, "空行"},
		{map[string]any{"home": "天安门", "places": []string{"家: 西单"}}, "重名"},
		{map[string]any{"home": 123}, "读不懂"},
	}
	for _, tc := range badConfigs {
		ok, msg := callCheck(tc.cfg, testKey)
		if ok || !strings.Contains(msg, tc.want) {
			t.Errorf("cfg %#v: ok=%v msg=%q want %q", tc.cfg, ok, msg, tc.want)
		}
	}
	if f.count("") != 0 {
		t.Fatalf("invalid config still called AMap %d times", f.count(""))
	}

	// Valid configuration with empty key succeeds cleanly without calling AMap.
	ok, msg := callCheck(map[string]any{
		"home":    "39.9032,116.3978",
		"company": "北京南站",
		"places":  []string{"机场: 40.0799,116.6031"},
	}, "")
	if !ok {
		t.Fatalf("valid config without key refused: %s", msg)
	}
	if f.count("") != 0 {
		t.Fatalf("empty-key check with valid config called AMap")
	}

	// Valid configuration + valid key still probes AMap.
	ok, msg = callCheck(map[string]any{"home": "天安门"}, testKey)
	if !ok || f.count("/v3/geocode/geo") != 1 {
		t.Fatalf("valid config + key: ok=%v msg=%q hits=%d", ok, msg, f.count("/v3/geocode/geo"))
	}
}

func TestConfigPassedViaInitialize(t *testing.T) {
	f := newFake(t)
	a := &app{logf: t.Logf, tune: tuneFor(f, newClock())}
	cfgJSON := json.RawMessage(`{"home":"天安门 39.9032,116.3978","company":"北京南站","places":["父母家: 西单"]}`)
	h := exttestStartWithConfig(t, a, testKey, cfgJSON)

	var res wire.ConnectorCallResult
	if err := h.Call("connector.tools.call", wire.ConnectorCallParams{
		Contribution: connectorID,
		Name:         "amap_places",
		Args:         json.RawMessage(`{}`),
	}, &res); err != nil {
		t.Fatal(err)
	}
	wantIn(t, res.Text, "共 3 个", "家 · 天安门 · WGS-84 39.903200,116.397800 · 来源：扩展设置", "公司 · 北京南站 · 来源：扩展设置", "父母家 · 西单 · 来源：扩展设置")
	if f.count("") != 0 {
		t.Fatalf("amap_places should spend 0 AMap calls, got %d", f.count(""))
	}
}
