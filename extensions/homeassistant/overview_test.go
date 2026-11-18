package main

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/sdk/go/wire"
)

// A Xiaomi Home home as Home Assistant writes it (#631): wall switches whose names start with the device's (and repeat
// it inside), the switch's chip temperature, and an IR air conditioner that is only buttons.
func TestOverviewReadsAXiaomiHome(t *testing.T) {
	const (
		relayL   = "switch.xiaomi_cn_100000003_w2_on_p_2_1"
		relayR   = "switch.xiaomi_cn_100000003_w2_on_p_3_1"
		bedL     = "switch.xiaomi_cn_100000002_w2_on_p_2_1"
		chipTemp = "sensor.xiaomi_cn_100000003_w2_temperature_p_2_7"
		fan      = "switch.xiaomi_cn_100000001_w2_on_p_2_1"
		acOn     = "button.miir_cn_ir_1_ir02_turn_on_a_2_6"
		acOff    = "button.miir_cn_ir_1_ir02_turn_off_a_2_5"
		acUp     = "button.miir_cn_ir_1_ir02_temperature_up_a_2_4"
		acDown   = "button.miir_cn_ir_1_ir02_temperature_down_a_2_3"
		acSpeed  = "button.miir_cn_ir_1_ir02_fan_speed_up_a_2_2"
		acMode   = "select.miir_cn_ir_1_ir02_ir_mode_p_2_1"
	)
	r := newRichHA(t)
	pressed := time.Now().Add(-time.Minute).UTC().Format(time.RFC3339Nano)
	r.put(
		State{EntityID: relayL, State: "on", Attributes: map[string]any{"friendly_name": "小米智能开关（双开） 3  灯光1 开关"}},
		State{EntityID: relayR, State: "off", Attributes: map[string]any{"friendly_name": "小米智能开关（双开） 3  灯光2 开关"}},
		State{EntityID: bedL, State: "off", Attributes: map[string]any{"friendly_name": "小米智能开关（双开） 2  左键-小米智能开关（双开） 2 开关"}},
		State{EntityID: chipTemp, State: "29", Attributes: map[string]any{"friendly_name": "小米智能开关（双开） 3  灯光1 温度",
			"device_class": "temperature", "unit_of_measurement": "°C"}},
		State{EntityID: fan, State: "off", Attributes: map[string]any{"friendly_name": "小米智能开关（双开）  排气扇 开关"}},
		State{EntityID: acOn, State: "unknown", Attributes: map[string]any{"friendly_name": "客厅空调  红外空调控制 开机"}},
		State{EntityID: acOff, State: pressed, Attributes: map[string]any{"friendly_name": "客厅空调  红外空调控制 关机"}},
		State{EntityID: acUp, State: "unknown", Attributes: map[string]any{"friendly_name": "客厅空调  红外空调控制 温度+"}},
		State{EntityID: acDown, State: "unknown", Attributes: map[string]any{"friendly_name": "客厅空调  红外空调控制 温度-"}},
		State{EntityID: acSpeed, State: "unknown", Attributes: map[string]any{"friendly_name": "客厅空调  红外空调控制 风速+"}},
		State{EntityID: acMode, State: "unknown", Attributes: map[string]any{"friendly_name": "客厅空调  红外空调控制 模式"}},
	)
	for _, e := range []struct{ id, dev string }{{relayL, "w3"}, {relayR, "w3"}, {chipTemp, "w3"}, {bedL, "w2"}, {fan, "w1"},
		{acOn, "ac"}, {acOff, "ac"}, {acUp, "ac"}, {acDown, "ac"}, {acSpeed, "ac"}, {acMode, "ac"}} {
		r.ws.entities = append(r.ws.entities, map[string]any{"entity_id": e.id, "device_id": e.dev, "platform": "xiaomi_home"})
	}
	r.ws.devices = append(r.ws.devices, map[string]any{"id": "w3", "name": "小米智能开关（双开） 3"},
		map[string]any{"id": "w2", "name": "小米智能开关（双开） 2"}, map[string]any{"id": "w1", "name": "小米智能开关（双开）"},
		map[string]any{"id": "ac", "name": "客厅空调"})
	r.services = strings.TrimSuffix(r.services, "]") + `,{"domain":"button","services":{"press":{}}}]`
	r.areas = areasJSON([][3]any{{"ke_ting", "客厅", []string{relayL, relayR, chipTemp, acOn, acOff, acUp, acDown, acSpeed, acMode}},
		{"wo_shi", "卧室", []string{bedL}}, {"ce_suo", "厕所", []string{fan}}})
	a := richApp(t, r, Config{})
	v, got := renderV(t, a, 6, nil)

	// named by what each relay adds to its device, a relay for a light or a fan drawn as one; room by room
	contains(t, got, "tile 灯光1|| icon=light/amber active ctl(switch power:"+relayL+" true",
		"tile 灯光2|| icon=light/amber ctl(switch power:"+relayR+" false", "tile 左键|| icon=power/blue ctl(switch power:"+bedL,
		"tile 排气扇|| icon=fan/teal ctl(switch power:"+fan)
	if i := slices.Index(titles(v), "客厅 · 1 开着"); i < 0 || !slices.Equal(titles(v)[i:i+3], []string{"客厅 · 1 开着", "卧室", "厕所"}) {
		t.Errorf("sections %q, want the rooms in Home Assistant's order:\n%s", titles(v), got)
	}
	// the IR air conditioner: a tile in its room that presses, saying what was pressed last
	ac := tilesOf(v, "room:ke_ting")
	i := slices.IndexFunc(ac, func(x wire.Tile) bool { return x.Title == "空调" })
	if i < 0 {
		t.Fatalf("no air conditioner tile:\n%s", got)
	}
	tl := ac[i]
	var acts []string
	for _, x := range tl.Actions {
		acts = append(acts, x.Title+"="+x.Action.ID)
	}
	if tl.Icon != "snow" || !strings.HasPrefix(tl.Subtitle, "遥控 · ") || !strings.HasSuffix(tl.Subtitle, " 关机") || tl.Action.ID != "open:"+acOn ||
		!slices.Equal(acts, []string{"开机=do:" + acOn + "|press", "关机=do:" + acOff + "|press", "温度+=do:" + acUp + "|press", "温度-=do:" + acDown + "|press"}) {
		t.Errorf("the air conditioner's tile: %+v %q", tl, acts)
	}
	// a wall switch's chip is not the room's temperature
	for _, no := range []string{"29°", "温度=", "小米智能开关"} {
		if strings.Contains(got, no) {
			t.Errorf("%s on the overview:\n%s", no, got)
		}
	}
	// its menu presses the button, and only a button's press
	if res, _ := actV(t, a, 6, pageState{}, "do:"+acOff+"|press", nil); r.lastCall() != "button.press ["+acOff+"] {}" || res.Toast != "已发送「关机」" {
		t.Errorf("press: %q %q", r.lastCall(), res.Toast)
	}
	if res, _ := actV(t, a, 6, pageState{}, "do:"+acOff+"|turn_on", nil); r.lastCall() != "button.press ["+acOff+"] {}" || res.Toast != "不认识的操作" {
		t.Errorf("a button's other service went through: %q %q", r.lastCall(), res.Toast)
	}
	// the room's own page lists the remote too
	_, room := renderV(t, a, 6, pageState{Area: "ke_ting"})
	contains(t, room, "tile 空调|遥控 · ", "tile 灯光1|另有 1 项|")
}

func TestEntityNameKeepsWhatTheUserWrote(t *testing.T) {
	reg := &Registry{Entities: map[string]RegEntity{}, Devices: map[string]RegDevice{
		"lamp": {ID: "lamp", Name: "吸顶灯"}, "plug": {ID: "plug", Name: "Desk plug"}, "w": {ID: "w", Name: "Wall", NameByUser: "书房开关"}}}
	for _, c := range []struct{ id, dev, name, want string }{
		{"light.living_lamp", "lamp", "客厅吸顶灯", ""},               // the device's name with more in front: the user's own
		{"light.lamp", "lamp", "吸顶灯", ""},                        // the device's own entity
		{"switch.lamp_aux", "lamp", "吸顶灯 辅光开关", "辅光开关"},          // not only a switch's word
		{"switch.plug", "plug", "Desk plug Switch", "Desk plug"}, // nothing but that it is a switch: the device's
		{"switch.wall_l", "w", "书房开关 左键 开关", "左键"},               // the name the user gave the device
		{"button.speed", "lamp", "吸顶灯 风速-", "风速-"},               // a minus is not a separator
		{"light.none", "", "落地灯", ""},
	} {
		reg.Entities[c.id] = RegEntity{EntityID: c.id, DeviceID: c.dev}
		if got := entityName(reg, State{EntityID: c.id, Attributes: map[string]any{"friendly_name": c.name}}); got != c.want {
			t.Errorf("entityName(%q) = %q, want %q", c.name, got, c.want)
		}
	}
}
