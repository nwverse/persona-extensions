package main

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/nwverse/persona/sdk/go/wire"
)

// Details for the 3D home view (#943): frames for doors and windows, an open door's leaf, curtains, and furniture kept
// out of the walls. The furniture recipes themselves are in objectShapes.

const (
	frameColor   = "#7C5030"
	windowFrame  = "#D8D8DC"
	curtainColor = "#C8C2B6"
	goldColor    = "#CEAA54"
	curtainSpan  = 0.55 // how far a gathered curtain reaches along the wall at each side of an opening
	curtainFold  = 0.1
	curtainGap   = 0.17 // what furniture keeps clear of a wall with curtains
)

// shade lightens (delta > 0) or darkens a #RRGGBB color by delta on each channel.
func shade(hex string, delta int) string {
	if len(hex) != 7 || hex[0] != '#' {
		return hex
	}
	v, err := strconv.ParseUint(hex[1:], 16, 32)
	if err != nil {
		return hex
	}
	ch := func(sh uint) int { return max(0, min(255, int(v>>sh&0xff)+delta)) }
	return fmt.Sprintf("#%02X%02X%02X", ch(16), ch(8), ch(0))
}

// wallFrame is a wall's direction (u, from A to B), its normal (n) and length.
func wallFrame(w planWall) (ux, uy, nx, ny, length float64) {
	length = dist2D(w.A, w.B)
	if length < 1e-9 {
		return 1, 0, 0, 1, 0
	}
	ux, uy = (w.B[0]-w.A[0])/length, (w.B[1]-w.A[1])/length
	return ux, uy, -uy, ux, length
}

// footprintCorners are an object's four corners on the plan.
func footprintCorners(center, size [2]float64, yaw float64) [4][2]float64 {
	rad := yaw * math.Pi / 180
	cs, sn := math.Cos(rad), math.Sin(rad)
	var out [4][2]float64
	for i, s := range [4][2]float64{{-1, -1}, {1, -1}, {1, 1}, {-1, 1}} {
		lx, ly := s[0]*size[0]/2, s[1]*size[1]/2
		out[i] = [2]float64{center[0] + lx*cs - ly*sn, center[1] + lx*sn + ly*cs}
	}
	return out
}

// inFootprint says whether pt is on an object's footprint.
func inFootprint(pt [2]float64, ob scanObject) bool {
	rad := ob.Yaw * math.Pi / 180
	cs, sn := math.Cos(rad), math.Sin(rad)
	dx, dy := pt[0]-ob.Center[0], pt[1]-ob.Center[1]
	lx, ly := dx*cs+dy*sn, -dx*sn+dy*cs
	return math.Abs(lx) <= ob.Size[0]/2 && math.Abs(ly) <= ob.Size[1]/2
}

// standsFree: kinds that stand in the room rather than against a wall, so they are never moved off one.
func standsFree(kind string) bool {
	switch kind {
	case "chair", "table", "bar_table", "coffee_table", "round_coffee_table", "bench", "television":
		return true
	}
	return false
}

// wallBase is a wall's name without the "#2" a split gives its later pieces.
func wallBase(name string) string {
	if i := strings.IndexByte(name, '#'); i >= 0 {
		return name[:i]
	}
	return name
}

// keepOffWalls moves an object scanned a little into a wall back out to the wall's face, and clear of the curtains on
// a wall that has them.
func keepOffWalls(ob *scanObject, walls []planWall, curtained map[string]bool) {
	for _, w := range walls {
		ux, uy, nx, ny, L := wallFrame(w)
		if L < 0.05 {
			continue
		}
		corners := footprintCorners(ob.Center, ob.Size, ob.Yaw)
		lo, hi := math.Inf(1), math.Inf(-1)
		across := 0.0
		for _, c := range corners {
			along := (c[0]-w.A[0])*ux + (c[1]-w.A[1])*uy
			lo, hi = min(lo, along), max(hi, along)
			across = max(across, math.Abs((c[0]-ob.Center[0])*nx+(c[1]-ob.Center[1])*ny))
		}
		if hi < 0.05 || lo > L-0.05 {
			continue // beside the wall, not in front of it
		}
		s := (ob.Center[0]-w.A[0])*nx + (ob.Center[1]-w.A[1])*ny
		side := 1.0
		if s < 0 {
			side = -1
		}
		gap := math.Abs(s) - across
		need := w.Thickness/2 + 0.01
		if curtained[wallBase(w.Name)] {
			need += curtainGap
		}
		if gap > -0.6 && gap < need {
			ob.Center[0] += side * nx * (need - gap)
			ob.Center[1] += side * ny * (need - gap)
		}
	}
}

// roomAt is the corrected room that holds pt, or nil.
func roomAt(rooms []planRoom, pt [2]float64) *planRoom {
	for i := range rooms {
		if pointInPolygon(pt, rooms[i].Polygon) {
			return &rooms[i]
		}
	}
	return nil
}

func polygonArea(poly [][2]float64) float64 {
	a := 0.0
	for i := range poly {
		j := (i + 1) % len(poly)
		a += poly[i][0]*poly[j][1] - poly[j][0]*poly[i][1]
	}
	return math.Abs(a) / 2
}

// openLeafSides picks where an open door's leaf goes: into the smaller of the two rooms the door joins (sgn, along the
// wall's normal), hinged at the end (side, along the wall) whose swung leaf misses the furniture.
func openLeafSides(center [2]float64, width, ux, uy, nx, ny float64, rooms []planRoom, objs []scanObject) (sgn, side float64) {
	sgn = 1
	front := roomAt(rooms, [2]float64{center[0] + nx*0.5, center[1] + ny*0.5})
	back := roomAt(rooms, [2]float64{center[0] - nx*0.5, center[1] - ny*0.5})
	switch {
	case front == nil && back != nil:
		sgn = -1
	case front != nil && back != nil && polygonArea(back.Polygon) < polygonArea(front.Polygon):
		sgn = -1
	}
	hits := func(sd float64) int {
		n := 0
		hx, hy := center[0]+ux*sd*(width/2-0.03), center[1]+uy*sd*(width/2-0.03)
		for k := 1; k <= 12; k++ {
			t := width * float64(k) / 12
			pt := [2]float64{hx + nx*sgn*t, hy + ny*sgn*t}
			for _, ob := range objs {
				if inFootprint(pt, ob) {
					n++
					break
				}
			}
		}
		return n
	}
	side = 1
	if hits(-1) < hits(1) {
		side = -1
	}
	return sgn, side
}

// doorShapes draws a hinged door in its wall: a frame, and the leaf, closed with handles on both faces or swung open.
func doorShapes(center [2]float64, width, thickness, yaw, top, wallH float64, open bool, ux, uy, nx, ny float64,
	rooms []planRoom, objs []scanObject) []wire.SceneShape {
	dt := min(top, wallH)
	if dt < 0.1 {
		dt = wallH
	}
	at := func(along, across float64) [2]float64 {
		return [2]float64{center[0] + ux*along + nx*across, center[1] + uy*along + ny*across}
	}
	fd := max(thickness, 0.04) + 0.04
	var out []wire.SceneShape
	for _, s := range []float64{-1, 1} {
		out = append(out, sceneBox(at(s*(width/2+0.025), 0), [2]float64{0.05, fd}, yaw, 0.005, 0, dt, frameColor, wire.SceneMaterialWood))
	}
	if dt < wallH-0.05 {
		out = append(out, sceneBox(at(0, 0), [2]float64{width + 0.1, fd}, yaw, 0.005, dt, dt+0.05, frameColor, wire.SceneMaterialWood))
	}
	if !open {
		out = append(out, sceneBox(at(0, 0), [2]float64{width, 0.04}, yaw, 0.005, 0, dt, "#8B6B4E", wire.SceneMaterialWood))
		if dt > 1.05 {
			for _, s := range []float64{-1, 1} {
				out = append(out, sceneBox(at(width/2-0.09, s*0.035), [2]float64{0.1, 0.02}, yaw, 0.004, 0.98, 1.02, "#BEBEC2", wire.SceneMaterialMetal))
			}
		}
		return out
	}
	sgn, side := openLeafSides(center, width, ux, uy, nx, ny, rooms, objs)
	leaf := at(side*(width/2-0.02), sgn*width/2)
	out = append(out, sceneBox(leaf, [2]float64{0.04, width}, yaw, 0.005, 0, dt, "#8B6B4E", wire.SceneMaterialWood))
	return out
}

// windowFrameShapes draws a window's frame: a sill, jambs and mullions about every 60 cm.
func windowFrameShapes(center [2]float64, width, thickness, yaw, bottom, wallH, ux, uy float64) []wire.SceneShape {
	at := func(along float64) [2]float64 { return [2]float64{center[0] + ux*along, center[1] + uy*along} }
	fd := max(thickness, 0.04) + 0.02
	var out []wire.SceneShape
	if bottom > 0.05 {
		out = append(out, sceneBox(at(0), [2]float64{width + 0.06, fd + 0.02}, yaw, 0.005, bottom-0.03, bottom, windowFrame, wire.SceneMaterialMetal))
	}
	for _, s := range []float64{-1, 1} {
		out = append(out, sceneBox(at(s*(width/2-0.02)), [2]float64{0.04, fd}, yaw, 0.004, bottom, wallH, windowFrame, wire.SceneMaterialMetal))
	}
	panes := max(1, int(math.Round(width/0.6)))
	for k := 1; k < panes; k++ {
		out = append(out, sceneBox(at(-width/2+width*float64(k)/float64(panes)), [2]float64{0.03, fd}, yaw, 0.004, bottom, wallH, windowFrame, wire.SceneMaterialMetal))
	}
	return out
}

// curtainShapes hangs gathered curtains at both ends of an opening (center, width) in a wall, on the side sgn of its
// normal: folds standing just inside the wall, and a rail.
func curtainShapes(center [2]float64, width, thickness, yaw, top, ux, uy, nx, ny, sgn float64, color string) []wire.SceneShape {
	at := func(along, across float64) [2]float64 {
		return [2]float64{center[0] + ux*along + nx*sgn*across, center[1] + uy*along + ny*sgn*across}
	}
	ct := top - 0.03
	var out []wire.SceneShape
	for _, s := range []float64{-1, 1} {
		from := s * (width/2 + 0.1)
		n := int(math.Ceil(curtainSpan / curtainFold))
		for k := range n {
			along := from - s*curtainFold*(float64(k)+0.5)
			off := thickness/2 + 0.06
			col := shade(color, -6)
			if k%2 == 1 {
				off += 0.03
				col = shade(color, 6)
			}
			out = append(out, sceneBox(at(along, off), [2]float64{curtainFold + 0.004, 0.035}, yaw, 0.01, 0.02, ct, col, wire.SceneMaterialMatte))
		}
	}
	out = append(out, sceneBox(at(0, thickness/2+0.09), [2]float64{width + 0.26, 0.08}, yaw, 0.01, ct, top, "#C8C8CC", wire.SceneMaterialMetal))
	return out
}

// curtainSide is the side of a wall (+1 or -1 along its normal) where curtains hang: the named room's, else the side
// that has a room.
func curtainSide(center [2]float64, nx, ny float64, rooms []planRoom, room string) float64 {
	front := roomAt(rooms, [2]float64{center[0] + nx*0.4, center[1] + ny*0.4})
	back := roomAt(rooms, [2]float64{center[0] - nx*0.4, center[1] - ny*0.4})
	if room != "" {
		if back != nil && back.Name == room {
			return -1
		}
		return 1
	}
	if front == nil && back != nil {
		return -1
	}
	return 1
}
