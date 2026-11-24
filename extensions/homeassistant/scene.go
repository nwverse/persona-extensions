package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/sdk/go/ext"
	"github.com/nwverse/persona/sdk/go/wire"
)

// 3D scene builder and runtime loader (#936): reads home.usdz and optional home3d.json from the data directory,
// matches scanned televisions, builds wire.Component{Type: "scene"}, and serves rendered poster PNGs through ui.image.

const (
	homeUSDZFile      = "home.usdz"
	posterRefPrefix   = "home3d:"
	posterCacheMost   = 8
	sceneShapesBudget = 499 // strictly under the 500-shape schema cap
	scenePinsMost     = 100
	sceneLabelsMost   = 32
	doorHostDistMost  = 0.25
	tvPinDistMost     = 0.8
	heightCap         = 2.4
	neutralWallColor  = "#ECE7DE"
	neutralTileColor  = "#EAE4DA"
	defaultWoodColor  = "#C8A882"
	sceneCameraYaw    = 225.0 // the view the scene opens at, and its poster's
)

type fileStamp struct {
	modNano int64
	size    int64
	exists  bool
}

type posterEntry struct {
	ref  string
	comp wire.Component
	pngs map[int][]byte
}

type sceneCache struct {
	mu        sync.Mutex
	usdzDir   string
	usdzStamp fileStamp
	scan      *scan
	fixStamp  fileStamp
	fix       *sceneFix
	posters   []posterEntry // bounded LRU, most recently used last
}

func (a *app) getDataDir() string {
	if a == nil {
		return ""
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.dataDir
}

// hasScene reports whether a valid home.usdz exists in the extension's data directory.
func (a *app) hasScene() bool {
	_, _, ok := a.loadSceneFiles()
	return ok
}

// loadSceneFiles reads and caches home.usdz and home3d.json by (mtime, size). Missing home.usdz is silent; a corrupt
// home.usdz or malformed home3d.json is logged once per file mtime.
func (a *app) loadSceneFiles() (*scan, *sceneFix, bool) {
	dir := a.getDataDir()
	if dir == "" {
		return nil, nil, false
	}
	usdzPath := filepath.Join(dir, homeUSDZFile)
	ust, err := os.Stat(usdzPath)
	if err != nil || ust.IsDir() {
		a.scene.mu.Lock()
		if !errors.Is(err, os.ErrNotExist) && err != nil && (a.scene.usdzDir != dir || a.scene.usdzStamp.exists) {
			if a.logf != nil {
				a.logf("home3d: %s: %v", homeUSDZFile, err)
			}
		}
		a.scene.usdzDir = dir
		a.scene.usdzStamp = fileStamp{}
		a.scene.scan = nil
		a.scene.mu.Unlock()
		return nil, nil, false
	}

	curUSDZ := fileStamp{modNano: ust.ModTime().UnixNano(), size: ust.Size(), exists: true}
	fixPath := filepath.Join(dir, sceneFixFile)
	fst, ferr := os.Stat(fixPath)
	var curFix fileStamp
	if ferr == nil && !fst.IsDir() {
		curFix = fileStamp{modNano: fst.ModTime().UnixNano(), size: fst.Size(), exists: true}
	}

	a.scene.mu.Lock()
	needUSDZ := a.scene.usdzDir != dir || a.scene.usdzStamp != curUSDZ
	hadScan := a.scene.scan != nil
	needFix := a.scene.usdzDir != dir || a.scene.fixStamp != curFix || a.scene.fix == nil
	a.scene.mu.Unlock()

	var newScan *scan
	var scanErr error
	if needUSDZ {
		newScan, scanErr = readRoomPlanFile(usdzPath)
	}
	var newFix *sceneFix
	var fixErr error
	if needFix {
		if curFix.exists {
			newFix, fixErr = loadSceneFix(dir)
		} else {
			newFix = &sceneFix{}
			if ferr != nil && !errors.Is(ferr, os.ErrNotExist) {
				fixErr = fmt.Errorf("%s: %w", sceneFixFile, ferr)
			}
		}
	}

	pokeWatch := false
	a.scene.mu.Lock()
	if a.scene.usdzDir != dir {
		a.scene.usdzDir = dir
		a.scene.usdzStamp = fileStamp{}
		a.scene.fixStamp = fileStamp{}
		a.scene.scan = nil
		a.scene.fix = nil
	}
	if needUSDZ {
		a.scene.usdzStamp = curUSDZ
		a.scene.scan = newScan
		if scanErr != nil && a.logf != nil {
			a.logf("home3d: %s: %v", homeUSDZFile, scanErr)
		}
		if newScan != nil && !hadScan {
			pokeWatch = true
		}
	}
	if needFix {
		a.scene.fixStamp = curFix
		a.scene.fix = newFix
		if fixErr != nil && a.logf != nil {
			a.logf("home3d: %v", fixErr)
		}
	}
	sc, fix := a.scene.scan, a.scene.fix
	a.scene.mu.Unlock()

	if pokeWatch && a.watch != nil {
		a.watch.poke()
	}
	if sc == nil {
		return nil, nil, false
	}
	if fix == nil {
		fix = &sceneFix{}
	}
	return sc, fix, true
}

func (c *sceneCache) putPoster(ref string, comp wire.Component) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i := range c.posters {
		if c.posters[i].ref == ref {
			e := c.posters[i]
			c.posters = append(c.posters[:i], c.posters[i+1:]...)
			c.posters = append(c.posters, e)
			return
		}
	}
	if len(c.posters) >= posterCacheMost {
		c.posters = c.posters[1:]
	}
	c.posters = append(c.posters, posterEntry{ref: ref, comp: comp, pngs: map[int][]byte{}})
}

func (c *sceneCache) getPoster(ref string, width int) (comp wire.Component, png []byte, hasComp bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i := range c.posters {
		if c.posters[i].ref == ref {
			e := c.posters[i]
			c.posters = append(c.posters[:i], c.posters[i+1:]...)
			c.posters = append(c.posters, e)
			return e.comp, e.pngs[width], true
		}
	}
	return wire.Component{}, nil, false
}

func (c *sceneCache) savePosterPNG(ref string, width int, png []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i := range c.posters {
		if c.posters[i].ref == ref {
			if len(c.posters[i].pngs) >= 4 {
				clear(c.posters[i].pngs)
			}
			c.posters[i].pngs[width] = png
			return
		}
	}
}

func posterRefFor(comp *wire.Component) string {
	cp := *comp
	cp.Poster = nil
	raw, _ := json.Marshal(cp)
	sum := sha256.Sum256(raw)
	return posterRefPrefix + hex.EncodeToString(sum[:12])
}

func (a *app) attachPoster(comp *wire.Component) {
	ref := posterRefFor(comp)
	if a != nil {
		a.scene.putPoster(ref, *comp)
	}
	comp.Poster = &wire.ImageSource{Ref: ref, Width: posterDefaultWidth, Height: posterDefaultHeight}
}

// homeScene loads home3d.json from the extension's data directory and builds the 3D scene component. When home3d.json
// is malformed, the error is returned for logging and the scene is still built without corrections.
func (a *app) homeScene(sc *scan, hm *home) (wire.Component, error) {
	fix, err := loadSceneFix(a.getDataDir())
	comp := a.buildSceneWith(sc, fix, hm, nil)
	a.attachPoster(&comp)
	return comp, err
}

// homeSceneSection builds the overview's top 3D section when home.usdz is in dataDir: a scene on UI version 11, its
// rendered poster image on UI version 6–10, and a plain text summary with pin rows on older clients.
func (a *app) homeSceneSection(r *ext.UIRequest, hm *home, reg *Registry) (wire.Section, bool) {
	sc, fix, ok := a.loadSceneFiles()
	if !ok {
		return wire.Section{}, false
	}
	comp := a.buildSceneWith(sc, fix, hm, reg)
	a.attachPoster(&comp)

	if r.UIVersion >= 11 {
		return wire.Section{Items: []wire.Component{comp}}, true
	}
	if r.UIVersion >= 6 {
		return wire.Section{Items: []wire.Component{{
			Type:    wire.ComponentImage,
			Key:     "home3d",
			Src:     comp.Poster,
			Alt:     comp.Alt,
			Caption: comp.Caption,
			Aspect:  wire.ImageAspectAuto,
		}}}, true
	}
	summary := comp.Alt
	if comp.Caption != "" {
		summary = clip(summary+" · "+comp.Caption, 200)
	}
	items := []wire.Component{{Type: wire.ComponentText, Key: "home3d", Text: summary, Style: wire.TextStyleSecondary}}
	for _, p := range comp.Pins {
		if len(items) >= 40 {
			break
		}
		row := wire.Component{
			Type:   wire.ComponentRow,
			Key:    ref("p:", p.ID),
			Title:  p.Label,
			Action: p.Action,
		}
		if p.State != "" {
			row.Badge = &wire.Badge{Text: clip(p.State, 12), Tone: p.Tone}
		}
		items = append(items, row)
	}
	return wire.Section{Items: items}, true
}

// image answers ui.image for a scene poster ref, rendering from the LRU cache or on demand after a restart.
func (a *app) image(ctx context.Context, r *ext.ImageRequest) (*wire.UIImage, error) {
	if r.Ref != "home3d" && !strings.HasPrefix(r.Ref, posterRefPrefix) {
		return nil, &ext.Error{Code: "not_found", Message: "unknown image ref"}
	}
	w := clampPosterWidth(r.Width)
	if comp, png, ok := a.scene.getPoster(r.Ref, w); ok {
		if len(png) > 0 {
			return ext.Image("image/png", png, 60), nil
		}
		rendered, err := renderScenePNG(comp, w)
		if err != nil {
			return nil, err
		}
		a.scene.savePosterPNG(r.Ref, w, rendered)
		return ext.Image("image/png", rendered, 60), nil
	}

	sc, fix, ok := a.loadSceneFiles()
	if !ok {
		return nil, errors.New("没有家的三维模型")
	}
	var hm *home
	var reg *Registry
	if h, _, _ := a.client(); h.configured() {
		if cur, err := a.home(ctx, statesFor); err == nil {
			hm = cur
		} else {
			hm = a.cachedHome()
		}
		reg = a.pageRegistry(ctx)
	}
	if hm == nil {
		hm = &home{}
	} else {
		hm = hm.named(reg)
	}
	comp := a.buildSceneWith(sc, fix, hm, reg)
	a.attachPoster(&comp)
	rendered, err := renderScenePNG(comp, w)
	if err != nil {
		return nil, err
	}
	if comp.Poster != nil {
		a.scene.savePosterPNG(comp.Poster.Ref, w, rendered)
	}
	return ext.Image("image/png", rendered, 60), nil
}

// buildScene builds a UI version 11 scene component from a RoomPlan scan, optional corrections and home states.
func buildScene(sc *scan, fix *sceneFix, hm *home) wire.Component {
	return (*app)(nil).buildSceneWith(sc, fix, hm, nil)
}

func (a *app) buildSceneWith(sc *scan, fix *sceneFix, hm *home, reg *Registry) wire.Component {
	if hm == nil {
		hm = &home{}
	}
	fp := applySceneFix(sc, fix)

	var archShapes []wire.SceneShape
	prismPoints := 0

	// Floors: one prism per corrected room polygon, else the scanned floor polygon.
	if len(fp.Rooms) > 0 {
		for _, rm := range fp.Rooms {
			if prismPoints+len(rm.Polygon) > wire.ScenePrismPointsMost {
				break
			}
			mat := wire.SceneMaterialTile
			col := neutralTileColor
			if rm.Floor == "wood" {
				mat, col = wire.SceneMaterialWood, defaultWoodColor
			}
			if rm.FloorColor != "" {
				col = rm.FloorColor
			}
			archShapes = append(archShapes, scenePrism(rm.Polygon, -0.02, 0, col, mat))
			prismPoints += len(rm.Polygon)
		}
	} else if len(fp.Floor) >= 3 && len(fp.Floor) <= wire.ScenePrismPointsMost {
		archShapes = append(archShapes, scenePrism(fp.Floor, -0.02, 0, neutralTileColor, wire.SceneMaterialTile))
		prismPoints += len(fp.Floor)
	}

	// Match doors, openings and windows to their closest host wall within 0.25 m, then split walls around gaps.
	archShapes = append(archShapes, buildWallsAndOpenings(fp)...)

	// Furniture / objects: orient to walls/tables, cap heights at 2.4 m, and drop smallest objects' detail first if over budget.
	objShapes := buildObjectsWithBudget(fp, max(0, sceneShapesBudget-len(archShapes)))
	shapes := append(archShapes, objShapes...)
	if len(shapes) > sceneShapesBudget {
		shapes = shapes[:sceneShapesBudget]
	}
	if len(shapes) == 0 {
		shapes = []wire.SceneShape{sceneBox([2]float64{0, 0}, [2]float64{1, 1}, 0, 0, -0.02, 0, neutralTileColor, wire.SceneMaterialTile)}
	}

	// Room names go in alt only: drawn on the floor they crowd the device pins, which already say what each one is (#1029).
	_, roomNames := buildRoomLabels(fp)
	devs := a.matchScannedTVs(fp, hm, reg)
	pins, pinnedLightsOn := a.buildScenePins(devs, hm, reg)
	// an air conditioner on the wall at each pinned one, when the walls are tall enough to hold it
	if ac := airConditionerShapes(pins, fp.Walls, fp.WallHeight); len(shapes)+len(ac) <= sceneShapesBudget {
		shapes = append(shapes, ac...)
	}

	lightsOn := countHomeLightsOn(hm, reg, devs, pinnedLightsOn)
	caption := ""
	if lightsOn > 0 {
		caption = fmt.Sprintf("%d 盏灯开着", lightsOn)
	}

	alt := "家的三维视图"
	if len(roomNames) > 0 {
		alt = clip("家的三维视图："+strings.Join(roomNames, "、"), 200)
	}

	camYaw, camPitch, camZoom := sceneCameraYaw, 35.0, 1.0
	return wire.Component{
		Type:    wire.ComponentScene,
		Key:     "home3d",
		Alt:     alt,
		Caption: caption,
		Camera:  &wire.SceneCamera{Yaw: &camYaw, Pitch: &camPitch, Zoom: &camZoom},
		Shapes:  shapes,
		Pins:    pins,
	}
}

// ---- Walls, doors, openings and windows ----

type wallSpan struct {
	from, to float64
	sillTop  float64 // 0 for full-depth door/opening cuts; > 0 for a window sill
}

func buildWallsAndOpenings(fp *fixedPlan) []wire.SceneShape {
	walls := fp.Walls
	spans := make([][]wallSpan, len(walls))
	wallH := fp.WallHeight

	findHost := func(pt [2]float64, width float64) (idx int, proj float64, ok bool) {
		bestIdx := -1
		bestDist := doorHostDistMost + 1e-9
		bestProj := 0.0
		for i, w := range walls {
			L := dist2D(w.A, w.B)
			if L < 0.02 {
				continue
			}
			ux, uy := (w.B[0]-w.A[0])/L, (w.B[1]-w.A[1])/L
			vx, vy := pt[0]-w.A[0], pt[1]-w.A[1]
			s := vx*ux + vy*uy
			d := math.Abs(-vx*uy + vy*ux)
			if d <= doorHostDistMost && s+width/2 > 0.01 && s-width/2 < L-0.01 && s >= -doorHostDistMost && s <= L+doorHostDistMost {
				if d < bestDist {
					bestIdx, bestDist, bestProj = i, d, s
				}
			}
		}
		return bestIdx, bestProj, bestIdx >= 0
	}

	type placedDoor struct {
		center    [2]float64
		width     float64
		thickness float64
		yaw       float64
		nx, ny    float64
		ux, uy    float64
		top       float64
		open      bool
		kind      string
		host      int
	}
	var doors []placedDoor
	for _, d := range fp.Doors {
		w := max(d.Size[0], 0.04)
		idx, s, ok := findHost(d.Center, w)
		if ok {
			hw := walls[idx]
			L := dist2D(hw.A, hw.B)
			ux, uy := (hw.B[0]-hw.A[0])/L, (hw.B[1]-hw.A[1])/L
			lo, hi := max(0, s-w/2), min(L, s+w/2)
			if hi-lo >= 0.02 {
				spans[idx] = append(spans[idx], wallSpan{from: lo, to: hi})
				mid := (lo + hi) / 2
				doors = append(doors, placedDoor{
					center: [2]float64{hw.A[0] + ux*mid, hw.A[1] + uy*mid}, width: hi - lo, thickness: hw.Thickness,
					yaw: math.Atan2(uy, ux) * 180 / math.Pi, nx: -uy, ny: ux, ux: ux, uy: uy,
					top: d.Top, open: d.Open, kind: d.Kind, host: idx,
				})
				continue
			}
		}
		rad := d.Yaw * math.Pi / 180
		ux, uy := math.Cos(rad), math.Sin(rad)
		doors = append(doors, placedDoor{
			center: d.Center, width: w, thickness: max(d.Size[1], 0.08),
			yaw: d.Yaw, nx: -uy, ny: ux, ux: ux, uy: uy, top: d.Top, open: d.Open, kind: d.Kind, host: -1,
		})
	}

	for _, op := range fp.Openings {
		w := max(op.Size[0], 0.04)
		if idx, s, ok := findHost(op.Center, w); ok {
			L := dist2D(walls[idx].A, walls[idx].B)
			lo, hi := max(0, s-w/2), min(L, s+w/2)
			if hi-lo >= 0.02 {
				spans[idx] = append(spans[idx], wallSpan{from: lo, to: hi})
			}
		}
	}

	for _, op := range fp.WallOpenings {
		for i, w := range walls {
			if w.Name != op.Wall {
				continue
			}
			L := dist2D(w.A, w.B)
			lo, hi := max(0, min(op.From, op.To)), min(L, max(op.From, op.To))
			if hi-lo < 0.02 {
				continue
			}
			spans[i] = append(spans[i], wallSpan{from: lo, to: hi})
			if op.Kind == "sliding_glass" {
				ux, uy := (w.B[0]-w.A[0])/L, (w.B[1]-w.A[1])/L
				mid := (lo + hi) / 2
				doors = append(doors, placedDoor{
					center: [2]float64{w.A[0] + ux*mid, w.A[1] + uy*mid}, width: hi - lo, thickness: w.Thickness,
					yaw: math.Atan2(uy, ux) * 180 / math.Pi, nx: -uy, ny: ux, ux: ux, uy: uy,
					top: wallH, open: false, kind: "sliding_glass", host: i,
				})
			}
		}
	}

	type placedWin struct {
		center    [2]float64
		width     float64
		thickness float64
		yaw       float64
		bottom    float64
		host      int
	}
	var wins []placedWin
	for _, wn := range fp.Windows {
		wb := max(0, wn.Bottom)
		if wb >= wallH-0.01 {
			continue // only draw windows whose bottom sits below wall_height
		}
		w := max(wn.Size[0], 0.04)
		if idx, s, ok := findHost(wn.Center, w); ok {
			hw := walls[idx]
			L := dist2D(hw.A, hw.B)
			ux, uy := (hw.B[0]-hw.A[0])/L, (hw.B[1]-hw.A[1])/L
			lo, hi := max(0, s-w/2), min(L, s+w/2)
			if hi-lo >= 0.02 {
				spans[idx] = append(spans[idx], wallSpan{from: lo, to: hi, sillTop: wb})
				mid := (lo + hi) / 2
				wins = append(wins, placedWin{
					center: [2]float64{hw.A[0] + ux*mid, hw.A[1] + uy*mid}, width: hi - lo, thickness: hw.Thickness,
					yaw: math.Atan2(uy, ux) * 180 / math.Pi, bottom: wb, host: idx,
				})
				continue
			}
		}
		wins = append(wins, placedWin{center: wn.Center, width: w, thickness: 0.08, yaw: wn.Yaw, bottom: wb, host: -1})
	}

	var shapes []wire.SceneShape
	for i, w := range walls {
		L := dist2D(w.A, w.B)
		if L < 0.02 {
			continue
		}
		ux, uy := (w.B[0]-w.A[0])/L, (w.B[1]-w.A[1])/L
		yaw := math.Atan2(uy, ux) * 180 / math.Pi
		th := max(w.Thickness, 0.04)
		nx, ny := -uy, ux

		for _, pc := range splitWallPieces(L, wallH, spans[i]) {
			// drawn on to (or short of) the walls it meets
			if pc.from < 1e-4 {
				pc.from = -w.ReachA
			}
			if pc.to > L-1e-4 {
				pc.to = L + w.ReachB
			}
			if pc.to-pc.from < 0.01 {
				continue
			}
			mid := (pc.from + pc.to) / 2
			ctr := [2]float64{w.A[0] + ux*mid, w.A[1] + uy*mid}
			left, right := wallFaceColors(w, pc.from, pc.to, fp.Rooms, fp.WallFaces)
			if left == right {
				wall := sceneBox(ctr, [2]float64{pc.to - pc.from, th}, yaw, 0.01, 0, pc.top, left, wire.SceneMaterialMatte)
				wall.Wall = true // for the app's cutaway (#947)
				shapes = append(shapes, wall)
				continue
			}
			// a wall between two differently colored rooms (or a face colored on its own): one layer per face
			for _, layer := range []struct {
				side float64
				col  string
			}{{1, left}, {-1, right}} {
				c := [2]float64{ctr[0] + nx*layer.side*th/4, ctr[1] + ny*layer.side*th/4}
				wall := sceneBox(c, [2]float64{pc.to - pc.from, th / 2}, yaw, 0.005, 0, pc.top, layer.col, wire.SceneMaterialMatte)
				wall.Wall = true
				shapes = append(shapes, wall)
			}
		}
	}

	for _, d := range doors {
		if d.kind == "sliding_glass" {
			// Glass panels in a slim frame: one closed, one slid behind it when open.
			offN := d.thickness*0.4 + 0.02
			offU := d.width * 0.25
			if !d.open {
				offU = 0
			}
			ctr := [2]float64{d.center[0] + d.nx*offN + d.ux*offU, d.center[1] + d.ny*offN + d.uy*offU}
			shapes = append(shapes, sceneBox(ctr, [2]float64{d.width, 0.03}, d.yaw, 0.005, 0, wallH, "#B8D8E8", wire.SceneMaterialGlass))
			shapes = append(shapes, windowFrameShapes(d.center, d.width, d.thickness, d.yaw, 0, wallH, d.ux, d.uy)...)
			continue
		}
		shapes = append(shapes, doorShapes(d.center, d.width, d.thickness, d.yaw, d.top, wallH, d.open, d.ux, d.uy, d.nx, d.ny, fp.Rooms, fp.Objects)...)
	}

	for _, wn := range wins {
		shapes = append(shapes, sceneBox(wn.center, [2]float64{wn.width, 0.03}, wn.yaw, 0.005, wn.bottom, wallH, "#B8D8E8", wire.SceneMaterialGlass))
		rad := wn.yaw * math.Pi / 180
		shapes = append(shapes, windowFrameShapes(wn.center, wn.width, wn.thickness, wn.yaw, wn.bottom, wallH, math.Cos(rad), math.Sin(rad))...)
	}

	// Curtains on the windows and glass doors of the walls home3d.json names.
	for _, cu := range fp.Curtains {
		color := cu.Color
		if color == "" {
			color = curtainColor
		}
		hang := func(host int, center [2]float64, width float64) {
			if host < 0 || wallBase(walls[host].Name) != cu.Wall {
				return
			}
			ux, uy, nx, ny, _ := wallFrame(walls[host])
			sgn := curtainSide(center, nx, ny, fp.Rooms, cu.Room)
			yaw := math.Atan2(uy, ux) * 180 / math.Pi
			shapes = append(shapes, curtainShapes(center, width, walls[host].Thickness, yaw, wallH, ux, uy, nx, ny, sgn, color)...)
		}
		for _, wn := range wins {
			hang(wn.host, wn.center, wn.width)
		}
		for _, d := range doors {
			if d.kind == "sliding_glass" {
				hang(d.host, d.center, d.width)
			}
		}
	}
	return shapes
}

type wallPiece struct {
	from, to, top float64
}

func splitWallPieces(length, wallH float64, cuts []wallSpan) []wallPiece {
	if len(cuts) == 0 {
		return []wallPiece{{from: 0, to: length, top: wallH}}
	}
	// Collect unique breakpoint coordinates along [0, length], keeping the lowest ceiling over each sub-interval.
	pts := []float64{0, length}
	for _, c := range cuts {
		pts = append(pts, max(0, c.from), min(length, c.to))
	}
	sort.Float64s(pts)
	var uniq []float64
	for _, x := range pts {
		if len(uniq) == 0 || x-uniq[len(uniq)-1] > 1e-4 {
			uniq = append(uniq, x)
		}
	}
	var raw []wallPiece
	for i := 0; i+1 < len(uniq); i++ {
		a, b := uniq[i], uniq[i+1]
		mid := (a + b) / 2
		top := wallH
		for _, c := range cuts {
			if mid >= c.from && mid <= c.to {
				top = min(top, c.sillTop)
			}
		}
		if top < 0.02 || b-a < 1e-4 {
			continue
		}
		if len(raw) > 0 && math.Abs(raw[len(raw)-1].to-a) < 1e-4 && math.Abs(raw[len(raw)-1].top-top) < 1e-4 {
			raw[len(raw)-1].to = b
		} else {
			raw = append(raw, wallPiece{from: a, to: b, top: top})
		}
	}
	var out []wallPiece
	for _, p := range raw {
		if p.to-p.from >= 0.02 {
			out = append(out, p)
		}
	}
	return out
}

// wallFaceColors are the colors of a wall's two faces along [from, to] (metres from A): on the left of A→B and on the
// right. A face takes its wall_faces entry for the room it faces, else that room's wall_color; a face with no room
// (outdoors) takes the other face's color, and a wall with none neutralWallColor.
func wallFaceColors(w planWall, from, to float64, rooms []planRoom, faces []fixCurtain) (left, right string) {
	L := dist2D(w.A, w.B)
	if L < 0.02 {
		return neutralWallColor, neutralWallColor
	}
	ux, uy := (w.B[0]-w.A[0])/L, (w.B[1]-w.A[1])/L
	nx, ny := -uy, ux
	probe := w.Thickness/2 + 0.15
	face := func(side float64) string {
		best, bestHits := -1, 0
		for ri, rm := range rooms {
			hits := 0
			for _, t := range []float64{0.1, 0.3, 0.5, 0.7, 0.9} {
				at := from + (to-from)*t
				p := [2]float64{w.A[0] + ux*at + nx*side*probe, w.A[1] + uy*at + ny*side*probe}
				if pointInPolygon(p, rm.Polygon) {
					hits++
				}
			}
			if hits > bestHits {
				best, bestHits = ri, hits
			}
		}
		if best < 0 {
			return ""
		}
		rm := rooms[best]
		for _, f := range faces {
			if f.Wall == w.Name && f.Room == rm.Name {
				return f.Color
			}
		}
		return rm.WallColor
	}
	left, right = face(1), face(-1)
	switch {
	case left == "" && right == "":
		return neutralWallColor, neutralWallColor
	case left == "":
		return right, right
	case right == "":
		return left, left
	}
	return left, right
}

func pointInPolygon(pt [2]float64, poly [][2]float64) bool {
	inside := false
	n := len(poly)
	for i, j := 0, n-1; i < n; j, i = i, i+1 {
		xi, yi := poly[i][0], poly[i][1]
		xj, yj := poly[j][0], poly[j][1]
		if ((yi > pt[1]) != (yj > pt[1])) && (pt[0] < (xj-xi)*(pt[1]-yi)/(yj-yi)+xi) {
			inside = !inside
		}
	}
	return inside
}

// ---- Furniture / objects & 500-shape budget ----

func buildObjectsWithBudget(fp *fixedPlan, budget int) []wire.SceneShape {
	if len(fp.Objects) == 0 || budget <= 0 {
		return nil
	}
	objs := make([]scanObject, len(fp.Objects))
	copy(objs, fp.Objects)

	// Orient objects before building parts: chairs face the nearest table, other objects back onto the nearest wall.
	curtained := map[string]bool{}
	for _, cu := range fp.Curtains {
		curtained[cu.Wall] = true
	}
	for i := range objs {
		orientObject(&objs[i], fp.Walls, objs)
		kind := objs[i].Kind
		if kind == "" {
			kind = objs[i].Category
		}
		if !standsFree(kind) {
			keepOffWalls(&objs[i], fp.Walls, curtained)
		}
	}

	type builtObj struct {
		idx      int
		area     float64
		detailed []wire.SceneShape
		plain    wire.SceneShape
		simple   bool
	}
	built := make([]builtObj, len(objs))
	total := 0
	for i, ob := range objs {
		det, pl := objectShapes(ob)
		built[i] = builtObj{idx: i, area: ob.Size[0] * ob.Size[1], detailed: det, plain: pl}
		total += len(det)
	}

	// Order indices smallest-footprint-first so detail is dropped on small objects before larger ones.
	order := make([]int, len(built))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(i, j int) bool { return built[order[i]].area < built[order[j]].area })

	for _, k := range order {
		if total <= budget {
			break
		}
		if len(built[k].detailed) > 1 {
			total -= len(built[k].detailed) - 1
			built[k].simple = true
		}
	}

	dropped := map[int]bool{}
	for _, k := range order {
		if total <= budget {
			break
		}
		dropped[k] = true
		if built[k].simple {
			total--
		} else {
			total -= len(built[k].detailed)
		}
	}

	var out []wire.SceneShape
	for i, b := range built {
		if dropped[i] {
			continue
		}
		if b.simple {
			out = append(out, b.plain)
		} else {
			out = append(out, b.detailed...)
		}
	}
	return out
}

func orientObject(ob *scanObject, walls []planWall, all []scanObject) {
	if ob.FixedYaw {
		return
	}
	kind := ob.Kind
	if kind == "" {
		kind = ob.Category
	}
	targetYaw := ob.Yaw
	found := false

	if kind == "chair" {
		bestDist := math.MaxFloat64
		for _, other := range all {
			okind := other.Kind
			if okind == "" {
				okind = other.Category
			}
			if okind != "table" && okind != "bar_table" && okind != "coffee_table" {
				continue
			}
			dx, dy := other.Center[0]-ob.Center[0], other.Center[1]-ob.Center[1]
			d := math.Hypot(dx, dy)
			if d > 1e-3 && d < bestDist {
				bestDist = d
				// Local +y (front) points at the nearest table: (-sin yaw, cos yaw) = (dx, dy) / d.
				targetYaw = math.Atan2(-dx, dy) * 180 / math.Pi
				found = true
			}
		}
	}
	if !found && len(walls) > 0 {
		bestDist := math.MaxFloat64
		var awayX, awayY float64
		for _, w := range walls {
			cp := closestPointOnSegment(ob.Center, w.A, w.B)
			dx, dy := ob.Center[0]-cp[0], ob.Center[1]-cp[1]
			d := math.Hypot(dx, dy)
			if d < bestDist {
				bestDist = d
				if d > 1e-3 {
					awayX, awayY = dx, dy
				} else {
					awayX, awayY = -(w.B[1] - w.A[1]), w.B[0]-w.A[0]
				}
				found = true
			}
		}
		if found && math.Hypot(awayX, awayY) > 1e-6 {
			// Local -y (back) faces the wall, so local +y (front) points along (awayX, awayY).
			targetYaw = math.Atan2(-awayX, awayY) * 180 / math.Pi
		}
	}
	if !found {
		return
	}
	diff := (targetYaw - ob.Yaw) * math.Pi / 180
	if math.Abs(math.Sin(diff)) > math.Sqrt2/2 {
		ob.Size[0], ob.Size[1] = ob.Size[1], ob.Size[0]
	}
	ob.Yaw = normAngle(targetYaw)
}

func closestPointOnSegment(p, a, b [2]float64) [2]float64 {
	vx, vy := b[0]-a[0], b[1]-a[1]
	l2 := vx*vx + vy*vy
	if l2 < 1e-9 {
		return a
	}
	t := ((p[0]-a[0])*vx + (p[1]-a[1])*vy) / l2
	t = max(0, min(1, t))
	return [2]float64{a[0] + t*vx, a[1] + t*vy}
}

func objectShapes(ob scanObject) (detailed []wire.SceneShape, plain wire.SceneShape) {
	kind := ob.Kind
	if kind == "" {
		kind = ob.Category
	}
	w, d := max(ob.Size[0], 0.08), max(ob.Size[1], 0.08)
	b0 := min(max(ob.Bottom, 0), heightCap-0.06)
	top := min(max(ob.Top, b0+0.06), heightCap)
	h := top - b0

	rad := ob.Yaw * math.Pi / 180
	cs, sn := math.Cos(rad), math.Sin(rad)
	at := func(lx, ly float64) [2]float64 {
		return [2]float64{ob.Center[0] + lx*cs - ly*sn, ob.Center[1] + lx*sn + ly*cs}
	}
	box := func(lx, ly, sw, sd, round, z0, z1 float64, col, mat string) wire.SceneShape {
		return sceneBox(at(lx, ly), [2]float64{sw, sd}, ob.Yaw, round, z0, z1, col, mat)
	}
	cyl := func(lx, ly, r, z0, z1 float64, col, mat string) wire.SceneShape {
		return sceneCyl(at(lx, ly), r, z0, z1, col, mat)
	}
	pickCol := func(def string) string {
		if ob.Color != "" {
			return ob.Color
		}
		return def
	}

	plainCol, plainMat := pickCol("#B89872"), wire.SceneMaterialWood
	var parts []wire.SceneShape

	switch kind {
	case "sofa":
		// A plinth, a base, two arms, and a seat and back cushion per seat.
		col := pickCol("#C8BBAE")
		plainCol, plainMat = col, wire.SceneMaterialMatte
		armW := min(0.2, w*0.12)
		backD := min(0.24, d*0.26)
		baseTop := b0 + h*0.36
		seatTop := b0 + h*0.53
		parts = append(parts,
			box(0, 0, max(0.06, w-0.08), max(0.06, d-0.08), 0.01, b0, b0+h*0.13, shade(col, -28), wire.SceneMaterialMatte),
			box(0, 0, w, d, 0.03, b0+h*0.13, baseTop, col, wire.SceneMaterialMatte),
			box(-w/2+armW/2, 0, armW, d, 0.05, b0+h*0.13, b0+h*0.74, shade(col, -8), wire.SceneMaterialMatte),
			box(w/2-armW/2, 0, armW, d, 0.05, b0+h*0.13, b0+h*0.74, shade(col, -8), wire.SceneMaterialMatte),
		)
		inner := max(0.1, w-2*armW)
		seats := max(1, min(4, int(math.Round(inner/0.65))))
		cw := inner / float64(seats)
		for k := range seats {
			lx := -inner/2 + cw*(float64(k)+0.5)
			parts = append(parts,
				box(lx, (backD-0.03)/2, max(0.06, cw-0.03), max(0.06, d-backD-0.03), 0.05, baseTop, seatTop, shade(col, 10), wire.SceneMaterialMatte),
				box(lx, -d/2+backD/2+0.02, max(0.06, cw-0.04), backD, 0.06, baseTop, top, shade(col, 4), wire.SceneMaterialMatte),
			)
		}

	case "bed":
		wood := pickCol("#B59672")
		plainCol, plainMat = "#F0ECE4", wire.SceneMaterialMatte
		hbD := min(0.10, d*0.08)
		frameTop := b0 + h*0.28
		matTop := b0 + h*0.56
		pilTop := min(top, matTop+max(0.08, h*0.14))
		pw, pd := max(0.10, (w-0.24)/2), min(0.36, (d-hbD)*0.25)
		parts = append(parts,
			box(0, hbD/2, w, max(0.10, d-hbD), 0.02, b0, frameTop, wood, wire.SceneMaterialWood),
			box(0, hbD/2, max(0.10, w-0.06), max(0.10, d-hbD-0.04), 0.04, frameTop, matTop, "#F2EFE9", wire.SceneMaterialMatte),
			box(0, -d/2+hbD/2, w, hbD, 0.02, b0, top, wood, wire.SceneMaterialWood),
			box(-w*0.23, -d/2+hbD+pd/2+0.04, pw, pd, 0.04, matTop, pilTop, "#FFFFFF", wire.SceneMaterialMatte),
			box(w*0.23, -d/2+hbD+pd/2+0.04, pw, pd, 0.04, matTop, pilTop, "#FFFFFF", wire.SceneMaterialMatte),
		)

	case "table", "coffee_table":
		col := pickCol("#BA9870")
		plainCol = col
		topThick := min(0.05, h*0.2)
		legR := min(0.03, min(w, d)*0.08)
		under := max(b0+0.02, top-topThick)
		parts = append(parts, box(0, 0, w, d, 0.02, under, top, col, wire.SceneMaterialWood))
		ox, oy := max(0, w/2-legR-0.03), max(0, d/2-legR-0.03)
		for _, sx := range []float64{-1, 1} {
			for _, sy := range []float64{-1, 1} {
				parts = append(parts, cyl(sx*ox, sy*oy, max(0.015, legR), b0, under, shade(col, -40), wire.SceneMaterialMetal))
			}
		}

	case "bar_table":
		// A top with one rounded end, on two cages of thin gold rods standing on white plates.
		col := pickCol("#F4F2EC")
		plainCol = col
		under := max(b0+0.05, top-0.04)
		long, short := d, w
		along := func(t float64) (float64, float64) { return 0, t }
		if w > d {
			long, short = w, d
			along = func(t float64) (float64, float64) { return t, 0 }
		}
		r := short / 2
		rx, ry := along(long/2 - r)
		cx, cy := along(-r / 2)
		if long > short {
			if w > d {
				parts = append(parts, box(cx, cy, long-r, short, 0.01, under, top, col, wire.SceneMaterialMatte))
			} else {
				parts = append(parts, box(cx, cy, short, long-r, 0.01, under, top, col, wire.SceneMaterialMatte))
			}
		}
		parts = append(parts, cyl(rx, ry, r, under, top, col, wire.SceneMaterialMatte))
		cage := min(0.2, short*0.36)
		for _, t := range []float64{-long * 0.33, long * 0.28} {
			px, py := along(t)
			parts = append(parts, cyl(px, py, cage, b0, b0+0.03, col, wire.SceneMaterialMatte))
			for k := range 8 {
				a := 2 * math.Pi * float64(k) / 8
				parts = append(parts, cyl(px+cage*0.95*math.Cos(a), py+cage*0.95*math.Sin(a), 0.012, b0+0.03, under, goldColor, wire.SceneMaterialMetal))
			}
		}

	case "round_coffee_table":
		// A round top over a round shelf, on a gold post and foot.
		col := pickCol("#F6F4EE")
		plainCol, plainMat = col, wire.SceneMaterialMatte
		r := max(0.2, min(w, d)/2)
		parts = append(parts,
			cyl(0, 0, r, max(b0+0.1, top-0.05), top, col, wire.SceneMaterialMatte),
			cyl(0, 0, r*0.9, b0+h*0.27, b0+h*0.34, col, wire.SceneMaterialMatte),
			cyl(0, 0, r*0.55, b0, b0+0.03, goldColor, wire.SceneMaterialMetal),
			cyl(0, 0, 0.04, b0+0.03, max(b0+0.1, top-0.05), goldColor, wire.SceneMaterialMetal),
		)

	case "bench":
		col := pickCol("#3C3430")
		plainCol = col
		parts = append(parts,
			box(0, 0, w, d, 0.02, max(b0+0.05, top-0.05), top, col, wire.SceneMaterialWood),
			box(0, 0, max(0.06, w-0.1), max(0.06, d-0.08), 0.01, b0, max(b0+0.05, top-0.05), shade(col, -16), wire.SceneMaterialWood),
		)

	case "chair":
		col := pickCol("#C4A47C")
		plainCol = col
		seatZ0 := b0 + h*0.46
		seatZ1 := b0 + h*0.53
		backD := min(0.06, d*0.18)
		legR := 0.018
		parts = append(parts,
			box(0, 0, w, d, 0.02, seatZ0, seatZ1, col, wire.SceneMaterialWood),
			box(0, -d/2+backD/2, w, backD, 0.02, seatZ1, top, col, wire.SceneMaterialWood),
		)
		ox, oy := max(0, w/2-0.04), max(0, d/2-0.04)
		for _, sx := range []float64{-1, 1} {
			for _, sy := range []float64{-1, 1} {
				parts = append(parts, cyl(sx*ox, sy*oy, legR, b0, seatZ0, "#505054", wire.SceneMaterialMetal))
			}
		}

	case "storage", "sideboard":
		// A plinth, a carcass, doors with handles, and a top: trim on a tall cabinet, a dark slab on a sideboard. Matte: a
		// painted or laminated front, which the app's wood grain would turn into bricks at this size.
		col := pickCol("#C6AA86")
		plainCol, plainMat = col, wire.SceneMaterialMatte
		tall := h > 1.5
		zb := b0
		if b0 < 0.05 && h > 0.25 {
			zb = b0 + 0.08
			parts = append(parts, box(0, -0.01, max(0.06, w-0.04), max(0.06, d-0.04), 0.005, b0, zb, shade(col, -40), wire.SceneMaterialMatte))
		}
		parts = append(parts, box(0, -0.015, w, max(0.04, d-0.03), 0.01, zb, top, shade(col, -12), wire.SceneMaterialMatte))
		doorW := 0.5
		if tall {
			doorW = 0.45
		}
		n := max(1, min(8, int(math.Round(w/doorW))))
		dw := w / float64(n)
		for k := range n {
			lx := -w/2 + dw*(float64(k)+0.5)
			parts = append(parts, box(lx, d/2-0.015, max(0.02, dw-0.012), 0.03, 0.005, zb+0.006, top-0.006, col, wire.SceneMaterialMatte))
			hx := lx + dw/2 - 0.05
			if k%2 == 1 {
				hx = lx - dw/2 + 0.05
			}
			hz0, hz1 := top-0.22, top-0.1
			switch {
			case tall:
				hz0, hz1 = zb+0.85, zb+1.25
			case b0 > 1.1:
				hz0, hz1 = zb+0.04, zb+0.16
			}
			if hz1 <= top && dw > 0.12 {
				parts = append(parts, box(hx, d/2+0.012, 0.02, 0.025, 0.004, hz0, hz1, "#B0B2B8", wire.SceneMaterialMetal))
			}
		}
		if tall {
			parts = append(parts, box(0, 0, w+0.02, d+0.015, 0.005, top, min(heightCap, top+0.04), shade(col, 6), wire.SceneMaterialMatte))
		}
		if kind == "sideboard" {
			parts = append(parts, box(0, 0.01, w, d+0.02, 0.005, top, min(heightCap, top+0.03), "#3A3A3E", wire.SceneMaterialMatte))
		}

	case "glass_cabinet":
		col := pickCol("#7A624C")
		plainCol = col
		splitZ := b0 + h*0.36
		parts = append(parts,
			box(0, 0, w, d, 0.01, b0, splitZ, col, wire.SceneMaterialWood),
			box(0, 0, w, d, 0.01, splitZ, top, "#C6DCE6", wire.SceneMaterialGlass),
		)

	case "refrigerator":
		col := pickCol("#D8DCE0")
		plainCol, plainMat = col, wire.SceneMaterialMetal
		splitZ := b0 + h*0.38
		parts = append(parts, box(0, 0, w, d, 0.02, b0, splitZ-0.005, col, wire.SceneMaterialMetal))
		if w >= 0.82 {
			dw := (w - 0.01) / 2
			parts = append(parts,
				box(-w/2+dw/2, 0, dw, d, 0.02, splitZ+0.005, top, col, wire.SceneMaterialMetal),
				box(w/2-dw/2, 0, dw, d, 0.02, splitZ+0.005, top, col, wire.SceneMaterialMetal),
			)
		} else {
			parts = append(parts, box(0, 0, w, d, 0.02, splitZ+0.005, top, col, wire.SceneMaterialMetal))
		}

	case "television":
		plainCol, plainMat = "#1E2024", wire.SceneMaterialScreen
		if b0 > 0.35 || h < 0.25 {
			parts = append(parts, box(0, 0, w, min(d, 0.06), 0.01, b0, top, "#1E2024", wire.SceneMaterialScreen))
		} else {
			cabTop := b0 + min(0.44, h*0.42)
			scrTop := max(cabTop+0.08, top)
			scrTop = min(scrTop, heightCap)
			parts = append(parts,
				box(0, 0, w, d, 0.015, b0, cabTop, pickCol("#6E5844"), wire.SceneMaterialWood),
				box(0, -d*0.15, max(0.10, w*0.88), 0.04, 0.005, cabTop+0.02, scrTop, "#1E2024", wire.SceneMaterialScreen),
			)
		}

	case "piano":
		// A digital piano: side panels to the floor, a body, white keys with black keys, a back board and pedals.
		col := pickCol("#242428")
		plainCol, plainMat = col, wire.SceneMaterialWood
		keyZ := b0 + min(0.74, h*0.75)
		boardTop := min(top, keyZ+0.21)
		parts = append(parts,
			box(-w/2+0.02, -0.03, 0.04, max(0.06, d-0.06), 0.005, b0, keyZ-0.02, shade(col, -18), wire.SceneMaterialWood),
			box(w/2-0.02, -0.03, 0.04, max(0.06, d-0.06), 0.005, b0, keyZ-0.02, shade(col, -18), wire.SceneMaterialWood),
			box(0, -0.04, w, max(0.06, d-0.08), 0.01, keyZ-0.12, keyZ, col, wire.SceneMaterialWood),
			box(0, d/2-0.1, max(0.1, w-0.12), 0.16, 0.003, keyZ-0.04, keyZ, "#F8F8F6", wire.SceneMaterialMatte),
			box(0, -d/2+0.03, w, 0.06, 0.005, keyZ, max(keyZ+0.02, boardTop), col, wire.SceneMaterialWood),
			box(0, -0.05, min(0.5, w*0.4), max(0.06, d-0.2), 0.005, b0+0.04, b0+0.08, shade(col, -30), wire.SceneMaterialMetal),
		)
		white := int((w - 0.12) / 0.045)
		black := 0
		for k := 0; k < white-1 && black < 24; k++ {
			if k%7 == 2 || k%7 == 6 {
				continue // no black key after E or B
			}
			lx := -w/2 + 0.06 + float64(k+1)*0.045
			parts = append(parts, box(lx, d/2-0.135, 0.022, 0.09, 0, keyZ, keyZ+0.015, "#222226", wire.SceneMaterialScreen))
			black++
		}

	case "sink":
		plainCol, plainMat = "#F2F2F0", wire.SceneMaterialTile
		rimZ := max(b0+0.04, top-0.12)
		faucetTop := min(heightCap, top+0.14)
		parts = append(parts,
			box(0, 0, w, d, 0.015, b0, rimZ, pickCol("#B89E82"), wire.SceneMaterialWood),
			box(0, 0, w, d, 0.02, rimZ, top, "#F4F4F2", wire.SceneMaterialTile),
			cyl(0, -d*0.3, 0.02, top, max(top+0.02, faucetTop), "#C8CCD0", wire.SceneMaterialMetal),
		)

	case "toilet":
		col := pickCol("#F6F6F4")
		plainCol, plainMat = col, wire.SceneMaterialTile
		tankD := min(0.20, d*0.32)
		bowlR := min(w*0.46, (d-tankD)*0.48)
		parts = append(parts,
			box(0, -d/2+tankD/2, w*0.92, tankD, 0.03, b0, top, col, wire.SceneMaterialTile),
			cyl(0, tankD*0.35, max(0.08, bowlR), b0, b0+h*0.54, col, wire.SceneMaterialTile),
		)

	case "bathtub":
		col := pickCol("#F5F5F3")
		plainCol, plainMat = col, wire.SceneMaterialTile
		rim := min(0.08, min(w, d)*0.14)
		parts = append(parts,
			box(0, 0, w, d, min(0.08, min(w, d)*0.3), b0, b0+h*0.25, col, wire.SceneMaterialTile),
			box(0, -d/2+rim/2, w, rim, 0.02, b0+h*0.25, top, col, wire.SceneMaterialTile),
			box(0, d/2-rim/2, w, rim, 0.02, b0+h*0.25, top, col, wire.SceneMaterialTile),
			box(-w/2+rim/2, 0, rim, max(0.06, d-2*rim), 0.02, b0+h*0.25, top, col, wire.SceneMaterialTile),
			box(w/2-rim/2, 0, rim, max(0.06, d-2*rim), 0.02, b0+h*0.25, top, col, wire.SceneMaterialTile),
		)

	case "washerdryer", "washer", "dishwasher":
		col := pickCol("#ECEEF0")
		plainCol, plainMat = col, wire.SceneMaterialMetal
		parts = append(parts,
			box(0, -0.02, w, max(0.08, d-0.04), 0.02, b0, top, col, wire.SceneMaterialMetal),
			cyl(0, d/2-0.02, min(0.20, w*0.32), b0+h*0.28, b0+h*0.76, "#8898A6", wire.SceneMaterialGlass),
		)

	case "stove", "oven":
		// A stove on a base cabinet: a plinth, two doors with handles, a worktop and a black hob with two burners.
		col := pickCol("#D2D6DA")
		plainCol, plainMat = col, wire.SceneMaterialMatte
		wt := b0 + min(h, 0.9) - 0.04
		zb := b0 + min(0.08, h*0.1)
		parts = append(parts,
			box(0, -0.01, max(0.06, w-0.04), max(0.06, d-0.04), 0.005, b0, zb, shade(col, -40), wire.SceneMaterialMatte),
			box(0, -0.015, w, max(0.04, d-0.03), 0.01, zb, wt, shade(col, -12), wire.SceneMaterialMatte),
			box(-w/4, d/2-0.015, max(0.02, w/2-0.012), 0.03, 0.005, zb+0.006, wt-0.006, col, wire.SceneMaterialMatte),
			box(w/4, d/2-0.015, max(0.02, w/2-0.012), 0.03, 0.005, zb+0.006, wt-0.006, col, wire.SceneMaterialMatte),
			box(-0.04, d/2+0.012, 0.02, 0.025, 0.004, wt-0.22, wt-0.06, "#B0B2B8", wire.SceneMaterialMetal),
			box(0.04, d/2+0.012, 0.02, 0.025, 0.004, wt-0.22, wt-0.06, "#B0B2B8", wire.SceneMaterialMetal),
			box(0, 0.01, w, d+0.02, 0.005, wt, wt+0.04, "#ECEAE4", wire.SceneMaterialMatte),
			box(0, 0, max(0.08, w-0.12), max(0.08, d-0.12), 0.01, wt+0.04, wt+0.055, "#1C1C20", wire.SceneMaterialScreen),
		)
		for _, sx := range []float64{-1, 1} {
			r := min(0.09, min(w, d)*0.18)
			parts = append(parts, cyl(sx*(w/4-0.03), 0, r, wt+0.055, wt+0.065, "#46464C", wire.SceneMaterialMetal))
		}

	case "fireplace":
		col := pickCol("#D8D2C8")
		plainCol, plainMat = col, wire.SceneMaterialMatte
		parts = append(parts,
			box(0, 0, w, d, 0.015, b0, top, col, wire.SceneMaterialMatte),
			box(0, d*0.2, max(0.08, w*0.6), max(0.06, d*0.5), 0.01, b0+0.06, b0+h*0.65, "#2C2826", wire.SceneMaterialMatte),
		)

	case "stairs":
		col := pickCol("#BC9B72")
		plainCol = col
		steps := 5
		sd := d / float64(steps)
		for s := range steps {
			ly := -d/2 + sd*(float64(s)+0.5)
			stTop := b0 + h*float64(s+1)/float64(steps)
			parts = append(parts, box(0, ly, w, max(0.04, sd), 0.005, b0, stTop, col, wire.SceneMaterialWood))
		}
	}

	plain = box(0, 0, w, d, 0.02, b0, top, plainCol, plainMat)
	if len(parts) == 0 {
		parts = []wire.SceneShape{plain}
	}
	return parts, plain
}

// ---- Labels & Pins ----

func buildRoomLabels(fp *fixedPlan) (labels []wire.SceneLabel, names []string) {
	if len(fp.Rooms) > 0 {
		for _, rm := range fp.Rooms {
			if len(labels) >= sceneLabelsMost {
				break
			}
			txt := clip(rm.Name, 40)
			if txt == "" {
				continue
			}
			c := polygonCentroid(rm.Polygon)
			labels = append(labels, wire.SceneLabel{Text: txt, At: []float64{clampCoord(c[0]), clampCoord(c[1])}})
			names = append(names, txt)
		}
		return labels, names
	}
	counts := map[string]int{}
	for _, sr := range fp.ScanRooms {
		counts[scanRoomBase(sr.Name)]++
	}
	seen := map[string]int{}
	for _, sr := range fp.ScanRooms {
		if len(labels) >= sceneLabelsMost {
			break
		}
		base := scanRoomBase(sr.Name)
		seen[base]++
		txt := base
		if counts[base] > 1 {
			txt = fmt.Sprintf("%s %d", base, seen[base])
		}
		txt = clip(txt, 40)
		if txt == "" {
			continue
		}
		labels = append(labels, wire.SceneLabel{Text: txt, At: []float64{clampCoord(sr.Center[0]), clampCoord(sr.Center[1])}})
		names = append(names, txt)
	}
	return labels, names
}

var trailingDigitsRE = regexp.MustCompile(`\d+$`)

func scanRoomBase(raw string) string {
	key := strings.ToLower(trailingDigitsRE.ReplaceAllString(strings.TrimSpace(raw), ""))
	switch key {
	case "livingroom", "living":
		return "客厅"
	case "bedroom", "masterbedroom":
		return "卧室"
	case "kitchen":
		return "厨房"
	case "bathroom", "restroom", "washroom", "toilet":
		return "卫生间"
	case "diningroom", "dining":
		return "餐厅"
	case "study", "office":
		return "书房"
	case "corridor", "hallway", "entryway", "foyer":
		return "玄关"
	case "laundry", "laundryroom":
		return "洗衣房"
	case "closet", "storage", "pantry":
		return "储物间"
	case "balcony", "patio", "terrace":
		return "阳台"
	case "stairs", "stairway":
		return "楼梯"
	}
	trimmed := trailingDigitsRE.ReplaceAllString(strings.TrimSpace(raw), "")
	if trimmed != "" {
		return trimmed
	}
	return raw
}

func polygonCentroid(poly [][2]float64) [2]float64 {
	n := len(poly)
	if n == 0 {
		return [2]float64{}
	}
	var a2, cx, cy float64
	for i := range n {
		p, q := poly[i], poly[(i+1)%n]
		cross := p[0]*q[1] - q[0]*p[1]
		a2 += cross
		cx += (p[0] + q[0]) * cross
		cy += (p[1] + q[1]) * cross
	}
	if math.Abs(a2) < 1e-9 {
		var sx, sy float64
		for _, p := range poly {
			sx += p[0]
			sy += p[1]
		}
		return [2]float64{sx / float64(n), sy / float64(n)}
	}
	return [2]float64{cx / (3 * a2), cy / (3 * a2)}
}

type tvCandidate struct {
	entity string
	area   *Area
	rank   int // 0: media_player/switch TV, 1: IR TV remote, 2: remote.* TV
}

// matchScannedTVs pairs scanned television objects with visible TV entities (or IR TV remotes) when home3d.json has not
// already pinned that TV or placed a pin within 0.8 m of the scanned screen.
func (a *app) matchScannedTVs(fp *fixedPlan, hm *home, reg *Registry) []fixDevice {
	devs := slices.Clone(fp.Devices)
	if len(fp.Objects) == 0 || len(hm.States) == 0 {
		return devs
	}

	remoteButtons := map[string]bool{}
	var candidates []tvCandidate
	for i := range hm.Areas {
		ar := &hm.Areas[i]
		for _, rm := range remotes(hm, reg, ar) {
			remoteButtons[rm.on] = true
			if rm.off != "" {
				remoteButtons[rm.off] = true
			}
			for _, m := range rm.more {
				remoteButtons[m.EntityID] = true
			}
			if icon, _, ok := nameLook(rm.name); ok && icon == wire.NavIconTv {
				candidates = append(candidates, tvCandidate{entity: rm.on, area: ar, rank: 1})
			}
		}
	}

	for _, s := range hm.States {
		eid := s.EntityID
		if !hm.visible(eid) || regHidden(reg, eid) || remoteButtons[eid] {
			continue
		}
		if reg != nil && reg.skip(eid, hm.ByID) != "" {
			continue
		}
		d := s.Domain()
		icon, _ := lookOf(hm, s)
		rank := -1
		switch {
		case d == "remote":
			if ni, _, ok := nameLook(s.Name()); ok && ni == wire.NavIconTv {
				rank = 2
			}
		case icon == wire.NavIconTv:
			rank = 0
		case d == "media_player":
			if ni, _, ok := nameLook(s.Name()); ok && ni == wire.NavIconTv {
				rank = 0
			}
		}
		if rank >= 0 {
			candidates = append(candidates, tvCandidate{entity: eid, area: hm.AreaOf[eid], rank: rank})
		}
	}
	if len(candidates) == 0 {
		return devs
	}

	pinned := map[string]bool{}
	for _, dv := range devs {
		pinned[strings.TrimSpace(dv.Entity)] = true
	}

	pickBest := func(list []tvCandidate) (string, bool) {
		if len(list) == 0 {
			return "", false
		}
		if len(list) == 1 {
			return list[0].entity, true
		}
		bestRank := list[0].rank
		for _, c := range list[1:] {
			bestRank = min(bestRank, c.rank)
		}
		var top []tvCandidate
		for _, c := range list {
			if c.rank == bestRank {
				top = append(top, c)
			}
		}
		if len(top) == 1 {
			return top[0].entity, true
		}
		return "", false
	}

	for _, ob := range fp.Objects {
		kind := ob.Kind
		if kind == "" {
			kind = ob.Category
		}
		if kind != "television" {
			continue
		}
		near := false
		for _, dv := range devs {
			if pt, ok := checkCoord2(dv.At); ok && dist2D(pt, ob.Center) <= tvPinDistMost {
				near = true
				break
			}
		}
		if near {
			continue
		}

		var avail []tvCandidate
		for _, c := range candidates {
			if !pinned[c.entity] {
				avail = append(avail, c)
			}
		}
		if len(avail) == 0 {
			break
		}

		var chosen string
		var ok bool
		if ar := areaAtPlanPoint(fp, hm, ob.Center); ar != nil {
			var inRoom []tvCandidate
			for _, c := range avail {
				if c.area == ar {
					inRoom = append(inRoom, c)
				}
			}
			chosen, ok = pickBest(inRoom)
		}
		if !ok {
			chosen, ok = pickBest(avail)
		}
		if !ok {
			continue
		}

		pinned[chosen] = true
		midH := min(heightCap, max(0, (ob.Bottom+ob.Top)/2))
		devs = append(devs, fixDevice{
			Entity: chosen,
			At:     []float64{ob.Center[0], ob.Center[1]},
			Height: &midH,
		})
	}
	return devs
}

func areaAtPlanPoint(fp *fixedPlan, hm *home, pt [2]float64) *Area {
	if len(fp.Rooms) > 0 {
		for _, rm := range fp.Rooms {
			if pointInPolygon(pt, rm.Polygon) || polygonDist(pt, rm.Polygon) <= 0.35 {
				if rm.Area != "" {
					if ar := hm.area(rm.Area); ar != nil {
						return ar
					}
				}
				if ar := hm.area(rm.Name); ar != nil {
					return ar
				}
			}
		}
	}
	if len(fp.ScanRooms) > 0 {
		bestIdx, bestDist := -1, math.MaxFloat64
		for i, sr := range fp.ScanRooms {
			if d := dist2D(pt, sr.Center); d < bestDist {
				bestIdx, bestDist = i, d
			}
		}
		if bestIdx >= 0 {
			sr := fp.ScanRooms[bestIdx]
			if ar := hm.area(scanRoomBase(sr.Name)); ar != nil {
				return ar
			}
			if ar := hm.area(trailingDigitsRE.ReplaceAllString(sr.Name, "")); ar != nil {
				return ar
			}
		}
	}
	return nil
}

func polygonDist(pt [2]float64, poly [][2]float64) float64 {
	best := math.MaxFloat64
	for i := range poly {
		cp := closestPointOnSegment(pt, poly[i], poly[(i+1)%len(poly)])
		best = min(best, dist2D(pt, cp))
	}
	return best
}

func indexRemotesByButton(hm *home, reg *Registry) map[string]remote {
	if reg == nil {
		return nil
	}
	out := map[string]remote{}
	for i := range hm.Areas {
		for _, rm := range remotes(hm, reg, &hm.Areas[i]) {
			out[rm.on] = rm
			if rm.off != "" {
				out[rm.off] = rm
			}
			for _, b := range rm.more {
				out[b.EntityID] = rm
			}
		}
	}
	return out
}

func (a *app) remotePinDetails(hm *home, rm remote) (label, icon, state, tone string) {
	label = rm.name
	if rm.area != nil && rm.area.Name != "" && !hm.ByType && strings.HasPrefix(label, rm.area.Name) {
		if rest := strings.TrimSpace(strings.TrimPrefix(label, rm.area.Name)); len([]rune(rest)) >= 2 {
			label = rest
		}
	}
	label = clip(label, 80)
	var ok bool
	icon, _, ok = nameLook(rm.name)
	if !ok {
		icon = "power"
	}
	if a != nil && a.remoteOn(hm, rm) {
		return label, icon, "开", wire.TonePositive
	}
	onAt, offAt := pressedAt(hm, rm.on), pressedAt(hm, rm.off)
	var markAt time.Time
	if a != nil {
		markAt = a.marks.get(rm.on)
	}
	if onAt.IsZero() && offAt.IsZero() && markAt.IsZero() && rm.last == nil {
		return label, icon, "不知道开没开", ""
	}
	return label, icon, "关", ""
}

func (a *app) buildScenePins(devs []fixDevice, hm *home, reg *Registry) (pins []wire.ScenePin, litEntities map[string]bool) {
	litEntities = map[string]bool{}
	usedIDs := map[string]bool{}
	lights := 0
	remoteOf := indexRemotesByButton(hm, reg)

	for i, dv := range devs {
		if len(pins) >= scenePinsMost {
			break
		}
		eid := strings.TrimSpace(dv.Entity)
		if !entityRE.MatchString(eid) {
			continue
		}
		s, ok := hm.ByID[eid]
		if !ok || !hm.visible(eid) {
			continue
		}
		pt, ok := checkCoord2(dv.At)
		if !ok {
			continue
		}

		var label, icon, st, tone string
		var light *wire.SceneLight
		targetID := eid

		if rm, isRemote := remoteOf[eid]; isRemote {
			targetID = rm.on
			label, icon, st, tone = a.remotePinDetails(hm, rm)
			if icon == wire.NavIconLight && tone == wire.TonePositive {
				litEntities[targetID] = true
				if lights < wire.SceneLightsMost {
					light = &wire.SceneLight{On: true, Warmth: wire.SceneWarmthWarm}
					lights++
				}
			}
		} else {
			label = s.Name()
			if dn, ok := hm.Names[eid]; ok && dn != "" {
				label = dn
			}
			label = clip(label, 80)
			if label == "" {
				label = clip(eid, 80)
			}
			icon, _ = lookOf(hm, s)
			st = pinStateText(s)
			if stateTone(s) == wire.TonePositive {
				tone = wire.TonePositive
			}
			if icon == wire.NavIconLight && s.State == "on" {
				litEntities[eid] = true
				if lights < wire.SceneLightsMost {
					light = &wire.SceneLight{On: true, Warmth: wire.SceneWarmthWarm}
					lights++
				}
			}
			if st == "" && (tone != "" || light != nil) {
				st = "开"
			}
		}

		pid := ref("", targetID)
		if usedIDs[pid] {
			pid = ref(fmt.Sprintf("%d:", i), targetID)
		}
		usedIDs[pid] = true

		var height *float64
		if dv.Height != nil && isFinite(*dv.Height) && *dv.Height >= 0 {
			h := min(*dv.Height, 500)
			height = &h
		}

		pins = append(pins, wire.ScenePin{
			ID:     pid,
			At:     []float64{pt[0], pt[1]},
			Height: height,
			Label:  label,
			State:  st,
			Icon:   icon,
			Tone:   tone,
			Light:  light,
			Action: openAction(targetID),
		})
	}
	return pins, litEntities
}

func pinStateText(s State) string {
	st := stateText(s)
	if s.Domain() == "climate" && s.State != "off" && !s.Unavailable() {
		if t, ok := s.num("temperature"); ok {
			if st != "" {
				st += " " + tidy(t) + " °C"
			} else {
				st = tidy(t) + " °C"
			}
		}
	}
	return clip(st, 40)
}

func countHomeLightsOn(hm *home, reg *Registry, devs []fixDevice, pinnedLights map[string]bool) int {
	seen := map[string]bool{}
	for eid := range pinnedLights {
		seen[eid] = true
	}
	// a switch's indicator light is a light too, but a setting of the switch (entity_category config), not a lamp: the
	// rest of the page leaves such entities out of its counts, and so does this one (#940)
	setting := func(e string) bool {
		if reg == nil {
			return false
		}
		re, ok := reg.Entities[e]
		return ok && (re.EntityCategory != "" || regHidden(reg, e))
	}
	for _, s := range hm.States {
		if s.Domain() == "light" && s.State == "on" && hm.visible(s.EntityID) && !setting(s.EntityID) {
			seen[s.EntityID] = true
		}
	}
	for _, dv := range devs {
		eid := strings.TrimSpace(dv.Entity)
		if s, ok := hm.ByID[eid]; ok && hm.visible(eid) && s.State == "on" {
			if icon, _ := lookOf(hm, s); icon == wire.NavIconLight {
				seen[eid] = true
			}
		}
	}
	return len(seen)
}

// ---- Validated shape constructors ----

func clampCoord(v float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0
	}
	return max(-500, min(500, v))
}

func sceneBox(center, size [2]float64, yaw, round, bottom, top float64, color, material string) wire.SceneShape {
	w := max(0.01, min(1000, size[0]))
	d := max(0.01, min(1000, size[1]))
	b := clampCoord(bottom)
	t := max(b+0.006, clampCoord(top))
	y := normAngle(yaw)
	var rp *float64
	if round > 0 {
		r := min(round, min(w, d)*0.49)
		if r > 0 {
			rp = &r
		}
	}
	return wire.SceneShape{
		Kind:     wire.SceneShapeBox,
		Center:   []float64{clampCoord(center[0]), clampCoord(center[1])},
		Size:     []float64{w, d},
		Yaw:      &y,
		Round:    rp,
		Bottom:   &b,
		Top:      &t,
		Color:    color,
		Material: material,
	}
}

func sceneCyl(center [2]float64, radius, bottom, top float64, color, material string) wire.SceneShape {
	r := max(0.01, min(500, radius))
	b := clampCoord(bottom)
	t := max(b+0.006, clampCoord(top))
	return wire.SceneShape{
		Kind:     wire.SceneShapeCylinder,
		Center:   []float64{clampCoord(center[0]), clampCoord(center[1])},
		Radius:   r,
		Bottom:   &b,
		Top:      &t,
		Color:    color,
		Material: material,
	}
}

func scenePrism(poly [][2]float64, bottom, top float64, color, material string) wire.SceneShape {
	pts := make([][]float64, len(poly))
	for i, p := range poly {
		pts[i] = []float64{clampCoord(p[0]), clampCoord(p[1])}
	}
	b := clampCoord(bottom)
	t := max(b+0.006, clampCoord(top))
	return wire.SceneShape{
		Kind:     wire.SceneShapePrism,
		Points:   pts,
		Bottom:   &b,
		Top:      &t,
		Color:    color,
		Material: material,
	}
}
