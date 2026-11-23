package main

import (
	"math"
	"slices"

	"github.com/nwverse/persona/sdk/go/wire"
)

// The cutaway (#947, nwverse/persona#13036): the app lowers the outer walls marked `wall` that face its camera as the
// model turns. The poster has one camera, so it takes the same cut here before it is drawn: the walls in the camera's
// half that face it with no floor just past their camera-side face (outer walls; a wall between rooms, with the TV on
// it, keeps its height) come down to cutawayHeight, what stands in one is cut to that height or left out when it starts above
// it, and what rises cutawaySeeThroughAbove over it within cutawayReach of one, on the room's side, is drawn
// see-through. The numbers are the app's (Theme.Scene.cutaway*).

const (
	cutawayHeight           = 0.6
	cutawayFacing           = 0.35
	cutawayProbe            = 0.3
	cutawayFloorTop         = 0.1
	cutawayBand             = 0.16
	cutawaySeeThroughAbove  = 0.6
	cutawayReach            = 0.9
	airConditionerBottom    = 2.05
	airConditionerTop       = 2.33
	airConditionerWallReach = 0.8
	airConditionerIcon      = "snow"
)

type cutWall struct {
	index                     int
	center, along, across     [2]float64
	halfLength, halfThickness float64
	bottom, top               float64
	// a floor lies just past its face on the across side, and on the other side
	floorAcross, floorOpposite bool
}

func dot2(a, b [2]float64) float64 { return a[0]*b[0] + a[1]*b[1] }

// cutawayWalls are the boxes marked wall, running along their longer side.
func cutawayWalls(shapes []wire.SceneShape) []cutWall {
	floors := cutawayFloors(shapes)
	onFloor := func(p [2]float64) bool {
		for _, f := range floors {
			if pointInPolygon(p, f) {
				return true
			}
		}
		return false
	}
	var out []cutWall
	for i, s := range shapes {
		if s.Kind != wire.SceneShapeBox || !s.Wall || len(s.Center) < 2 || len(s.Size) < 2 || s.Bottom == nil || s.Top == nil {
			continue
		}
		yaw := 0.0
		if s.Yaw != nil {
			yaw = *s.Yaw * math.Pi / 180
		}
		x := [2]float64{math.Cos(yaw), math.Sin(yaw)}
		y := [2]float64{-math.Sin(yaw), math.Cos(yaw)}
		along, across := x, y
		if s.Size[0] < s.Size[1] {
			along, across = y, x
		}
		center := [2]float64{s.Center[0], s.Center[1]}
		reach := min(s.Size[0], s.Size[1])/2 + cutawayProbe
		out = append(out, cutWall{
			index: i, center: center, along: along, across: across,
			halfLength: max(s.Size[0], s.Size[1]) / 2, halfThickness: min(s.Size[0], s.Size[1]) / 2,
			bottom: min(*s.Bottom, *s.Top), top: max(*s.Bottom, *s.Top),
			floorAcross:   onFloor([2]float64{center[0] + across[0]*reach, center[1] + across[1]*reach}),
			floorOpposite: onFloor([2]float64{center[0] - across[0]*reach, center[1] - across[1]*reach}),
		})
	}
	return out
}

// cutawayFloors are the prisms low enough to be floors.
func cutawayFloors(shapes []wire.SceneShape) [][][2]float64 {
	var out [][][2]float64
	for _, s := range shapes {
		if s.Kind != wire.SceneShapePrism || s.Bottom == nil || s.Top == nil || max(*s.Bottom, *s.Top) > cutawayFloorTop {
			continue
		}
		var poly [][2]float64
		for _, p := range s.Points {
			if len(p) >= 2 {
				poly = append(poly, [2]float64{p[0], p[1]})
			}
		}
		if len(poly) >= 3 {
			out = append(out, poly)
		}
	}
	return out
}

// planMiddle is the middle of the shapes' extent on the plan.
func planMiddle(shapes []wire.SceneShape) [2]float64 {
	minX, minY, maxX, maxY := math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)
	for _, s := range shapes {
		for _, p := range shapeBoundsPoints(s) {
			minX, maxX = min(minX, p.x), max(maxX, p.x)
			minY, maxY = min(minY, p.y), max(maxY, p.y)
		}
	}
	if math.IsInf(minX, 0) {
		return [2]float64{}
	}
	return [2]float64{(minX + maxX) / 2, (minY + maxY) / 2}
}

// cutawayLowered are the walls (shape indices, ascending) in the camera's half of the model that face a camera at yaw,
// which looks from (cos yaw, sin yaw) on the plan, with no floor on its side.
func cutawayLowered(walls []cutWall, middle [2]float64, yaw float64) []int {
	r := yaw * math.Pi / 180
	cam := [2]float64{math.Cos(r), math.Sin(r)}
	var out []int
	for _, w := range walls {
		rel := [2]float64{w.center[0] - middle[0], w.center[1] - middle[1]}
		facing := dot2(w.across, cam)
		floor := w.floorOpposite
		if facing > 0 {
			floor = w.floorAcross
		}
		if dot2(rel, cam) > 0 && math.Abs(facing) >= cutawayFacing && !floor {
			out = append(out, w.index)
		}
	}
	slices.Sort(out)
	return out
}

type cutFootprint struct {
	center       [2]float64
	bottom, top  float64
	halfX, halfY float64
}

// shapeFootprint is a box's or a cylinder's middle, heights and extent on the plan; false for a prism (a floor).
func shapeFootprint(s wire.SceneShape) (cutFootprint, bool) {
	if s.Bottom == nil || s.Top == nil || len(s.Center) < 2 {
		return cutFootprint{}, false
	}
	f := cutFootprint{center: [2]float64{s.Center[0], s.Center[1]}, bottom: min(*s.Bottom, *s.Top), top: max(*s.Bottom, *s.Top)}
	switch s.Kind {
	case wire.SceneShapeBox:
		if len(s.Size) < 2 {
			return cutFootprint{}, false
		}
		yaw := 0.0
		if s.Yaw != nil {
			yaw = *s.Yaw * math.Pi / 180
		}
		c, sn := math.Abs(math.Cos(yaw)), math.Abs(math.Sin(yaw))
		f.halfX, f.halfY = (s.Size[0]*c+s.Size[1]*sn)/2, (s.Size[0]*sn+s.Size[1]*c)/2
	case wire.SceneShapeCylinder:
		f.halfX, f.halfY = s.Radius, s.Radius
	default:
		return cutFootprint{}, false
	}
	return f, true
}

// sceneCutaway is the scene's shapes as an app draws them with a camera at yaw: lowered walls and what stands in them
// cut (or left out), and what stands tall in front of one see-through (glass). The shapes passed are not changed.
func sceneCutaway(shapes []wire.SceneShape, yaw float64) []wire.SceneShape {
	walls := cutawayWalls(shapes)
	lowered := cutawayLowered(walls, planMiddle(shapes), yaw)
	if len(lowered) == 0 {
		return shapes
	}
	r := yaw * math.Pi / 180
	cam := [2]float64{math.Cos(r), math.Sin(r)}
	var low []cutWall
	for _, w := range walls {
		if slices.Contains(lowered, w.index) {
			low = append(low, w)
		}
	}
	cutTop := func(w cutWall) float64 { return min(w.top, w.bottom+cutawayHeight) }
	clip := func(s wire.SceneShape, top float64) wire.SceneShape {
		b, t := *s.Bottom, *s.Top
		if t >= b {
			t = max(b+0.006, min(t, top))
		} else {
			b = max(t+0.006, min(b, top))
		}
		s.Bottom, s.Top = &b, &t
		return s
	}
	out := make([]wire.SceneShape, 0, len(shapes))
	for i, s := range shapes {
		if slices.Contains(lowered, i) {
			for _, w := range low {
				if w.index == i {
					out = append(out, clip(s, cutTop(w)))
				}
			}
			continue
		}
		f, ok := shapeFootprint(s)
		if !ok || s.Wall {
			out = append(out, s)
			continue
		}
		kept, done := s, false
		for _, w := range low {
			rel := [2]float64{f.center[0] - w.center[0], f.center[1] - w.center[1]}
			along := math.Abs(dot2(rel, w.along))
			across := dot2(rel, w.across)
			cut := cutTop(w)
			if along <= w.halfLength+cutawayBand && math.Abs(across) <= w.halfThickness+cutawayBand {
				switch {
				case f.bottom >= cut:
					done = true // left out
				case f.top > cut:
					kept = clip(s, cut)
				}
				break
			}
			roomSide := 1.0
			if dot2(w.across, cam) > 0 {
				roomSide = -1
			}
			depth := across * roomSide
			reach := math.Abs(w.across[0])*f.halfX + math.Abs(w.across[1])*f.halfY
			if f.top > cut+cutawaySeeThroughAbove && along <= w.halfLength && depth > 0 &&
				depth-reach <= w.halfThickness+cutawayReach {
				kept.Material = wire.SceneMaterialGlass
				break
			}
		}
		if !done {
			out = append(out, kept)
		}
	}
	return out
}

// airConditionerShapes hangs an air conditioner on the wall nearest each pinned one (a pin whose name says 空调, so its
// icon is snow): a white body high on the wall, its outlet and its status light. Only on a wall tall enough to hold it.
func airConditionerShapes(pins []wire.ScenePin, walls []planWall, wallH float64) []wire.SceneShape {
	if wallH < airConditionerTop {
		return nil
	}
	var out []wire.SceneShape
	for _, p := range pins {
		if p.Icon != airConditionerIcon || len(p.At) < 2 {
			continue
		}
		pt := [2]float64{p.At[0], p.At[1]}
		best, bestDist := -1, airConditionerWallReach
		for i, w := range walls {
			if d := dist2D(pt, closestPointOnSegment(pt, w.A, w.B)); d < bestDist {
				best, bestDist = i, d
			}
		}
		if best < 0 {
			continue
		}
		w := walls[best]
		ux, uy, nx, ny, L := wallFrame(w)
		if L < 1.0 {
			continue
		}
		s := (pt[0]-w.A[0])*ux + (pt[1]-w.A[1])*uy
		s = min(max(s, 0.5), L-0.5)
		side := 1.0
		if (pt[0]-w.A[0])*nx+(pt[1]-w.A[1])*ny < 0 {
			side = -1
		}
		at := func(along, off float64) [2]float64 {
			return [2]float64{w.A[0] + ux*along + nx*side*off, w.A[1] + uy*along + ny*side*off}
		}
		yaw := math.Atan2(uy, ux) * 180 / math.Pi
		face := w.Thickness / 2
		out = append(out,
			sceneBox(at(s, face+0.11), [2]float64{0.9, 0.22}, yaw, 0.04, airConditionerBottom, airConditionerTop, "#F4F4F2", wire.SceneMaterialMatte),
			sceneBox(at(s, face+0.215), [2]float64{0.8, 0.02}, yaw, 0.005, airConditionerBottom+0.02, airConditionerBottom+0.05, "#464A50", wire.SceneMaterialMatte),
			sceneBox(at(s+0.34, face+0.222), [2]float64{0.06, 0.01}, yaw, 0.002, airConditionerTop-0.06, airConditionerTop-0.04, "#78C878", wire.SceneMaterialScreen),
		)
	}
	return out
}
