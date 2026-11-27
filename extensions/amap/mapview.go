package main

import (
	"fmt"
	"math"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/nwverse/persona/sdk/go/wire"
)

// UI version 12 MapComponent builders and geometry helpers for both in-chat tool_views (compact snapshot cards) and
// interactive nav_page tabs. All MapComponent coordinates are WGS-84; AMap GCJ-02 polylines are converted on decode.

const (
	routePointsTotalBudget = 1500 // comfortably below wire.MapPointsMost (2000) and 128 KiB ConnectorCallResult limit
	routePointsPerRouteMax = 1000 // schema maxItems per MapRoute.points
)

func floatPtr(v float64) *float64 { return &v }
func intPtr(v int) *int           { return &v }

// clipFit truncates s so its rune length never exceeds maxLen (using "…" when shortened).
func clipFit(s string, maxLen int) string {
	s = strings.TrimSpace(s)
	if maxLen <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= maxLen {
		return s
	}
	if maxLen == 1 {
		return "…"
	}
	return string(r[:maxLen-1]) + "…"
}

func samePoint(a, b point) bool {
	return math.Abs(a.Lat-b.Lat) < 1e-7 && math.Abs(a.Lng-b.Lng) < 1e-7
}

func mapCoord(p point) wire.MapCoordinate {
	return wire.MapCoordinate{Lat: floatPtr(p.Lat), Lon: floatPtr(p.Lng)}
}

// decodePolyline converts an AMap semicolon-separated GCJ-02 polyline string ("lng,lat;...") into WGS-84 points,
// skipping invalid entries and consecutive duplicates.
func decodePolyline(raw polylineStr) []point {
	s := raw.String()
	if s == "" {
		return nil
	}
	tokens := strings.Split(s, ";")
	out := make([]point, 0, len(tokens))
	for _, tok := range tokens {
		tok = strings.TrimSpace(tok)
		if tok == "" {
			continue
		}
		if p, ok := parseAMapLocation(tok); ok && p.valid() {
			if len(out) == 0 || !samePoint(out[len(out)-1], p) {
				out = append(out, p)
			}
		}
	}
	return out
}

// appendPoints appends src onto dst while avoiding duplicate adjacent vertices.
func appendPoints(dst, src []point) []point {
	for _, p := range src {
		if !p.valid() {
			continue
		}
		if len(dst) == 0 || !samePoint(dst[len(dst)-1], p) {
			dst = append(dst, p)
		}
	}
	return dst
}

// downsamplePoints deterministically reduces pts to at most limit vertices while always preserving start and end.
func downsamplePoints(pts []point, limit int) []point {
	if limit < 2 {
		limit = 2
	}
	if len(pts) <= limit {
		return pts
	}
	out := make([]point, 0, limit)
	out = append(out, pts[0])
	n := len(pts)
	for i := 1; i < limit-1; i++ {
		idx := i * (n - 1) / (limit - 1)
		if idx <= 0 {
			idx = 1
		}
		if idx >= n-1 {
			idx = n - 2
		}
		if !samePoint(out[len(out)-1], pts[idx]) {
			out = append(out, pts[idx])
		}
	}
	last := pts[n-1]
	if !samePoint(out[len(out)-1], last) {
		out = append(out, last)
	}
	if len(out) < 2 {
		out = append(out, last)
	}
	return out
}

// fitViewport computes a padded WGS-84 MapViewport covering pts, respecting UI v12 span bounds.
func fitViewport(pts []point, minSpanLat, minSpanLon float64) *wire.MapViewport {
	minLat, maxLat := math.Inf(1), math.Inf(-1)
	minLon, maxLon := math.Inf(1), math.Inf(-1)
	count := 0
	for _, p := range pts {
		if !p.valid() {
			continue
		}
		count++
		minLat = min(minLat, p.Lat)
		maxLat = max(maxLat, p.Lat)
		minLon = min(minLon, p.Lng)
		maxLon = max(maxLon, p.Lng)
	}
	if count == 0 {
		return nil
	}
	centerLat := (minLat + maxLat) / 2
	centerLon := (minLon + maxLon) / 2
	spanLat := max((maxLat-minLat)*1.3, minSpanLat)
	spanLon := max((maxLon-minLon)*1.3, minSpanLon)
	spanLat = max(0.0001, min(spanLat, 180))
	spanLon = max(0.0001, min(spanLon, 360))
	return &wire.MapViewport{
		Center:  wire.MapCoordinate{Lat: floatPtr(centerLat), Lon: floatPtr(centerLon)},
		SpanLat: floatPtr(spanLat),
		SpanLon: floatPtr(spanLon),
	}
}

// zoomSpan translates an AMap zoom level (3..17) into an approximate viewport span in degrees.
func zoomSpan(zoom int) (spanLat, spanLon float64) {
	if zoom <= 0 {
		return 0.01, 0.012
	}
	deg := 720.0 / math.Exp2(float64(zoom))
	spanLat = max(0.002, min(deg, 180))
	spanLon = max(0.002, min(deg*1.35, 360))
	return spanLat, spanLon
}

func mapTravelMode(amapMode string) string {
	switch amapMode {
	case "walking":
		return wire.MapTravelModeWalking
	case "cycling", "ebike":
		return wire.MapTravelModeCycling
	case "transit":
		return wire.MapTravelModeTransit
	default:
		return wire.MapTravelModeDriving
	}
}

func markerIconForName(name string) string {
	switch canonicalSlotKey(name) {
	case "home":
		return "home"
	default:
		return "location"
	}
}

func sanitizeMapLinks(links []wire.MapLink) []wire.MapLink {
	out := make([]wire.MapLink, 0, min(len(links), wire.MapLinksMost))
	for _, lk := range links {
		if len(out) >= wire.MapLinksMost {
			break
		}
		title := clipFit(lk.Title, 80)
		u := strings.TrimSpace(lk.URL)
		if title == "" || !strings.HasPrefix(u, "https://") || utf8.RuneCountInString(u) > 2000 {
			continue
		}
		if err := wire.CheckHTTPS(u, "map link"); err != nil {
			continue
		}
		out = append(out, wire.MapLink{Title: title, URL: u})
	}
	return out
}

// amapMarkerURL builds an https AMap URI link to view a point (GCJ-02 lng,lat).
func amapMarkerURL(p point, name string) string {
	q := url.Values{"position": {amapLocation(p)}, "callnative": {"1"}}
	if clipped := clipFit(name, 80); clipped != "" {
		q.Set("name", clipped)
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
		"from":       {amapLocation(from.at) + "," + clipFit(from.name, 60)},
		"to":         {amapLocation(to.at) + "," + clipFit(to.name, 60)},
		"mode":       {amapMode},
		"callnative": {"1"},
	}
	return "https://uri.amap.com/navigation?" + q.Encode()
}

type routeStop struct {
	id       string
	name     string
	subtitle string
	detail   string
	at       point
}

type routeOption struct {
	id              string
	label           string
	distanceMeters  *int
	durationSeconds *int
	summary         string
	points          []point
	stops           []routeStop // intermediate transit boarding/transfer stops
}

func buildRouteMapComponent(mapKey, title, alt, caption string, from, to place, viaPlaces []place, mode string, options []routeOption, showList bool) *wire.Component {
	if len(options) == 0 || !from.at.valid() || !to.at.valid() {
		return nil
	}
	if len(options) > wire.MapRoutesMost {
		options = options[:wire.MapRoutesMost]
	}
	perRouteCap := min(routePointsPerRouteMax, routePointsTotalBudget/len(options))
	if perRouteCap < 2 {
		perRouteCap = 2
	}

	wireMode := mapTravelMode(mode)
	routes := make([]wire.MapRoute, 0, len(options))
	var allPts []point
	for i, opt := range options {
		pts := opt.points
		if len(pts) < 2 {
			fallback := make([]point, 0, 2+len(viaPlaces))
			fallback = appendPoints(fallback, []point{from.at})
			for _, vp := range viaPlaces {
				fallback = appendPoints(fallback, []point{vp.at})
			}
			fallback = appendPoints(fallback, []point{to.at})
			if len(fallback) == 1 {
				fallback = append(fallback, to.at)
			}
			pts = fallback
		}
		if len(pts) < 2 {
			continue
		}
		pts = downsamplePoints(pts, perRouteCap)
		wirePts := make([]wire.MapCoordinate, len(pts))
		for j, pt := range pts {
			wirePts[j] = mapCoord(pt)
		}
		allPts = append(allPts, pts...)

		mr := wire.MapRoute{
			ID:              clipFit(opt.id, 128),
			Label:           clipFit(opt.label, 200),
			Mode:            wireMode,
			DistanceMeters:  opt.distanceMeters,
			DurationSeconds: opt.durationSeconds,
			Summary:         clipFit(opt.summary, 200),
			Points:          wirePts,
		}
		if mr.ID == "" {
			mr.ID = fmt.Sprintf("route-%d", i+1)
		}
		switch i {
		case 0:
			mr.Selected = true
			mr.Tone = wire.TonePositive
		case 1:
			mr.Color = wire.ColorBlue
		default:
			mr.Color = wire.ColorGray
		}
		routes = append(routes, mr)
	}
	if len(routes) == 0 {
		return nil
	}

	fromLabel := clipFit(from.name, 200)
	if fromLabel == "" {
		fromLabel = "起点"
	}
	toLabel := clipFit(to.name, 200)
	if toLabel == "" {
		toLabel = "终点"
	}

	markers := make([]wire.MapMarker, 0, 2+len(viaPlaces)+6)
	markers = append(markers, wire.MapMarker{
		ID:       "origin",
		Lat:      floatPtr(from.at.Lat),
		Lon:      floatPtr(from.at.Lng),
		Kind:     wire.MapMarkerKindOrigin,
		Label:    fromLabel,
		Subtitle: clipFit(from.where, 200),
		Icon:     markerIconForName(from.name),
		Tone:     wire.TonePositive,
	})
	allPts = append(allPts, from.at, to.at)

	for i, vp := range viaPlaces {
		if !vp.at.valid() || len(markers) >= wire.MapMarkersMost-1 {
			break
		}
		vpLabel := clipFit(vp.name, 200)
		if vpLabel == "" {
			vpLabel = fmt.Sprintf("途经点 %d", i+1)
		}
		markers = append(markers, wire.MapMarker{
			ID:       fmt.Sprintf("wp-%d", i+1),
			Lat:      floatPtr(vp.at.Lat),
			Lon:      floatPtr(vp.at.Lng),
			Kind:     wire.MapMarkerKindWaypoint,
			Label:    vpLabel,
			Subtitle: clipFit(vp.where, 200),
			Icon:     "location",
			Color:    wire.ColorBlue,
		})
		allPts = append(allPts, vp.at)
	}

	if len(options[0].stops) > 0 {
		var added []point
		wpNum := 0
		for _, st := range options[0].stops {
			if !st.at.valid() || len(markers) >= wire.MapMarkersMost-1 || wpNum >= 6 {
				break
			}
			// Skip boarding/alighting stops that overlap origin/destination or already added stops.
			if metres(st.at, from.at) < 120 || metres(st.at, to.at) < 120 {
				continue
			}
			tooClose := false
			for _, prev := range added {
				if metres(st.at, prev) < 80 {
					tooClose = true
					break
				}
			}
			if tooClose {
				continue
			}
			wpNum++
			added = append(added, st.at)
			id := st.id
			if id == "" {
				id = fmt.Sprintf("stop-%d", wpNum)
			}
			label := clipFit(st.name, 200)
			if label == "" {
				label = fmt.Sprintf("站点 %d", wpNum)
			}
			markers = append(markers, wire.MapMarker{
				ID:       clipFit(id, 128),
				Lat:      floatPtr(st.at.Lat),
				Lon:      floatPtr(st.at.Lng),
				Kind:     wire.MapMarkerKindWaypoint,
				Label:    label,
				Subtitle: clipFit(st.subtitle, 200),
				Detail:   clipFit(st.detail, 2000),
				Icon:     "location",
				Color:    wire.ColorBlue,
			})
			allPts = append(allPts, st.at)
		}
	}

	markers = append(markers, wire.MapMarker{
		ID:       "destination",
		Lat:      floatPtr(to.at.Lat),
		Lon:      floatPtr(to.at.Lng),
		Kind:     wire.MapMarkerKindDestination,
		Label:    toLabel,
		Subtitle: clipFit(to.where, 200),
		Icon:     markerIconForName(to.name),
		Tone:     wire.ToneDanger,
	})

	altText := clipFit(alt, 500)
	if altText == "" {
		altText = clipFit(fmt.Sprintf("%s 到 %s 路线地图", fromLabel, toLabel), 500)
	}
	links := sanitizeMapLinks([]wire.MapLink{{
		Title: "在高德地图中打开导航",
		URL:   amapNavURL(from, to, mode),
	}})

	return &wire.Component{
		Type:     wire.ComponentMap,
		Key:      clipFit(mapKey, 128),
		Title:    clipFit(title, 2000),
		Alt:      altText,
		Caption:  clipFit(caption, 2000),
		Aspect:   wire.ImageAspectWide,
		Viewport: fitViewport(allPts, 0.012, 0.015),
		Markers:  markers,
		Routes:   routes,
		Links:    links,
		ShowList: showList,
	}
}

type placePin struct {
	id       string
	at       point
	kind     string
	label    string
	subtitle string
	detail   string
	icon     string
	tone     string
	color    string
	action   *wire.Action
}

func buildPlacesMapComponent(mapKey, title, alt, caption, aspect string, viewport *wire.MapViewport, pins []placePin, links []wire.MapLink, showList bool) *wire.Component {
	if len(pins) == 0 && viewport == nil {
		return nil
	}
	if len(pins) > wire.MapMarkersMost {
		pins = pins[:wire.MapMarkersMost]
	}
	markers := make([]wire.MapMarker, 0, len(pins))
	pts := make([]point, 0, len(pins))
	for i, p := range pins {
		if !p.at.valid() {
			continue
		}
		id := clipFit(p.id, 128)
		if id == "" {
			id = fmt.Sprintf("pin-%d", i+1)
		}
		label := clipFit(p.label, 200)
		if label == "" {
			label = fmt.Sprintf("地点 %d", i+1)
		}
		icon := p.icon
		if icon == "" {
			icon = "location"
		}
		m := wire.MapMarker{
			ID:       id,
			Lat:      floatPtr(p.at.Lat),
			Lon:      floatPtr(p.at.Lng),
			Kind:     p.kind,
			Label:    label,
			Subtitle: clipFit(p.subtitle, 200),
			Detail:   clipFit(p.detail, 2000),
			Icon:     icon,
			Action:   p.action,
		}
		if p.tone != "" {
			m.Tone = p.tone
		} else if p.color != "" {
			m.Color = p.color
		}
		markers = append(markers, m)
		pts = append(pts, p.at)
	}
	if len(markers) == 0 && viewport == nil {
		return nil
	}
	if viewport == nil {
		viewport = fitViewport(pts, 0.01, 0.012)
	}
	if aspect == "" {
		aspect = wire.ImageAspectWide
	}
	altText := clipFit(alt, 500)
	if altText == "" {
		altText = "高德地图标注"
	}
	return &wire.Component{
		Type:     wire.ComponentMap,
		Key:      clipFit(mapKey, 128),
		Title:    clipFit(title, 2000),
		Alt:      altText,
		Caption:  clipFit(caption, 2000),
		Aspect:   aspect,
		Viewport: viewport,
		Markers:  markers,
		Links:    sanitizeMapLinks(links),
		ShowList: showList,
	}
}
