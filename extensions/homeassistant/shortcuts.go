package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nwverse/persona/sdk/go/ext"
	"github.com/nwverse/persona/sdk/go/wire"
)

// Shortcuts (#639): one-tap combinations the user makes on the page (回家, 睡觉), each a Home Assistant script
// (script.persona_<id>) written through its config API, so Home Assistant runs it and the assistant can too. The
// form offers what the page can switch (the devices whose tile has a switch) and the IR remotes' power buttons; the
// script turns off, then on, then presses the buttons a second apart (one IR blaster sends one code at a time). Only a
// script the form could have written is a shortcut (shortcutOf): one edited in Home Assistant into anything else is
// drawn as any other script, which asks before it runs.

const (
	shortcutPrefix  = "persona_"
	shortcutsMost   = 12
	shortcutNameMax = 20
	shortcutMost    = 30 // devices and buttons in one shortcut
	shortcutTTL     = 5 * time.Minute
)

var (
	shortcutKeyRE = regexp.MustCompile(`^persona_[a-z0-9_]{1,40}$`)
	// pressGap is the wait between two remote buttons.
	pressGap = map[string]any{"seconds": 1}
	// shortcutSaved is how long saving waits for Home Assistant to load the script (it reloads them after answering).
	shortcutSaved = 4 * time.Second
	// popRedraw is when the page below a form is asked to draw again after the form goes back: an app may show it as
	// it was (the web keeps a page of a mixed stack mounted), and the shortcut saved or deleted would be missing.
	popRedraw = 600 * time.Millisecond
)

// shortcut is what one does, in the order it does it.
type shortcut struct {
	Key   string
	Name  string
	Off   []string
	On    []string
	Press []string
}

func (sc *shortcut) entity() string { return "script." + sc.Key }

func (sc *shortcut) entities() []string {
	return slices.Concat(sc.Off, sc.On, sc.Press)
}

// summary is the tile's line: 关 3 · 开 2 · 遥控 1.
func (sc *shortcut) summary() string {
	var out []string
	for _, p := range []struct {
		word string
		n    int
	}{{"关", len(sc.Off)}, {"开", len(sc.On)}, {"遥控", len(sc.Press)}} {
		if p.n > 0 {
			out = append(out, fmt.Sprintf("%s %d", p.word, p.n))
		}
	}
	return strings.Join(out, " · ")
}

// shortcutLooks are a shortcut's icon and color by its name, with the Home Assistant icon its script gets; the first
// match wins, a star otherwise.
var shortcutLooks = []struct {
	words            []string
	icon, color, mdi string
}{
	{[]string{"回家", "到家", "home"}, "home", "amber", "mdi:home"},
	{[]string{"睡", "晚安", "夜", "night", "sleep"}, "moon", "indigo", "mdi:weather-night"},
	{[]string{"起床", "早安", "早上", "morning"}, "sun", "orange", "mdi:weather-sunny"},
	{[]string{"离家", "出门", "外出", "away", "leave"}, "location", "blue", "mdi:exit-run"},
	{[]string{"电视", "观影", "电影", "影院", "tv", "movie"}, "tv", "purple", "mdi:television"},
	{[]string{"音乐", "music"}, "music", "pink", "mdi:music"},
	{[]string{"灯", "light"}, "light", "amber", "mdi:lightbulb"},
}

func shortcutLook(name string) (icon, color, mdi string) {
	n := strings.ToLower(name)
	for _, l := range shortcutLooks {
		if slices.ContainsFunc(l.words, func(w string) bool { return strings.Contains(n, w) }) {
			return l.icon, l.color, l.mdi
		}
	}
	return "star", "amber", "mdi:star"
}

// config is the script Home Assistant keeps for sc.
func (sc *shortcut) config() map[string]any {
	var steps []any
	group := func(ids []string, service string) {
		var order []string
		by := map[string][]string{}
		for _, e := range ids {
			d, _, _ := strings.Cut(e, ".")
			if _, ok := by[d]; !ok {
				order = append(order, d)
			}
			by[d] = append(by[d], e)
		}
		for _, d := range order {
			steps = append(steps, map[string]any{"action": d + "." + service, "target": map[string]any{"entity_id": by[d]},
				"continue_on_error": true})
		}
	}
	group(sc.Off, "turn_off")
	group(sc.On, "turn_on")
	for i, e := range sc.Press {
		if i > 0 {
			steps = append(steps, map[string]any{"delay": pressGap})
		}
		steps = append(steps, map[string]any{"action": "button.press", "target": map[string]any{"entity_id": e}, "continue_on_error": true})
	}
	_, _, mdi := shortcutLook(sc.Name)
	return map[string]any{"alias": sc.Name, "description": "在 Persona 的「家」里建的快捷", "icon": mdi, "mode": "single", "sequence": steps}
}

// shortcutOf reads a script's config as a shortcut: false unless it is one the form could have written (the
// steps config writes, nothing else).
func shortcutOf(key string, c map[string]any) (*shortcut, bool) {
	if !shortcutKeyRE.MatchString(key) || c == nil {
		return nil, false
	}
	for k := range c {
		if !slices.Contains([]string{"alias", "description", "icon", "mode", "sequence"}, k) {
			return nil, false
		}
	}
	name, _ := c["alias"].(string)
	steps, ok := c["sequence"].([]any)
	if strings.TrimSpace(name) == "" || !ok || len(steps) == 0 {
		return nil, false
	}
	sc := &shortcut{Key: key, Name: name}
	for _, raw := range steps {
		step, ok := raw.(map[string]any)
		if !ok {
			return nil, false
		}
		if _, ok := step["delay"]; ok {
			if len(step) != 1 {
				return nil, false
			}
			continue
		}
		action, _ := step["action"].(string)
		if action == "" {
			action, _ = step["service"].(string) // Home Assistant's name for it before 2024.8
		}
		for k := range step {
			if !slices.Contains([]string{"action", "service", "target", "continue_on_error"}, k) {
				return nil, false
			}
		}
		target, _ := step["target"].(map[string]any)
		if len(target) != 1 {
			return nil, false
		}
		var ids []string
		switch v := target["entity_id"].(type) {
		case string:
			ids = []string{v}
		case []any:
			for _, x := range v {
				s, ok := x.(string)
				if !ok {
					return nil, false
				}
				ids = append(ids, s)
			}
		default:
			return nil, false
		}
		d, service, _ := strings.Cut(action, ".")
		for _, e := range ids {
			if ed, _, _ := strings.Cut(e, "."); !entityRE.MatchString(e) || ed != d {
				return nil, false
			}
		}
		switch {
		case slices.Contains(powerDomains, d) && service == "turn_off":
			sc.Off = append(sc.Off, ids...)
		case slices.Contains(powerDomains, d) && service == "turn_on":
			sc.On = append(sc.On, ids...)
		case d == "button" && service == "press":
			sc.Press = append(sc.Press, ids...)
		default:
			return nil, false
		}
	}
	if len(sc.entities()) == 0 {
		return nil, false
	}
	return sc, true
}

// scriptCfg is a script's config as last read: nil when it has none Home Assistant would give (not one of its UI
// scripts).
type scriptCfg struct {
	cfg map[string]any
	at  time.Time
}

// scriptConfig reads a UI script's config by its key (nil, nil: none), kept shortcutTTL unless fresh.
func (h *ha) scriptConfig(ctx context.Context, key string, fresh bool) (map[string]any, error) {
	h.mu.Lock()
	c, ok := h.scripts[key]
	h.mu.Unlock()
	if ok && !fresh && time.Since(c.at) < shortcutTTL {
		return c.cfg, nil
	}
	var cfg map[string]any
	err := h.do(ctx, http.MethodGet, "/api/config/script/config/"+url.PathEscape(key), nil, &cfg)
	if errors.Is(err, errNoEntity) {
		cfg, err = nil, nil
	}
	if err != nil {
		return nil, err
	}
	h.mu.Lock()
	if h.scripts == nil {
		h.scripts = map[string]scriptCfg{}
	}
	h.scripts[key] = scriptCfg{cfg: cfg, at: time.Now()}
	h.mu.Unlock()
	return cfg, nil
}

// forgetScript drops what is kept of a script and of the states, after writing it.
func (h *ha) forgetScript(key string) {
	h.mu.Lock()
	delete(h.scripts, key)
	h.statesAt = time.Time{}
	h.mu.Unlock()
}

// shortcuts are the home's shortcuts, oldest first (a key starts with when it was made), at most shortcutsMost; each
// script's config is read once per shortcutTTL.
func (a *app) shortcuts(ctx context.Context, hm *home) []*shortcut {
	h, _, _ := a.client()
	var out []*shortcut
	for _, s := range hm.States {
		key, ok := strings.CutPrefix(s.EntityID, "script.")
		if !ok || !strings.HasPrefix(key, shortcutPrefix) || !hm.visible(s.EntityID) {
			continue
		}
		cfg, err := h.scriptConfig(ctx, key, false)
		if err != nil {
			continue
		}
		if sc, ok := shortcutOf(key, cfg); ok {
			out = append(out, sc)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out[:min(len(out), shortcutsMost)]
}

// canMakeShortcuts says the page may write scripts: a domain the settings allow, and areas read with an
// administrator's token (Home Assistant keeps its config API for administrators too).
func (a *app) canMakeShortcuts(hm *home) bool {
	return !hm.ByType && a.canWrite("script", false) == nil
}

// shortcutTiles are the shortcuts' tiles for the scenes row: a tap runs one at once (the user made it, and it only
// switches what the page offers); its menu edits or deletes it.
func (a *app) shortcutTiles(hm *home) []wire.Tile {
	var out []wire.Tile
	for _, sc := range hm.Shortcuts {
		icon, color, _ := shortcutLook(sc.Name)
		s := hm.ByID[sc.entity()]
		t := wire.Tile{Key: ref("my:", sc.Key), Title: clip(sc.Name, 40), Subtitle: clip(sc.summary(), 80), Icon: icon, Color: color,
			Active: s.State == "on", Action: &wire.Action{ID: "sc:run", Values: map[string]string{"sc": sc.Key}}}
		if s.State == "on" {
			t.Subtitle = "执行中"
		}
		t.Actions = []wire.RowAction{
			{Title: "编辑", Icon: "edit", Action: wire.Action{ID: "sc:edit", Push: &wire.Push{Title: "编辑快捷", State: ext.EncodeState(pageState{SC: sc.Key})}}},
			{Title: "删除", Icon: "trash", Role: "destructive", Action: wire.Action{ID: "sc:del", Values: map[string]string{"sc": sc.Key},
				Confirm: clip("删除快捷「"+sc.Name+"」？", 200)}},
		}
		out = append(out, t)
	}
	return out
}

// newShortcutTile opens the form for a new shortcut, its key made now (so saving twice writes one script).
func newShortcutTile() wire.Tile {
	return wire.Tile{Key: "my:new", Title: "新建", Subtitle: "快捷", Icon: "plus", Color: "gray",
		Action: &wire.Action{ID: "sc:new", Push: &wire.Push{Title: "新建快捷", State: ext.EncodeState(pageState{SC: newShortcutKey()})}}}
}

// newShortcutKey is persona_<seconds since 1970, hex><4 random hex>: they sort by when they were made.
func newShortcutKey() string {
	var b [2]byte
	_, _ = rand.Read(b[:])
	return fmt.Sprintf("%s%08x%s", shortcutPrefix, time.Now().Unix(), hex.EncodeToString(b[:]))
}

// shortcutChoices are what a shortcut may switch, as the form lists them: the devices whose tile has a switch, room by
// room in Home Assistant's order ("客厅 · 灯光1"), then those in no room; and each remote's power buttons ("客厅 · 空调
// 开机").
func (a *app) shortcutChoices(hm *home, reg *Registry) (devices, buttons []wire.Choice) {
	seen := map[string]bool{}
	add := func(s State, where string) {
		d := s.Domain()
		if seen[s.EntityID] || !slices.Contains(powerDomains, d) || !switchable(s) || a.canWrite(d, false) != nil ||
			!hm.visible(s.EntityID) || regHidden(reg, s.EntityID) || reg.skip(s.EntityID, hm.ByID) != "" {
			return
		}
		seen[s.EntityID] = true
		title := shortName(hm, s)
		if where != "" {
			title = where + " · " + title
		}
		if offline(s) {
			title += "（离线）"
		}
		devices = append(devices, wire.Choice{Value: s.EntityID, Title: clip(title, 60)})
	}
	for i := range hm.Areas {
		ar := &hm.Areas[i]
		for _, row := range deviceRows(hm, reg, ar) {
			add(row.s, ar.Name)
		}
		if a.canWrite("button", false) != nil {
			continue
		}
		for _, rm := range remotes(hm, reg, ar) {
			name := rm.name
			if rest := strings.TrimSpace(strings.TrimPrefix(name, ar.Name)); rest != name && utf8.RuneCountInString(rest) >= 2 {
				name = rest
			}
			buttons = append(buttons, wire.Choice{Value: rm.on, Title: clip(ar.Name+" · "+name+" 开机", 60)})
			if rm.off != "" {
				buttons = append(buttons, wire.Choice{Value: rm.off, Title: clip(ar.Name+" · "+name+" 关机", 60)})
			}
		}
	}
	for _, s := range hm.States {
		if hm.AreaOf[s.EntityID] == nil && slices.Contains(deviceDomains, s.Domain()) {
			add(s, "")
		}
	}
	return devices, buttons
}

// shortcutDraft is the form's values: what was submitted, else the shortcut's own.
type shortcutDraft struct {
	name           string
	off, on, press []string
}

func draftOf(sc *shortcut) shortcutDraft {
	if sc == nil {
		return shortcutDraft{}
	}
	return shortcutDraft{name: sc.Name, off: sc.Off, on: sc.On, press: sc.Press}
}

func draftFrom(values map[string]string) shortcutDraft {
	list := func(k string) []string {
		var out []string
		_ = json.Unmarshal([]byte(values[k]), &out)
		return out
	}
	return shortcutDraft{name: values["name"], off: list("off"), on: list("on"), press: list("press")}
}

// shortcutPage is the form: a name, what to turn off, on and which remote buttons to press, 保存 (and 删除 for one
// that exists). draft is what to show (the shortcut's own unless a save was refused).
func (a *app) shortcutPage(ctx context.Context, hm *home, reg *Registry, st pageState, draft *shortcutDraft, v *wire.View) {
	h, _, _ := a.client()
	v.RefreshAfter = 0
	cfg, err := h.scriptConfig(ctx, st.SC, false)
	if err != nil {
		v.Sections = []wire.Section{{Items: []wire.Component{{Type: "text", Text: err.Error(), Style: "secondary"}}}}
		return
	}
	sc, ok := shortcutOf(st.SC, cfg)
	exists := cfg != nil
	v.Title = "新建快捷"
	if exists {
		v.Title = "编辑快捷"
	}
	if exists && !ok {
		v.Sections = []wire.Section{{Items: []wire.Component{{Type: "text", Style: "secondary",
			Text: "这个快捷在 Home Assistant 里被改过，这里改不了了：到 Home Assistant 的脚本里改它，或者在「自动化」里运行它"}}}}
		return
	}
	d := draftOf(sc)
	if draft != nil {
		d = *draft
	}
	devices, buttons := a.shortcutChoices(hm, reg)
	// only what is offered (a refused draft may hold more; the hub refuses a field's value outside its choices)
	asJSON := func(ids []string, choices []wire.Choice) string {
		kept := []string{}
		for _, e := range ids {
			if slices.ContainsFunc(choices, func(c wire.Choice) bool { return c.Value == e }) && !slices.Contains(kept, e) {
				kept = append(kept, e)
			}
		}
		b, _ := json.Marshal(kept)
		return string(b)
	}
	items := []wire.Component{{Type: "field", Name: "name", Label: "名字", Kind: "text", Value: clip(d.name, 40), Placeholder: "回家、睡觉、看电视"}}
	if len(devices) > 0 {
		items = append(items,
			wire.Component{Type: "field", Name: "off", Label: "关掉", Kind: "multichoice", Value: asJSON(d.off, devices), Choices: devices},
			wire.Component{Type: "field", Name: "on", Label: "打开", Kind: "multichoice", Value: asJSON(d.on, devices), Choices: devices})
	}
	if len(buttons) > 0 {
		items = append(items, wire.Component{Type: "field", Name: "press", Label: "按遥控", Kind: "multichoice", Value: asJSON(d.press, buttons),
			Choices: buttons, Detail: "红外遥控只管发信号，不知道空调、电视收没收到；按几个时，一个隔一秒"})
	}
	if len(devices) == 0 && len(buttons) == 0 {
		v.Sections = []wire.Section{{Items: []wire.Component{{Type: "text", Style: "secondary",
			Text: "家里还没有能一键开关的设备：灯、开关、风扇和红外遥控接进 Home Assistant 后就能放进快捷"}}}}
		return
	}
	buttonsRow := []wire.Component{{Type: "button", Title: "保存", Action: &wire.Action{ID: "sc:save", Submit: true}}}
	if exists {
		buttonsRow = append(buttonsRow, wire.Component{Type: "button", Title: "删除", Role: "destructive",
			Action: &wire.Action{ID: "sc:del", Values: map[string]string{"sc": st.SC}, Confirm: clip("删除快捷「"+sc.Name+"」？", 200)}})
	}
	v.Sections = []wire.Section{
		{Items: []wire.Component{{Type: "header", Title: v.Title, Subtitle: "点一下就把选的设备一起关掉、打开"}}},
		{Items: items, Footer: "先关、再开、最后按遥控。存在 Home Assistant 里，是一个脚本：助手也能运行它"},
		{Items: buttonsRow},
	}
}

// saveShortcut checks the form's values against what it offers and writes the script; it waits until Home Assistant
// has it.
func (a *app) saveShortcut(ctx context.Context, key string, d shortcutDraft) (string, error) {
	if !shortcutKeyRE.MatchString(key) {
		return "", errUnknownOp
	}
	hm, err := a.home(ctx, statesFor)
	if err != nil {
		return "", err
	}
	if !a.canMakeShortcuts(hm) {
		return "", fmt.Errorf("建快捷要能写脚本：扩展设置的可操作类型里要有 script，令牌要是 Home Assistant 管理员的")
	}
	reg := a.pageRegistry(ctx)
	hm = hm.named(reg)
	h, _, _ := a.client()
	cfg, err := h.scriptConfig(ctx, key, true)
	if err != nil {
		return "", err
	}
	if _, ok := shortcutOf(key, cfg); cfg != nil && !ok {
		return "", fmt.Errorf("这个快捷在 Home Assistant 里被改过，这里改不了了")
	}
	name := strings.TrimSpace(oneLine(d.name))
	switch {
	case name == "":
		return "", fmt.Errorf("先起个名字")
	case utf8.RuneCountInString(name) > shortcutNameMax:
		return "", fmt.Errorf("名字最多 %d 个字", shortcutNameMax)
	}
	for _, other := range a.shortcuts(ctx, hm) {
		if other.Key != key && other.Name == name {
			return "", fmt.Errorf("已经有叫「%s」的快捷了", name)
		}
	}
	devices, buttons := a.shortcutChoices(hm, reg)
	titles := map[string]string{}
	for _, c := range devices {
		titles[c.Value] = c.Title
	}
	pressable := map[string]bool{}
	for _, c := range buttons {
		pressable[c.Value] = true
	}
	sc := &shortcut{Key: key, Name: name}
	seen := map[string]bool{}
	for _, part := range []struct {
		ids  []string
		dst  *[]string
		okay func(string) bool
	}{
		{d.off, &sc.Off, func(e string) bool { return titles[e] != "" }},
		{d.on, &sc.On, func(e string) bool { return titles[e] != "" }},
		{d.press, &sc.Press, func(e string) bool { return pressable[e] }},
	} {
		for _, e := range part.ids {
			if !part.okay(e) {
				return "", fmt.Errorf("「%s」不是这里能放进快捷的设备", clip(e, 80))
			}
			if seen[e] {
				return "", fmt.Errorf("「%s」不能既关又开", titles[e])
			}
			seen[e] = true
			*part.dst = append(*part.dst, e)
		}
	}
	switch n := len(sc.entities()); {
	case n == 0:
		return "", fmt.Errorf("至少选一个要关、要开的设备或者遥控按钮")
	case n > shortcutMost:
		return "", fmt.Errorf("一个快捷最多 %d 个设备和按钮", shortcutMost)
	}
	if _, err := h.send(ctx, http.MethodPost, "/api/config/script/config/"+url.PathEscape(key), sc.config()); err != nil {
		h.forgetScript(key)
		return "", err
	}
	h.forgetScript(key)
	a.waitScript(ctx, key, func(s State, ok bool) bool { return ok && s.Name() == name })
	return "已保存「" + name + "」", nil
}

// deleteShortcut deletes the script, only one that is a shortcut.
func (a *app) deleteShortcut(ctx context.Context, key string) (string, error) {
	h, _, _ := a.client()
	if !shortcutKeyRE.MatchString(key) {
		return "", errUnknownOp
	}
	if err := a.canWrite("script", false); err != nil {
		return "", err
	}
	cfg, err := h.scriptConfig(ctx, key, true)
	if err != nil {
		return "", err
	}
	sc, ok := shortcutOf(key, cfg)
	if !ok {
		return "", fmt.Errorf("没有这个快捷了")
	}
	_, err = h.send(ctx, http.MethodDelete, "/api/config/script/config/"+url.PathEscape(key), nil)
	h.forgetScript(key)
	if err != nil {
		return "", err
	}
	a.waitScript(ctx, key, func(_ State, ok bool) bool { return !ok })
	return "已删除「" + sc.Name + "」", nil
}

// waitScript waits, up to shortcutSaved, until Home Assistant's script.<key> is as done says (it reloads scripts
// after answering a write).
func (a *app) waitScript(ctx context.Context, key string, done func(State, bool) bool) {
	h, _, _ := a.client()
	until := time.Now().Add(shortcutSaved)
	for {
		s, err := h.State(ctx, "script."+key)
		if err != nil && !errors.Is(err, errNoEntity) {
			return
		}
		if done(s, err == nil) || time.Now().After(until) || ctx.Err() != nil {
			h.forgetScript(key)
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(200 * time.Millisecond):
		}
	}
}

// runShortcut starts a shortcut: its config read again, and every device and button in it still one the form offers.
// Home Assistant runs it (script.turn_on returns at once; a remote's buttons take a second each).
func (a *app) runShortcut(ctx context.Context, key string) (string, error) {
	h, _, _ := a.client()
	if !shortcutKeyRE.MatchString(key) {
		return "", errUnknownOp
	}
	if err := a.canWrite("script", false); err != nil {
		return "", err
	}
	cfg, err := h.scriptConfig(ctx, key, true)
	if err != nil {
		return "", err
	}
	sc, ok := shortcutOf(key, cfg)
	if !ok {
		return "", fmt.Errorf("这个快捷在 Home Assistant 里被改过：到「自动化」里运行它")
	}
	hm, err := a.home(ctx, statesFor)
	if err != nil {
		return "", err
	}
	reg := a.pageRegistry(ctx)
	devices, buttons := a.shortcutChoices(hm.named(reg), reg)
	offered := map[string]bool{}
	for _, c := range slices.Concat(devices, buttons) {
		offered[c.Value] = true
	}
	for _, e := range sc.entities() {
		if !offered[e] {
			name := e
			if s, ok := hm.ByID[e]; ok {
				name = s.Name()
			}
			return "", fmt.Errorf("「%s」现在不能从这里开关了：先编辑这个快捷", clip(name, 40))
		}
	}
	if _, err := a.run(ctx, call{Domain: "script", Service: "turn_on", Entities: []string{sc.entity()}}); err != nil {
		return "", err
	}
	return "开始执行「" + sc.Name + "」", nil
}

// shortcutAction is sc:<op>: run (a tile's tap), save (the form, submitted), del (a tile's menu or the form), and new
// or edit for an app that opened no page itself.
func (a *app) shortcutAction(ctx context.Context, r *ext.UIRequest, st pageState, op string) (*wire.ActionResult, error) {
	key := r.Values["sc"]
	said := func(msg string, err error) string {
		if err != nil {
			return err.Error()
		}
		return msg
	}
	switch op {
	case "run":
		msg, err := a.runShortcut(ctx, key)
		return &wire.ActionResult{View: a.page(ctx, r, st, 0), Toast: said(msg, err)}, nil
	case "save":
		if st.SC == "" {
			return &wire.ActionResult{View: a.page(ctx, r, st, statesFor), Toast: errUnknownOp.Error()}, nil
		}
		d := draftFrom(r.Values)
		msg, err := a.saveShortcut(ctx, st.SC, d)
		if err != nil {
			return &wire.ActionResult{View: a.pageWith(ctx, r, st, statesFor, &d), Toast: err.Error()}, nil
		}
		a.redrawSoon()
		return &wire.ActionResult{Toast: msg, Pop: true}, nil
	case "del":
		if key == "" {
			key = st.SC
		}
		msg, err := a.deleteShortcut(ctx, key)
		if err == nil && st.SC != "" {
			a.redrawSoon()
			return &wire.ActionResult{Toast: msg, Pop: true}, nil
		}
		return &wire.ActionResult{View: a.page(ctx, r, st, 0), Toast: said(msg, err)}, nil
	case "new":
		st = pageState{SC: newShortcutKey()}
	case "edit":
		st = pageState{SC: key}
		st.clean()
	}
	return &wire.ActionResult{View: a.page(ctx, r, st, statesFor)}, nil
}

// redrawSoon asks the apps to draw the page again once a form's answer has taken it back (popRedraw).
func (a *app) redrawSoon() {
	a.mu.Lock()
	rpc := a.rpc
	a.mu.Unlock()
	if rpc == nil {
		return
	}
	time.AfterFunc(popRedraw, func() { _ = rpc.Invalidate(pageUI) })
}
