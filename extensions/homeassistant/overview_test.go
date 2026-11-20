package main

import (
	"encoding/json"
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

// An IR remote with both power buttons has a switch on its tile (#774): on when 开机 was pressed after 关机, turning it
// on and off pressing them, and nothing but that remote's own power buttons.
func TestRemoteTileSwitch(t *testing.T) {
	const (
		tvOn   = "button.tv_turn_on"
		tvOff  = "button.tv_turn_off"
		tvUp   = "button.tv_volume_up"
		fanOn  = "button.fan_turn_on" // a remote with one power button: no switch
		fanSpd = "button.fan_speed"
	)
	at := func(ago time.Duration) string { return time.Now().Add(-ago).UTC().Format(time.RFC3339Nano) }
	r := newRichHA(t)
	put := func(on, off string) {
		r.put(State{EntityID: tvOn, State: on, Attributes: map[string]any{"friendly_name": "电视 红外电视控制 开机"}},
			State{EntityID: tvOff, State: off, Attributes: map[string]any{"friendly_name": "电视 红外电视控制 关机"}})
	}
	put("unknown", "unknown")
	r.put(State{EntityID: tvUp, State: "unknown", Attributes: map[string]any{"friendly_name": "电视 红外电视控制 音量+"}},
		State{EntityID: fanOn, State: "unknown", Attributes: map[string]any{"friendly_name": "风扇 红外风扇控制 开机"}},
		State{EntityID: fanSpd, State: "unknown", Attributes: map[string]any{"friendly_name": "风扇 红外风扇控制 风速"}})
	for _, e := range []struct{ id, dev string }{{tvOn, "tv"}, {tvOff, "tv"}, {tvUp, "tv"}, {fanOn, "fan"}, {fanSpd, "fan"}} {
		r.ws.entities = append(r.ws.entities, map[string]any{"entity_id": e.id, "device_id": e.dev, "platform": "xiaomi_home"})
	}
	r.ws.devices = append(r.ws.devices, map[string]any{"id": "tv", "name": "电视"}, map[string]any{"id": "fan", "name": "风扇"})
	r.services = strings.TrimSuffix(r.services, "]") + `,{"domain":"button","services":{"press":{}}}]`
	r.areas = areasJSON([][3]any{{"ke_ting", "客厅", []string{tvOn, tvOff, tvUp, fanOn, fanSpd}}})
	a := richApp(t, r, Config{})
	a.marks.load(a.dataDir, t.Logf)

	tile := func() map[string]wire.Tile {
		t.Helper()
		h, _, _ := a.client()
		h.mu.Lock()
		h.statesAt = time.Time{} // read the buttons' new times
		h.mu.Unlock()
		v, _ := renderV(t, a, 6, nil)
		out := map[string]wire.Tile{}
		for _, x := range tilesOf(v, "room:ke_ting") {
			out[x.Title] = x
		}
		return out
	}
	sw := func(want string) {
		t.Helper()
		tv, ok := tile()["电视"]
		if !ok || tv.Control == nil {
			t.Fatalf("the TV's tile has no switch: %+v", tv)
		}
		if got := controlText(tv.Control); got != want {
			t.Errorf("switch %s, want %s", got, want)
		}
	}
	off, on := "ctl(switch ir:"+tvOn+" false →ctl:ir "+tvOn+")", "ctl(switch ir:"+tvOn+" true →ctl:ir "+tvOn+")"
	sw(off) // never pressed
	put(at(time.Minute), at(time.Hour))
	sw(on) // 开机 after 关机
	put(at(time.Hour), at(time.Minute))
	sw(off) // 关机 after 开机
	put(at(time.Minute), "unknown")
	sw(on) // 开机, 关机 never
	if fan := tile()["风扇"]; fan.Control != nil || len(fan.Actions) == 0 {
		t.Errorf("a remote with one power button: %+v", fan)
	}

	press := func(entity, value string) *wire.ActionResult {
		t.Helper()
		res, _ := actV(t, a, 6, pageState{}, "ctl:ir", map[string]string{"entity": entity, "ir:" + entity: value})
		return res
	}
	if res := press(tvOn, "true"); r.lastCall() != "button.press ["+tvOn+"] {}" || res.Toast != "已发送「开机」" {
		t.Errorf("on: %q %q", r.lastCall(), res.Toast)
	}
	if res := press(tvOn, "false"); r.lastCall() != "button.press ["+tvOff+"] {}" || res.Toast != "已发送「关机」" {
		t.Errorf("off: %q %q", r.lastCall(), res.Toast)
	}
	for _, c := range []struct{ entity, value, toast string }{
		{tvUp, "true", errNoEntity.Error()},  // not a remote's 开机
		{tvOff, "true", errNoEntity.Error()}, // nor its 关机
		{fanOn, "true", errNoEntity.Error()}, // a remote without a switch
		{tvOn, "on", errUnknownOp.Error()},
	} {
		if res := press(c.entity, c.value); r.lastCall() != "button.press ["+tvOff+"] {}" || res.Toast != c.toast {
			t.Errorf("%s=%s went through: %q %q", c.entity, c.value, r.lastCall(), res.Toast)
		}
	}

	// 标记为已关 (irmarks.go): offered only while the switch is on, it sends nothing and keeps the switch off until 开机
	// is pressed again, across a restart (the data directory). The menu stays within four entries.
	titles := func(x wire.Tile) (out []string) {
		for _, m := range x.Actions {
			out = append(out, m.Title)
		}
		return out
	}
	put(at(time.Hour), at(2*time.Hour))
	sw(on)
	tv := tile()["电视"]
	if got := titles(tv); !slices.Equal(got, []string{"开机", "关机", "标记为已关", "音量+"}) {
		t.Errorf("the menu while on: %q", got)
	}
	calls := r.lastCall()
	res, _ := actV(t, a, 6, pageState{}, tv.Actions[2].Action.ID, tv.Actions[2].Action.Values)
	if res.Toast != "已标记为关，没有发送信号" || r.lastCall() != calls {
		t.Errorf("marking off: %q, sent %q", res.Toast, r.lastCall())
	}
	sw(off)
	if got := titles(tile()["电视"]); slices.Contains(got, "标记为已关") {
		t.Errorf("offered while off: %q", got)
	}
	b := newApp(t.Logf)
	b.marks.load(a.dataDir, t.Logf)
	if b.marks.get(tvOn).IsZero() {
		t.Errorf("the mark was not kept in %s", a.dataDir)
	}
	put(at(0), at(2*time.Hour))
	sw(on) // 开机 pressed after the mark
	for _, eid := range []string{tvUp, tvOff, fanOn, "bad id"} {
		if res, _ := actV(t, a, 6, pageState{}, "irmark", map[string]string{"entity": eid}); res.Toast == "已标记为关，没有发送信号" {
			t.Errorf("marked %q", eid)
		}
	}
	if act := irMarkAction("button." + strings.Repeat("x", idMost)); act.ID != "irmark" || act.Values["entity"] == "" {
		t.Errorf("a long id: %+v", act)
	}
}

// Opening an entity pushes its page (#774), so the app's back returns to the page below; an older app calls
// open:<entity> and gets the page in place (TestPage opens it so).
func TestOpenPushesTheEntityPage(t *testing.T) {
	for _, eid := range []string{"light.living", "sensor." + strings.Repeat("x", idMost)} {
		act := openAction(eid)
		var st pageState
		if act.Push == nil || json.Unmarshal([]byte(act.Push.State), &st) != nil || st != (pageState{Entity: eid}) {
			t.Errorf("%s: push %+v", eid, act.Push)
		}
		if act.ID != "open:"+eid && (act.ID != "open" || act.Values["entity"] != eid) {
			t.Errorf("%s: an older app's action %+v", eid, act)
		}
	}
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
