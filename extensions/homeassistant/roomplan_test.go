package main

import (
	"archive/zip"
	"bytes"
	"fmt"
	"math"
	"strings"
	"testing"
)

// Synthetic two-room RoomPlan export (livingRoom0: 4 × 3 m at x 0..4, y 0..3; bedroom0: 3 × 3 m at x 4..7, y 0..3),
// built in memory with archive/zip (stored, no compression, as scanning apps write it).
func syntheticHomeUSDZ(t testing.TB, extra map[string]string) []byte {
	t.Helper()
	// Lowest wall bottom sits at world Y = -0.40 so readRoomPlan's floor shift to 0 is exercised.
	const yShift = -0.40

	entries := map[string]string{
		"Home.usda": `#usda 1.0
(
    defaultPrim = "Root"
    metersPerUnit = 1
    upAxis = "Y"
)

def Xform "Root"
{
    def Xform "Section_grp"
    {
        def Xform "livingRoom0" (
            kind = "assembly"
        )
        {
            matrix4d xformOp:transform = ( (1, 0, 0, 0), (0, 1, 0, 0), (0, 0, 1, 0), (2.0, 0, -1.5, 1) )
            uniform token[] xformOpOrder = ["xformOp:transform"]
        }
        def Xform "bedroom0" (
            kind = "assembly"
        )
        {
            matrix4d xformOp:transform = ( (1, 0, 0, 0), (0, 1, 0, 0), (0, 0, 1, 0), (5.5, 0, -1.5, 1) )
            uniform token[] xformOpOrder = ["xformOp:transform"]
        }
    }
}
`,
		// Outer walls + partition wall between the two rooms:
		"assets/Mesh/Wall/Wall1.usda": usdaBoxAsset("Wall1", "Wall", 7.0, 2.6, 0.14, 3.5, 1.3+yShift, 0, 0),
		"assets/Mesh/Wall/Wall2.usda": usdaBoxAsset("Wall2", "Wall", 7.0, 2.6, 0.14, 3.5, 1.3+yShift, -3.0, 0),
		"assets/Mesh/Wall/Wall3.usda": usdaBoxAsset("Wall3", "Wall", 3.0, 2.6, 0.14, 0.0, 1.3+yShift, -1.5, 90),
		"assets/Mesh/Wall/Wall4.usda": usdaBoxAsset("Wall4", "Wall", 3.0, 2.6, 0.14, 7.0, 1.3+yShift, -1.5, 90),
		"assets/Mesh/Wall/Wall5.usda": usdaBoxAsset("Wall5", "Wall", 3.0, 2.6, 0.12, 4.0, 1.3+yShift, -1.5, 90),

		// Closed entrance door on Wall1, open interior door on Wall5, one window on Wall2:
		"assets/Mesh/Door/Door1.usda":     usdaBoxAsset("Door1", "Door(Isopen: False)", 0.9, 2.1, 0.05, 1.2, 1.05+yShift, 0, 0),
		"assets/Mesh/Door/Door2.usda":     usdaBoxAsset("Door2", "Door(Isopen: True)", 0.85, 2.1, 0.05, 4.0, 1.05+yShift, -1.5, 90),
		"assets/Mesh/Window/Window1.usda": usdaBoxAsset("Window1", "Window", 1.4, 1.2, 0.05, 2.2, 1.4+yShift, -3.0, 0),

		// Furniture: sofa, bed, table with two chairs, television, storage cabinet:
		"assets/Mesh/Sofa/Sofa1.usda":             usdaBoxAsset("Sofa1", "Sofa", 2.0, 0.82, 0.9, 0.55, 0.41+yShift, -1.5, 90),
		"assets/Mesh/Bed/Bed1.usda":               usdaBoxAsset("Bed1", "Bed", 1.6, 0.95, 2.0, 5.8, 0.475+yShift, -2.0, 0),
		"assets/Mesh/Table/Table1.usda":           usdaBoxAsset("Table1", "Table", 1.2, 0.75, 0.8, 2.6, 0.375+yShift, -1.5, 0),
		"assets/Mesh/Chair/Chair1.usda":           usdaBoxAsset("Chair1", "Chair", 0.46, 0.82, 0.46, 2.6, 0.41+yShift, -0.8, 0),
		"assets/Mesh/Chair/Chair2.usda":           usdaBoxAsset("Chair2", "Chair", 0.46, 0.82, 0.46, 2.6, 0.41+yShift, -2.2, 180),
		"assets/Mesh/Television/Television1.usda": usdaBoxAsset("Television1", "Television", 1.3, 1.1, 0.38, 2.4, 0.55+yShift, -2.75, 0),
		"assets/Mesh/Storage/Storage1.usda":       usdaBoxAsset("Storage1", "Storage", 1.5, 1.8, 0.45, 3.2, 0.9+yShift, -0.3, 0),

		// Floor outline (0,0) -> (7,0) -> (7,3) -> (0,3) in plan coordinates (world z = -plan y):
		"assets/Mesh/Floor/Floor0.usda": fmt.Sprintf(`#usda 1.0
(
    defaultPrim = "Floor0"
    metersPerUnit = 1
    upAxis = "Y"
)

def Xform "Floor0" (
    customData = {
        string Category = "Floor"
    }
    kind = "component"
)
{
    def Mesh "Floor0"
    {
        point3f[] points = [(0, %g, 0), (7, %g, 0), (7, %g, -3), (0, %g, -3)]
        matrix4d xformOp:transform = ( (1, 0, 0, 0), (0, 1, 0, 0), (0, 0, 1, 0), (0, 0, 0, 1) )
        uniform token[] xformOpOrder = ["xformOp:transform"]
    }
}
`, yShift, yShift, yShift, yShift),
	}
	for k, v := range extra {
		if v == "" {
			delete(entries, k)
		} else {
			entries[k] = v
		}
	}
	return zipStored(t, entries)
}

// usdaBoxAsset writes one synthetic RoomPlan component .usda rotated CCW by yawDeg in plan around world (wx, wy, wz).
func usdaBoxAsset(name, category string, lx, ly, lz, wx, wy, wz, yawDeg float64) string {
	rad := yawDeg * math.Pi / 180
	c, s := math.Cos(rad), math.Sin(rad)
	hx, hy, hz := lx/2, ly/2, lz/2
	var pts string
	if hz == 0 {
		pts = fmt.Sprintf("[(-%.4f, -%.4f, 0), (%.4f, -%.4f, 0), (%.4f, %.4f, 0), (-%.4f, %.4f, 0)]",
			hx, hy, hx, hy, hx, hy, hx, hy)
	} else {
		pts = fmt.Sprintf("[(-%.4f, -%.4f, -%.4f), (%.4f, -%.4f, -%.4f), (%.4f, %.4f, %.4f), (-%.4f, %.4f, %.4f)]",
			hx, hy, hz, hx, hy, hz, hx, hy, hz, hx, hy, hz)
	}
	// Row-vector USD transform: local +x -> (cos, 0, -sin), local +z -> (sin, 0, cos), translation (wx, wy, wz, 1).
	return fmt.Sprintf(`#usda 1.0
(
    defaultPrim = %q
    metersPerUnit = 1
    upAxis = "Y"
)

def Xform %q (
    customData = {
        string Category = %q
        string UUID = "00000000-0000-0000-0000-000000000000"
    }
    kind = "component"
)
{
    def Mesh %q
    {
        point3f[] points = %s
        matrix4d xformOp:transform = ( (%.6f, 0, %.6f, 0), (0, 1, 0, 0), (%.6f, 0, %.6f, 0), (%.6f, %.6f, %.6f, 1) )
        uniform token[] xformOpOrder = ["xformOp:transform"]
    }
}
`, name, name, category, name, pts, c, -s, s, c, wx, wy, wz)
}

func zipStored(t testing.TB, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range files {
		w, err := zw.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Store})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestReadRoomPlan(t *testing.T) {
	raw := syntheticHomeUSDZ(t, map[string]string{
		"assets/Mesh/Mystery/Mystery1.usda": usdaBoxAsset("Mystery1", "PlantStand", 0.4, 0.7, 0.4, 1.0, 0.0, -2.5, 0),
	})
	sc, err := readRoomPlan(raw)
	if err != nil {
		t.Fatalf("readRoomPlan: %v", err)
	}

	if len(sc.Walls) != 5 {
		t.Fatalf("walls = %d, want 5", len(sc.Walls))
	}
	if len(sc.Doors) != 2 {
		t.Fatalf("doors = %d, want 2", len(sc.Doors))
	}
	if len(sc.Windows) != 1 {
		t.Fatalf("windows = %d, want 1", len(sc.Windows))
	}
	if len(sc.Objects) != 8 {
		t.Fatalf("objects = %d, want 8 (7 + 1 unknown category)", len(sc.Objects))
	}
	if len(sc.Floor) != 4 {
		t.Fatalf("floor vertices = %d, want 4: %+v", len(sc.Floor), sc.Floor)
	}
	if len(sc.Rooms) != 2 || sc.Rooms[0].Name != "livingRoom0" || sc.Rooms[1].Name != "bedroom0" {
		t.Fatalf("rooms = %+v", sc.Rooms)
	}
	if math.Hypot(sc.Rooms[0].Center[0]-2.0, sc.Rooms[0].Center[1]-1.5) > 1e-4 ||
		math.Hypot(sc.Rooms[1].Center[0]-5.5, sc.Rooms[1].Center[1]-1.5) > 1e-4 {
		t.Errorf("room centres = %+v, want (2, 1.5) and (5.5, 1.5)", sc.Rooms)
	}

	// Lowest wall bottom becomes 0, and Window1 bottom (1.4 - 0.6 - 0 = 0.8) shifts accordingly.
	for _, w := range sc.Walls {
		if math.Abs(w.Bottom) > 1e-4 || math.Abs(w.Top-2.6) > 1e-4 {
			t.Errorf("wall %s heights = [%g, %g], want [0, 2.6]", w.Name, w.Bottom, w.Top)
		}
	}
	if math.Abs(sc.Windows[0].Bottom-0.8) > 1e-4 || math.Abs(sc.Windows[0].Top-2.0) > 1e-4 {
		t.Errorf("window heights = [%g, %g], want [0.8, 2.0]", sc.Windows[0].Bottom, sc.Windows[0].Top)
	}

	doorOpen := map[string]bool{}
	for _, d := range sc.Doors {
		doorOpen[d.Name] = d.Open
	}
	if doorOpen["Door1"] || !doorOpen["Door2"] {
		t.Errorf("door open states = %+v, want Door1=false Door2=true", doorOpen)
	}

	cats := map[string]string{}
	for _, ob := range sc.Objects {
		cats[ob.Name] = ob.Category
	}
	for name, want := range map[string]string{
		"Sofa1": "sofa", "Bed1": "bed", "Table1": "table", "Chair1": "chair", "Chair2": "chair",
		"Television1": "television", "Storage1": "storage", "Mystery1": "storage",
	} {
		if cats[name] != want {
			t.Errorf("%s category = %q, want %q", name, cats[name], want)
		}
	}
}

func TestReadRoomPlanTransformConvention(t *testing.T) {
	// Row-vector USD transform: world = (x, y, z, 1) · M, plan = (world x, -world z), yaw CCW from +x.
	raw := zipStored(t, map[string]string{
		"assets/Mesh/Wall/WallRot.usda": `#usda 1.0
(
    defaultPrim = "WallRot"
    metersPerUnit = 1
    upAxis = "Y"
)

def Xform "WallRot" (
    customData = {
        string Category = "Wall"
    }
    kind = "component"
)
{
    def Mesh "WallRot"
    {
        point3f[] points = [(-0.74, -1.43, 0), (0.74, -1.43, 0), (0.74, 1.43, 0), (-0.74, 1.43, 0)]
        matrix4d xformOp:transform = ( (-0.62, 0, -0.78, 0), (0, 1, 0, 0), (0.78, 0, -0.62, 0), (-6.02, -0.02, -0.10, 1) )
        uniform token[] xformOpOrder = ["xformOp:transform"]
    }
}
`,
	})
	sc, err := readRoomPlan(raw)
	if err != nil {
		t.Fatalf("readRoomPlan: %v", err)
	}
	if len(sc.Walls) != 1 {
		t.Fatalf("walls = %d, want 1", len(sc.Walls))
	}
	w := sc.Walls[0]
	if math.Abs(w.Center[0]-(-6.02)) > 1e-4 || math.Abs(w.Center[1]-0.10) > 1e-4 {
		t.Errorf("center = %v, want [-6.02, 0.10]", w.Center)
	}
	wantYaw := math.Atan2(0.78, -0.62) * 180 / math.Pi
	if math.Abs(w.Yaw-wantYaw) > 1e-3 {
		t.Errorf("yaw = %g, want %g", w.Yaw, wantYaw)
	}
	wantLen := 1.48 * math.Hypot(-0.62, -0.78)
	if math.Abs(w.Size[0]-wantLen) > 1e-3 || w.Size[1] < 0.10 {
		t.Errorf("size = %v, want [~%.3f, >=0.10]", w.Size, wantLen)
	}
	if math.Abs(w.Bottom) > 1e-6 || math.Abs(w.Top-2.86) > 1e-4 {
		t.Errorf("heights = [%g, %g], want [0, 2.86]", w.Bottom, w.Top)
	}
}

func TestReadRoomPlanRefusals(t *testing.T) {
	// 1. Binary USD magic (raw and inside zip, and .usdc extension).
	for name, raw := range map[string][]byte{
		"raw-usdc":   []byte("PXR-USDC\x00\x01\x02"),
		"zip-magic":  zipStored(t, map[string]string{"Room.usda": "PXR-USDC\x00\x00"}),
		"zip-ext":    zipStored(t, map[string]string{"Room.usdc": "anything"}),
		"asset-usdc": zipStored(t, map[string]string{"assets/Mesh/Wall/W.usda": "PXR-USDCbinary"}),
	} {
		_, err := readRoomPlan(raw)
		if err == nil || !strings.Contains(strings.ToLower(err.Error()), "ascii") {
			t.Errorf("%s: got %v, want error asking for ASCII USDZ", name, err)
		}
	}

	// 2. Oversized zip (> 20 MB), > 500 entries, and > 2 MB .usda entry.
	if _, err := readRoomPlan(make([]byte, usdzBytesMost+1)); err == nil {
		t.Error("oversized zip (> 20 MB) was accepted")
	}
	many := map[string]string{}
	for i := range usdzEntriesMost + 1 {
		many[fmt.Sprintf("f%d.txt", i)] = "x"
	}
	if _, err := readRoomPlan(zipStored(t, many)); err == nil {
		t.Error("zip with > 500 entries was accepted")
	}
	bigUSDA := "#usda 1.0\n" + strings.Repeat("# padding comment line\n", usdaBytesMost/20+100)
	if _, err := readRoomPlan(zipStored(t, map[string]string{"Big.usda": bigUSDA})); err == nil {
		t.Error("usda over 2 MB was accepted")
	}

	// 3. Non-finite numbers (NaN, inf, -inf, overflow).
	for _, bad := range []string{"nan", "inf", "-inf", "1e500"} {
		pointUSDA := fmt.Sprintf(`#usda 1.0
def Xform "W" ( customData = { string Category = "Wall" } ) {
    def Mesh "W" {
        point3f[] points = [(%s, 0, 0), (1, 1, 0), (-1, 1, 0)]
        matrix4d xformOp:transform = ( (1,0,0,0), (0,1,0,0), (0,0,1,0), (0,0,0,1) )
    }
}`, bad)
		if _, err := readRoomPlan(zipStored(t, map[string]string{"W.usda": pointUSDA})); err == nil {
			t.Errorf("non-finite point %q was accepted", bad)
		}

		matUSDA := fmt.Sprintf(`#usda 1.0
def Xform "W" ( customData = { string Category = "Wall" } ) {
    def Mesh "W" {
        point3f[] points = [(-1, 0, 0), (1, 1, 0), (-1, 1, 0)]
        matrix4d xformOp:transform = ( (1,0,0,0), (0,1,0,0), (0,0,1,0), (%s,0,0,1) )
    }
}`, bad)
		if _, err := readRoomPlan(zipStored(t, map[string]string{"W.usda": matUSDA})); err == nil {
			t.Errorf("non-finite matrix %q was accepted", bad)
		}
	}
}
