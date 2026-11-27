package main

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/nwverse/persona/sdk/go/ext"
	"github.com/nwverse/persona/sdk/go/wire"
)

// Room tabs and the scenes row (#634): a small home (at most roomTabsMost rooms with devices, Home Assistant's own
// areas, UI version 6) has its rooms as tabs on top, between 全屋 and 动态 / 自动化, as a home app has them; a room's
// tab is the room's page without a breadcrumb (the tabs are the way back), and the area filter goes (the tabs are it).
// 全屋 and each room start with a row of scenes: 全关 when something is on, then Home Assistant's scenes and scripts;
// 全屋's starts with the user's own shortcuts and ends with 新建, which makes one (shortcuts.go, #639).

const (
	roomTabsMost = 5 // 全屋 + rooms + 动态 + 自动化 within a tab bar's 8
	roomTab      = "room:"
	scenesMost   = 20 // a carousel takes 20
)

// offDomains are what 全关 turns off.
var offDomains = []string{"light", "switch", "fan"}

// roomTabs are the rooms that get a tab: the areas with a device or a remote, in Home Assistant's order; nil when the
// home has more than roomTabsMost of them, none, or no areas of its own, or the app draws no tiles.
func roomTabs(r *ext.UIRequest, hm *home, reg *Registry) []*Area {
	if r.UIVersion < 6 || hm.ByType {
		return nil
	}
	var out []*Area
	for i := range hm.Areas {
		ar := &hm.Areas[i]
		if len(deviceRows(hm, reg, ar)) > 0 || len(remotes(hm, reg, ar)) > 0 {
			out = append(out, ar)
		}
	}
	// a room's tab says its id (roomTab): an id longer than a tab's value takes leaves the home without them
	if len(out) > roomTabsMost || slices.ContainsFunc(out, func(ar *Area) bool { return len(roomTab)+len(ar.ID) > idMost }) {
		return nil
	}
	return out
}

// tabValue is the tab st is on: a room's (room:<id>) when it is on a room that has a tab, else its own.
func tabValue(st pageState, rooms []*Area) string {
	if st.Area != "" && slices.ContainsFunc(rooms, func(ar *Area) bool { return ar.ID == st.Area }) {
		return roomTab + st.Area
	}
	if st.Tab == "" {
		return "home"
	}
	return st.Tab
}

// tabItems are the tabs: 家 (全屋 when the rooms follow it), the rooms, 动态, 自动化.
func tabItems(rooms []*Area, alarms int) []wire.Tab {
	var out []wire.Tab
	for _, c := range tabs {
		tab := wire.Tab{Value: c.Value, Title: c.Title}
		if c.Value == "home" {
			if alarms > 0 {
				tab.Badge = fmt.Sprint(min(alarms, 99))
			}
			if len(rooms) > 0 {
				tab.Title = "全屋"
			}
			out = append(out, tab)
			for _, ar := range rooms {
				out = append(out, wire.Tab{Value: roomTab + ar.ID, Title: clip(ar.Name, 20)})
			}
			continue
		}
		out = append(out, tab)
	}
	return out
}

// offTargets are what 全关 turns off in ar (the whole home when nil): the lights, switches and fans that are on, that
// the page shows, that are not a device's setting or accessory switch, of a domain the settings allow.
func (a *app) offTargets(hm *home, reg *Registry, ar *Area) []State {
	var ids []string
	if ar != nil {
		ids = ar.Entities
	} else {
		for _, s := range hm.States {
			ids = append(ids, s.EntityID)
		}
	}
	var out []State
	for _, e := range ids {
		s, ok := hm.ByID[e]
		d := s.Domain()
		if !ok || s.State != "on" || !slices.Contains(offDomains, d) || !hm.visible(e) || regHidden(reg, e) ||
			reg.skip(e, hm.ByID) != "" || a.canWrite(d, false) != nil {
			continue
		}
		out = append(out, s)
	}
	return out
}

// scenesRow is the row at the top of 全屋 (ar nil) or a room: 全关 when something is on there, then its scenes and
// scripts (asking first, as their rows do). ok is false when it has none of them.
func (a *app) scenesRow(hm *home, reg *Registry, ar *Area) (wire.Component, bool) {
	var tiles []wire.Tile
	where, id := "全屋", ""
	if ar != nil {
		where, id = ar.Name, ar.ID
	}
	// the user's own shortcuts first (shortcuts.go), on 全屋
	mine := map[string]bool{}
	if ar == nil {
		tiles = append(tiles, a.shortcutTiles(hm)...)
		for _, sc := range hm.Shortcuts {
			mine[sc.entity()] = true
		}
	}
	if on := a.offTargets(hm, reg, ar); len(on) > 0 {
		tiles = append(tiles, wire.Tile{Key: "alloff", Title: "全关", Subtitle: fmt.Sprintf("%d 个开着", len(on)), Icon: "power", Color: "gray",
			Action: &wire.Action{ID: "alloff", Values: map[string]string{"area": id},
				Confirm: clip(fmt.Sprintf("关掉%s开着的 %d 个灯和开关？", where, len(on)), 200)}})
	}
	ids := hm.States
	if ar != nil {
		ids = nil
		for _, e := range ar.Entities {
			if s, ok := hm.ByID[e]; ok {
				ids = append(ids, s)
			}
		}
	}
	for _, s := range ids {
		d := s.Domain()
		if (d != "scene" && d != "script") || mine[s.EntityID] || !hm.visible(s.EntityID) || regHidden(reg, s.EntityID) || offline(s) ||
			len(tiles) == scenesMost {
			continue
		}
		row := a.autoRow(s, a.loc())
		t := wire.Tile{Key: ref("sc:", s.EntityID), Title: clip(shortName(hm, s), 40), Icon: entityIcon(s), Color: "amber",
			Active: d == "script" && s.State == "on", Action: row.Action}
		if row.Action == nil || !strings.HasPrefix(row.Action.ID, "do") {
			t.Action = openAction(s.EntityID)
		}
		tiles = append(tiles, t)
	}
	if ar == nil && a.canMakeShortcuts(hm) {
		tiles = append(tiles[:min(len(tiles), scenesMost-1)], newShortcutTile())
	}
	if len(tiles) == 0 {
		return wire.Component{}, false
	}
	return wire.Component{Type: "carousel", Key: "scenes", Width: "narrow", Tiles: tiles}, true
}

// allOff turns off what 全关 offers in the area (the whole home when ""): checked again here, from a fresh read.
func (a *app) allOff(ctx context.Context, area string) (string, error) {
	hm, err := a.home(ctx, statesFor)
	if err != nil {
		return "", err
	}
	var ar *Area
	if area != "" {
		if ar = hm.areaByID(area); ar == nil {
			return "", errNoEntity
		}
	}
	reg := a.pageRegistry(ctx)
	by := map[string][]string{}
	n := 0
	for _, s := range a.offTargets(hm, reg, ar) {
		by[s.Domain()] = append(by[s.Domain()], s.EntityID)
		n++
	}
	if n == 0 {
		return "都关着", nil
	}
	for _, d := range offDomains {
		if len(by[d]) == 0 {
			continue
		}
		if _, err := a.run(ctx, call{Domain: d, Service: "turn_off", Entities: by[d]}); err != nil {
			return "", err
		}
	}
	return fmt.Sprintf("关掉了 %d 个", n), nil
}
