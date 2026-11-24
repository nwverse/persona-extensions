package main

import (
	"math"
	"testing"

	"github.com/nwverse/persona/sdk/go/wire"
)

// The same synthetic room as the app's cutaway tests (nwverse/persona#13036): 6 x 4 m, walls 2.5 m high, the south
// wall along y = 0 and the west wall along x = 0.
func cutawayRoom() []wire.SceneShape {
	wall := func(c, s [2]float64) wire.SceneShape {
		b := sceneBox(c, s, 0, 0, 0, 2.5, "#EAE6DF", wire.SceneMaterialMatte)
		b.Wall = true
		return b
	}
	return []wire.SceneShape{
		wall([2]float64{3, 0}, [2]float64{6, 0.1}),                                                                  // 0 south
		wall([2]float64{3, 4}, [2]float64{6, 0.1}),                                                                  // 1 north
		wall([2]float64{0, 2}, [2]float64{0.1, 4}),                                                                  // 2 west
		wall([2]float64{6, 2}, [2]float64{0.1, 4}),                                                                  // 3 east
		sceneBox([2]float64{2, 0}, [2]float64{1.2, 0.04}, 0, 0, 0.9, 2.3, "#B8D8E8", wire.SceneMaterialGlass),       // 4 window
		sceneBox([2]float64{4.5, 0.15}, [2]float64{0.9, 0.2}, 0, 0, 2.05, 2.33, "#F4F4F2", wire.SceneMaterialMatte), // 5 air conditioner
		sceneBox([2]float64{1, 0.36}, [2]float64{1.2, 0.6}, 0, 0, 0, 2.1, "#EFEBE4", wire.SceneMaterialWood),        // 6 wardrobe, south
		sceneBox([2]float64{3, 3.64}, [2]float64{1.2, 0.6}, 0, 0, 0, 2.1, "#EFEBE4", wire.SceneMaterialWood),        // 7 wardrobe, north
		sceneBox([2]float64{4, 0.5}, [2]float64{2, 0.9}, 0, 0, 0, 0.85, "#C8C8C6", wire.SceneMaterialMatte),         // 8 sofa
		scenePrism([][2]float64{{0, 0}, {6, 0}, {6, 4}, {0, 4}}, -0.02, 0, "#EAE4DA", wire.SceneMaterialTile),       // 9 floor
		sceneBox([2]float64{5, 0}, [2]float64{0.05, 0.14}, 0, 0, 0, 2.1, frameColor, wire.SceneMaterialWood),        // 10 door jamb
	}
}

func TestCutawayLowersTheWallsFacingTheCamera(t *testing.T) {
	shapes := cutawayRoom()
	walls := cutawayWalls(shapes)
	if len(walls) != 4 {
		t.Fatalf("walls = %d, want 4", len(walls))
	}
	mid := planMiddle(shapes)
	for _, c := range []struct {
		yaw  float64
		want []int
	}{{225, []int{0, 2}}, {45, []int{1, 3}}, {270, []int{0}}} {
		got := cutawayLowered(walls, mid, c.yaw)
		if len(got) != len(c.want) || (len(got) > 0 && (got[0] != c.want[0] || got[len(got)-1] != c.want[len(c.want)-1])) {
			t.Errorf("yaw %g: lowered %v, want %v", c.yaw, got, c.want)
		}
	}
}

func TestCutawayCutsWhatStandsInAWallAndShowsThroughTallFurniture(t *testing.T) {
	shapes := cutawayRoom()
	cut := sceneCutaway(shapes, 225)
	// the window and the air conditioner start above the cut: left out
	if len(cut) != len(shapes)-2 {
		t.Fatalf("shapes = %d, want %d (window and air conditioner left out)", len(cut), len(shapes)-2)
	}
	top := func(s wire.SceneShape) float64 { return *s.Top }
	// 0 south and 2 west are cut; 1 north and 3 east stay full height
	if math.Abs(top(cut[0])-cutawayHeight) > 1e-9 || math.Abs(top(cut[2])-cutawayHeight) > 1e-9 {
		t.Errorf("lowered walls at %g and %g, want %g", top(cut[0]), top(cut[2]), cutawayHeight)
	}
	if top(cut[1]) != 2.5 || top(cut[3]) != 2.5 {
		t.Errorf("far walls at %g and %g, want 2.5", top(cut[1]), top(cut[3]))
	}
	// after leaving out 4 and 5: 4 = south wardrobe, 5 = north wardrobe, 6 = sofa, 7 = floor, 8 = jamb
	if cut[4].Material != wire.SceneMaterialGlass {
		t.Errorf("the wardrobe against the lowered wall is %q, want see-through (glass)", cut[4].Material)
	}
	if cut[5].Material != wire.SceneMaterialWood || cut[6].Material != wire.SceneMaterialMatte {
		t.Errorf("the far wardrobe and the sofa changed: %q, %q", cut[5].Material, cut[6].Material)
	}
	if math.Abs(top(cut[8])-cutawayHeight) > 1e-9 || *cut[8].Bottom != 0 {
		t.Errorf("the door jamb in the lowered wall stands %g..%g, want 0..%g", *cut[8].Bottom, top(cut[8]), cutawayHeight)
	}
	// the shapes passed in are not changed
	if top(shapes[0]) != 2.5 || shapes[6].Material != wire.SceneMaterialWood {
		t.Error("sceneCutaway changed the shapes it was given")
	}
	// without walls marked nothing is cut
	for i := range shapes {
		shapes[i].Wall = false
	}
	if got := sceneCutaway(shapes, 225); len(got) != len(shapes) || top(got[0]) != 2.5 {
		t.Error("shapes without a wall mark were cut")
	}
}

func TestCutawayKeepsAWallBetweenRoomsAndWhatHangsOnIt(t *testing.T) {
	// a wall across the room's south part at y = 1.2 (a room on each side) with a TV on its south face
	inner := sceneBox([2]float64{3, 1.2}, [2]float64{6, 0.1}, 0, 0, 0, 2.5, "#EAE6DF", wire.SceneMaterialMatte)
	inner.Wall = true
	tv := sceneBox([2]float64{3, 1.13}, [2]float64{1.2, 0.04}, 0, 0, 1.1, 1.8, "#1E2024", wire.SceneMaterialScreen)
	shapes := append(cutawayRoom(), inner, tv)
	walls := cutawayWalls(shapes)
	if w := walls[len(walls)-1]; !w.floorAcross || !w.floorOpposite {
		t.Errorf("the inner wall has floors %v / %v, want both", w.floorAcross, w.floorOpposite)
	}
	if got := cutawayLowered(walls, planMiddle(shapes), 225); len(got) != 2 || got[0] != 0 || got[1] != 2 {
		t.Errorf("lowered %v, want the south and west walls only", got)
	}
	cut := sceneCutaway(shapes, 225)
	if last := cut[len(cut)-1]; last.Color != "#1E2024" || *last.Top != 1.8 {
		t.Error("the TV on the inner wall was cut or left out")
	}
	if in := cut[len(cut)-2]; *in.Top != 2.5 {
		t.Errorf("the inner wall stands %g, want 2.5", *in.Top)
	}

	// without floors every wall in the camera's half that faces it comes down, the inner one too
	var bare []wire.SceneShape
	for _, s := range shapes {
		if s.Kind != wire.SceneShapePrism {
			bare = append(bare, s)
		}
	}
	if got := cutawayLowered(cutawayWalls(bare), planMiddle(bare), 225); len(got) != 3 {
		t.Errorf("without floors lowered %v, want 3 walls", got)
	}
}

func TestSceneMarksWallsAndHangsAirConditioners(t *testing.T) {
	fix := detailFix()
	comp := buildScene(detailScan(), fix, nil)
	checkSceneView(t, comp)
	walls := 0
	for _, s := range comp.Shapes {
		if s.Wall {
			walls++
			if s.Kind != wire.SceneShapeBox {
				t.Errorf("a %s is marked a wall", s.Kind)
			}
		}
	}
	if walls < 5 {
		t.Errorf("wall boxes marked = %d, want the five walls and their pieces", walls)
	}

	fp := applySceneFix(detailScan(), fix)
	pins := []wire.ScenePin{
		{ID: "ac", At: []float64{1.5, 3.7}, Label: "卧室空调", Icon: airConditionerIcon},
		{ID: "lamp", At: []float64{1.5, 2}, Label: "灯", Icon: wire.NavIconLight},
		{ID: "far", At: []float64{1.5, 2}, Label: "客厅空调", Icon: airConditionerIcon},
	}
	ac := airConditionerShapes(pins, fp.Walls, 2.4)
	if len(ac) != 3 {
		t.Fatalf("air conditioner shapes = %d, want 3 (one unit by the north wall; none for the lamp or the pin far from walls)", len(ac))
	}
	body := ac[0]
	if *body.Bottom != airConditionerBottom || *body.Top != airConditionerTop {
		t.Errorf("unit from %g to %g, want %g to %g", *body.Bottom, *body.Top, airConditionerBottom, airConditionerTop)
	}
	// on the room's side of the north wall (y = 4, 0.08 thick): its middle 0.11 m in from the face
	if math.Abs(body.Center[1]-(4-0.04-0.11)) > 1e-9 {
		t.Errorf("unit at y=%g, want %g", body.Center[1], 4-0.04-0.11)
	}
	if airConditionerShapes(pins, fp.Walls, 1.3) != nil {
		t.Error("an air conditioner was hung on walls lower than it")
	}
}
