package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/sdk/go/wire"
)

const (
	acOn  = "button.living_ac_turn_on"
	acOff = "button.living_ac_turn_off"
)

// shortcutHA is the rich fake with an IR air conditioner in the living room and Home Assistant's script config API:
// a write loads the script as an entity, as Home Assistant's reload does.
func shortcutHA(t *testing.T) (*richHA, map[string]map[string]any, *[]string) {
	t.Helper()
	r := newRichHA(t)
	r.put(State{EntityID: acOn, State: "unknown", Attributes: map[string]any{"friendly_name": "客厅空调 红外空调控制 开机"}},
		State{EntityID: acOff, State: "unknown", Attributes: map[string]any{"friendly_name": "客厅空调 红外空调控制 关机"}},
		State{EntityID: "button.living_ac_temp_up", State: "unknown", Attributes: map[string]any{"friendly_name": "客厅空调 红外空调控制 温度+"}})
	r.ws.entities = append(r.ws.entities,
		map[string]any{"entity_id": acOn, "device_id": "dev_ac"}, map[string]any{"entity_id": acOff, "device_id": "dev_ac"},
		map[string]any{"entity_id": "button.living_ac_temp_up", "device_id": "dev_ac"})
	r.ws.devices = append(r.ws.devices, map[string]any{"id": "dev_ac", "name": "客厅空调"})
	r.areas = areasJSON([][3]any{
		{"living", "客厅", []string{"light.living", "light.ceiling", auxSwitch, "switch.ceiling_power_default", "switch.living_plug",
			"switch.living_hidden", acOn, acOff, "button.living_ac_temp_up", "lock.front"}},
		{"bedroom", "卧室", []string{"light.bed"}}})
	scripts := map[string]map[string]any{}
	var writes []string
	serve := r.fakeHA.more
	r.fakeHA.more = func(w http.ResponseWriter, req *http.Request) bool {
		key, ok := strings.CutPrefix(req.URL.Path, "/api/config/script/config/")
		if !ok || !r.authed(req) {
			return serve(w, req)
		}
		var body bytes.Buffer
		_, _ = body.ReadFrom(req.Body)
		r.mu.Lock()
		defer r.mu.Unlock()
		switch req.Method {
		case http.MethodGet:
			c, ok := scripts[key]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				fmt.Fprint(w, `{"message":"Resource not found"}`)
				return true
			}
			_ = json.NewEncoder(w).Encode(c)
		case http.MethodPost:
			var c map[string]any
			_ = json.Unmarshal(body.Bytes(), &c)
			scripts[key] = c
			writes = append(writes, "POST "+key+" "+body.String())
			r.state["script."+key] = State{EntityID: "script." + key, State: "off", Attributes: map[string]any{"friendly_name": c["alias"]}}
			fmt.Fprint(w, `{"result":"ok"}`)
		case http.MethodDelete:
			delete(scripts, key)
			delete(r.state, "script."+key)
			writes = append(writes, "DELETE "+key)
			fmt.Fprint(w, `{"result":"ok"}`)
		}
		return true
	}
	return r, scripts, &writes
}

// shortcutAct runs an action of the page as the app sends it; Pop answers have no view.
func shortcutAct(t *testing.T, a *app, st pageState, action string, values map[string]string) *wire.ActionResult {
	t.Helper()
	res, err := a.action(context.Background(), reqV(6, st, action, values))
	if err != nil {
		t.Fatalf("%s: %v", action, err)
	}
	if res.View != nil {
		check(t, res.View)
	}
	return res
}

func fieldOf(v *wire.View, name string) *wire.Component {
	for _, s := range v.Sections {
		for i := range s.Items {
			if s.Items[i].Type == "field" && s.Items[i].Name == name {
				return &s.Items[i]
			}
		}
	}
	return nil
}

func choiceValues(c *wire.Component) []string {
	var out []string
	for _, ch := range c.Choices {
		out = append(out, ch.Value+"="+ch.Title)
	}
	return out
}

// invalidated records the pages the apps were asked to draw again.
type invalidated chan string

func (c invalidated) Invalidate(contribution string) error { c <- contribution; return nil }

func TestShortcuts(t *testing.T) {
	r, scripts, writes := shortcutHA(t)
	a := richApp(t, r, Config{})
	was := popRedraw
	popRedraw = 0
	t.Cleanup(func() { popRedraw = was })
	redrawn := make(invalidated, 8)
	a.rpc = redrawn
	v, _ := renderV(t, a, 6, nil)
	row := tilesOf(v, "scenes")
	if len(row) != 2 || row[0].Title != "全关" || row[1].Title != "新建" || row[1].Action.Push == nil {
		t.Fatalf("the row without shortcuts: %+v", row)
	}
	var st pageState
	if err := json.Unmarshal([]byte(row[1].Action.Push.State), &st); err != nil || !shortcutKeyRE.MatchString(st.SC) {
		t.Fatalf("新建 opens %q", row[1].Action.Push.State)
	}

	// the form: what the page can switch (not a device's settings, accessory or hidden switch, nor a lock), each
	// remote's power buttons
	form, _ := renderV(t, a, 6, st)
	if form.Title != "新建快捷" || form.Tabs != nil || form.Search != nil || form.RefreshAfter != 0 {
		t.Errorf("the form's page: %q tabs %v search %v refresh %d", form.Title, form.Tabs, form.Search, form.RefreshAfter)
	}
	off, on, press := fieldOf(form, "off"), fieldOf(form, "on"), fieldOf(form, "press")
	if fieldOf(form, "name") == nil || off == nil || on == nil || press == nil || off.Kind != "multichoice" {
		t.Fatalf("the form's fields: %+v", form.Sections)
	}
	wantDevices := []string{"light.living=客厅 · 客厅灯", "light.ceiling=客厅 · 吸顶灯", "switch.living_plug=客厅 · 插座", "light.bed=卧室 · 卧室灯",
		"light.other_home=别处的灯", "switch.fan_plug=风扇插座（离线）"}
	if got := choiceValues(off); !slices.Equal(got, wantDevices) || !slices.Equal(choiceValues(on), wantDevices) {
		t.Errorf("the devices offered: %q", got)
	}
	if got := choiceValues(press); !slices.Equal(got, []string{acOn + "=客厅 · 空调 开机", acOff + "=客厅 · 空调 关机"}) {
		t.Errorf("the buttons offered: %q", got)
	}

	// refused: kept on the form with what was typed
	for _, c := range []struct {
		values map[string]string
		toast  string
	}{
		{map[string]string{"name": " ", "off": `["light.living"]`}, "先起个名字"},
		{map[string]string{"name": "睡觉"}, "至少选一个"},
		{map[string]string{"name": "睡觉", "off": `["light.living"]`, "on": `["light.living"]`}, "不能既关又开"},
		{map[string]string{"name": "睡觉", "off": `["lock.front"]`}, "不是这里能放进快捷的设备"},
		{map[string]string{"name": "睡觉", "press": `["button.living_ac_temp_up"]`}, "不是这里能放进快捷的设备"},
		{map[string]string{"name": "这个名字比二十个字还要长很多很多很多很多很多", "off": `["light.living"]`}, "最多 20 个字"},
	} {
		res := shortcutAct(t, a, st, "sc:save", c.values)
		if res.Pop || !strings.Contains(res.Toast, c.toast) || res.View == nil || fieldOf(res.View, "name").Value != c.values["name"] {
			t.Errorf("%v: %q pop %v", c.values, res.Toast, res.Pop)
		}
	}
	if len(*writes) != 0 {
		t.Fatalf("refused saves wrote: %q", *writes)
	}

	// saved: a script that turns off, then on, then presses the buttons a second apart
	res := shortcutAct(t, a, st, "sc:save", map[string]string{"name": "睡觉", "off": `["light.living","switch.living_plug","light.bed"]`,
		"on": `["light.ceiling"]`, "press": `["` + acOff + `","` + acOn + `"]`})
	if !res.Pop || res.Toast != "已保存「睡觉」" {
		t.Fatalf("save: %+v", res)
	}
	// the page below is asked to draw again, with the shortcut in it
	select {
	case ui := <-redrawn:
		if ui != pageUI {
			t.Errorf("redrew %q", ui)
		}
	case <-time.After(2 * time.Second):
		t.Error("the page below was not asked to draw again")
	}
	got := canonical(scripts[st.SC]["sequence"])
	want := canonical([]any{
		map[string]any{"action": "light.turn_off", "target": map[string]any{"entity_id": []string{"light.living", "light.bed"}}, "continue_on_error": true},
		map[string]any{"action": "switch.turn_off", "target": map[string]any{"entity_id": []string{"switch.living_plug"}}, "continue_on_error": true},
		map[string]any{"action": "light.turn_on", "target": map[string]any{"entity_id": []string{"light.ceiling"}}, "continue_on_error": true},
		map[string]any{"action": "button.press", "target": map[string]any{"entity_id": acOff}, "continue_on_error": true},
		map[string]any{"delay": map[string]any{"seconds": 1}},
		map[string]any{"action": "button.press", "target": map[string]any{"entity_id": acOn}, "continue_on_error": true},
	})
	if got != want || scripts[st.SC]["alias"] != "睡觉" || scripts[st.SC]["icon"] != "mdi:weather-night" {
		t.Errorf("the script:\n%s\nwant\n%s\n%v", got, want, scripts[st.SC])
	}

	// the row: the shortcut first, run at once, its menu edits and deletes; not again as a script
	v, _ = renderV(t, a, 6, nil)
	row = tilesOf(v, "scenes")
	if len(row) != 3 || row[0].Title != "睡觉" || row[0].Icon != "moon" || row[0].Subtitle != "关 3 · 开 1 · 遥控 2" ||
		row[0].Action.ID != "sc:run" || row[0].Action.Confirm != "" || len(row[0].Actions) != 2 ||
		row[0].Actions[0].Action.Push == nil || row[0].Actions[1].Action.Confirm == "" || row[1].Title != "全关" || row[2].Title != "新建" {
		t.Fatalf("the row with a shortcut: %+v", row)
	}
	run := shortcutAct(t, a, pageState{}, "sc:run", row[0].Action.Values)
	if r.lastCall() != "script.turn_on [script."+st.SC+"] {}" || run.Toast != "开始执行「睡觉」" || run.View == nil {
		t.Errorf("run: %q %q", r.lastCall(), run.Toast)
	}
	// a second one with the same name is refused
	if res := shortcutAct(t, a, pageState{SC: newShortcutKey()}, "sc:save", map[string]string{"name": "睡觉", "on": `["light.bed"]`}); !strings.Contains(res.Toast, "已经有") {
		t.Errorf("the same name: %q", res.Toast)
	}

	// editing: the form holds the shortcut, and saving writes the same script
	var edit pageState
	_ = json.Unmarshal([]byte(row[0].Actions[0].Action.Push.State), &edit)
	form, _ = renderV(t, a, 6, edit)
	if form.Title != "编辑快捷" || fieldOf(form, "name").Value != "睡觉" || fieldOf(form, "off").Value != `["light.living","light.bed","switch.living_plug"]` {
		t.Errorf("the edit form: %q %+v", form.Title, fieldOf(form, "off"))
	}
	if res := shortcutAct(t, a, edit, "sc:save", map[string]string{"name": "晚安", "off": `["light.living"]`}); !res.Pop || res.Toast != "已保存「晚安」" {
		t.Errorf("edit: %+v", res)
	}
	if n := len(scripts); n != 1 || scripts[edit.SC]["alias"] != "晚安" {
		t.Errorf("after the edit: %v", scripts)
	}

	// a device the settings no longer allow: the shortcut does not run
	b := richApp(t, r, Config{Writable: []string{"switch", "script", "button"}})
	if res := shortcutAct(t, b, pageState{}, "sc:run", map[string]string{"sc": edit.SC}); !strings.Contains(res.Toast, "先编辑这个快捷") {
		t.Errorf("a light no longer writable: %q", res.Toast)
	}

	// edited in Home Assistant into something else: runs only from 自动化, drawn as any script (asking first)
	r.mu.Lock()
	scripts[edit.SC]["sequence"] = append(scripts[edit.SC]["sequence"].([]any), map[string]any{"action": "lock.unlock", "target": map[string]any{"entity_id": "lock.front"}})
	r.mu.Unlock()
	if res := shortcutAct(t, a, pageState{}, "sc:run", map[string]string{"sc": edit.SC}); !strings.Contains(res.Toast, "被改过") {
		t.Errorf("a hand-edited script ran: %q", res.Toast)
	}
	v, _ = renderV(t, a, 6, nil)
	row = tilesOf(v, "scenes")
	if row[0].Title != "全关" || !slices.ContainsFunc(row, func(t wire.Tile) bool { return t.Title == "晚安" && t.Action.Confirm != "" }) {
		t.Errorf("a hand-edited script in the row: %+v", row)
	}
	if res := shortcutAct(t, a, pageState{}, "sc:del", map[string]string{"sc": edit.SC}); !strings.Contains(res.Toast, "没有这个快捷") || len(scripts) != 1 {
		t.Errorf("deleting a hand-edited script: %q", res.Toast)
	}

	// deleting from the form goes back; from the row redraws
	r.mu.Lock()
	scripts[edit.SC]["sequence"] = []any{map[string]any{"service": "light.turn_off", "target": map[string]any{"entity_id": "light.living"}}}
	r.mu.Unlock()
	res = shortcutAct(t, a, edit, "sc:del", nil)
	if !res.Pop || res.Toast != "已删除「晚安」" || len(scripts) != 0 || !slices.Contains(*writes, "DELETE "+edit.SC) {
		t.Errorf("delete: %+v %v", res, scripts)
	}
}

func TestShortcutOf(t *testing.T) {
	ok := map[string]any{"alias": "回家", "mode": "single", "sequence": []any{
		map[string]any{"service": "switch.turn_on", "target": map[string]any{"entity_id": "switch.a"}},
		map[string]any{"action": "light.turn_on", "target": map[string]any{"entity_id": []any{"light.a", "light.b"}}, "continue_on_error": true}}}
	sc, good := shortcutOf("persona_1", ok)
	if !good || !slices.Equal(sc.On, []string{"switch.a", "light.a", "light.b"}) || sc.summary() != "开 3" {
		t.Fatalf("shortcutOf: %+v %v", sc, good)
	}
	for name, c := range map[string]map[string]any{
		"another key":    {"alias": "x", "sequence": ok["sequence"]},
		"a field":        {"alias": "x", "fields": map[string]any{}, "sequence": ok["sequence"]},
		"a condition":    {"alias": "x", "sequence": []any{map[string]any{"condition": "state"}}},
		"data":           {"alias": "x", "sequence": []any{map[string]any{"action": "light.turn_on", "target": map[string]any{"entity_id": "light.a"}, "data": map[string]any{"brightness": 1}}}},
		"another domain": {"alias": "x", "sequence": []any{map[string]any{"action": "lock.unlock", "target": map[string]any{"entity_id": "lock.a"}}}},
		"a mismatch":     {"alias": "x", "sequence": []any{map[string]any{"action": "light.turn_on", "target": map[string]any{"entity_id": "switch.a"}}}},
		"by area":        {"alias": "x", "sequence": []any{map[string]any{"action": "light.turn_on", "target": map[string]any{"area_id": "living"}}}},
		"only a delay":   {"alias": "x", "sequence": []any{map[string]any{"delay": 1}}},
		"no name":        {"sequence": ok["sequence"]},
	} {
		key := "persona_1"
		if name == "another key" {
			key = "bedtime"
		}
		if _, good := shortcutOf(key, c); good {
			t.Errorf("%s: read as a shortcut", name)
		}
	}
	// what config writes reads back the same
	in := &shortcut{Key: "persona_2", Name: "看电视", Off: []string{"light.a"}, On: []string{"switch.tv", "light.b"}, Press: []string{"button.x", "button.y"}}
	raw, _ := json.Marshal(in.config())
	var c map[string]any
	_ = json.Unmarshal(raw, &c)
	out, good := shortcutOf(in.Key, c)
	if !good || out.Name != in.Name || !slices.Equal(out.Off, in.Off) || !slices.Equal(out.On, []string{"switch.tv", "light.b"}) || !slices.Equal(out.Press, in.Press) {
		t.Errorf("round trip: %+v", out)
	}
	if icon, _, mdi := shortcutLook("看电视"); icon != "tv" || mdi != "mdi:television" {
		t.Errorf("look: %s %s", icon, mdi)
	}
}
