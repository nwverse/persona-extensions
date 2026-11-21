package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/sdk/go/ext"
	"github.com/nwverse/persona/sdk/go/wire"
)

func checkSceneView(t *testing.T, sc wire.Component) {
	t.Helper()
	v := &wire.View{
		Title:    "家",
		Sections: []wire.Section{{Items: []wire.Component{sc}}},
	}
	if err := wire.CheckView(v); err != nil {
		t.Fatalf("CheckView failed: %v", err)
	}
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal View: %v", err)
	}
	if err := wire.CheckViewJSON(raw); err != nil {
		t.Fatalf("CheckViewJSON failed: %v", err)
	}
}

func TestSceneCheckView(t *testing.T) {
	raw := syntheticHomeUSDZ(t, nil)
	parsed, err := readRoomPlan(raw)
	if err != nil {
		t.Fatalf("readRoomPlan: %v", err)
	}

	states := []State{
		{EntityID: "light.living", State: "on", Attributes: map[string]any{"friendly_name": "客厅吸顶灯"}},
		{EntityID: "light.bedroom", State: "off", Attributes: map[string]any{"friendly_name": "卧室主灯"}},
		{EntityID: "sensor.living_temp", State: "26", Attributes: map[string]any{"friendly_name": "客厅温度", "unit_of_measurement": "°C"}},
	}
	areas := []Area{
		{ID: "living", Name: "客厅", Entities: []string{"light.living", "sensor.living_temp"}},
		{ID: "bedroom", Name: "卧室", Entities: []string{"light.bedroom"}},
	}
	hm := buildHome(Config{}, states, areas, nil)

	hLamp := 2.4
	fix := &sceneFix{
		Devices: []fixDevice{
			{Entity: "light.living", At: []float64{2.0, 1.5}, Height: &hLamp},
			{Entity: "light.bedroom", At: []float64{5.5, 1.5}, Height: &hLamp},
			{Entity: "sensor.living_temp", At: []float64{1.2, 2.2}},
		},
	}

	comp := buildScene(parsed, fix, hm)
	checkSceneView(t, comp)

	if comp.Camera == nil || *comp.Camera.Yaw != 225 || *comp.Camera.Pitch != 35 || *comp.Camera.Zoom != 1 {
		t.Errorf("camera = %+v, want yaw=225 pitch=35 zoom=1", comp.Camera)
	}
	if !strings.Contains(comp.Alt, "客厅") || !strings.Contains(comp.Alt, "卧室") {
		t.Errorf("alt = %q, want both rooms named", comp.Alt)
	}
	if comp.Caption != "1 盏灯开着" {
		t.Errorf("caption = %q, want 1 盏灯开着", comp.Caption)
	}
	if len(comp.Labels) != 2 {
		t.Errorf("labels = %+v, want 2 room labels", comp.Labels)
	}

	// Window1 (bottom 0.8 < wall_height 1.1) produces a glass box from 0.8 to 1.1.
	hasWindowGlass := false
	for _, s := range comp.Shapes {
		if s.Material == wire.SceneMaterialGlass && math.Abs(*s.Bottom-0.8) < 1e-3 && math.Abs(*s.Top-1.1) < 1e-3 {
			hasWindowGlass = true
		}
		if *s.Top > heightCap+1e-6 {
			t.Errorf("shape top %g exceeds 2.4 m cap", *s.Top)
		}
	}
	if !hasWindowGlass {
		t.Error("expected glass window box from 0.8 m to 1.1 m")
	}

	// Lowering wall_height below window bottom (0.8 m) suppresses the window glass box.
	lowWall := 0.7
	compLow := buildScene(parsed, &sceneFix{WallHeight: &lowWall}, hm)
	checkSceneView(t, compLow)
	for _, s := range compLow.Shapes {
		if s.Material == wire.SceneMaterialGlass {
			t.Errorf("unexpected glass window above wall_height=0.7: %+v", s)
		}
	}
}

func TestSceneDoorCuts(t *testing.T) {
	// One 4 m wall along x from (0, 0) to (4, 0) with a 1.0 m door centred at (2, 0):
	// the host wall must split into two 1.5 m boxes beside the [1.5, 2.5] gap.
	sc := &scan{
		Walls: []scanWall{{Name: "WallMain", Center: [2]float64{2.0, 0.0}, Size: [2]float64{4.0, 0.14}, Yaw: 0, Bottom: 0, Top: 2.5}},
		Doors: []scanDoor{{Name: "DoorMid", Open: false, Center: [2]float64{2.0, 0.0}, Size: [2]float64{1.0, 0.05}, Yaw: 0, Bottom: 0, Top: 2.1}},
	}
	comp := buildScene(sc, nil, nil)
	checkSceneView(t, comp)

	var wallBoxes, woodLeaves []wire.SceneShape
	for _, s := range comp.Shapes {
		switch s.Material {
		case wire.SceneMaterialMatte:
			wallBoxes = append(wallBoxes, s)
		case wire.SceneMaterialWood:
			woodLeaves = append(woodLeaves, s)
		}
	}
	if len(wallBoxes) != 2 {
		t.Fatalf("wall boxes = %d, want 2: %+v", len(wallBoxes), wallBoxes)
	}
	if math.Abs(wallBoxes[0].Center[0]-0.75) > 1e-3 || math.Abs(wallBoxes[0].Size[0]-1.5) > 1e-3 ||
		math.Abs(wallBoxes[1].Center[0]-3.25) > 1e-3 || math.Abs(wallBoxes[1].Size[0]-1.5) > 1e-3 {
		t.Errorf("split wall boxes = %+v, want centres 0.75 and 3.25 of length 1.5", wallBoxes)
	}
	if len(woodLeaves) != 1 || math.Abs(woodLeaves[0].Center[0]-2.0) > 1e-3 || math.Abs(woodLeaves[0].Size[0]-1.0) > 1e-3 {
		t.Errorf("closed door leaf = %+v", woodLeaves)
	}

	// Open door draws as a low threshold only; sliding_glass draws as a glass panel beside the gap.
	openTrue := true
	compOpen := buildScene(sc, &sceneFix{Doors: []fixDoor{{Name: "DoorMid", Open: &openTrue}}}, nil)
	checkSceneView(t, compOpen)
	for _, s := range compOpen.Shapes {
		if s.Material == wire.SceneMaterialWood && *s.Top > 0.03 {
			t.Errorf("open door drew tall leaf: top=%g", *s.Top)
		}
	}

	compGlass := buildScene(sc, &sceneFix{Doors: []fixDoor{{Name: "DoorMid", Kind: "sliding_glass", Open: &openTrue}}}, nil)
	checkSceneView(t, compGlass)
	hasGlass := slices.ContainsFunc(compGlass.Shapes, func(s wire.SceneShape) bool { return s.Material == wire.SceneMaterialGlass })
	if !hasGlass {
		t.Error("sliding_glass door did not draw a glass panel")
	}
}

func TestSceneCorrections(t *testing.T) {
	raw := syntheticHomeUSDZ(t, nil)
	parsed, err := readRoomPlan(raw)
	if err != nil {
		t.Fatalf("readRoomPlan: %v", err)
	}

	fixJSON := `{
		"rotate": 0,
		"wall_height": 1.2,
		"drop": ["Storage1", "Chair2"],
		"walls": [{"name": "HalfWall", "from": [2.0, 0.0], "to": [2.0, 1.2]}],
		"doors": [{"name": "Door1", "at": [3.0, 0.0], "kind": "sliding_glass", "open": true}],
		"split": [{"name": "Wall5", "keep": [[4.0, 2.2], [4.0, 3.0]]}],
		"openings": [{"wall": "Wall2", "from": 4.5, "to": 6.0, "kind": "sliding_glass"}],
		"rooms": [
			{"name": "起居室", "area": "客厅", "polygon": [[0, 0], [4, 0], [4, 3], [0, 3]], "floor": "tile", "floor_color": "#EEE8DC", "wall_color": "#C4DEEE"},
			{"name": "主卧", "area": "卧室", "polygon": [[4, 0], [7, 0], [7, 3], [4, 3]], "floor": "wood", "floor_color": "#CFAF88", "wall_color": "#E8DFD5"}
		],
		"objects": [{"name": "Table1", "kind": "piano"}, {"name": "Chair1", "kind": "bar_table"}],
		"add": [
			{"kind": "sideboard", "center": [1.0, 0.3], "size": [1.6, 0.44], "yaw": 0, "top": 0.77, "color": "#3A3A3E"},
			{"kind": "coffee_table", "center": [1.6, 1.5], "size": [1.0, 0.55], "top": 0.42},
			{"kind": "glass_cabinet", "center": [6.4, 0.4], "size": [1.0, 0.42], "top": 1.8}
		],
		"unknown_future_field": {"ignored": true}
	}`
	fix, err := parseSceneFix([]byte(fixJSON))
	if err != nil {
		t.Fatalf("parseSceneFix: %v", err)
	}

	fp := applySceneFix(parsed, fix)
	for _, ob := range fp.Objects {
		if ob.Name == "Storage1" || ob.Name == "Chair2" {
			t.Errorf("dropped object %s remained in plan", ob.Name)
		}
	}
	if !slices.ContainsFunc(fp.Walls, func(w planWall) bool { return w.Name == "HalfWall" && math.Abs(dist2D(w.A, w.B)-1.2) < 1e-6 }) {
		t.Errorf("added HalfWall missing: %+v", fp.Walls)
	}
	for _, w := range fp.Walls {
		if w.Name == "Wall5" && math.Abs(dist2D(w.A, w.B)-0.8) > 1e-4 {
			t.Errorf("split Wall5 length = %g, want 0.8 (from 2.2 to 3.0)", dist2D(w.A, w.B))
		}
	}
	for _, d := range fp.Doors {
		if d.Name == "Door1" && (math.Abs(d.Center[0]-3.0) > 1e-4 || d.Kind != "sliding_glass" || !d.Open) {
			t.Errorf("moved Door1 = %+v", d)
		}
	}

	comp := buildScene(parsed, fix, nil)
	checkSceneView(t, comp)
	if len(comp.Labels) != 2 || comp.Labels[0].Text != "起居室" || comp.Labels[1].Text != "主卧" {
		t.Errorf("labels = %+v", comp.Labels)
	}
	if math.Hypot(comp.Labels[0].At[0]-2.0, comp.Labels[0].At[1]-1.5) > 1e-4 ||
		math.Hypot(comp.Labels[1].At[0]-5.5, comp.Labels[1].At[1]-1.5) > 1e-4 {
		t.Errorf("label centroids = %+v", comp.Labels)
	}
	hasLivingWallColor := slices.ContainsFunc(comp.Shapes, func(s wire.SceneShape) bool { return s.Color == "#C4DEEE" })
	hasSideboardColor := slices.ContainsFunc(comp.Shapes, func(s wire.SceneShape) bool { return s.Color == "#3A3A3E" })
	if !hasLivingWallColor || !hasSideboardColor {
		t.Errorf("expected room wall color #C4DEEE (%v) and added sideboard #3A3A3E (%v)", hasLivingWallColor, hasSideboardColor)
	}

	// Explicit rotate=90 turns (4, 0) to (0, 4); omitted rotate uses median wall direction to align tilted walls.
	rot90 := 90.0
	fpRot := applySceneFix(&scan{Walls: []scanWall{{Name: "W", Center: [2]float64{4, 0}, Size: [2]float64{2, 0.12}, Yaw: 0, Top: 2.4}}}, &sceneFix{Rotate: &rot90})
	mid := [2]float64{(fpRot.Walls[0].A[0] + fpRot.Walls[0].B[0]) / 2, (fpRot.Walls[0].A[1] + fpRot.Walls[0].B[1]) / 2}
	if math.Hypot(mid[0], mid[1]-4) > 1e-4 {
		t.Errorf("rotate 90 midpoint = %v, want [0, 4]", mid)
	}

	tilted := &scan{Walls: []scanWall{
		{Name: "W1", Center: [2]float64{0, 0}, Size: [2]float64{4, 0.12}, Yaw: 18, Top: 2.4},
		{Name: "W2", Center: [2]float64{0, 0}, Size: [2]float64{3, 0.12}, Yaw: 108, Top: 2.4},
		{Name: "W3", Center: [2]float64{0, 0}, Size: [2]float64{4, 0.12}, Yaw: 18, Top: 2.4},
	}}
	fpAuto := applySceneFix(tilted, nil)
	dy := math.Abs(fpAuto.Walls[0].B[1] - fpAuto.Walls[0].A[1])
	dx := math.Abs(fpAuto.Walls[1].B[0] - fpAuto.Walls[1].A[0])
	if dy > 1e-4 || dx > 1e-4 {
		t.Errorf("auto-aligned walls not axis-aligned: W1=%+v W2=%+v", fpAuto.Walls[0], fpAuto.Walls[1])
	}

	// Malformed home3d.json is reported plainly while homeScene still builds a valid scene without corrections.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, sceneFixFile), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	a := newApp(t.Logf)
	a.dataDir = dir
	fallbackScene, loadErr := a.homeScene(parsed, nil)
	if loadErr == nil {
		t.Error("expected plain error for malformed home3d.json")
	}
	checkSceneView(t, fallbackScene)
}

func TestSceneShapeBudget(t *testing.T) {
	sc := &scan{
		Walls: []scanWall{{Name: "W1", Center: [2]float64{5, 0}, Size: [2]float64{10, 0.12}, Yaw: 0, Top: 2.5}},
		Objects: []scanObject{
			// One large bed (5 parts: frame, mattress, headboard, 2 pillows) that must keep its detail.
			{Category: "bed", Kind: "bed", Name: "BigBed", Center: [2]float64{2, 2}, Size: [2]float64{2.0, 2.2}, Top: 1.0, Color: "#AA8866"},
		},
	}
	// Add 120 small chairs (6 parts each = 720 shapes if unsimplified).
	for i := range 120 {
		sc.Objects = append(sc.Objects, scanObject{
			Category: "chair", Kind: "chair", Name: fmt.Sprintf("Chair%d", i),
			Center: [2]float64{float64(i%12) * 0.8, 4 + float64(i/12)*0.8}, Size: [2]float64{0.42, 0.42}, Top: 0.82,
		})
	}

	comp := buildScene(sc, nil, nil)
	checkSceneView(t, comp)
	if len(comp.Shapes) >= 500 {
		t.Fatalf("shapes = %d, want strictly under 500", len(comp.Shapes))
	}
	// BigBed kept its two white pillows, proving small chairs lost detail before the large bed.
	pillows := 0
	for _, s := range comp.Shapes {
		if s.Color == "#FFFFFF" {
			pillows++
		}
	}
	if pillows != 2 {
		t.Errorf("BigBed pillows = %d, want 2 (smallest objects must simplify first)", pillows)
	}
}

func TestScenePins(t *testing.T) {
	sc := &scan{
		Walls: []scanWall{{Name: "W1", Center: [2]float64{2, 0}, Size: [2]float64{4, 0.12}, Yaw: 0, Top: 2.5}},
	}
	var states []State
	var devs []fixDevice
	h := 2.4
	// 20 lights that are on (to exercise the 16-light cap), 1 light that is off, 1 switch on, 1 temperature sensor.
	for i := range 20 {
		eid := fmt.Sprintf("light.lamp_%02d", i)
		states = append(states, State{EntityID: eid, State: "on", Attributes: map[string]any{"friendly_name": fmt.Sprintf("射灯 %d", i+1)}})
		devs = append(devs, fixDevice{Entity: eid, At: []float64{float64(i) * 0.2, 1.0}, Height: &h})
	}
	states = append(states,
		State{EntityID: "light.off_lamp", State: "off", Attributes: map[string]any{"friendly_name": "床头灯"}},
		State{EntityID: "switch.wall_plug", State: "on", Attributes: map[string]any{"friendly_name": "桌面插座", "device_class": "outlet"}},
		State{EntityID: "sensor.room_temp", State: "26", Attributes: map[string]any{"friendly_name": "室温", "unit_of_measurement": "°C"}},
	)
	devs = append(devs,
		fixDevice{Entity: "light.off_lamp", At: []float64{1.0, 2.0}, Height: &h},
		fixDevice{Entity: "switch.wall_plug", At: []float64{1.5, 2.0}},
		fixDevice{Entity: "sensor.room_temp", At: []float64{2.0, 2.0}},
	)

	hm := buildHome(Config{}, states, nil, nil)
	comp := buildScene(sc, &sceneFix{Devices: devs}, hm)
	checkSceneView(t, comp)

	if len(comp.Pins) != 23 {
		t.Fatalf("pins = %d, want 23", len(comp.Pins))
	}
	if comp.Caption != "20 盏灯开着" {
		t.Errorf("caption = %q, want 20 盏灯开着", comp.Caption)
	}

	lit := 0
	byLabel := map[string]wire.ScenePin{}
	for _, p := range comp.Pins {
		byLabel[p.Label] = p
		if p.Light != nil {
			if !p.Light.On || p.Light.Warmth != wire.SceneWarmthWarm {
				t.Errorf("pin %s light = %+v, want on + warm", p.Label, p.Light)
			}
			lit++
		}
	}
	if lit != wire.SceneLightsMost {
		t.Errorf("lit pins = %d, want capped at %d", lit, wire.SceneLightsMost)
	}
	// Even the 20th on-light (past the 16-light cap) keeps State="开" and Tone="positive".
	if p := byLabel["射灯 20"]; p.State != "开" || p.Tone != wire.TonePositive || p.Light != nil || p.Icon != "light" {
		t.Errorf("20th light pin = %+v", p)
	}
	// Off light has no SceneLight and neutral/empty tone.
	if p := byLabel["床头灯"]; p.State != "关" || p.Tone != "" || p.Light != nil || p.Action == nil || p.Action.ID != "open:light.off_lamp" {
		t.Errorf("off light pin = %+v", p)
	}
	// Non-light on device has Tone="positive" and plug icon, no SceneLight.
	if p := byLabel["桌面插座"]; p.State != "开" || p.Tone != wire.TonePositive || p.Light != nil || p.Icon != "plug" {
		t.Errorf("plug pin = %+v", p)
	}
	// Temperature sensor formats as "26 °C".
	if p := byLabel["室温"]; p.State != "26 °C" || p.Tone != "" || p.Light != nil || p.Icon != "gauge" {
		t.Errorf("sensor pin = %+v", p)
	}
}

func TestHomePageUnchangedWithoutUSDZ(t *testing.T) {
	f := newFakeHA(t)

	// Baseline with no dataDir at all.
	baseApp := newTestApp(t, f, Config{})
	var logs []string
	dirApp := newApp(func(format string, args ...any) {
		logs = append(logs, fmt.Sprintf(format, args...))
	})
	dirApp.configure(Config{URL: f.srv.URL, HiddenAreas: []string{"别处"}}, f.token)
	dirApp.dataDir = t.TempDir()

	if dirApp.hasScene() {
		t.Fatal("hasScene() = true on empty dataDir")
	}

	nonceRe := regexp.MustCompile(`persona_[0-9a-f]+`)
	stripFooter := func(v *wire.View) []byte {
		cp := *v
		// Footer contains time.Now().Format("15:04:05"), and the "my:new" tile carries a per-render shortcut ID.
		if n := len(cp.Sections); n > 0 {
			cp.Sections = slices.Clone(cp.Sections[:n-1])
		}
		raw, err := json.Marshal(cp)
		if err != nil {
			t.Fatal(err)
		}
		return nonceRe.ReplaceAll(raw, []byte("persona_id"))
	}

	for _, ver := range []int{11, 10, 6, 5, 1} {
		vBase, _ := renderV(t, baseApp, ver, nil)
		vDir, _ := renderV(t, dirApp, ver, nil)
		if !bytes.Equal(stripFooter(vBase), stripFooter(vDir)) {
			t.Fatalf("UIVersion %d changed with empty dataDir:\n%s\nvs\n%s", ver, stripFooter(vBase), stripFooter(vDir))
		}
	}

	// Corrupt binary home.usdz logs plainly once and leaves 家 byte-for-byte unchanged.
	corrupt := zipStored(t, map[string]string{"Home.usdc": "PXR-USDC\x00\x01\x02"})
	if err := os.WriteFile(filepath.Join(dirApp.dataDir, homeUSDZFile), corrupt, 0o600); err != nil {
		t.Fatal(err)
	}
	if dirApp.hasScene() {
		t.Fatal("hasScene() = true on binary USDC")
	}
	vBase11, _ := renderV(t, baseApp, 11, nil)
	vCorrupt11, _ := renderV(t, dirApp, 11, nil)
	if !bytes.Equal(stripFooter(vBase11), stripFooter(vCorrupt11)) {
		t.Fatalf("corrupt home.usdz altered 家:\n%s\nvs\n%s", stripFooter(vBase11), stripFooter(vCorrupt11))
	}
	if len(logs) != 1 || !strings.Contains(logs[0], "ASCII USDZ") {
		t.Errorf("logs = %q, want 1 plain ASCII USDZ error", logs)
	}
	// Redrawing unchanged corrupt file does not spam logs a second time.
	renderV(t, dirApp, 11, nil)
	if len(logs) != 1 {
		t.Errorf("logs after second render = %d, want 1", len(logs))
	}
}

func TestHomePageWithSceneAndPoster(t *testing.T) {
	f := newFakeHA(t)
	a := newTestApp(t, f, Config{})
	a.dataDir = t.TempDir()

	if err := os.WriteFile(filepath.Join(a.dataDir, homeUSDZFile), syntheticHomeUSDZ(t, nil), 0o600); err != nil {
		t.Fatal(err)
	}
	fixJSON := `{
		"wall_height": 0.9,
		"rooms": [
			{"name": "客厅", "area": "客厅", "polygon": [[0, 0], [4, 0], [4, 3], [0, 3]], "floor": "tile", "floor_color": "#EAE4DA", "wall_color": "#ECE7DE"},
			{"name": "卧室", "area": "卧室", "polygon": [[4, 0], [7, 0], [7, 3], [4, 3]], "floor": "wood", "floor_color": "#CFAF88", "wall_color": "#E8DFD5"}
		],
		"devices": [
			{"entity": "light.living", "at": [1.8, 1.8], "height": 2.4},
			{"entity": "light.bed", "at": [5.0, 1.2], "height": 2.4}
		]
	}`
	if err := os.WriteFile(filepath.Join(a.dataDir, sceneFixFile), []byte(fixJSON), 0o600); err != nil {
		t.Fatal(err)
	}

	// UI version 11: interactive scene at the top with Poster set.
	v11, _ := renderV(t, a, 11, nil)
	sc := v11.Sections[0].Items[0]
	if sc.Type != wire.ComponentScene || sc.Key != "home3d" || sc.Poster == nil || !strings.HasPrefix(sc.Poster.Ref, "home3d:") {
		t.Fatalf("UIVersion 11 top item = %+v, poster = %+v", sc, sc.Poster)
	}
	if sc.Poster.Width != posterDefaultWidth || sc.Poster.Height != posterDefaultHeight {
		t.Errorf("poster dimensions = %d×%d, want 960×720", sc.Poster.Width, sc.Poster.Height)
	}
	refOn := sc.Poster.Ref

	// UI version 10 (and 6): standalone image pointing at the same poster ref.
	v10, _ := renderV(t, a, 10, nil)
	im := v10.Sections[0].Items[0]
	if im.Type != wire.ComponentImage || im.Key != "home3d" || im.Src == nil || im.Src.Ref != refOn || im.Aspect != wire.ImageAspectAuto {
		t.Fatalf("UIVersion 10 top item = %+v, src = %+v", im, im.Src)
	}

	// UI version 5 (before image existed in UI v6) and UI version 1: text summary + pin rows, passing CheckView.
	v5, _ := renderV(t, a, 5, nil)
	if v5.Sections[0].Items[0].Type != wire.ComponentText || len(v5.Sections[0].Items) != 1+len(sc.Pins) {
		t.Fatalf("UIVersion 5 top section = %+v", v5.Sections[0].Items)
	}
	renderV(t, a, 1, nil)

	decodeUIImage := func(img *wire.UIImage) image.Image {
		t.Helper()
		checked, err := wire.CheckImage(img)
		if err != nil {
			t.Fatalf("CheckImage: %v", err)
		}
		if checked.Mime != "image/png" {
			t.Fatalf("mime = %q, want image/png", checked.Mime)
		}
		raw, err := base64.StdEncoding.DecodeString(img.Data)
		if err != nil {
			t.Fatalf("base64: %v", err)
		}
		dec, err := png.Decode(bytes.NewReader(raw))
		if err != nil {
			t.Fatalf("png.Decode: %v", err)
		}
		return dec
	}

	// ui.image serves a valid 4:3 PNG at requested width and default 960×720.
	ans640, err := a.image(context.Background(), &ext.ImageRequest{UIImageParams: wire.UIImageParams{Contribution: pageUI, Ref: refOn, Width: 640}})
	if err != nil {
		t.Fatalf("a.image(640): %v", err)
	}
	decOn := decodeUIImage(ans640)
	if decOn.Bounds().Dx() != 640 || decOn.Bounds().Dy() != 480 {
		t.Fatalf("640 render bounds = %v, want 640×480", decOn.Bounds())
	}
	ansDef, err := a.image(context.Background(), &ext.ImageRequest{UIImageParams: wire.UIImageParams{Contribution: pageUI, Ref: refOn}})
	if err != nil {
		t.Fatalf("a.image(default): %v", err)
	}
	decDef := decodeUIImage(ansDef)
	if decDef.Bounds().Dx() != 960 || decDef.Bounds().Dy() != 720 {
		t.Fatalf("default render bounds = %v, want 960×720", decDef.Bounds())
	}

	// Unknown home3d: ref after a restart renders the current scene on demand.
	restarted := newTestApp(t, f, Config{})
	restarted.dataDir = a.dataDir
	ansStale, err := restarted.image(context.Background(), &ext.ImageRequest{UIImageParams: wire.UIImageParams{Contribution: pageUI, Ref: "home3d:stale000", Width: 320}})
	if err != nil {
		t.Fatalf("restarted a.image(stale): %v", err)
	}
	if b := decodeUIImage(ansStale).Bounds(); b.Dx() != 320 || b.Dy() != 240 {
		t.Fatalf("320 render bounds = %v, want 320×240", b)
	}

	// Turning light.living off makes the living-room floor pixels cooler and dimmer than when light.living is on.
	f.put(State{EntityID: "light.living", State: "off", Attributes: map[string]any{"friendly_name": "客厅灯"}})
	h, _, _ := a.client()
	h.mu.Lock()
	h.statesAt = time.Time{}
	h.mu.Unlock()

	v11Off, _ := renderV(t, a, 11, nil)
	scOff := v11Off.Sections[0].Items[0]
	if scOff.Poster.Ref == refOn {
		t.Fatal("poster ref did not change when light.living turned off")
	}
	ansOff, err := a.image(context.Background(), &ext.ImageRequest{UIImageParams: wire.UIImageParams{Contribution: pageUI, Ref: scOff.Poster.Ref, Width: 640}})
	if err != nil {
		t.Fatalf("a.image(off): %v", err)
	}
	decOff := decodeUIImage(ansOff)

	proj := newSceneProjector(sc, 640, 480)
	fx, fy, _ := proj.project(1.8, 1.8, 0.0)
	samplePatch := func(im image.Image, cx, cy int) (sum, warmth float64) {
		var rSum, gSum, bSum, n float64
		for dy := -3; dy <= 3; dy++ {
			for dx := -3; dx <= 3; dx++ {
				r, g, b, _ := im.At(cx+dx, cy+dy).RGBA()
				rSum += float64(r >> 8)
				gSum += float64(g >> 8)
				bSum += float64(b >> 8)
				n++
			}
		}
		rAvg, gAvg, bAvg := rSum/n, gSum/n, bSum/n
		return rAvg + gAvg + bAvg, rAvg - bAvg
	}
	sumOn, warmOn := samplePatch(decOn, int(math.Round(fx)), int(math.Round(fy)))
	sumOff, warmOff := samplePatch(decOff, int(math.Round(fx)), int(math.Round(fy)))
	if sumOn <= sumOff+20 || warmOn <= warmOff+10 {
		t.Errorf("lit room floor at (%d,%d) not warmer/brighter: on(sum=%.1f, R-B=%.1f) vs off(sum=%.1f, R-B=%.1f)",
			int(math.Round(fx)), int(math.Round(fy)), sumOn, warmOn, sumOff, warmOff)
	}
}

func TestSceneAutoMatchTVAndRemotePin(t *testing.T) {
	raw := syntheticHomeUSDZ(t, nil)
	parsed, err := readRoomPlan(raw)
	if err != nil {
		t.Fatalf("readRoomPlan: %v", err)
	}

	// 1. Auto-match scanned Television1 in livingRoom0 (2.4, 2.75) to a media_player TV entity in 客厅.
	r := newRichHA(t)
	r.put(
		State{EntityID: "media_player.living_tv", State: "playing", Attributes: map[string]any{"friendly_name": "客厅电视", "device_class": "tv"}},
		State{EntityID: "media_player.bedroom_tv", State: "off", Attributes: map[string]any{"friendly_name": "卧室电视", "device_class": "tv"}},
	)
	r.areas = areasJSON([][3]any{
		{"living", "客厅", []string{"light.living", "media_player.living_tv"}},
		{"bedroom", "卧室", []string{"light.bed", "media_player.bedroom_tv"}},
	})
	a := richApp(t, r, Config{})
	hm, err := a.home(context.Background(), 0)
	if err != nil {
		t.Fatal(err)
	}
	reg := a.pageRegistry(context.Background())
	hm = hm.named(reg)

	comp := a.buildSceneWith(parsed, nil, hm, reg)
	checkSceneView(t, comp)
	if len(comp.Pins) != 1 {
		t.Fatalf("auto-matched pins = %+v, want 1 TV pin", comp.Pins)
	}
	tvPin := comp.Pins[0]
	wantTile := a.deviceTile(hm, hm.ByID["media_player.living_tv"], true)
	if tvPin.Icon != wire.NavIconTv || tvPin.Tone != wire.TonePositive || tvPin.State != "播放中" {
		t.Errorf("media_player TV pin = %+v", tvPin)
	}
	if math.Hypot(tvPin.At[0]-2.4, tvPin.At[1]-2.75) > 1e-3 || tvPin.Height == nil || math.Abs(*tvPin.Height-0.55) > 1e-3 {
		t.Errorf("TV pin position = %v height=%v, want [2.4, 2.75] height=0.55", tvPin.At, tvPin.Height)
	}
	if tvPin.Action == nil || wantTile.Action == nil || tvPin.Action.ID != wantTile.Action.ID ||
		tvPin.Action.Push == nil || tvPin.Action.Push.State != wantTile.Action.Push.State {
		t.Errorf("TV pin action = %+v, want tile's %+v", tvPin.Action, wantTile.Action)
	}

	// Explicit pin within 0.8 m of Television1 suppresses duplicate auto-matching.
	compNear := a.buildSceneWith(parsed, &sceneFix{Devices: []fixDevice{{Entity: "light.living", At: []float64{2.4, 2.5}}}}, hm, reg)
	if len(compNear.Pins) != 1 || compNear.Pins[0].Icon != wire.NavIconLight {
		t.Errorf("pin near TV should suppress auto-match, got %+v", compNear.Pins)
	}

	// 2. Auto-match scanned Television1 to an IR TV remote (button.*) and track unknown -> 开 -> 关.
	const (
		tvOn  = "button.ir_tv_turn_on"
		tvOff = "button.ir_tv_turn_off"
	)
	r2 := newRichHA(t)
	r2.put(
		State{EntityID: tvOn, State: "unknown", Attributes: map[string]any{"friendly_name": "客厅电视 红外电视控制 开机"}},
		State{EntityID: tvOff, State: "unknown", Attributes: map[string]any{"friendly_name": "客厅电视 红外电视控制 关机"}},
	)
	r2.ws.entities = append(r2.ws.entities,
		map[string]any{"entity_id": tvOn, "device_id": "dev_irtv", "platform": "xiaomi_home"},
		map[string]any{"entity_id": tvOff, "device_id": "dev_irtv", "platform": "xiaomi_home"},
	)
	r2.ws.devices = append(r2.ws.devices, map[string]any{"id": "dev_irtv", "name": "客厅电视"})
	r2.areas = areasJSON([][3]any{{"living", "客厅", []string{"light.living", tvOn, tvOff}}})
	a2 := richApp(t, r2, Config{})

	pinNow := func() wire.ScenePin {
		t.Helper()
		h2, _, _ := a2.client()
		h2.mu.Lock()
		h2.statesAt = time.Time{}
		h2.mu.Unlock()
		cur, err := a2.home(context.Background(), 0)
		if err != nil {
			t.Fatal(err)
		}
		rg := a2.pageRegistry(context.Background())
		sc2 := a2.buildSceneWith(parsed, nil, cur.named(rg), rg)
		checkSceneView(t, sc2)
		if len(sc2.Pins) != 1 {
			t.Fatalf("IR TV pins = %+v, want 1", sc2.Pins)
		}
		return sc2.Pins[0]
	}

	p0 := pinNow()
	if p0.Label != "电视" || p0.Icon != wire.NavIconTv || p0.State != "不知道开没开" || p0.Tone != "" || p0.Action == nil || p0.Action.ID != "open:"+tvOn {
		t.Errorf("IR TV pin before press = %+v", p0)
	}

	now := time.Now().UTC()
	r2.put(
		State{EntityID: tvOn, State: now.Add(-time.Minute).Format(time.RFC3339Nano), Attributes: map[string]any{"friendly_name": "客厅电视 红外电视控制 开机"}},
		State{EntityID: tvOff, State: now.Add(-time.Hour).Format(time.RFC3339Nano), Attributes: map[string]any{"friendly_name": "客厅电视 红外电视控制 关机"}},
	)
	pOn := pinNow()
	if pOn.State != "开" || pOn.Tone != wire.TonePositive {
		t.Errorf("IR TV pin after power on = %+v", pOn)
	}

	if err := a2.marks.mark(tvOn, now); err != nil {
		t.Fatal(err)
	}
	pOff := pinNow()
	if pOff.State != "关" || pOff.Tone != "" {
		t.Errorf("IR TV pin after mark off = %+v", pOff)
	}
}

func TestSceneWatchInvalidatesOnStateChange(t *testing.T) {
	was := sceneInvalidateFor
	sceneInvalidateFor = 15 * time.Millisecond
	t.Cleanup(func() { sceneInvalidateFor = was })

	r := newRichHA(t)
	a := richApp(t, r, Config{}) // zero watch rules
	if err := os.WriteFile(filepath.Join(a.dataDir, homeUSDZFile), syntheticHomeUSDZ(t, nil), 0o600); err != nil {
		t.Fatal(err)
	}
	redrawn := make(invalidated, 8)
	a.rpc = redrawn

	// Warm the 5-second states cache with light.bed = off.
	vBefore, _ := renderV(t, a, 11, nil)
	if vBefore.Sections[0].Items[0].Caption != "2 盏灯开着" { // light.living + light.ceiling on, light.bed off
		t.Fatalf("initial caption = %q, want 2 盏灯开着", vBefore.Sections[0].Items[0].Caption)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go a.watch.run(ctx)

	waitFor(t, "scene state_changed subscription", func() bool { return len(r.asked("subscribe_events")) == 1 })
	waitFor(t, "watcher connected", func() bool { a.watch.mu.Lock(); defer a.watch.mu.Unlock(); return a.watch.connected })

	// Turn light.bed on and push state_changed: watcher must clear cached states and send ui.invalidate("home").
	bedAttr := map[string]any{"friendly_name": "卧室灯", "supported_color_modes": []any{"onoff"}}
	r.put(State{EntityID: "light.bed", State: "on", Attributes: bedAttr})
	r.push("light.bed", st("light.bed", "off", bedAttr), st("light.bed", "on", bedAttr))

	select {
	case ui := <-redrawn:
		if ui != pageUI {
			t.Fatalf("invalidated %q, want %q", ui, pageUI)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for rpc.Invalidate(\"home\") on state_changed")
	}

	// Immediate re-render (within statesFor=5s) must see the fresh state rather than stale cached /api/states.
	vAfter, _ := renderV(t, a, 11, nil)
	if vAfter.Sections[0].Items[0].Caption != "3 盏灯开着" {
		t.Fatalf("caption after state_changed = %q, want 3 盏灯开着", vAfter.Sections[0].Items[0].Caption)
	}
}
