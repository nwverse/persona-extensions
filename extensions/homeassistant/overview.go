package main

import (
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/nwverse/persona/sdk/go/wire"
)

// What the overview (home.go) needs to read a home well: names without their device's, the icon a switch's name
// implies, devices room by room, and remotes (an IR air conditioner or TV is only buttons) as tiles.

// named is hm with Names (entityName for every entity that has one), for one page: hm itself is shared.
func (hm *home) named(reg *Registry) *home {
	if reg == nil {
		return hm
	}
	h := *hm
	h.Names = map[string]string{}
	for _, s := range hm.States {
		if n := entityName(reg, s); n != "" && n != s.Name() {
			h.Names[s.EntityID] = n
		}
	}
	return &h
}

// cutSep is where a device's name was cut out of an entity's, with the separator on either side of it.
var cutSep = regexp.MustCompile(`\s*[-–—·:：|/]?\s*\x00\s*[-–—·:：|/]?\s*`)

// powerWords are a switch's last word that only says it is a switch.
var powerWords = []string{"开关", "电源", "switch", "power"}

// entityName is s's name less its device's, "" when that says nothing new. Home Assistant writes the device's name
// first ("小米智能开关（双开） 3 灯光1 开关"), and an integration may repeat it inside ("左键-小米智能开关（双开） 2 开关");
// a switch's last word saying only that it is a switch goes too (the tile shows the switch). With nothing left, the
// device's name. A name that does not start with its device's (renamed in Home Assistant, or the device's name with
// more in front, like 客厅吸顶灯 of 吸顶灯) is the user's own and stays.
func entityName(reg *Registry, s State) string {
	re, ok := reg.Entities[s.EntityID]
	if !ok || re.DeviceID == "" {
		return ""
	}
	dev := strings.TrimSpace(reg.Devices[re.DeviceID].title())
	n := strings.TrimSpace(s.Name())
	if dev == "" || n == dev || !strings.HasPrefix(n, dev) {
		return ""
	}
	fields := strings.Fields(cutSep.ReplaceAllString(strings.ReplaceAll(n, dev, "\x00"), " "))
	if d := s.Domain(); (d == "switch" || d == "input_boolean") && len(fields) > 0 &&
		slices.Contains(powerWords, strings.ToLower(fields[len(fields)-1])) {
		fields = fields[:len(fields)-1]
	}
	if len(fields) == 0 {
		return dev
	}
	return strings.Join(fields, " ")
}

// nameLooks are an icon and a color by what a name says the thing is; the first match wins.
var nameLooks = []struct {
	words       []string
	icon, color string
}{
	{[]string{"插座", "插排", "outlet", "socket", "plug"}, "plug", "blue"}, // a fan's plug is a plug
	{[]string{"排气", "换气", "风扇", "吊扇", "扇", "fan"}, "fan", "teal"},
	{[]string{"空调", "冷气", "aircon", "air con", "a/c"}, "snow", "blue"},
	{[]string{"电视", "tv", "television"}, "tv", "purple"},
	{[]string{"灯", "light", "lamp"}, "light", "amber"},
}

func nameLook(name string) (icon, color string, ok bool) {
	name = strings.ToLower(name)
	for _, l := range nameLooks {
		for _, w := range l.words {
			if strings.Contains(name, w) {
				return l.icon, l.color, true
			}
		}
	}
	return "", "", false
}

// lookOf is s's icon and color: a switch's by what its name says it drives (a wall switch's relay for a light or a
// fan), else its kind's (entityIcon, entityColor).
func lookOf(hm *home, s State) (icon, color string) {
	icon, color = entityIcon(s), entityColor(s)
	if d := s.Domain(); (d == "switch" || d == "input_boolean") && icon == "power" {
		if i, c, ok := nameLook(shortName(hm, s)); ok {
			return i, c
		}
	}
	return icon, color
}

// remote is a device that is only buttons (an IR remote's air conditioner or TV, learned or built in): it has no
// state, so its tile presses.
type remote struct {
	name    string
	area    *Area
	on, off string  // its power buttons
	more    []State // its other buttons, temperature and volume first (buttonRank)
	last    *State  // the button pressed last (a button's state is when)
	lastAt  time.Time
	buttons int
}

var (
	powerOnRE  = regexp.MustCompile(`(?i)(^|_)(turn_on|power_on)(_|$)`)
	powerOffRE = regexp.MustCompile(`(?i)(^|_)(turn_off|power_off)(_|$)`)
)

// powerButton says whether a button turns its device on or off, by its id or the last word of its name.
func powerButton(hm *home, s State) (on, off bool) {
	obj := strings.TrimPrefix(s.EntityID, "button.")
	n := strings.Fields(strings.ToLower(shortName(hm, s)))
	last := ""
	if len(n) > 0 {
		last = n[len(n)-1]
	}
	on = powerOnRE.MatchString(obj) || slices.Contains([]string{"开机", "打开", "开启", "on"}, last)
	off = powerOffRE.MatchString(obj) || slices.Contains([]string{"关机", "关闭", "off"}, last)
	return on && !off, off && !on
}

// remotes are an area's remotes: devices whose entities are buttons (and the numbers and selects that go with them),
// with a button that turns them on or off.
func remotes(hm *home, reg *Registry, ar *Area) []remote {
	if reg == nil || ar == nil {
		return nil
	}
	var out []remote
	seen := map[string]bool{}
	for _, e := range ar.Entities {
		re, ok := reg.Entities[e]
		if !ok || re.DeviceID == "" || seen[re.DeviceID] {
			continue
		}
		seen[re.DeviceID] = true
		rm := remote{name: reg.Devices[re.DeviceID].title(), area: ar}
		only := true
		for _, sib := range reg.ByDevice[re.DeviceID] {
			s, ok := hm.ByID[sib]
			if !ok || !hm.visible(sib) || reg.Entities[sib].left() != "" && reg.Entities[sib].EntityCategory == "" {
				continue // hidden or disabled
			}
			switch s.Domain() {
			case "button":
				if reg.Entities[sib].EntityCategory != "" || offline(s) { // a button never pressed is unknown
					continue
				}
				rm.buttons++
				switch on, off := powerButton(hm, s); {
				case on && rm.on == "":
					rm.on = sib
				case off && rm.off == "":
					rm.off = sib
				default:
					rm.more = append(rm.more, s)
				}
				if at, err := time.Parse(time.RFC3339Nano, s.State); err == nil && at.After(rm.lastAt) {
					st := s
					rm.last, rm.lastAt = &st, at
				}
			case "number", "select", "sensor", "binary_sensor", "event":
			default:
				only = false
			}
		}
		if only && rm.on != "" && rm.name != "" {
			sort.SliceStable(rm.more, func(i, j int) bool { return buttonRank(hm, rm.more[i]) < buttonRank(hm, rm.more[j]) })
			out = append(out, rm)
		}
	}
	return out
}

// buttonRank orders a remote's other buttons for its menu: what one changes most (temperature, volume) first, up
// before down, then the rest as they were.
func buttonRank(hm *home, s State) int {
	w := strings.ToLower(buttonWord(hm, s) + " " + s.EntityID)
	rank := 4
	for i, words := range [][]string{{"温度", "temperature"}, {"音量", "volume"}} {
		if slices.ContainsFunc(words, func(x string) bool { return strings.Contains(w, x) }) {
			rank = i * 2
			break
		}
	}
	if rank < 4 && (strings.Contains(w, "-") || strings.Contains(w, "down")) {
		rank++
	}
	return rank
}

// remoteMore is how many of a remote's other buttons its tile's menu takes besides power on and off.
const remoteMore = 2

// remoteTile is a remote as a tile: its name and what it is, the last button pressed and when (a remote cannot say
// whether the device is on); a tap opens its power-on button (the page lists the device's other buttons), its menu
// presses power on, off and the first others.
func (a *app) remoteTile(hm *home, rm remote, loc *time.Location) wire.Tile {
	name := rm.name
	if area := rm.area.Name; area != "" && !hm.ByType && strings.HasPrefix(name, area) {
		if rest := strings.TrimSpace(strings.TrimPrefix(name, area)); len([]rune(rest)) >= 2 {
			name = rest
		}
	}
	icon, color, ok := nameLook(rm.name)
	if !ok {
		icon, color = "power", "gray"
	}
	sub := "遥控 · 不知道开没开"
	if rm.last != nil {
		at := rm.lastAt.In(loc)
		when := at.Format("01-02 15:04")
		if y, m, d := time.Now().In(loc).Date(); at.Year() == y && at.Month() == m && at.Day() == d {
			when = at.Format("15:04")
		}
		sub = fmt.Sprintf("遥控 · %s %s", when, clip(buttonWord(hm, *rm.last), 12))
	}
	t := wire.Tile{Key: ref("r:", rm.on), Title: clip(name, 40), Subtitle: sub, Icon: icon, Color: color, Action: openAction(rm.on)}
	if a.canWrite("button", false) != nil {
		return t
	}
	t.Actions = append(t.Actions, wire.RowAction{Title: "开机", Icon: "power", Action: *rowDo(rm.on, "press")})
	if rm.off != "" {
		t.Actions = append(t.Actions, wire.RowAction{Title: "关机", Icon: "power", Action: *rowDo(rm.off, "press")})
	}
	for _, s := range rm.more[:min(remoteMore, len(rm.more))] {
		t.Actions = append(t.Actions, wire.RowAction{Title: clip(buttonWord(hm, s), 20), Action: *rowDo(s.EntityID, "press")})
	}
	return t
}

// buttonWord is what a remote's button says: the last word of its name (红外空调控制 温度+ → 温度+).
func buttonWord(hm *home, s State) string {
	f := strings.Fields(shortName(hm, s))
	if len(f) == 0 {
		return s.Name()
	}
	return f[len(f)-1]
}

// powerDevice says e belongs to a device that is a light or a switch (a relay, a plug, a bulb): its temperature is
// its own chip's, not the room's. A device that also heats, cools or moves air (a climate, a fan, a humidifier) reads
// the room.
func powerDevice(reg *Registry, e string) bool {
	if reg == nil {
		return false
	}
	dev := reg.Entities[e].DeviceID
	if dev == "" {
		return false
	}
	power := false
	for _, sib := range reg.ByDevice[dev] {
		d, _, _ := strings.Cut(sib, ".")
		switch d {
		case "climate", "fan", "humidifier", "water_heater":
			return false
		case "light", "switch":
			if reg.Entities[sib].left() == "" {
				power = true
			}
		}
	}
	return power
}

// roomGrids are 设备 room by room (UI version 6, a home of at most a grid's devices): a section per area with
// something in it, its devices in Home Assistant's order (a tile stays where it is when switched) and its remotes
// after them; the devices in no area last.
func (a *app) roomGrids(hm *home, reg *Registry, f *homeFacts, v *wire.View) {
	loc := a.loc()
	by := map[*Area][]wire.Tile{}
	on := map[*Area]int{}
	var loose []wire.Tile
	looseOn := 0
	for _, s := range f.devices {
		if !slices.Contains(overviewDomains, s.Domain()) || offline(s) {
			continue
		}
		t := a.deviceTile(hm, s, false)
		if ar := hm.AreaOf[s.EntityID]; ar != nil {
			by[ar] = append(by[ar], t)
		} else {
			loose = append(loose, t)
		}
	}
	// what is on, counted as the figures and the area tiles count it (a siren too, which has no tile here)
	looseOn = len(f.on)
	for _, af := range f.areas {
		on[af.ar] = len(af.on)
		looseOn -= len(af.on)
	}
	title := func(name string, n int) string {
		if n > 0 {
			return fmt.Sprintf("%s · %d 开着", name, n)
		}
		return name
	}
	for i := range hm.Areas {
		ar := &hm.Areas[i]
		tiles := by[ar]
		for _, rm := range remotes(hm, reg, ar) {
			tiles = append(tiles, a.remoteTile(hm, rm, loc))
		}
		if len(tiles) == 0 {
			continue
		}
		v.Sections = append(v.Sections, wire.Section{Title: title(ar.Name, on[ar]),
			Items: []wire.Component{{Type: "grid", Key: ref("room:", ar.ID), Tiles: tiles[:min(len(tiles), tilesMost)], MaxColumns: deviceColumns}}})
	}
	if len(loose) > 0 {
		v.Sections = append(v.Sections, wire.Section{Title: title("其他", looseOn),
			Items: []wire.Component{{Type: "grid", Key: "room:", Tiles: loose[:min(len(loose), tilesMost)], MaxColumns: deviceColumns}}})
	}
}

// byRoom says 设备 goes room by room: areas that are Home Assistant's, and no more devices than a grid takes.
func byRoom(hm *home, f *homeFacts) bool {
	if hm.ByType || len(hm.Areas) == 0 {
		return false
	}
	_, total := quickList(f, true)
	return total <= tilesMost
}
