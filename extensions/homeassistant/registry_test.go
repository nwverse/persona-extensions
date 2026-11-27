package main

import (
	"slices"
	"testing"
)

// A Xiaomi Home wall switch (xiaomi.switch.w2) is one device: two relays that drive the lights (MIoT property on), the
// switch's own indicator LED, a light registered as config, and an anti-flicker switch Xiaomi Home registers with no
// category. The LED must not make the relays its accessory switches, and anti-flicker is a setting of a relay (#623).
func TestWallSwitchRelaysAreNotSettingsOfItsIndicatorLight(t *testing.T) {
	const (
		relay1 = "switch.xiaomi_cn_100000001_w2_on_p_2_1"
		relay2 = "switch.xiaomi_cn_100000001_w2_on_p_3_1"
		led    = "light.xiaomi_cn_100000001_w2_s_7_indicator_light"
		flick  = "switch.xiaomi_cn_100000001_w2_anti_flicker_p_2_4"
		lamp   = "light.ceiling_lamp"
		aux    = "switch.ceiling_lamp_aux"
		gone   = "light.old_lamp"
		plug   = "switch.old_lamp_plug"
		strip1 = "switch.power_strip_1"
		strip2 = "switch.power_strip_usb"
	)
	disabled := "user"
	reg := &Registry{Entities: map[string]RegEntity{
		relay1: {EntityID: relay1, DeviceID: "wall", Platform: "xiaomi_home"},
		relay2: {EntityID: relay2, DeviceID: "wall", Platform: "xiaomi_home"},
		led:    {EntityID: led, DeviceID: "wall", Platform: "xiaomi_home", EntityCategory: "config"},
		flick:  {EntityID: flick, DeviceID: "wall", Platform: "xiaomi_home"},
		// a lamp's own switch still folds into the lamp
		lamp: {EntityID: lamp, DeviceID: "lamp"},
		aux:  {EntityID: aux, DeviceID: "lamp"},
		// a disabled light is not what its device is either
		gone: {EntityID: gone, DeviceID: "old", DisabledBy: &disabled},
		plug: {EntityID: plug, DeviceID: "old"},
		// two switches of another integration's device stay two devices
		strip1: {EntityID: strip1, DeviceID: "strip"},
		strip2: {EntityID: strip2, DeviceID: "strip"},
	}, ByDevice: map[string][]string{
		"wall":  {led, flick, relay1, relay2},
		"lamp":  {lamp, aux},
		"old":   {gone, plug},
		"strip": {strip1, strip2},
	}}
	hm := &home{ByID: map[string]State{}}
	ar := &Area{ID: "living", Name: "客厅"}
	for _, e := range []string{relay1, relay2, led, flick, lamp, aux, plug, strip1, strip2} {
		hm.ByID[e] = State{EntityID: e, State: "on"}
		ar.Entities = append(ar.Entities, e)
	}

	for e, want := range map[string]string{relay1: "", relay2: "", plug: "", strip1: "", strip2: "", led: "设备的配置项",
		flick: "是「" + relay1 + "」（" + relay1 + "）的附属开关"} {
		if got := reg.skip(e, hm.ByID); got != want {
			t.Errorf("skip(%s) = %q, want %q", e, got, want)
		}
	}
	if got := reg.skip(aux, hm.ByID); got == "" {
		t.Errorf("skip(%s) = \"\", want it to be the lamp's accessory switch", aux)
	}

	targets, _ := batch(hm, reg, ar, "switch")
	var ids []string
	for _, s := range targets {
		ids = append(ids, s.EntityID)
	}
	if want := []string{relay1, relay2, plug, strip1, strip2}; !slices.Equal(ids, want) {
		t.Errorf("switches by area = %v, want %v", ids, want)
	}

	var rows []string
	for _, r := range deviceRows(hm, reg, ar) {
		rows = append(rows, r.s.EntityID)
	}
	for _, e := range []string{relay1, relay2, lamp, plug, strip1, strip2} {
		if !slices.Contains(rows, e) {
			t.Errorf("the room's device rows %v miss %s", rows, e)
		}
	}
	for _, e := range []string{led, flick, aux} {
		if slices.Contains(rows, e) {
			t.Errorf("the room's device rows %v show the setting %s", rows, e)
		}
	}
}
