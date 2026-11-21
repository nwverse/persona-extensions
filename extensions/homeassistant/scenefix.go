package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
)

// Corrections in home3d.json (#936): kept in the extension's data directory and applied after every RoomPlan scan so a
// rescan keeps hand-written fixes (rotation, rooms, wall edits, furniture kinds, and device pin positions).

const (
	sceneFixFile      = "home3d.json"
	sceneFixBytesMost = 1 << 20 // 1 MB
	defaultWallHeight = 1.1
)

var hexColorRE = regexp.MustCompile(`^#[0-9A-Fa-f]{6}$`)

type sceneFix struct {
	Rotate     *float64     `json:"rotate,omitempty"`
	WallHeight *float64     `json:"wall_height,omitempty"`
	Drop       []string     `json:"drop,omitempty"`
	Walls      []fixWall    `json:"walls,omitempty"`
	Doors      []fixDoor    `json:"doors,omitempty"`
	Split      []fixSplit   `json:"split,omitempty"`
	Openings   []fixOpening `json:"openings,omitempty"`
	Rooms      []fixRoom    `json:"rooms,omitempty"`
	Objects    []fixObject  `json:"objects,omitempty"`
	Add        []fixAdd     `json:"add,omitempty"`
	Devices    []fixDevice  `json:"devices,omitempty"`
}

type fixWall struct {
	Name      string    `json:"name,omitempty"`
	From      []float64 `json:"from"`
	To        []float64 `json:"to"`
	Thickness float64   `json:"thickness,omitempty"`
}

type fixDoor struct {
	Name  string    `json:"name,omitempty"`
	At    []float64 `json:"at,omitempty"`
	Kind  string    `json:"kind,omitempty"`
	Open  *bool     `json:"open,omitempty"`
	Width float64   `json:"width,omitempty"`
}

type fixSplit struct {
	Name string       `json:"name"`
	Keep fixKeepPairs `json:"keep"`
}

// fixKeepPairs accepts either a flat pair list [[x1,y1],[x2,y2],…] or nested segments [[[x1,y1],[x2,y2]],…].
type fixKeepPairs [][2][2]float64

func (kp *fixKeepPairs) UnmarshalJSON(data []byte) error {
	var flat [][]float64
	if err := json.Unmarshal(data, &flat); err == nil {
		if len(flat) == 0 || len(flat)%2 != 0 {
			return errors.New("keep needs pairs of [x, y] points")
		}
		out := make([][2][2]float64, 0, len(flat)/2)
		for i := 0; i < len(flat); i += 2 {
			p, ok1 := checkCoord2(flat[i])
			q, ok2 := checkCoord2(flat[i+1])
			if !ok1 || !ok2 {
				return errors.New("keep points must be finite [x, y] coordinates")
			}
			out = append(out, [2][2]float64{p, q})
		}
		*kp = out
		return nil
	}
	var nested [][][]float64
	if err := json.Unmarshal(data, &nested); err != nil {
		return errors.New("keep must be [[x1, y1], [x2, y2]]")
	}
	if len(nested) == 0 {
		return errors.New("keep needs at least one segment")
	}
	out := make([][2][2]float64, 0, len(nested))
	for _, seg := range nested {
		if len(seg) != 2 {
			return errors.New("each keep segment needs two [x, y] points")
		}
		p, ok1 := checkCoord2(seg[0])
		q, ok2 := checkCoord2(seg[1])
		if !ok1 || !ok2 {
			return errors.New("keep points must be finite [x, y] coordinates")
		}
		out = append(out, [2][2]float64{p, q})
	}
	*kp = out
	return nil
}

type fixOpening struct {
	Wall string  `json:"wall"`
	From float64 `json:"from"`
	To   float64 `json:"to"`
	Kind string  `json:"kind,omitempty"`
}

type fixRoom struct {
	Name       string      `json:"name"`
	Area       string      `json:"area,omitempty"`
	Polygon    [][]float64 `json:"polygon"`
	Floor      string      `json:"floor,omitempty"`
	FloorColor string      `json:"floor_color,omitempty"`
	WallColor  string      `json:"wall_color,omitempty"`
}

type fixObject struct {
	Name  string `json:"name"`
	Kind  string `json:"kind"`
	Color string `json:"color,omitempty"`
}

type fixAdd struct {
	Name   string    `json:"name,omitempty"`
	Kind   string    `json:"kind"`
	Center []float64 `json:"center"`
	Size   []float64 `json:"size,omitempty"`
	Yaw    *float64  `json:"yaw,omitempty"`
	Bottom float64   `json:"bottom,omitempty"`
	Top    float64   `json:"top,omitempty"`
	Color  string    `json:"color,omitempty"`
}

type fixDevice struct {
	Entity string    `json:"entity"`
	At     []float64 `json:"at"`
	Height *float64  `json:"height,omitempty"`
}

// loadSceneFix reads home3d.json from dir. A missing file returns an empty sceneFix and nil error; a malformed file
// returns an empty sceneFix alongside a plain error so the scene still builds without corrections.
func loadSceneFix(dir string) (*sceneFix, error) {
	if dir == "" {
		return &sceneFix{}, nil
	}
	f, err := os.Open(filepath.Join(dir, sceneFixFile))
	if errors.Is(err, os.ErrNotExist) {
		return &sceneFix{}, nil
	}
	if err != nil {
		return &sceneFix{}, fmt.Errorf("%s: %w", sceneFixFile, err)
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, sceneFixBytesMost+1))
	if err != nil {
		return &sceneFix{}, fmt.Errorf("%s: %w", sceneFixFile, err)
	}
	if len(raw) > sceneFixBytesMost {
		return &sceneFix{}, fmt.Errorf("%s: file exceeds 1 MB", sceneFixFile)
	}
	fix, err := parseSceneFix(raw)
	if err != nil {
		return &sceneFix{}, err
	}
	return fix, nil
}

// parseSceneFix decodes and validates home3d.json; unknown JSON keys are ignored.
func parseSceneFix(raw []byte) (*sceneFix, error) {
	var fix sceneFix
	if err := json.Unmarshal(raw, &fix); err != nil {
		return nil, fmt.Errorf("%s: invalid JSON (%v)", sceneFixFile, err)
	}
	if fix.Rotate != nil && !isFinite(*fix.Rotate) {
		return nil, fmt.Errorf("%s: rotate must be a finite number", sceneFixFile)
	}
	if fix.WallHeight != nil && (!isFinite(*fix.WallHeight) || *fix.WallHeight < 0.05 || *fix.WallHeight > 10) {
		return nil, fmt.Errorf("%s: wall_height must be between 0.05 and 10 m", sceneFixFile)
	}
	for i, w := range fix.Walls {
		a, ok1 := checkCoord2(w.From)
		b, ok2 := checkCoord2(w.To)
		if !ok1 || !ok2 || dist2D(a, b) < 0.02 || !isFinite(w.Thickness) || w.Thickness < 0 {
			return nil, fmt.Errorf("%s: walls[%d] needs distinct [x, y] endpoints", sceneFixFile, i)
		}
	}
	for i, d := range fix.Doors {
		if len(d.At) > 0 {
			if _, ok := checkCoord2(d.At); !ok {
				return nil, fmt.Errorf("%s: doors[%d].at must be [x, y]", sceneFixFile, i)
			}
		}
		if d.Name == "" && len(d.At) == 0 {
			return nil, fmt.Errorf("%s: doors[%d] needs name or at", sceneFixFile, i)
		}
		if d.Kind != "" && d.Kind != "door" && d.Kind != "sliding_glass" {
			return nil, fmt.Errorf("%s: doors[%d].kind %q is unknown", sceneFixFile, i, d.Kind)
		}
		if !isFinite(d.Width) || d.Width < 0 {
			return nil, fmt.Errorf("%s: doors[%d].width is invalid", sceneFixFile, i)
		}
	}
	for i, sp := range fix.Split {
		if strings.TrimSpace(sp.Name) == "" || len(sp.Keep) == 0 {
			return nil, fmt.Errorf("%s: split[%d] needs wall name and keep segments", sceneFixFile, i)
		}
		for _, seg := range sp.Keep {
			if dist2D(seg[0], seg[1]) < 0.02 {
				return nil, fmt.Errorf("%s: split[%d] segment is shorter than 0.02 m", sceneFixFile, i)
			}
		}
	}
	for i, op := range fix.Openings {
		if strings.TrimSpace(op.Wall) == "" || !isFinite(op.From) || !isFinite(op.To) || math.Abs(op.To-op.From) < 0.02 {
			return nil, fmt.Errorf("%s: openings[%d] needs wall and distinct from/to offsets", sceneFixFile, i)
		}
		if op.Kind != "" && op.Kind != "opening" && op.Kind != "sliding_glass" {
			return nil, fmt.Errorf("%s: openings[%d].kind %q is unknown", sceneFixFile, i, op.Kind)
		}
	}
	for i, rm := range fix.Rooms {
		if strings.TrimSpace(rm.Name) == "" {
			return nil, fmt.Errorf("%s: rooms[%d] needs a name", sceneFixFile, i)
		}
		pts := make([][2]float64, 0, len(rm.Polygon))
		for _, p := range rm.Polygon {
			c, ok := checkCoord2(p)
			if !ok {
				return nil, fmt.Errorf("%s: rooms[%d].polygon needs [x, y] points", sceneFixFile, i)
			}
			pts = append(pts, c)
		}
		if !simplePolygon(pts) {
			return nil, fmt.Errorf("%s: rooms[%d].polygon (%s) must be a simple polygon of 3 to 64 points", sceneFixFile, i, rm.Name)
		}
		if rm.Floor != "" && rm.Floor != "tile" && rm.Floor != "wood" {
			return nil, fmt.Errorf("%s: rooms[%d].floor must be tile or wood", sceneFixFile, i)
		}
		if rm.FloorColor != "" && !hexColorRE.MatchString(rm.FloorColor) {
			return nil, fmt.Errorf("%s: rooms[%d].floor_color must be #RRGGBB", sceneFixFile, i)
		}
		if rm.WallColor != "" && !hexColorRE.MatchString(rm.WallColor) {
			return nil, fmt.Errorf("%s: rooms[%d].wall_color must be #RRGGBB", sceneFixFile, i)
		}
	}
	for i, ob := range fix.Objects {
		if strings.TrimSpace(ob.Name) == "" || strings.TrimSpace(ob.Kind) == "" {
			return nil, fmt.Errorf("%s: objects[%d] needs name and kind", sceneFixFile, i)
		}
		if ob.Color != "" && !hexColorRE.MatchString(ob.Color) {
			return nil, fmt.Errorf("%s: objects[%d].color must be #RRGGBB", sceneFixFile, i)
		}
	}
	for i, ad := range fix.Add {
		if strings.TrimSpace(ad.Kind) == "" {
			return nil, fmt.Errorf("%s: add[%d] needs kind", sceneFixFile, i)
		}
		if _, ok := checkCoord2(ad.Center); !ok {
			return nil, fmt.Errorf("%s: add[%d].center must be [x, y]", sceneFixFile, i)
		}
		if len(ad.Size) > 0 {
			if len(ad.Size) != 2 || !isFinite(ad.Size[0]) || !isFinite(ad.Size[1]) || ad.Size[0] < 0.02 || ad.Size[1] < 0.02 || ad.Size[0] > 100 || ad.Size[1] > 100 {
				return nil, fmt.Errorf("%s: add[%d].size must be positive [width, depth]", sceneFixFile, i)
			}
		}
		if ad.Yaw != nil && !isFinite(*ad.Yaw) {
			return nil, fmt.Errorf("%s: add[%d].yaw must be finite", sceneFixFile, i)
		}
		if !isFinite(ad.Bottom) || !isFinite(ad.Top) || ad.Bottom < 0 || (ad.Top > 0 && ad.Top <= ad.Bottom) {
			return nil, fmt.Errorf("%s: add[%d] heights are invalid", sceneFixFile, i)
		}
		if ad.Color != "" && !hexColorRE.MatchString(ad.Color) {
			return nil, fmt.Errorf("%s: add[%d].color must be #RRGGBB", sceneFixFile, i)
		}
	}
	for i, dv := range fix.Devices {
		if strings.TrimSpace(dv.Entity) == "" {
			return nil, fmt.Errorf("%s: devices[%d] needs entity", sceneFixFile, i)
		}
		if _, ok := checkCoord2(dv.At); !ok {
			return nil, fmt.Errorf("%s: devices[%d].at must be [x, y]", sceneFixFile, i)
		}
		if dv.Height != nil && (!isFinite(*dv.Height) || *dv.Height < 0 || *dv.Height > 500) {
			return nil, fmt.Errorf("%s: devices[%d].height must be between 0 and 500 m", sceneFixFile, i)
		}
	}
	return &fix, nil
}

func isFinite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }

func checkCoord2(p []float64) ([2]float64, bool) {
	if len(p) != 2 || !isFinite(p[0]) || !isFinite(p[1]) || p[0] < -500 || p[0] > 500 || p[1] < -500 || p[1] > 500 {
		return [2]float64{}, false
	}
	return [2]float64{p[0], p[1]}, true
}

// ---- Plan representation after rotation and corrections ----

type planWall struct {
	Name      string
	A, B      [2]float64
	Thickness float64
}

type planRoom struct {
	Name       string
	Area       string
	Polygon    [][2]float64
	Floor      string
	FloorColor string
	WallColor  string
}

type fixedPlan struct {
	WallHeight   float64
	Walls        []planWall
	Doors        []scanDoor
	Windows      []scanWindow
	Openings     []scanOpening
	WallOpenings []fixOpening
	Objects      []scanObject
	Floor        [][2]float64
	Rooms        []planRoom
	ScanRooms    []scanRoom
	Devices      []fixDevice
}

// applySceneFix rotates a scan into axis-aligned plan coordinates and applies home3d.json corrections without mutating sc.
func applySceneFix(sc *scan, fix *sceneFix) *fixedPlan {
	if sc == nil {
		sc = &scan{}
	}
	if fix == nil {
		fix = &sceneFix{}
	}
	rotDeg := autoAlignAngle(sc.Walls)
	if fix.Rotate != nil {
		rotDeg = *fix.Rotate
	}
	rad := rotDeg * math.Pi / 180
	c, s := math.Cos(rad), math.Sin(rad)
	rotPt := func(p [2]float64) [2]float64 {
		return [2]float64{p[0]*c - p[1]*s, p[0]*s + p[1]*c}
	}
	dropped := func(name string) bool {
		return name != "" && slices.Contains(fix.Drop, name)
	}

	wallH := defaultWallHeight
	if fix.WallHeight != nil && *fix.WallHeight > 0 {
		wallH = min(max(*fix.WallHeight, 0.05), 2.4)
	}
	fp := &fixedPlan{
		WallHeight:   wallH,
		WallOpenings: slices.Clone(fix.Openings),
		Devices:      slices.Clone(fix.Devices),
	}

	// Rotate and convert scanned walls to endpoints, ordered west-to-east (then south-to-north).
	splits := map[string]fixKeepPairs{}
	for _, sp := range fix.Split {
		splits[sp.Name] = sp.Keep
	}
	for _, w := range sc.Walls {
		if dropped(w.Name) {
			continue
		}
		th := max(w.Size[1], 0.04)
		if keep, ok := splits[w.Name]; ok {
			for k, seg := range keep {
				nm := w.Name
				if k > 0 {
					nm = fmt.Sprintf("%s#%d", w.Name, k+1)
				}
				fp.Walls = append(fp.Walls, planWall{Name: nm, A: seg[0], B: seg[1], Thickness: th})
			}
			continue
		}
		yaw := (w.Yaw + rotDeg) * math.Pi / 180
		dx, dy := math.Cos(yaw)*w.Size[0]/2, math.Sin(yaw)*w.Size[0]/2
		ctr := rotPt(w.Center)
		a := [2]float64{ctr[0] - dx, ctr[1] - dy}
		b := [2]float64{ctr[0] + dx, ctr[1] + dy}
		if a[0] > b[0]+1e-4 || (math.Abs(a[0]-b[0]) <= 1e-4 && a[1] > b[1]) {
			a, b = b, a
		}
		fp.Walls = append(fp.Walls, planWall{Name: w.Name, A: a, B: b, Thickness: th})
	}
	for i, w := range fix.Walls {
		a, ok1 := checkCoord2(w.From)
		b, ok2 := checkCoord2(w.To)
		if !ok1 || !ok2 || dist2D(a, b) < 0.02 {
			continue
		}
		th := w.Thickness
		if th <= 0 {
			th = 0.12
		}
		nm := w.Name
		if nm == "" {
			nm = fmt.Sprintf("AddedWall%d", i+1)
		}
		fp.Walls = append(fp.Walls, planWall{Name: nm, A: a, B: b, Thickness: max(th, 0.04)})
	}

	for _, d := range sc.Doors {
		if dropped(d.Name) {
			continue
		}
		d.Center = rotPt(d.Center)
		d.Yaw = normAngle(d.Yaw + rotDeg)
		fp.Doors = append(fp.Doors, d)
	}
	for _, fd := range fix.Doors {
		matched := false
		if fd.Name != "" {
			for i := range fp.Doors {
				if fp.Doors[i].Name == fd.Name {
					if pt, ok := checkCoord2(fd.At); ok {
						fp.Doors[i].Center = pt
					}
					if fd.Kind != "" {
						fp.Doors[i].Kind = fd.Kind
					}
					if fd.Open != nil {
						fp.Doors[i].Open = *fd.Open
					}
					if fd.Width > 0 {
						fp.Doors[i].Size[0] = fd.Width
					}
					matched = true
				}
			}
		}
		if !matched {
			if pt, ok := checkCoord2(fd.At); ok {
				w := 0.9
				if fd.Width > 0 {
					w = fd.Width
				}
				open := false
				if fd.Open != nil {
					open = *fd.Open
				}
				fp.Doors = append(fp.Doors, scanDoor{
					Name: fd.Name, Kind: fd.Kind, Open: open, Center: pt, Size: [2]float64{w, 0.04}, Bottom: 0, Top: 2.1,
				})
			}
		}
	}

	for _, wn := range sc.Windows {
		if dropped(wn.Name) {
			continue
		}
		wn.Center = rotPt(wn.Center)
		wn.Yaw = normAngle(wn.Yaw + rotDeg)
		fp.Windows = append(fp.Windows, wn)
	}
	for _, op := range sc.Openings {
		if dropped(op.Name) {
			continue
		}
		op.Center = rotPt(op.Center)
		op.Yaw = normAngle(op.Yaw + rotDeg)
		fp.Openings = append(fp.Openings, op)
	}

	objFixes := map[string]fixObject{}
	for _, ob := range fix.Objects {
		objFixes[ob.Name] = ob
	}
	for _, ob := range sc.Objects {
		if dropped(ob.Name) {
			continue
		}
		ob.Center = rotPt(ob.Center)
		ob.Yaw = normAngle(ob.Yaw + rotDeg)
		if ov, ok := objFixes[ob.Name]; ok {
			ob.Kind = strings.ToLower(strings.TrimSpace(ov.Kind))
			if ov.Color != "" {
				ob.Color = ov.Color
			}
		}
		fp.Objects = append(fp.Objects, ob)
	}
	for _, ad := range fix.Add {
		pt, ok := checkCoord2(ad.Center)
		if !ok {
			continue
		}
		kind := strings.ToLower(strings.TrimSpace(ad.Kind))
		sz := defaultObjectSize(kind)
		if len(ad.Size) == 2 {
			sz = [2]float64{ad.Size[0], ad.Size[1]}
		}
		top := ad.Top
		if top <= ad.Bottom {
			top = ad.Bottom + defaultObjectHeight(kind)
		}
		var yaw float64
		fixedYaw := false
		if ad.Yaw != nil {
			yaw, fixedYaw = normAngle(*ad.Yaw), true
		}
		fp.Objects = append(fp.Objects, scanObject{
			Category: kind, Kind: kind, Name: ad.Name, Center: pt, Size: sz,
			Yaw: yaw, FixedYaw: fixedYaw, Bottom: ad.Bottom, Top: top, Color: ad.Color,
		})
	}

	if len(sc.Floor) >= 3 {
		poly := make([][2]float64, len(sc.Floor))
		for i, p := range sc.Floor {
			poly[i] = rotPt(p)
		}
		if simplePolygon(poly) {
			fp.Floor = poly
		}
	}
	for _, sr := range sc.Rooms {
		fp.ScanRooms = append(fp.ScanRooms, scanRoom{Name: sr.Name, Center: rotPt(sr.Center)})
	}
	for _, rm := range fix.Rooms {
		poly := make([][2]float64, 0, len(rm.Polygon))
		for _, p := range rm.Polygon {
			if c, ok := checkCoord2(p); ok {
				poly = append(poly, c)
			}
		}
		if simplePolygon(poly) {
			fp.Rooms = append(fp.Rooms, planRoom{
				Name: strings.TrimSpace(rm.Name), Area: strings.TrimSpace(rm.Area), Polygon: poly,
				Floor: rm.Floor, FloorColor: rm.FloorColor, WallColor: rm.WallColor,
			})
		}
	}
	return fp
}

// autoAlignAngle folds each wall's direction into 0–90° (centred on [-45°, 45°) so walls near 0° and 90° agree) and
// returns the rotation that aligns the median wall direction with the x/y axes.
func autoAlignAngle(walls []scanWall) float64 {
	if len(walls) == 0 {
		return 0
	}
	folded := make([]float64, 0, len(walls))
	for _, w := range walls {
		a := math.Mod(w.Yaw, 90)
		if a < 0 {
			a += 90
		}
		if a >= 45 {
			a -= 90
		}
		folded = append(folded, a)
	}
	sort.Float64s(folded)
	mid := len(folded) / 2
	med := folded[mid]
	if len(folded)%2 == 0 {
		med = (folded[mid-1] + folded[mid]) / 2
	}
	if math.Abs(med) < 1e-6 {
		return 0
	}
	return -med
}

func normAngle(deg float64) float64 {
	a := math.Mod(deg, 360)
	switch {
	case a > 180:
		a -= 360
	case a <= -180:
		a += 360
	}
	return a
}

func defaultObjectSize(kind string) [2]float64 {
	switch kind {
	case "sofa":
		return [2]float64{2.0, 0.9}
	case "bed":
		return [2]float64{1.6, 2.0}
	case "table", "bar_table":
		return [2]float64{1.4, 0.8}
	case "coffee_table":
		return [2]float64{1.1, 0.6}
	case "chair":
		return [2]float64{0.48, 0.48}
	case "piano":
		return [2]float64{1.48, 0.62}
	case "sideboard", "glass_cabinet", "storage":
		return [2]float64{1.5, 0.45}
	case "television":
		return [2]float64{1.4, 0.4}
	case "refrigerator":
		return [2]float64{0.75, 0.72}
	case "bathtub":
		return [2]float64{1.6, 0.75}
	case "toilet":
		return [2]float64{0.42, 0.68}
	default:
		return [2]float64{0.8, 0.6}
	}
}

func defaultObjectHeight(kind string) float64 {
	switch kind {
	case "sofa":
		return 0.82
	case "bed":
		return 0.95
	case "table":
		return 0.75
	case "bar_table":
		return 1.05
	case "coffee_table":
		return 0.42
	case "chair":
		return 0.82
	case "piano":
		return 1.18
	case "sideboard":
		return 0.78
	case "glass_cabinet", "refrigerator":
		return 1.85
	case "television":
		return 1.15
	case "bathtub":
		return 0.58
	case "toilet":
		return 0.76
	default:
		return 0.85
	}
}
