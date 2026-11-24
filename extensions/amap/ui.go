package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/nwverse/persona/sdk/go/ext"
	"github.com/nwverse/persona/sdk/go/wire"
)

// Declarative UI for Persona apps:
//   - amap (nav_page, icon "location"): interactive place search, route planning (with waypoints and one-tap commute),
//     static map viewer (served via ui.image with zoom and traffic toggles), saved places management ("常用地点"), city
//     weather and monthly quota dashboard.
//   - amap-card (overview_card): compact summary of monthly AMap usage and status, zero network calls on render.

const (
	tabSearch = "search"
	tabRoute  = "route"
	tabMap    = "map"
	tabPlaces = "places"
	tabUsage  = "usage"

	mapRefCap = 64
)

// uiState is carried in View.State across renders and actions (compact JSON keys, well under 16 KB).
type uiState struct {
	Tab         string `json:"t,omitempty"`
	Keywords    string `json:"kw,omitempty"`
	City        string `json:"c,omitempty"`
	CityOnly    bool   `json:"co,omitempty"`
	From        string `json:"f,omitempty"`
	To          string `json:"to,omitempty"`
	Waypoints   string `json:"wp,omitempty"`
	Mode        string `json:"m,omitempty"`
	Prefer      string `json:"p,omitempty"`
	Place       string `json:"pl,omitempty"`
	PlaceLabel  string `json:"plab,omitempty"`
	Markers     string `json:"mk,omitempty"`
	Zoom        int    `json:"z,omitempty"`
	Traffic     bool   `json:"tr,omitempty"`
	WeatherCity string `json:"wc,omitempty"`
	DraftName   string `json:"dn,omitempty"`
	DraftTarget string `json:"dt,omitempty"`
	PlaceErr    string `json:"pe,omitempty"`
	Run         bool   `json:"r,omitempty"`
}

func readUIState(r *ext.UIRequest) uiState {
	var st uiState
	_ = r.DecodeState(&st)
	switch st.Tab {
	case tabSearch, tabRoute, tabMap, tabPlaces, tabUsage:
	default:
		st.Tab = tabSearch
	}
	if st.Mode == "" {
		st.Mode = "driving"
	}
	if st.Prefer == "" {
		st.Prefer = "recommended"
	}
	if st.Zoom < 0 {
		st.Zoom = 0
	} else if st.Zoom > 17 {
		st.Zoom = 17
	}
	return st
}

// mapRefSpec is the server-built staticmap query behind a ui.image ref. Keeping the spec server-side ensures ui.image
// never takes arbitrary query strings from clients.
type mapRefSpec struct {
	query   url.Values
	traffic bool
}

type mapRefStore struct {
	mu    sync.Mutex
	items map[string]mapRefSpec
	order []string
}

func newMapRefStore() *mapRefStore {
	return &mapRefStore{items: map[string]mapRefSpec{}}
}

func (s *mapRefStore) put(q url.Values, traffic bool) string {
	cp := url.Values{}
	for k, v := range q {
		cp[k] = append([]string(nil), v...)
	}
	sum := sha256.Sum256([]byte(cp.Encode()))
	ref := "map:" + hex.EncodeToString(sum[:8])
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.items[ref]; !exists {
		if len(s.order) >= mapRefCap {
			evict := s.order[0]
			s.order = s.order[1:]
			delete(s.items, evict)
		}
		s.order = append(s.order, ref)
	}
	s.items[ref] = mapRefSpec{query: cp, traffic: traffic}
	return ref
}

func (s *mapRefStore) get(ref string) (mapRefSpec, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	spec, ok := s.items[ref]
	if !ok {
		return mapRefSpec{}, false
	}
	cp := url.Values{}
	for k, v := range spec.query {
		cp[k] = append([]string(nil), v...)
	}
	return mapRefSpec{query: cp, traffic: spec.traffic}, true
}

// renderCard renders the compact overview card (amap-card) from memory without spending quota.
func (a *app) renderCard(_ context.Context, _ *ext.UIRequest) (*wire.View, error) {
	if a.client.key == "" {
		return &wire.View{
			Title: "高德地图",
			Sections: []wire.Section{{
				Items: []wire.Component{{
					Type:     wire.ComponentEmpty,
					Icon:     "location",
					Title:    "还没配置高德 Key",
					Subtitle: "在 设置 › 扩展 › 高德地图 填入「Web服务」Key",
				}},
			}},
		}, nil
	}
	keyErr, _, u := a.client.state()
	if keyErr != nil {
		return &wire.View{
			Title: "高德地图",
			Sections: []wire.Section{{
				Items: []wire.Component{{
					Type:     wire.ComponentRow,
					Icon:     "warning",
					Title:    "高德 Key 异常",
					Subtitle: clip(keyWords[keyErr.code], 200),
					Badge:    &wire.Badge{Text: "需处理", Tone: wire.ToneDanger},
				}},
			}},
		}, nil
	}
	stats := usageStats(u)
	items := []wire.Component{{Type: wire.ComponentStatGrid, Stats: stats}}
	if pause := pausedSummary(u); pause != "" {
		items = append(items, wire.Component{
			Type:     wire.ComponentRow,
			Icon:     "clock",
			Title:    "部分服务额度暂停",
			Subtitle: pause,
			Badge:    &wire.Badge{Text: "限流中", Tone: wire.ToneCaution},
		})
	}
	return &wire.View{
		Title:    "高德地图",
		Sections: []wire.Section{{Title: "本月用量", Items: items}},
	}, nil
}

// renderPage renders the main nav_page (amap).
func (a *app) renderPage(ctx context.Context, r *ext.UIRequest) (*wire.View, error) {
	st := readUIState(r)
	return a.buildPage(ctx, st)
}

// actionPage handles form submits, tab switches, saved-place actions, map zoom/traffic toggles and POI selection.
func (a *app) actionPage(ctx context.Context, r *ext.UIRequest) (*wire.ActionResult, error) {
	st := readUIState(r)
	var toast string
	switch r.Action {
	case "tab":
		if t := strings.TrimSpace(r.Values["tab"]); t != "" {
			st.Tab = t
		}
		st.PlaceErr = ""
		st.Run = false
	case "search":
		st.Tab = tabSearch
		st.Keywords = strings.TrimSpace(r.Values["keywords"])
		st.City = strings.TrimSpace(r.Values["city"])
		st.CityOnly = parseBool(r.Values["city_only"])
		st.Run = st.Keywords != ""
	case "route":
		st.Tab = tabRoute
		st.From = strings.TrimSpace(r.Values["from"])
		st.To = strings.TrimSpace(r.Values["to"])
		st.Waypoints = strings.TrimSpace(r.Values["waypoints"])
		st.City = strings.TrimSpace(r.Values["city"])
		if m := strings.TrimSpace(r.Values["mode"]); m != "" {
			st.Mode = m
		}
		if p := strings.TrimSpace(r.Values["prefer"]); p != "" {
			st.Prefer = p
		}
		st.Zoom = 0
		st.Run = st.From != "" && st.To != ""
	case "quick_route":
		st.Tab = tabRoute
		st.From = strings.TrimSpace(r.Values["from"])
		st.To = strings.TrimSpace(r.Values["to"])
		st.Waypoints = ""
		st.City = ""
		st.Zoom = 0
		st.Run = st.From != "" && st.To != ""
	case "swap_route":
		st.Tab = tabRoute
		fromVal, hasFrom := r.Values["from"]
		toVal, hasTo := r.Values["to"]
		if !hasFrom && !hasTo {
			fromVal, toVal = st.From, st.To
		}
		st.From = strings.TrimSpace(toVal)
		st.To = strings.TrimSpace(fromVal)
		if w, ok := r.Values["waypoints"]; ok {
			st.Waypoints = strings.TrimSpace(w)
		}
		if c, ok := r.Values["city"]; ok {
			st.City = strings.TrimSpace(c)
		}
		if m := strings.TrimSpace(r.Values["mode"]); m != "" {
			st.Mode = m
		}
		if p := strings.TrimSpace(r.Values["prefer"]); p != "" {
			st.Prefer = p
		}
		st.Zoom = 0
		st.Run = st.From != "" && st.To != ""
	case "use_from":
		st.Tab = tabRoute
		if p := strings.TrimSpace(r.Values["place"]); p != "" {
			st.From = p
			toast = fmt.Sprintf("已把「%s」填入起点", clip(p, 20))
		}
		st.City = ""
		st.Zoom = 0
		st.Run = false
	case "use_to":
		st.Tab = tabRoute
		if p := strings.TrimSpace(r.Values["place"]); p != "" {
			st.To = p
			toast = fmt.Sprintf("已把「%s」填入终点", clip(p, 20))
		}
		st.City = ""
		st.Zoom = 0
		st.Run = false
	case "map":
		st.Tab = tabMap
		st.Place = strings.TrimSpace(r.Values["place"])
		st.PlaceLabel = ""
		st.Markers = strings.TrimSpace(r.Values["markers"])
		st.City = strings.TrimSpace(r.Values["city"])
		st.Traffic = parseBool(r.Values["traffic"])
		st.Zoom = 0
		st.Run = st.Place != "" || st.Markers != ""
	case "weather":
		st.Tab = tabUsage
		st.WeatherCity = strings.TrimSpace(r.Values["weather_city"])
		st.Run = st.WeatherCity != ""
	case "quick_weather":
		st.Tab = tabUsage
		if wc := strings.TrimSpace(r.Values["weather_city"]); wc != "" {
			st.WeatherCity = wc
		}
		st.Run = st.WeatherCity != ""
	case "open_poi":
		st.Tab = tabMap
		lat := strings.TrimSpace(r.Values["lat"])
		lng := strings.TrimSpace(r.Values["lng"])
		name := strings.TrimSpace(r.Values["name"])
		st.PlaceLabel = name
		st.City = ""
		if lat != "" && lng != "" {
			st.Place = lat + "," + lng
			st.Markers = st.Place
		} else if name != "" {
			st.Place = name
			st.Markers = name
		}
		st.Zoom = 15
		st.Run = st.Place != ""
	case "view_place":
		name := strings.TrimSpace(r.Values["name"])
		st.Tab = tabMap
		st.Place = name
		st.PlaceLabel = name
		st.Markers = ""
		st.City = ""
		st.Zoom = 15
		st.Run = name != ""
	case "prepare_save":
		st.Tab = tabPlaces
		st.DraftName = strings.TrimSpace(r.Values["name"])
		st.DraftTarget = strings.TrimSpace(r.Values["target"])
		st.PlaceErr = ""
		st.Run = false
	case "save_place":
		st.Tab = tabPlaces
		name := strings.TrimSpace(r.Values["place_name"])
		target := strings.TrimSpace(r.Values["place_target"])
		fp, updated, err := a.saveUIPlace(name, target)
		if err != nil {
			st.DraftName = name
			st.DraftTarget = target
			st.PlaceErr = err.Error()
		} else {
			st.DraftName = ""
			st.DraftTarget = ""
			st.PlaceErr = ""
			verb := "已保存常用地点"
			if updated {
				verb = "已更新常用地点"
			}
			toast = fmt.Sprintf("%s「%s」", verb, clip(fp.Name, 20))
		}
	case "delete_place":
		name := strings.TrimSpace(r.Values["name"])
		if !a.removeUIPlace(name) {
			toast = fmt.Sprintf("「%s」未在本页保存列表里（若来自扩展设置，请到 设置 › 扩展 › 高德地图 修改）", clip(name, 20))
		} else {
			toast = fmt.Sprintf("已删除常用地点「%s」", clip(name, 20))
			st.Tab = tabPlaces
			st.PlaceErr = ""
			if normalizePlaceKey(st.Place) == normalizePlaceKey(name) {
				st.Place, st.PlaceLabel, st.Run = "", "", false
			}
		}
	case "zoom_in":
		if st.Zoom == 0 {
			st.Zoom = 14
		}
		st.Zoom = min(st.Zoom+1, 17)
		st.Run = true
	case "zoom_out":
		if st.Zoom == 0 {
			st.Zoom = 14
		}
		st.Zoom = max(st.Zoom-1, 3)
		st.Run = true
	case "toggle_traffic":
		st.Traffic = !st.Traffic
		st.Run = true
	default:
		return nil, &ext.Error{Code: "invalid", Message: "不认识的操作：" + r.Action}
	}
	v, err := a.buildPage(ctx, st)
	if err != nil {
		return nil, err
	}
	return &wire.ActionResult{View: v, Toast: toast}, nil
}

// imagePage answers ui.image for static map refs produced by buildPage.
func (a *app) imagePage(ctx context.Context, r *ext.ImageRequest) (*wire.UIImage, error) {
	spec, ok := a.mapRefs().get(r.Ref)
	if !ok {
		return nil, &ext.Error{Code: "not_found", Message: "地图预览已过期，请重新查询"}
	}
	w := 600
	if r.Width > 0 {
		// With scale=2 the returned PNG is 2*w pixels wide; stay within AMap's 1024*1024 and Persona's limits.
		w = max(320, min(r.Width/2, 640))
	}
	h := max(240, min(w*2/3, 480))
	q := spec.query
	q.Set("size", fmt.Sprintf("%d*%d", w, h))
	q.Set("scale", "2")
	ttl := routeFor
	maxAge := 900
	if spec.traffic {
		ttl = drivingFor
		maxAge = 120
	}
	img, err := a.client.getImage(ctx, groupLBS, "/v3/staticmap", q, ttl)
	if err != nil {
		return nil, err
	}
	if len(img) > staticMapMaxBytes {
		q.Set("scale", "1")
		img, err = a.client.getImage(ctx, groupLBS, "/v3/staticmap", q, ttl)
		if err != nil {
			return nil, err
		}
	}
	return ext.Image("image/png", img, maxAge), nil
}

func (a *app) buildPage(ctx context.Context, st uiState) (*wire.View, error) {
	if a.client.key == "" {
		return &wire.View{
			Title: "高德地图",
			Sections: []wire.Section{{
				Items: []wire.Component{
					{
						Type:     wire.ComponentEmpty,
						Icon:     "location",
						Title:    "还没填写高德 Web 服务 Key",
						Subtitle: "在 设置 › 扩展 › 高德地图 填入在高德开放平台申请的「Web服务」Key 即可使用搜地点、算路线、看地图和查天气。",
					},
					{Type: wire.ComponentLink, Title: "打开高德开放平台控制台", URL: consoleURL},
				},
			}},
		}, nil
	}
	keyErr, _, u := a.client.state()
	if keyErr != nil {
		return &wire.View{
			Title: "高德地图",
			Sections: []wire.Section{{
				Title: "Key 不可用",
				Items: []wire.Component{
					{Type: wire.ComponentText, Text: keyWords[keyErr.code]},
					{Type: wire.ComponentText, Text: strings.Join(keyGuide, "\n"), Style: wire.TextStyleSecondary},
					{Type: wire.ComponentLink, Title: "打开高德开放平台控制台", URL: consoleURL},
				},
			}},
		}, nil
	}

	var sections []wire.Section
	switch st.Tab {
	case tabSearch:
		sections = a.searchSections(ctx, st)
	case tabRoute:
		sections = a.routeSections(ctx, st)
	case tabMap:
		sections = a.mapSections(ctx, st)
	case tabPlaces:
		sections = a.placesSections(st)
	case tabUsage:
		sections = a.usageSections(ctx, st, u)
	}

	placesBadge := ""
	if n := len(a.fixedPlaces()); n > 0 {
		placesBadge = strconv.Itoa(n)
	}

	return &wire.View{
		Title: "高德地图",
		State: ext.EncodeState(st),
		Tabs: &wire.Tabs{
			Name:   "tab",
			Value:  st.Tab,
			Action: wire.Action{ID: "tab"},
			Items: []wire.Tab{
				{Value: tabSearch, Title: "搜地点"},
				{Value: tabRoute, Title: "算路线"},
				{Value: tabMap, Title: "看地图"},
				{Value: tabPlaces, Title: "常用地点", Badge: placesBadge},
				{Value: tabUsage, Title: "天气与额度"},
			},
		},
		Sections: sections,
	}, nil
}

func (a *app) searchSections(ctx context.Context, st uiState) []wire.Section {
	form := wire.Section{
		Title: "搜索中国境内地点",
		Items: []wire.Component{
			{Type: wire.ComponentField, Kind: wire.FieldKindText, Name: "keywords", Label: "关键词", Value: st.Keywords, Placeholder: "店铺、景点、车站、地址，如 北京南站"},
			{Type: wire.ComponentField, Kind: wire.FieldKindText, Name: "city", Label: "城市（可选）", Value: st.City, Placeholder: "如 北京、上海"},
			{Type: wire.ComponentField, Kind: wire.FieldKindToggle, Name: "city_only", Label: "仅限该城市", Value: boolText(st.CityOnly)},
			{Type: wire.ComponentButton, Title: "搜索地点", Action: &wire.Action{ID: "search", Submit: true}},
		},
	}
	if !st.Run || st.Keywords == "" {
		return []wire.Section{form, {
			Items: []wire.Component{{
				Type:     wire.ComponentEmpty,
				Icon:     "search",
				Title:    "输入关键词开始搜索",
				Subtitle: "支持搜店铺、车站、景点和地址，点选结果可在地图上查看位置。",
			}},
		}}
	}
	pois, err := a.search(ctx, st.Keywords, st.City, st.CityOnly, 8)
	if err != nil {
		return []wire.Section{form, errorSection("搜索失败", err)}
	}
	if len(pois) == 0 {
		return []wire.Section{form, {
			Items: []wire.Component{{
				Type:     wire.ComponentEmpty,
				Icon:     "search",
				Title:    fmt.Sprintf("没搜到「%s」", st.Keywords),
				Subtitle: "试试换个关键词或去掉城市限制。",
			}},
		}}
	}

	var rows []wire.Component
	var markerParts []string
	var firstPoint point
	var firstName string
	hasFirst := false
	for i, p := range pois {
		title := fmt.Sprintf("%c. %s", rune('A'+i), p.Name.String())
		if c := category(p.Type.String()); c != "" && !strings.Contains(p.Name.String(), c) {
			title += "（" + c + "）"
		}
		var sub []string
		if addr := address(p.CityName.String(), p.AdName.String(), p.Address.String()); addr != "" {
			sub = append(sub, addr)
		}
		biz := p.Business.V
		if tel := phones(biz.Tel.String()); tel != "" {
			sub = append(sub, "电话 "+tel)
		}
		if h := biz.OpentimeToday.String(); h != "" {
			sub = append(sub, "今天 "+h)
		}
		var val []string
		if r, ok := biz.Rating.num(); ok && r > 0 {
			val = append(val, "★ "+biz.Rating.String())
		}
		if c, ok := biz.Cost.num(); ok && c > 0 {
			val = append(val, "¥"+strconv.FormatFloat(c, 'f', -1, 0)+"/人")
		}
		row := wire.Component{
			Type:     wire.ComponentRow,
			Icon:     "location",
			Title:    clip(title, 200),
			Subtitle: clip(strings.Join(sub, " · "), 400),
			Value:    strings.Join(val, " "),
		}
		if at, ok := parseAMapLocation(p.Location.String()); ok {
			if !hasFirst {
				firstPoint, firstName, hasFirst = at, p.Name.String(), true
			}
			markerParts = append(markerParts, fmt.Sprintf("mid,0xE53935,%c:%s", rune('A'+i), amapLocation(at)))
			row.Action = &wire.Action{
				ID: "open_poi",
				Values: map[string]string{
					"lat":  strconv.FormatFloat(at.Lat, 'f', 6, 64),
					"lng":  strconv.FormatFloat(at.Lng, 'f', 6, 64),
					"name": p.Name.String(),
				},
			}
		}
		rows = append(rows, row)
	}

	out := []wire.Section{form, {Title: fmt.Sprintf("搜索结果（%d 条，点选看大图）", len(rows)), Items: rows}}
	if len(markerParts) > 0 {
		q := url.Values{"markers": {strings.Join(markerParts, "|")}}
		if len(markerParts) == 1 {
			q.Set("zoom", "15")
		}
		ref := a.mapRefs().put(q, false)
		mapItems := []wire.Component{{
			Type:    wire.ComponentImage,
			Src:     &wire.ImageSource{Ref: ref, Width: 600, Height: 400},
			Alt:     fmt.Sprintf("「%s」搜索结果分布图", st.Keywords),
			Caption: fmt.Sprintf("标注 A–%c 对应上方 %d 处地点（点击放大）", rune('A'+len(markerParts)-1), len(markerParts)),
			Aspect:  wire.ImageAspectLandscape,
			Fit:     wire.ImageFitFit,
		}}
		if hasFirst {
			mapItems = append(mapItems, wire.Component{
				Type:  wire.ComponentLink,
				Title: "在高德地图中打开 " + clip(firstName, 30),
				URL:   amapMarkerURL(firstPoint, firstName),
			})
		}
		out = append(out, wire.Section{Title: "位置分布", Items: mapItems})
	}
	return out
}

func (a *app) routeSections(ctx context.Context, st uiState) []wire.Section {
	form := wire.Section{
		Title: "路线规划",
		Items: []wire.Component{
			{Type: wire.ComponentField, Kind: wire.FieldKindText, Name: "from", Label: "起点", Value: st.From, Placeholder: "地名、地址、常用地点名（如 家）或 WGS-84 纬度,经度"},
			{Type: wire.ComponentField, Kind: wire.FieldKindText, Name: "to", Label: "终点", Value: st.To, Placeholder: "地名、地址、常用地点名（如 公司）或 WGS-84 纬度,经度"},
			{Type: wire.ComponentField, Kind: wire.FieldKindText, Name: "waypoints", Label: "途经点（驾车可选）", Value: st.Waypoints, Placeholder: "多个用顿号、逗号或分号分隔"},
			{Type: wire.ComponentField, Kind: wire.FieldKindText, Name: "city", Label: "城市（可选）", Value: st.City, Placeholder: "同名地点消歧，如 北京"},
			{Type: wire.ComponentField, Kind: wire.FieldKindChoice, Name: "mode", Label: "出行方式", Value: st.Mode, Choices: []wire.Choice{
				{Value: "driving", Title: "驾车"},
				{Value: "transit", Title: "公交地铁"},
				{Value: "walking", Title: "步行"},
				{Value: "cycling", Title: "骑行"},
				{Value: "ebike", Title: "电动车"},
			}},
			{Type: wire.ComponentField, Kind: wire.FieldKindChoice, Name: "prefer", Label: "驾车偏好", Value: st.Prefer, Choices: []wire.Choice{
				{Value: "recommended", Title: "高德推荐"},
				{Value: "fastest", Title: "最快"},
				{Value: "avoid_congestion", Title: "躲避拥堵"},
				{Value: "avoid_tolls", Title: "不走收费路"},
				{Value: "avoid_highways", Title: "不走高速"},
			}},
			{Type: wire.ComponentButton, Title: "规划路线", Action: &wire.Action{ID: "route", Submit: true}},
			{Type: wire.ComponentButton, Title: "互换起点与终点", Action: &wire.Action{ID: "swap_route", Submit: true}},
		},
	}
	var commuteItems []wire.Component
	if home, company, ok := a.findCommutePair(); ok {
		commuteItems = append(commuteItems,
			wire.Component{
				Type:   wire.ComponentButton,
				Title:  fmt.Sprintf("%s → %s（一键算路）", clip(home.Name, 10), clip(company.Name, 10)),
				Action: &wire.Action{ID: "quick_route", Values: map[string]string{"from": home.Name, "to": company.Name}},
			},
			wire.Component{
				Type:   wire.ComponentButton,
				Title:  fmt.Sprintf("%s → %s（一键算路）", clip(company.Name, 10), clip(home.Name, 10)),
				Action: &wire.Action{ID: "quick_route", Values: map[string]string{"from": company.Name, "to": home.Name}},
			},
		)
	} else if fps := a.fixedPlaces(); len(fps) > 0 {
		commuteItems = append(commuteItems, wire.Component{
			Type:   wire.ComponentButton,
			Title:  fmt.Sprintf("从常用地点选起终点（%d 个）", len(fps)),
			Action: &wire.Action{ID: "tab", Values: map[string]string{"tab": tabPlaces}},
		})
	}
	if !st.Run || st.From == "" || st.To == "" {
		out := []wire.Section{form}
		if len(commuteItems) > 0 {
			out = append(out, wire.Section{Title: "快捷通勤", Items: commuteItems})
		}
		out = append(out, wire.Section{
			Items: []wire.Component{{
				Type:     wire.ComponentEmpty,
				Icon:     "car",
				Title:    "填好起点和终点即可算路",
				Subtitle: "支持写「家」「公司」等常用地点、驾车途经点、公交地铁、步行、骑行与电动车。",
			}},
		})
		return out
	}

	wps := splitList(st.Waypoints)
	in := args{
		Mode:         st.Mode,
		From:         st.From,
		To:           st.To,
		Waypoints:    wps,
		City:         st.City,
		Prefer:       st.Prefer,
		Alternatives: 2,
	}
	if p, ok := parseLatLng(st.From); ok {
		in.FromLatitude, in.FromLongitude = &p.Lat, &p.Lng
	}
	if p, ok := parseLatLng(st.To); ok {
		in.ToLatitude, in.ToLongitude = &p.Lat, &p.Lng
	}
	text, err := a.toolRoute(ctx, in)
	if err != nil {
		return []wire.Section{form, errorSection("路线规划失败", err)}
	}

	var resultItems []wire.Component
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		resultItems = append(resultItems, wire.Component{Type: wire.ComponentText, Text: clip(line, 1800)})
	}
	out := []wire.Section{form, {Title: "规划结果", Items: resultItems}}

	// Build a static map preview using the exact same cached endpoint/waypoint resolution order as toolRoute.
	fromAt, fromGiven, _ := pointArg(in.FromLatitude, in.FromLongitude, "起点")
	toAt, toGiven, _ := pointArg(in.ToLatitude, in.ToLongitude, "终点")
	var stops []place
	if fromPl, toPl, viaPls, rerr := a.resolveRouteStops(ctx, st.From, fromAt, fromGiven, st.To, toAt, toGiven, wps, st.City, st.Mode); rerr == nil {
		stops = append(stops, fromPl)
		stops = append(stops, viaPls...)
		stops = append(stops, toPl)
	}
	if len(stops) >= 2 {
		q := url.Values{}
		markerParts := make([]string, 0, min(len(stops), staticMapMarkersMost))
		pathCoords := make([]string, 0, len(stops))
		for i, s := range stops {
			loc := amapLocation(s.at)
			pathCoords = append(pathCoords, loc)
			if i < staticMapMarkersMost {
				colorHex := "0x1976D2"
				if i == 0 {
					colorHex = "0x2E7D32"
				} else if i == len(stops)-1 {
					colorHex = "0xE53935"
				}
				markerParts = append(markerParts, fmt.Sprintf("mid,%s,%c:%s", colorHex, rune('A'+i), loc))
			}
		}
		q.Set("markers", strings.Join(markerParts, "|"))
		q.Set("paths", "5,0x1976D2,1,,:"+strings.Join(pathCoords, ";"))
		if st.Zoom > 0 {
			q.Set("zoom", strconv.Itoa(st.Zoom))
		}
		if st.Traffic {
			q.Set("traffic", "1")
		}
		ref := a.mapRefs().put(q, st.Traffic)
		trafficBtn := "开启实时路况"
		if st.Traffic {
			trafficBtn = "关闭实时路况"
		}
		mapItems := []wire.Component{
			{
				Type:    wire.ComponentImage,
				Src:     &wire.ImageSource{Ref: ref, Width: 600, Height: 400},
				Alt:     fmt.Sprintf("%s 到 %s 路线示意图", stops[0].label(), stops[len(stops)-1].label()),
				Caption: fmt.Sprintf("起点 A（绿）→ 终点 %c（红）", rune('A'+min(len(stops), staticMapMarkersMost)-1)),
				Aspect:  wire.ImageAspectLandscape,
				Fit:     wire.ImageFitFit,
			},
			{Type: wire.ComponentButton, Title: trafficBtn, Action: &wire.Action{ID: "toggle_traffic"}},
			{Type: wire.ComponentButton, Title: "放大地图", Action: &wire.Action{ID: "zoom_in"}},
			{Type: wire.ComponentButton, Title: "缩小地图", Action: &wire.Action{ID: "zoom_out"}},
			{Type: wire.ComponentLink, Title: "在高德地图中打开导航", URL: amapNavURL(stops[0], stops[len(stops)-1], st.Mode)},
		}
		out = append(out, wire.Section{Title: "路线示意图", Items: mapItems})
	}
	return out
}

func (a *app) mapSections(ctx context.Context, st uiState) []wire.Section {
	form := wire.Section{
		Title: "查看静态地图",
		Items: []wire.Component{
			{Type: wire.ComponentField, Kind: wire.FieldKindText, Name: "place", Label: "中心地点或坐标", Value: st.Place, Placeholder: "地名、地址、常用地点名（如 家）或 WGS-84 纬度,经度"},
			{Type: wire.ComponentField, Kind: wire.FieldKindText, Name: "markers", Label: "标注点（可选，最多 10 个）", Value: st.Markers, Placeholder: "多个用分号或换行分隔，支持常用地点名"},
			{Type: wire.ComponentField, Kind: wire.FieldKindText, Name: "city", Label: "城市（可选）", Value: st.City, Placeholder: "如 北京、上海"},
			{Type: wire.ComponentField, Kind: wire.FieldKindToggle, Name: "traffic", Label: "显示实时路况", Value: boolText(st.Traffic)},
			{Type: wire.ComponentButton, Title: "生成地图", Action: &wire.Action{ID: "map", Submit: true}},
		},
	}
	if !st.Run || (st.Place == "" && st.Markers == "") {
		return []wire.Section{form, {
			Items: []wire.Component{{
				Type:     wire.ComponentEmpty,
				Icon:     "location",
				Title:    "输入地点或坐标生成地图",
				Subtitle: "支持写「家」「公司」等常用地点、标注最多 10 个地点、叠加实时路况与缩放预览。",
			}},
		}}
	}

	city := st.City
	q := url.Values{}
	var center place
	hasCenter := false
	rawMarkers := splitMarkers(st.Markers)
	if len(rawMarkers) > staticMapMarkersMost {
		rawMarkers = rawMarkers[:staticMapMarkersMost]
	}
	needMarkerCity := false
	for _, m := range rawMarkers {
		if !a.isFixedOrCoord(m) {
			needMarkerCity = true
			break
		}
	}
	if st.Place != "" {
		pl, err := a.resolvePoint(ctx, st.Place, city)
		if err != nil {
			return []wire.Section{form, errorSection("找不到中心点", err)}
		}
		center, hasCenter = pl, true
		q.Set("location", amapLocation(pl.at))
		if city == "" {
			city = pl.citycode
		}
		if city == "" && needMarkerCity {
			if c, cerr := a.cityCode(ctx, pl.at); cerr == nil {
				city = c
			}
		}
	}

	var markers []place
	for _, m := range rawMarkers {
		pl, err := a.resolvePoint(ctx, m, city)
		if err != nil {
			return []wire.Section{form, errorSection("找不到标注点", err)}
		}
		markers = append(markers, pl)
		if city == "" && pl.citycode != "" {
			city = pl.citycode
		}
	}
	if len(markers) == 0 && hasCenter {
		markers = []place{center}
	}
	if len(markers) > 0 {
		parts := make([]string, len(markers))
		for i, m := range markers {
			parts[i] = fmt.Sprintf("mid,0xE53935,%c:%s", rune('A'+i), amapLocation(m.at))
		}
		q.Set("markers", strings.Join(parts, "|"))
	}
	zoom := st.Zoom
	if zoom == 0 && len(markers) <= 1 {
		zoom = 14
	}
	if zoom > 0 {
		q.Set("zoom", strconv.Itoa(zoom))
	}
	if st.Traffic {
		q.Set("traffic", "1")
	}

	ref := a.mapRefs().put(q, st.Traffic)
	var capParts []string
	if zoom > 0 {
		capParts = append(capParts, fmt.Sprintf("缩放 %d 级", zoom))
	}
	if st.Traffic {
		capParts = append(capParts, "含实时路况")
	}
	capText := "点击图片可全屏查看"
	if len(capParts) > 0 {
		capText = strings.Join(capParts, " · ") + "（点击放大）"
	}
	trafficBtn := "开启实时路况"
	if st.Traffic {
		trafficBtn = "关闭实时路况"
	}
	items := []wire.Component{
		{
			Type:    wire.ComponentImage,
			Src:     &wire.ImageSource{Ref: ref, Width: 600, Height: 400},
			Alt:     "高德静态地图预览",
			Caption: capText,
			Aspect:  wire.ImageAspectLandscape,
			Fit:     wire.ImageFitFit,
		},
		{Type: wire.ComponentButton, Title: "放大 (+)", Action: &wire.Action{ID: "zoom_in"}},
		{Type: wire.ComponentButton, Title: "缩小 (-)", Action: &wire.Action{ID: "zoom_out"}},
		{Type: wire.ComponentButton, Title: trafficBtn, Action: &wire.Action{ID: "toggle_traffic"}},
	}
	for i, m := range markers {
		items = append(items, wire.Component{
			Type:     wire.ComponentRow,
			Icon:     "location",
			Title:    fmt.Sprintf("%c. %s", rune('A'+i), m.label()),
			Subtitle: "WGS-84: " + m.at.String(),
		})
	}
	target := center
	if !hasCenter && len(markers) > 0 {
		target = markers[0]
	}
	useTarget := target.at.String()
	weatherTarget := target.at.String()
	displayName := target.name
	var matchedFixed fixedPlace
	isFixed := false
	if st.Place != "" {
		if fp, ok := a.matchFixedPlace(st.Place); ok && len(rawMarkers) <= 1 &&
			(len(rawMarkers) == 0 || normalizePlaceKey(rawMarkers[0]) == normalizePlaceKey(st.Place)) {
			isFixed = true
			matchedFixed = fp
			useTarget = fp.Name
			weatherTarget = fp.Name
			displayName = fp.Name
		}
	}
	if !isFixed && st.PlaceLabel != "" {
		displayName = st.PlaceLabel
	}
	items = append(items,
		wire.Component{
			Type:   wire.ComponentButton,
			Title:  "设为算路起点",
			Action: &wire.Action{ID: "use_from", Values: map[string]string{"place": useTarget}},
		},
		wire.Component{
			Type:   wire.ComponentButton,
			Title:  "设为算路终点",
			Action: &wire.Action{ID: "use_to", Values: map[string]string{"place": useTarget}},
		},
		wire.Component{
			Type:   wire.ComponentButton,
			Title:  "查当地天气",
			Action: &wire.Action{ID: "quick_weather", Values: map[string]string{"weather_city": weatherTarget}},
		},
	)
	if !isFixed {
		draftName := ""
		if d := strings.TrimSpace(displayName); d != "" && d != target.at.String() {
			draftName = d
		}
		draftTarget := target.at.String()
		if draftName != "" {
			draftTarget = draftName + " " + target.at.String()
		}
		items = append(items, wire.Component{
			Type:  wire.ComponentButton,
			Title: "存为常用地点",
			Action: &wire.Action{
				ID:     "prepare_save",
				Values: map[string]string{"name": draftName, "target": draftTarget},
			},
		})
	} else if matchedFixed.Source == "ui" {
		items = append(items, wire.Component{
			Type:  wire.ComponentButton,
			Title: "删除常用地点",
			Role:  wire.ButtonRoleDestructive,
			Action: &wire.Action{
				ID:      "delete_place",
				Values:  map[string]string{"name": matchedFixed.Name},
				Confirm: fmt.Sprintf("要从常用地点里删掉「%s」吗？", clip(matchedFixed.Name, 20)),
			},
		})
	}
	items = append(items, wire.Component{
		Type:  wire.ComponentLink,
		Title: "在高德地图中打开 " + clip(displayName, 30),
		URL:   amapMarkerURL(target.at, displayName),
	})
	return []wire.Section{form, {Title: "地图预览", Items: items}}
}

func (a *app) placesSections(st uiState) []wire.Section {
	var sections []wire.Section
	if home, company, ok := a.findCommutePair(); ok {
		sections = append(sections, wire.Section{
			Title: "一键通勤",
			Items: []wire.Component{
				{
					Type:   wire.ComponentButton,
					Title:  fmt.Sprintf("%s → %s（规划路线）", clip(home.Name, 10), clip(company.Name, 10)),
					Action: &wire.Action{ID: "quick_route", Values: map[string]string{"from": home.Name, "to": company.Name}},
				},
				{
					Type:   wire.ComponentButton,
					Title:  fmt.Sprintf("%s → %s（规划路线）", clip(company.Name, 10), clip(home.Name, 10)),
					Action: &wire.Action{ID: "quick_route", Values: map[string]string{"from": company.Name, "to": home.Name}},
				},
			},
		})
	}

	fps := a.fixedPlaces()
	if len(fps) == 0 {
		sections = append(sections, wire.Section{
			Title: "已保存的常用地点",
			Items: []wire.Component{{
				Type:   wire.ComponentEmpty,
				Icon:   "location",
				Title:  "还没有常用地点",
				Detail: "在下面添加「家」「公司」或常去地点，或在 设置 › 扩展 › 高德地图 配置；之后可直接问助手「回家要多久」或在页面一键算路。",
			}},
		})
	} else {
		rows := make([]wire.Component, 0, len(fps))
		for _, fp := range fps {
			icon := "location"
			if canonicalSlotKey(fp.Name) == "home" {
				icon = "home"
			}
			tone := wire.ToneNeutral
			if fp.Source == "config" {
				tone = wire.TonePositive
			}
			sub := fp.summary()
			if len(fp.Aliases) > 0 {
				sub += " · 别名：" + strings.Join(fp.Aliases[:min(len(fp.Aliases), 3)], "、")
			}
			rowActions := []wire.RowAction{
				{
					Title:  "设为起点",
					Icon:   "car",
					Action: wire.Action{ID: "use_from", Values: map[string]string{"place": fp.Name}},
				},
				{
					Title:  "设为终点",
					Icon:   "location",
					Action: wire.Action{ID: "use_to", Values: map[string]string{"place": fp.Name}},
				},
				{
					Title:  "查天气",
					Icon:   "cloud",
					Action: wire.Action{ID: "quick_weather", Values: map[string]string{"weather_city": fp.Name}},
				},
			}
			if fp.Source == "ui" {
				rowActions = append(rowActions, wire.RowAction{
					Title: "删除",
					Icon:  "trash",
					Role:  wire.ButtonRoleDestructive,
					Action: wire.Action{
						ID:      "delete_place",
						Values:  map[string]string{"name": fp.Name},
						Confirm: fmt.Sprintf("要从常用地点里删掉「%s」吗？", clip(fp.Name, 20)),
					},
				})
			}
			rows = append(rows, wire.Component{
				Type:     wire.ComponentRow,
				Icon:     icon,
				Title:    clip(fp.Name, 80),
				Subtitle: clip(sub, 300),
				Badge:    &wire.Badge{Text: fp.sourceLabel(), Tone: tone},
				Action:   &wire.Action{ID: "view_place", Values: map[string]string{"name": fp.Name}},
				Actions:  rowActions,
			})
		}
		sections = append(sections, wire.Section{
			Title:  fmt.Sprintf("已保存的常用地点（%d 个，点选看地图）", len(fps)),
			Footer: "「扩展设置」来自 设置 › 扩展 › 高德地图（同名时优先）；「页面保存」存在扩展数据目录 places.json（仅限本机存储）。",
			Items:  rows,
		})
	}

	formItems := make([]wire.Component, 0, 4)
	if st.PlaceErr != "" {
		formItems = append(formItems, wire.Component{
			Type:     wire.ComponentRow,
			Icon:     "warning",
			Title:    "保存失败",
			Subtitle: clip(st.PlaceErr, 400),
			Badge:    &wire.Badge{Text: "请修改", Tone: wire.ToneDanger},
		})
	}
	formItems = append(formItems,
		wire.Component{Type: wire.ComponentField, Kind: wire.FieldKindText, Name: "place_name", Label: "地点名称", Value: st.DraftName, Placeholder: "如 家、公司、父母家"},
		wire.Component{Type: wire.ComponentField, Kind: wire.FieldKindText, Name: "place_target", Label: "地址、地名或 WGS-84 坐标", Value: st.DraftTarget, Placeholder: "地址、地名，或 39.9032,116.3978，也可 名称 纬度,经度"},
		wire.Component{Type: wire.ComponentButton, Title: "保存到常用地点", Action: &wire.Action{ID: "save_place", Submit: true}},
	)
	sections = append(sections, wire.Section{
		Title:  "添加常用地点",
		Footer: "在「算路线」「看地图」「天气与额度」以及与助手对话时，都可以直接写这里保存的名字或「家」「公司」。",
		Items:  formItems,
	})
	return sections
}

func (a *app) usageSections(ctx context.Context, st uiState, u usage) []wire.Section {
	weatherItems := []wire.Component{
		{Type: wire.ComponentField, Kind: wire.FieldKindText, Name: "weather_city", Label: "城市、区县或常用地点", Value: st.WeatherCity, Placeholder: "如 杭州、北京市朝阳区、330100 或 家"},
		{Type: wire.ComponentButton, Title: "查天气", Action: &wire.Action{ID: "weather", Submit: true}},
	}
	for i, fp := range a.fixedPlaces() {
		if i == 3 {
			break
		}
		weatherItems = append(weatherItems, wire.Component{
			Type:   wire.ComponentButton,
			Title:  fmt.Sprintf("查「%s」天气", clip(fp.Name, 8)),
			Action: &wire.Action{ID: "quick_weather", Values: map[string]string{"weather_city": fp.Name}},
		})
	}
	weatherForm := wire.Section{
		Title: "中国城市天气",
		Items: weatherItems,
	}
	var sections []wire.Section
	sections = append(sections, weatherForm)
	if st.Run && st.WeatherCity != "" {
		days := 4
		text, err := a.toolWeather(ctx, args{City: st.WeatherCity, Days: &days})
		if err != nil {
			sections = append(sections, errorSection("天气查询失败", err))
		} else {
			var lines []wire.Component
			for _, line := range strings.Split(text, "\n") {
				if line = strings.TrimSpace(line); line != "" {
					lines = append(lines, wire.Component{Type: wire.ComponentText, Text: clip(line, 1800)})
				}
			}
			sections = append(sections, wire.Section{Title: "天气结果", Items: lines})
		}
	}

	quotaItems := []wire.Component{
		{Type: wire.ComponentStatGrid, Stats: usageStats(u)},
	}
	if pause := pausedSummary(u); pause != "" {
		quotaItems = append(quotaItems, wire.Component{
			Type:     wire.ComponentRow,
			Icon:     "clock",
			Title:    "当前暂停的配额组",
			Subtitle: pause,
			Badge:    &wire.Badge{Text: "额度耗尽", Tone: wire.ToneCaution},
		})
	}
	quotaItems = append(quotaItems, wire.Component{
		Type:  wire.ComponentLink,
		Title: "打开高德开放平台控制台查看额度",
		URL:   consoleURL,
	})
	sections = append(sections, wire.Section{Title: fmt.Sprintf("本月 API 用量（%s）", u.Month), Items: quotaItems})
	return sections
}

func usageStats(u usage) []wire.Stat {
	stats := make([]wire.Stat, 0, len(groups))
	for _, g := range groups {
		s := wire.Stat{
			Label: g.label(),
			Value: fmt.Sprintf("%d 次", u.Counts[g]),
			Tone:  wire.TonePositive,
		}
		if until, paused := u.Blocked[g]; paused {
			s.Detail = until.In(beijing).Format("01-02 15:04") + " 恢复"
			s.Tone = wire.ToneCaution
		}
		stats = append(stats, s)
	}
	return stats
}

func pausedSummary(u usage) string {
	var paused []group
	for g := range u.Blocked {
		paused = append(paused, g)
	}
	if len(paused) == 0 {
		return ""
	}
	sort.Slice(paused, func(i, j int) bool { return paused[i] < paused[j] })
	parts := make([]string, len(paused))
	for i, g := range paused {
		parts[i] = fmt.Sprintf("%s（%s 恢复）", g.label(), u.Blocked[g].In(beijing).Format("01-02 15:04"))
	}
	return strings.Join(parts, "；")
}

func errorSection(title string, err error) wire.Section {
	return wire.Section{
		Title: title,
		Items: []wire.Component{{
			Type:     wire.ComponentRow,
			Icon:     "warning",
			Title:    title,
			Subtitle: clip(err.Error(), 600),
			Badge:    &wire.Badge{Text: "失败", Tone: wire.ToneDanger},
		}},
	}
}

func parseBool(v string) bool {
	v = strings.ToLower(strings.TrimSpace(v))
	return v == "true" || v == "1" || v == "yes" || v == "on"
}

func boolText(v bool) string {
	if v {
		return "true"
	}
	return "false"
}

// splitList splits a user-entered list of waypoints separated by Chinese/ASCII commas, semicolons or newlines.
// A "lat,lng" coordinate pair is preserved intact when separated by semicolons/newlines/Chinese separators.
func splitList(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	if strings.ContainsAny(s, ";；\n、") {
		return splitBy(s, func(r rune) bool { return r == ';' || r == '；' || r == '\n' || r == '、' })
	}
	if _, ok := parseLatLng(s); ok {
		return []string{s}
	}
	return splitBy(s, func(r rune) bool { return r == ',' || r == '，' })
}

// splitMarkers splits markers entered in the map form (semicolon, newline, or Chinese pause mark, preserving lat,lng).
func splitMarkers(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	return splitBy(s, func(r rune) bool { return r == ';' || r == '；' || r == '\n' || r == '、' })
}

func splitBy(s string, sep func(rune) bool) []string {
	var out []string
	for _, part := range strings.FieldsFunc(s, sep) {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

// amapMarkerURL builds an https AMap URI link to view a point (GCJ-02 lng,lat).
func amapMarkerURL(p point, name string) string {
	q := url.Values{"position": {amapLocation(p)}, "callnative": {"1"}}
	if name != "" {
		q.Set("name", name)
	}
	return "https://uri.amap.com/marker?" + q.Encode()
}

// amapNavURL builds an https AMap URI link to open route navigation (GCJ-02 lng,lat).
func amapNavURL(from, to place, mode string) string {
	amapMode := "car"
	switch mode {
	case "transit":
		amapMode = "bus"
	case "walking":
		amapMode = "walk"
	case "cycling", "ebike":
		amapMode = "ride"
	}
	q := url.Values{
		"from":       {amapLocation(from.at) + "," + from.name},
		"to":         {amapLocation(to.at) + "," + to.name},
		"mode":       {amapMode},
		"callnative": {"1"},
	}
	return "https://uri.amap.com/navigation?" + q.Encode()
}
