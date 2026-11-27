package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Fixed/saved places: configured in Settings (home, company, places) and/or pinned in the in-app "常用地点" tab
// (stored atomically in DataDir/places.json with 0600 permissions).
//
// Extension Settings take precedence over UI-saved entries on duplicate names so Settings edits are never shadowed.

const uiPlacesMost = 30

// placeConfig matches the manifest's "configuration" JSON Schema.
type placeConfig struct {
	Home    string   `json:"home"`
	Company string   `json:"company"`
	Places  []string `json:"places"`
}

// fixedPlace is one named anchor (家, 公司, or a user-defined place) ready for assistant tools and the UI.
type fixedPlace struct {
	Name     string   `json:"name"`
	Target   string   `json:"target,omitempty"` // address, POI name, or optional label when HasCoord
	Lat      float64  `json:"lat,omitempty"`
	Lng      float64  `json:"lng,omitempty"`
	HasCoord bool     `json:"has_coord,omitempty"`
	Source   string   `json:"-"` // "config" or "ui"
	Aliases  []string `json:"-"`
}

func (fp fixedPlace) coord() point { return point{fp.Lat, fp.Lng} }

func (fp fixedPlace) summary() string {
	switch {
	case fp.HasCoord && fp.Target != "" && !strings.EqualFold(fp.Target, fp.Name):
		return fp.Target + " · WGS-84 " + fp.coord().String()
	case fp.HasCoord:
		return "WGS-84 " + fp.coord().String()
	case fp.Target != "" && !strings.EqualFold(fp.Target, fp.Name):
		return fp.Target
	default:
		return "按地名「" + fp.Name + "」解析"
	}
}

func (fp fixedPlace) sourceLabel() string {
	if fp.Source == "config" {
		return "扩展设置"
	}
	return "页面保存"
}

type storedPlaces struct {
	Places []fixedPlace `json:"places,omitempty"`
}

var (
	bareCoordRe = regexp.MustCompile(`^(-?\d+(?:\.\d+)?)\s*[,，]\s*(-?\d+(?:\.\d+)?)$`)
	pinnedRe    = regexp.MustCompile(`^(.*\S)\s+(-?\d+(?:\.\d+)?)\s*[,，]\s*(-?\d+(?:\.\d+)?)$`)

	homeGroup    = []string{"家", "home", "我家", "家里", "my home"}
	companyGroup = []string{"公司", "company", "work", "office", "单位", "办公室", "my office"}
)

func normalizePlaceKey(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

func defaultAliasesFor(name string) []string {
	k := normalizePlaceKey(name)
	for _, group := range [][]string{homeGroup, companyGroup} {
		matched := false
		for _, g := range group {
			if k == normalizePlaceKey(g) {
				matched = true
				break
			}
		}
		if matched {
			var out []string
			for _, g := range group {
				if normalizePlaceKey(g) != k {
					out = append(out, g)
				}
			}
			return out
		}
	}
	return nil
}

func canonicalSlotKey(name string) string {
	k := normalizePlaceKey(name)
	for _, g := range homeGroup {
		if k == normalizePlaceKey(g) {
			return "home"
		}
	}
	for _, g := range companyGroup {
		if k == normalizePlaceKey(g) {
			return "company"
		}
	}
	return k
}

// parseCoordOnly reports whether s is written purely as "lat,lng" (ASCII or Chinese comma).
func parseCoordOnly(s string) (point, bool, error) {
	s = strings.TrimSpace(s)
	m := bareCoordRe.FindStringSubmatch(s)
	if m == nil {
		return point{}, false, nil
	}
	lat, err1 := strconv.ParseFloat(m[1], 64)
	lng, err2 := strconv.ParseFloat(m[2], 64)
	p := point{lat, lng}
	if err1 != nil || err2 != nil || !p.valid() {
		return point{}, true, errors.New("坐标超出范围或格式不对：请填写 WGS-84「纬度,经度」（纬度 -90 到 90，经度 -180 到 180，不能是 0,0）")
	}
	return p, true, nil
}

// parseTarget reads an address/POI name, pure "lat,lng", or "<label/address> <lat>,<lng>".
func parseTarget(raw string) (target string, pt point, hasCoord bool, err error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", point{}, false, errors.New("地点内容不能为空")
	}
	if p, isCoord, cerr := parseCoordOnly(raw); isCoord {
		if cerr != nil {
			return "", point{}, false, cerr
		}
		return "", p, true, nil
	}
	if m := pinnedRe.FindStringSubmatch(raw); m != nil {
		label := strings.TrimSpace(m[1])
		lat, err1 := strconv.ParseFloat(m[2], 64)
		lng, err2 := strconv.ParseFloat(m[3], 64)
		p := point{lat, lng}
		if err1 != nil || err2 != nil || !p.valid() {
			return "", point{}, false, fmt.Errorf("「%s」后面的坐标不对：纬度在 -90 到 90 之间，经度在 -180 到 180 之间（WGS-84，不能是 0,0）", label)
		}
		return label, p, true, nil
	}
	return raw, point{}, false, nil
}

// cutPlaceColon splits on the first ASCII ':' or full-width '：'.
func cutPlaceColon(s string) (before, after string, ok bool) {
	idx := strings.IndexAny(s, ":：")
	if idx < 0 {
		return s, "", false
	}
	_, width := utf8.DecodeRuneInString(s[idx:])
	return s[:idx], s[idx+width:], true
}

// parsePlaceEntry reads one item from configuration.places ("名称: 地址/地名/坐标", "名称 纬度,经度", or "地名").
func parsePlaceEntry(raw string) (fixedPlace, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fixedPlace{}, errors.New("常用地点里不能有空行")
	}
	if left, right, ok := cutPlaceColon(raw); ok {
		name := strings.TrimSpace(left)
		rhs := strings.TrimSpace(right)
		if name == "" {
			return fixedPlace{}, fmt.Errorf("常用地点「%s」缺少名称，请在冒号前写名称", raw)
		}
		if rhs == "" {
			return fixedPlace{}, fmt.Errorf("常用地点「%s」缺少地址、地名或坐标", name)
		}
		if _, isCoord, _ := parseCoordOnly(name); isCoord {
			return fixedPlace{}, fmt.Errorf("常用地点名称「%s」不能直接写经纬度，请起个名字", name)
		}
		target, pt, hasCoord, err := parseTarget(rhs)
		if err != nil {
			return fixedPlace{}, fmt.Errorf("常用地点「%s」：%w", name, err)
		}
		return fixedPlace{
			Name:     name,
			Target:   target,
			Lat:      pt.Lat,
			Lng:      pt.Lng,
			HasCoord: hasCoord,
			Source:   "config",
			Aliases:  defaultAliasesFor(name),
		}, nil
	}
	if _, isCoord, cerr := parseCoordOnly(raw); isCoord {
		if cerr != nil {
			return fixedPlace{}, cerr
		}
		return fixedPlace{}, fmt.Errorf("常用地点「%s」只有坐标没有名称，请写成「名称: %s」", raw, raw)
	}
	target, pt, hasCoord, err := parseTarget(raw)
	if err != nil {
		return fixedPlace{}, err
	}
	if hasCoord {
		return fixedPlace{
			Name:     target,
			Lat:      pt.Lat,
			Lng:      pt.Lng,
			HasCoord: true,
			Source:   "config",
			Aliases:  defaultAliasesFor(target),
		}, nil
	}
	return fixedPlace{
		Name:    raw,
		Target:  raw,
		Source:  "config",
		Aliases: defaultAliasesFor(raw),
	}, nil
}

func parseConfigPlaces(cfg placeConfig, strict bool, logf func(string, ...any)) ([]fixedPlace, error) {
	var out []fixedPlace
	seen := map[string]string{}

	addOne := func(fp fixedPlace) error {
		key := canonicalSlotKey(fp.Name)
		if prev, dup := seen[key]; dup {
			return fmt.Errorf("常用地点名称重复：「%s」和「%s」重名了", fp.Name, prev)
		}
		seen[key] = fp.Name
		out = append(out, fp)
		return nil
	}

	if v := strings.TrimSpace(cfg.Home); v != "" {
		target, pt, hasCoord, err := parseTarget(v)
		if err != nil {
			if strict {
				return nil, fmt.Errorf("「家」设置不对：%w", err)
			}
			if logf != nil {
				logf("amap: ignored home config: %v", err)
			}
		} else {
			_ = addOne(fixedPlace{
				Name:     "家",
				Target:   target,
				Lat:      pt.Lat,
				Lng:      pt.Lng,
				HasCoord: hasCoord,
				Source:   "config",
				Aliases:  defaultAliasesFor("家"),
			})
		}
	}

	if v := strings.TrimSpace(cfg.Company); v != "" {
		target, pt, hasCoord, err := parseTarget(v)
		if err != nil {
			if strict {
				return nil, fmt.Errorf("「公司」设置不对：%w", err)
			}
			if logf != nil {
				logf("amap: ignored company config: %v", err)
			}
		} else {
			_ = addOne(fixedPlace{
				Name:     "公司",
				Target:   target,
				Lat:      pt.Lat,
				Lng:      pt.Lng,
				HasCoord: hasCoord,
				Source:   "config",
				Aliases:  defaultAliasesFor("公司"),
			})
		}
	}

	for _, raw := range cfg.Places {
		if !strict && strings.TrimSpace(raw) == "" {
			continue
		}
		fp, err := parsePlaceEntry(raw)
		if err != nil {
			if strict {
				return nil, err
			}
			if logf != nil {
				logf("amap: ignored place entry %q: %v", raw, err)
			}
			continue
		}
		if err := addOne(fp); err != nil {
			if strict {
				return nil, err
			}
			if logf != nil {
				logf("amap: skipped duplicate place %q", fp.Name)
			}
		}
	}
	return out, nil
}

func validatePlaceConfigRaw(raw json.RawMessage) error {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return nil
	}
	var cfg placeConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return errors.New("常用地点配置读不懂")
	}
	_, err := parseConfigPlaces(cfg, true, nil)
	return err
}

func (a *app) initPlaces(cfg placeConfig, dataDir string) {
	a.placesSaveMu.Lock()
	defer a.placesSaveMu.Unlock()

	cps, _ := parseConfigPlaces(cfg, false, a.logf)

	var ups []fixedPlace
	var file string
	if dataDir != "" {
		file = filepath.Join(dataDir, "places.json")
		ups = readUIPlaces(file, a.logf)
	}

	a.placesMu.Lock()
	a.placesFile = file
	a.configPlaces = cps
	a.uiPlaces = ups
	a.placesMu.Unlock()
}

func readUIPlaces(file string, logf func(string, ...any)) []fixedPlace {
	b, err := os.ReadFile(file)
	if err != nil {
		return nil
	}
	var st storedPlaces
	if err := json.Unmarshal(b, &st); err != nil {
		if logf != nil {
			logf("amap: places.json unread: %v", err)
		}
		return nil
	}
	var out []fixedPlace
	seen := map[string]bool{}
	for _, fp := range st.Places {
		fp.Name = strings.TrimSpace(fp.Name)
		fp.Target = strings.TrimSpace(fp.Target)
		if fp.Name == "" {
			continue
		}
		if fp.HasCoord && !fp.coord().valid() {
			continue
		}
		if !fp.HasCoord && fp.Target == "" {
			fp.Target = fp.Name
		}
		k := canonicalSlotKey(fp.Name)
		if seen[k] {
			continue
		}
		seen[k] = true
		fp.Source = "ui"
		fp.Aliases = defaultAliasesFor(fp.Name)
		out = append(out, fp)
	}
	return out
}

func writeUIPlacesFile(file string, list []fixedPlace) error {
	if file == "" {
		return nil
	}
	b, err := json.Marshal(storedPlaces{Places: list})
	if err != nil {
		return err
	}
	tmp := file + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, file); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// fixedPlaces returns Extension Settings places followed by UI-saved places (config wins on duplicate names).
func (a *app) fixedPlaces() []fixedPlace {
	a.placesMu.Lock()
	defer a.placesMu.Unlock()

	out := make([]fixedPlace, 0, len(a.configPlaces)+len(a.uiPlaces))
	seen := map[string]bool{}
	seenSlot := map[string]bool{}
	for _, fp := range a.configPlaces {
		out = append(out, fp)
		seen[normalizePlaceKey(fp.Name)] = true
		if s := canonicalSlotKey(fp.Name); s == "home" || s == "company" {
			seenSlot[s] = true
		}
	}
	for _, fp := range a.uiPlaces {
		if seen[normalizePlaceKey(fp.Name)] {
			continue
		}
		if s := canonicalSlotKey(fp.Name); (s == "home" || s == "company") && seenSlot[s] {
			continue
		}
		out = append(out, fp)
	}
	return out
}

// matchFixedPlace looks up query across configured and UI-saved places (exact name match first, then aliases).
func (a *app) matchFixedPlace(query string) (fixedPlace, bool) {
	k := normalizePlaceKey(query)
	if k == "" {
		return fixedPlace{}, false
	}
	fps := a.fixedPlaces()
	for _, fp := range fps {
		if normalizePlaceKey(fp.Name) == k {
			return fp, true
		}
	}
	for _, fp := range fps {
		for _, al := range fp.Aliases {
			if normalizePlaceKey(al) == k {
				return fp, true
			}
		}
	}
	return fixedPlace{}, false
}

// findCommutePair returns the configured/saved "家" and "公司" places when both exist.
func (a *app) findCommutePair() (home, company fixedPlace, ok bool) {
	home, hOK := a.matchFixedPlace("家")
	company, cOK := a.matchFixedPlace("公司")
	return home, company, hOK && cOK
}

// saveUIPlace adds or updates a UI-saved place in places.json (refusing names owned by Extension Settings).
func (a *app) saveUIPlace(rawName, rawTarget string) (fp fixedPlace, updated bool, err error) {
	name := strings.TrimSpace(rawName)
	targetInput := strings.TrimSpace(rawTarget)
	if name == "" {
		return fixedPlace{}, false, errors.New("请填写地点名称（如 家、公司、机场）")
	}
	if utf8.RuneCountInString(name) > 40 {
		return fixedPlace{}, false, errors.New("地点名称请控制在 40 个字以内")
	}
	if _, isCoord, _ := parseCoordOnly(name); isCoord {
		return fixedPlace{}, false, errors.New("地点名称不能直接写经纬度，请起一个方便识别的名字")
	}
	if targetInput == "" {
		return fixedPlace{}, false, errors.New("请填写地址、地名或 WGS-84 坐标")
	}
	target, pt, hasCoord, err := parseTarget(targetInput)
	if err != nil {
		return fixedPlace{}, false, err
	}

	fp = fixedPlace{
		Name:     name,
		Target:   target,
		Lat:      pt.Lat,
		Lng:      pt.Lng,
		HasCoord: hasCoord,
		Source:   "ui",
		Aliases:  defaultAliasesFor(name),
	}

	slot := canonicalSlotKey(name)
	a.placesSaveMu.Lock()
	defer a.placesSaveMu.Unlock()

	a.placesMu.Lock()
	for _, cp := range a.configPlaces {
		if canonicalSlotKey(cp.Name) == slot {
			a.placesMu.Unlock()
			return fixedPlace{}, false, fmt.Errorf("「%s」已在扩展设置里配置，请到 设置 › 扩展 › 高德地图 修改，或换一个名字保存", cp.Name)
		}
	}
	idx := -1
	for i, up := range a.uiPlaces {
		if canonicalSlotKey(up.Name) == slot {
			idx = i
			break
		}
	}
	next := append([]fixedPlace(nil), a.uiPlaces...)
	if idx >= 0 {
		next[idx] = fp
		updated = true
	} else {
		if len(next) >= uiPlacesMost {
			a.placesMu.Unlock()
			return fixedPlace{}, false, fmt.Errorf("页面最多保存 %d 个常用地点，请先删掉几个再添加", uiPlacesMost)
		}
		next = append(next, fp)
	}
	file := a.placesFile
	a.placesMu.Unlock()

	if err := writeUIPlacesFile(file, next); err != nil {
		if a.logf != nil {
			a.logf("amap: places not saved: %v", err)
		}
		return fixedPlace{}, false, errors.New("常用地点保存失败，请检查数据目录是否可写后重试")
	}

	a.placesMu.Lock()
	a.uiPlaces = next
	a.placesMu.Unlock()
	return fp, updated, nil
}

func (a *app) deleteUIPlace(name string) (bool, error) {
	k := normalizePlaceKey(name)
	if k == "" {
		return false, nil
	}
	a.placesSaveMu.Lock()
	defer a.placesSaveMu.Unlock()

	a.placesMu.Lock()
	idx := -1
	for i, up := range a.uiPlaces {
		if normalizePlaceKey(up.Name) == k {
			idx = i
			break
		}
	}
	if idx < 0 {
		a.placesMu.Unlock()
		return false, nil
	}
	next := make([]fixedPlace, 0, len(a.uiPlaces)-1)
	next = append(next, a.uiPlaces[:idx]...)
	next = append(next, a.uiPlaces[idx+1:]...)
	file := a.placesFile
	a.placesMu.Unlock()

	if err := writeUIPlacesFile(file, next); err != nil {
		if a.logf != nil {
			a.logf("amap: places not saved: %v", err)
		}
		return false, errors.New("常用地点删除失败，请检查数据目录是否可写后重试")
	}

	a.placesMu.Lock()
	a.uiPlaces = next
	a.placesMu.Unlock()
	return true, nil
}

// removeUIPlace deletes a UI-saved place by exact name.
func (a *app) removeUIPlace(name string) bool {
	removed, err := a.deleteUIPlace(name)
	return err == nil && removed
}
