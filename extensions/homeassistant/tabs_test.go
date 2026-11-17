package main

import (
	"slices"
	"strings"
	"testing"
)

// Room tabs, the scenes row and 全关 (#634).
func TestRoomTabsScenesAndAllOff(t *testing.T) {
	r := newRichHA(t)
	r.put(State{EntityID: "scene.movie", State: "2026-01-02T03:04:05+00:00", Attributes: map[string]any{"friendly_name": "客厅观影"}})
	r.areas = areasJSON([][3]any{
		{"living", "客厅", []string{"light.living", "light.ceiling", auxSwitch, "switch.ceiling_power_default", "switch.living_plug",
			"switch.living_hidden", speaker, "scene.movie"}},
		{"bedroom", "卧室", []string{"light.bed"}},
		{"empty", "储物间", []string{}}})
	a := richApp(t, r, Config{})
	v, got := renderV(t, a, 6, nil)

	// the rooms with devices are tabs, the empty one is not
	contains(t, got, `[tabs tab="home" home=全屋 room:living=客厅 room:bedroom=卧室 log=动态 auto=自动化]`)
	// 全关 counts what it turns off in the whole home: the lamps (the one in no area too) and the plug, not the lamp's
	// own switch nor a hidden one
	row := tilesOf(v, "scenes")
	if len(row) != 2 || row[0].Title != "全关" || row[0].Subtitle != "4 个开着" || !strings.Contains(row[0].Action.Confirm, "全屋") ||
		row[1].Title != "观影" || !strings.HasPrefix(row[1].Action.ID, "do:scene.movie|turn_on") || row[1].Action.Confirm == "" {
		t.Errorf("the scenes row: %+v", row)
	}

	// a room's tab opens the room, its row its own 全关
	res, st := actV(t, a, 6, pageState{}, "tab", map[string]string{"tab": "room:living"})
	if st.Area != "living" || res.View.Tabs.Value != "room:living" {
		t.Fatalf("the room's tab: %+v, tab %q", st, res.View.Tabs.Value)
	}
	room := tilesOf(res.View, "scenes")
	if len(room) == 0 || room[0].Title != "全关" || room[0].Action.Values["area"] != "living" || !strings.Contains(room[0].Action.Confirm, "客厅") {
		t.Errorf("the room's row: %+v", room)
	}

	// 全关 in the living room: one call per domain, only what the row counted
	r.mu.Lock()
	r.calls = nil
	r.mu.Unlock()
	res, _ = actV(t, a, 6, st, "alloff", map[string]string{"area": "living"})
	r.mu.Lock()
	calls := slices.Clone(r.calls)
	r.mu.Unlock()
	if !slices.Equal(calls, []string{"light.turn_off [light.living light.ceiling] {}", "switch.turn_off [switch.living_plug] {}"}) || res.Toast != "关掉了 3 个" {
		t.Errorf("全关: %q, toast %q", calls, res.Toast)
	}
	// an area the page does not know: nothing
	r.mu.Lock()
	r.calls = nil
	r.mu.Unlock()
	if res, _ = actV(t, a, 6, st, "alloff", map[string]string{"area": "nowhere"}); r.lastCall() != "" || res.Toast != errNoEntity.Error() {
		t.Errorf("全关 somewhere else: %q %q", r.lastCall(), res.Toast)
	}

	// more rooms than the tab bar takes: the old tabs and the area filter
	var many [][3]any
	for _, n := range []string{"a", "b", "c", "d", "e", "f"} {
		r.put(State{EntityID: "light.room_" + n, State: "off", Attributes: map[string]any{"friendly_name": "房间" + n + "灯"}})
		many = append(many, [3]any{n, "房间" + n, []string{"light.room_" + n}})
	}
	r.areas = areasJSON(many)
	b := richApp(t, r, Config{})
	v, got = renderV(t, b, 6, nil)
	contains(t, got, `[tabs tab="home" home=家 log=动态 auto=自动化]`)
	if v.Filter == nil {
		t.Errorf("no area filter without room tabs:\n%s", got)
	}
}
