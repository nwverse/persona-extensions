package main

import (
	"encoding/json"
	"math"
	"testing"

	"github.com/nwverse/persona/sdk/go/wire"
)

// A synthetic two-room flat for #943: a 6 x 4 m box split by a wall at x = 3 with a 0.9 m door, a window in the south
// wall of the west room, and a few pieces of furniture.
func detailScan() *scan {
	return &scan{
		Walls: []scanWall{
			{Name: "South", Center: [2]float64{3, 0}, Size: [2]float64{6, 0.16}, Top: 2.5},
			{Name: "North", Center: [2]float64{3, 4}, Size: [2]float64{6, 0.16}, Top: 2.5},
			{Name: "West", Center: [2]float64{0, 2}, Size: [2]float64{4, 0.16}, Yaw: 90, Top: 2.5},
			{Name: "East", Center: [2]float64{6, 2}, Size: [2]float64{4, 0.16}, Yaw: 90, Top: 2.5},
			{Name: "Middle", Center: [2]float64{3, 2}, Size: [2]float64{4, 0.16}, Yaw: 90, Top: 2.5},
		},
		Doors:   []scanDoor{{Name: "Door", Open: true, Center: [2]float64{3, 1}, Size: [2]float64{0.9, 0.05}, Yaw: 90, Top: 2.1}},
		Windows: []scanWindow{{Name: "Window", Center: [2]float64{1.5, 0}, Size: [2]float64{1.2, 0.05}, Bottom: 0.9, Top: 2.2}},
		Objects: []scanObject{
			{Category: "sofa", Name: "Sofa", Center: [2]float64{4.5, 3.5}, Size: [2]float64{2.1, 0.9}, Top: 0.85},
			{Category: "storage", Name: "Wardrobe", Center: [2]float64{1.5, 3.72}, Size: [2]float64{1.8, 0.6}, Top: 2.1},
			{Category: "table", Name: "Round", Center: [2]float64{4.5, 2}, Size: [2]float64{0.8, 0.8}, Top: 0.45},
			{Category: "storage", Name: "Stove", Center: [2]float64{5.6, 0.6}, Size: [2]float64{0.8, 0.5}, Top: 0.9},
			// a cabinet right where the door's leaf would swing on one side
			{Category: "storage", Name: "ByDoor", Center: [2]float64{2.5, 1.35}, Size: [2]float64{0.6, 0.4}, Top: 0.8},
		},
	}
}

func detailFix() *sceneFix {
	thick, wallH := 0.08, 2.4
	yaw := 0.0
	bottom := 0.0
	return &sceneFix{
		Rotate: new(float64), WallHeight: &wallH, WallThickness: &thick,
		Rooms: []fixRoom{
			{Name: "West", Polygon: [][]float64{{0, 0}, {3, 0}, {3, 3.5}, {0, 3.5}}},
			{Name: "East", Polygon: [][]float64{{3, 0}, {6, 0}, {6, 4}, {3, 4}}},
		},
		Objects: []fixObject{
			{Name: "Round", Kind: "round_coffee_table", Color: "#F6F4EE"},
			{Name: "Stove", Kind: "stove", Center: []float64{5.5, 0.5}, Size: []float64{0.7, 0.5}, Yaw: &yaw, Bottom: &bottom},
		},
		Add: []fixAdd{
			{Kind: "piano", Center: []float64{1.5, 2.6}, Size: []float64{1.3, 0.4}, Yaw: &yaw},
			{Kind: "bar_table", Center: []float64{4.5, 0.9}, Size: []float64{0.56, 1.1}, Yaw: &yaw, Top: 1.04},
			{Kind: "bench", Center: []float64{1.5, 3.1}},
		},
		Curtains: []fixCurtain{{Wall: "South", Room: "West", Color: "#788E9C"}},
	}
}

func TestSceneDetailBuildsValidView(t *testing.T) {
	comp := buildScene(detailScan(), detailFix(), nil)
	checkSceneView(t, comp)
	if len(comp.Shapes) >= 500 {
		t.Fatalf("shapes = %d, want under 500", len(comp.Shapes))
	}
	count := func(match func(wire.SceneShape) bool) int {
		n := 0
		for _, s := range comp.Shapes {
			if match(s) {
				n++
			}
		}
		return n
	}
	if n := count(func(s wire.SceneShape) bool { return s.Color == "#F8F8F6" }); n != 1 {
		t.Errorf("piano white keys = %d, want 1", n)
	}
	if n := count(func(s wire.SceneShape) bool { return s.Color == "#222226" }); n < 10 || n > 24 {
		t.Errorf("piano black keys = %d, want 10 to 24", n)
	}
	if n := count(func(s wire.SceneShape) bool { return s.Color == goldColor && s.Kind == wire.SceneShapeCylinder }); n < 18 {
		t.Errorf("gold cylinders (bar cages and coffee table base) = %d, want at least 18", n)
	}
	if n := count(func(s wire.SceneShape) bool { return s.Color == "#1C1C20" }); n != 1 {
		t.Errorf("stove hobs = %d, want 1", n)
	}
	// every wall is drawn at the corrected thickness
	for _, s := range comp.Shapes {
		if s.Material == wire.SceneMaterialMatte && s.Color == neutralWallColor && math.Abs(s.Size[1]-0.08) > 1e-6 {
			t.Errorf("wall drawn %g thick, want 0.08: %+v", s.Size[1], s)
		}
	}
}

func TestSceneDetailCurtainsHangOnTheRoomSide(t *testing.T) {
	comp := buildScene(detailScan(), detailFix(), nil)
	folds := 0
	for _, s := range comp.Shapes {
		if s.Color != shade("#788E9C", -6) && s.Color != shade("#788E9C", 6) {
			continue
		}
		folds++
		if s.Center[1] <= 0 {
			t.Errorf("curtain fold at y=%g is outside the west room (y > 0)", s.Center[1])
		}
		if s.Center[0] > 3 {
			t.Errorf("curtain fold at x=%g is not by the west room's window", s.Center[0])
		}
	}
	if folds != 2*int(math.Ceil(curtainSpan/curtainFold)) {
		t.Errorf("curtain folds = %d, want %d", folds, 2*int(math.Ceil(curtainSpan/curtainFold)))
	}
}

func TestSceneDetailOpenDoorMissesFurniture(t *testing.T) {
	fp := applySceneFix(detailScan(), detailFix())
	var door scanDoor
	for _, d := range fp.Doors {
		if d.Name == "Door" {
			door = d
		}
	}
	// The wall runs along y at x = 3: u = (0, 1), n = (-1, 0) points into the west room, the smaller one here.
	sgn, side := openLeafSides(door.Center, 0.9, 0, 1, -1, 0, fp.Rooms, fp.Objects)
	if sgn != 1 {
		t.Errorf("leaf swings into sgn=%g, want 1 (the west room is smaller)", sgn)
	}
	// With the rooms the other way round it swings east.
	big := []planRoom{
		{Name: "West", Polygon: [][2]float64{{0, 0}, {3, 0}, {3, 4}, {0, 4}}},
		{Name: "East", Polygon: [][2]float64{{3, 0}, {6, 0}, {6, 3.5}, {3, 3.5}}},
	}
	if sgn, _ := openLeafSides(door.Center, 0.9, 0, 1, -1, 0, big, nil); sgn != -1 {
		t.Errorf("leaf swings into sgn=%g, want -1 (the east room is smaller)", sgn)
	}
	// Hinged at y = 1.45 (side +1) the leaf would sweep through the cabinet at (2.5, 1.35); hinged at y = 0.55 it is clear.
	if side != -1 {
		t.Errorf("hinge side = %g, want -1 (away from the cabinet)", side)
	}
}

func TestSceneDetailKeepsFurnitureOffWalls(t *testing.T) {
	fp := applySceneFix(detailScan(), detailFix())
	ob := scanObject{Category: "storage", Name: "Wardrobe", Center: [2]float64{1.5, 3.72}, Size: [2]float64{1.8, 0.6}, Top: 2.1}
	keepOffWalls(&ob, fp.Walls, nil)
	// The north wall's face is at y = 4 - 0.04: the wardrobe's back (center + 0.3) must not pass it.
	if back := ob.Center[1] + 0.3; back > 3.96+1e-9 {
		t.Errorf("wardrobe back at y=%g, past the wall face at 3.96", back)
	}
	if math.Abs(ob.Center[0]-1.5) > 1e-9 {
		t.Errorf("wardrobe moved along the wall to x=%g", ob.Center[0])
	}
	// With curtains on the wall, it keeps clear of them too.
	ob2 := scanObject{Category: "storage", Name: "Wardrobe", Center: [2]float64{1.5, 3.72}, Size: [2]float64{1.8, 0.6}, Top: 2.1}
	keepOffWalls(&ob2, fp.Walls, map[string]bool{"North": true})
	if back := ob2.Center[1] + 0.3; back > 3.96-curtainGap+1e-9 {
		t.Errorf("wardrobe back at y=%g, in the curtains", back)
	}
}

func TestSceneFixObjectPlacement(t *testing.T) {
	raw := []byte(`{"objects":[{"name":"Stove","kind":"stove","center":[5.5,0.5],"size":[0.7,0.5],"yaw":0,"bottom":0,"top":0.9}],
		"wall_thickness":0.08,"curtains":[{"wall":"South","room":"West","color":"#788E9C"}]}`)
	fix, err := parseSceneFix(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	fp := applySceneFix(detailScan(), fix)
	for _, ob := range fp.Objects {
		if ob.Name != "Stove" {
			continue
		}
		if ob.Kind != "stove" || ob.Center != [2]float64{5.5, 0.5} || ob.Size != [2]float64{0.7, 0.5} || !ob.FixedYaw || ob.Top != 0.9 {
			t.Errorf("stove = %+v, want the corrected kind, place, size, yaw and top", ob)
		}
	}
	for _, w := range fp.Walls {
		if w.Thickness != 0.08 {
			t.Errorf("wall %s thickness = %g, want 0.08", w.Name, w.Thickness)
		}
	}
	for _, bad := range []string{
		`{"wall_thickness":0.01}`,
		`{"objects":[{"name":"A","kind":"storage","center":[1]}]}`,
		`{"objects":[{"name":"A","kind":"storage","size":[0,1]}]}`,
		`{"objects":[{"name":"A","kind":"storage","bottom":1,"top":0.5}]}`,
		`{"curtains":[{"room":"West"}]}`,
		`{"curtains":[{"wall":"South","color":"blue"}]}`,
	} {
		if _, err := parseSceneFix([]byte(bad)); err == nil {
			t.Errorf("parseSceneFix(%s) accepted it", bad)
		}
	}
}

func TestSceneWallFacesTakeTheirRoomsColors(t *testing.T) {
	fix := detailFix()
	fix.Rooms[0].WallColor = "#F4F2EC"
	fix.Rooms[1].WallColor = "#C4DEEE"
	fix.WallFaces = []fixCurtain{{Wall: "North", Room: "East", Color: "#F1E6B6"}}
	comp := buildScene(detailScan(), fix, nil)
	checkSceneView(t, comp)
	colors := map[string]int{}
	for _, s := range comp.Shapes {
		if !s.Wall {
			continue
		}
		// the middle wall (x = 3) is drawn as two layers, one per room
		if math.Abs(s.Center[0]-3) < 0.05 && s.Center[1] > 0.2 && s.Center[1] < 3.8 {
			if math.Abs(s.Size[1]-0.04) > 1e-9 {
				t.Errorf("a layer of the middle wall is %g thick, want 0.04", s.Size[1])
			}
			colors[s.Color]++
		}
		// the north wall faces the east room (the west room stops at y = 3.5): its wall_faces color inside and out
		if s.Center[1] > 3.9 && s.Color != "#F1E6B6" {
			t.Errorf("north wall is %s, want #F1E6B6", s.Color)
		}
		// the south wall in the west room is one box in the room's color
		if s.Center[1] < 0.1 && s.Center[0] < 2.9 && (s.Color != "#F4F2EC" || math.Abs(s.Size[1]-0.08) > 1e-9) {
			t.Errorf("south wall in the west room is %s, %g thick; want #F4F2EC, 0.08", s.Color, s.Size[1])
		}
	}
	if colors["#F4F2EC"] == 0 || colors["#C4DEEE"] == 0 || colors["#F4F2EC"] != colors["#C4DEEE"] {
		t.Errorf("middle wall layers by color = %v, want as many of each room's color", colors)
	}

	bad := *fix
	bad.WallFaces = []fixCurtain{{Wall: "North", Color: "#F1E6B6"}}
	raw, _ := json.Marshal(bad)
	if _, err := parseSceneFix(raw); err == nil {
		t.Error("a wall face without a room was accepted")
	}
}

func TestWallEndsJoinWithoutGaps(t *testing.T) {
	// as a scan leaves them with 0.08 m walls: each wall stops at the old 0.16 m wall's face, 0.08 short of the line
	// it meets. South runs x 0..4 on y = 0, West runs y 0.08..3 on x = 0 (an L corner at the origin), Middle runs
	// y 0.08..3 on x = 2 (a T on South), and North runs on past West to x = -0.5 (West ends into its middle).
	walls := []planWall{
		{Name: "South", A: [2]float64{0, 0}, B: [2]float64{4, 0}, Thickness: 0.08},
		{Name: "West", A: [2]float64{0, 0.08}, B: [2]float64{0, 2.92}, Thickness: 0.08},
		{Name: "Middle", A: [2]float64{2, 0.08}, B: [2]float64{2, 2.92}, Thickness: 0.08},
		{Name: "North", A: [2]float64{-0.5, 3}, B: [2]float64{4, 3}, Thickness: 0.08},
	}
	joinWallEnds(walls)
	near := func(got, want float64) bool { return math.Abs(got-want) < 1e-9 }
	// L: South (earlier) runs through to West's outer face (x = -0.04), West only down to South's inner face (y = 0.04)
	if !near(walls[0].ReachA, 0.04) || !near(walls[1].ReachA, 0.04) {
		t.Errorf("corner reaches South %g, West %g; want 0.04 and 0.04", walls[0].ReachA, walls[1].ReachA)
	}
	// T: Middle reaches the faces of South and North (y = 0.04 and 2.96)
	if !near(walls[2].ReachA, 0.04) || !near(walls[2].ReachB, 0.04) {
		t.Errorf("Middle reaches %g, %g; want 0.04 and 0.04", walls[2].ReachA, walls[2].ReachB)
	}
	// West's top end meets North where North goes on past it: a T, so West stops at North's face
	if !near(walls[1].ReachB, 0.04) {
		t.Errorf("West reaches %g at North, want 0.04", walls[1].ReachB)
	}
	// nothing moved, and a free end is left alone
	if walls[0].A != [2]float64{0, 0} || walls[0].ReachB != 0 || walls[3].ReachA != 0 {
		t.Errorf("ends moved or free ends reached: %+v", walls)
	}
}
