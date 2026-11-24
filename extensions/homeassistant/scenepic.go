package main

import (
	"bytes"
	"image"
	"image/png"
	"math"
	"sort"
	"strconv"

	"github.com/nwverse/persona/sdk/go/wire"
)

// Pure-Go orthographic dollhouse renderer (#936): draws a wire.Component{Type: "scene"} into a 4:3 PNG for ui.image
// (the scene's Poster on UI version 11, and the standalone image on UI version 6–10).

const (
	posterDefaultWidth  = 960
	posterDefaultHeight = 720
	posterMinWidth      = 320
	posterMaxWidth      = 1600
	subdivideStep       = 0.35 // metres per floor/wall quad subdivision so radial lamp light pools smoothly
)

func clampPosterWidth(width int) int {
	if width <= 0 {
		return posterDefaultWidth
	}
	return min(max(width, posterMinWidth), posterMaxWidth)
}

type vec3 struct{ x, y, z float64 }

func (a vec3) add(b vec3) vec3      { return vec3{a.x + b.x, a.y + b.y, a.z + b.z} }
func (a vec3) sub(b vec3) vec3      { return vec3{a.x - b.x, a.y - b.y, a.z - b.z} }
func (a vec3) scale(s float64) vec3 { return vec3{a.x * s, a.y * s, a.z * s} }
func (a vec3) dot(b vec3) float64   { return a.x*b.x + a.y*b.y + a.z*b.z }

func (a vec3) norm() vec3 {
	l := math.Sqrt(a.x*a.x + a.y*a.y + a.z*a.z)
	if l < 1e-12 {
		return vec3{0, 0, 1}
	}
	return vec3{a.x / l, a.y / l, a.z / l}
}

type sceneProjector struct {
	width, height int
	right, up     vec3
	fwd           vec3 // unit vector from camera into the scene (depth increases along fwd)
	uMid, vMid    float64
	scale         float64
}

func newSceneProjector(comp wire.Component, width, height int) sceneProjector {
	yaw, pitch, zoom := 225.0, 35.0, 1.0
	if c := comp.Camera; c != nil {
		if c.Yaw != nil && isFinite(*c.Yaw) {
			yaw = *c.Yaw
		}
		if c.Pitch != nil && isFinite(*c.Pitch) {
			pitch = *c.Pitch
		}
		if c.Zoom != nil && isFinite(*c.Zoom) && *c.Zoom > 0 {
			zoom = *c.Zoom
		}
	}
	pitch = min(max(pitch, 5.0), 89.5)
	zoom = min(max(zoom, 0.25), 4.0)

	yr := yaw * math.Pi / 180
	pr := pitch * math.Pi / 180
	cy, sy := math.Cos(yr), math.Sin(yr)
	cp, sp := math.Cos(pr), math.Sin(pr)

	// Camera sits along (cp*cy, cp*sy, sp) from the centre, looking inward along fwd.
	fwd := vec3{-cp * cy, -cp * sy, -sp}
	right := vec3{-sy, cy, 0}
	up := vec3{-sp * cy, -sp * sy, cp}

	uMin, uMax := math.Inf(1), math.Inf(-1)
	vMin, vMax := math.Inf(1), math.Inf(-1)
	extend := func(p vec3) {
		u := p.dot(right)
		v := p.dot(up)
		uMin, uMax = min(uMin, u), max(uMax, u)
		vMin, vMax = min(vMin, v), max(vMax, v)
	}
	for _, s := range comp.Shapes {
		for _, pt := range shapeBoundsPoints(s) {
			extend(pt)
		}
	}
	for _, pin := range comp.Pins {
		if len(pin.At) < 2 || !isFinite(pin.At[0]) || !isFinite(pin.At[1]) {
			continue
		}
		z := 0.0
		if pin.Height != nil && isFinite(*pin.Height) {
			z = *pin.Height
		}
		extend(vec3{pin.At[0], pin.At[1], z + 0.15})
	}
	if !isFinite(uMin) || !isFinite(uMax) {
		uMin, uMax, vMin, vMax = -2, 2, -1.5, 1.5
	}
	uSpan := max(uMax-uMin, 0.5)
	vSpan := max(vMax-vMin, 0.5)
	scale := min(float64(width)*0.86/uSpan, float64(height)*0.84/vSpan) * zoom

	return sceneProjector{
		width:  width,
		height: height,
		right:  right,
		up:     up,
		fwd:    fwd,
		uMid:   (uMin + uMax) / 2,
		vMid:   (vMin + vMax) / 2,
		scale:  scale,
	}
}

func (p sceneProjector) project(x, y, z float64) (sx, sy, depth float64) {
	v := vec3{x, y, z}
	u := v.dot(p.right)
	w := v.dot(p.up)
	sx = float64(p.width)*0.5 + (u-p.uMid)*p.scale
	sy = float64(p.height)*0.52 - (w-p.vMid)*p.scale
	depth = v.dot(p.fwd)
	return sx, sy, depth
}

func shapeBoundsPoints(s wire.SceneShape) []vec3 {
	if s.Bottom == nil || s.Top == nil {
		return nil
	}
	b, t := *s.Bottom, *s.Top
	switch s.Kind {
	case wire.SceneShapeBox:
		if len(s.Center) < 2 || len(s.Size) < 2 {
			return nil
		}
		yaw := 0.0
		if s.Yaw != nil {
			yaw = *s.Yaw
		}
		rad := yaw * math.Pi / 180
		cs, sn := math.Cos(rad), math.Sin(rad)
		hx, hy := s.Size[0]/2, s.Size[1]/2
		var out []vec3
		for _, sx := range []float64{-hx, hx} {
			for _, sy := range []float64{-hy, hy} {
				wx := s.Center[0] + sx*cs - sy*sn
				wy := s.Center[1] + sx*sn + sy*cs
				out = append(out, vec3{wx, wy, b}, vec3{wx, wy, t})
			}
		}
		return out
	case wire.SceneShapeCylinder:
		if len(s.Center) < 2 {
			return nil
		}
		cx, cy, r := s.Center[0], s.Center[1], s.Radius
		return []vec3{
			{cx - r, cy - r, b}, {cx + r, cy - r, b}, {cx + r, cy + r, b}, {cx - r, cy + r, b},
			{cx - r, cy - r, t}, {cx + r, cy - r, t}, {cx + r, cy + r, t}, {cx - r, cy + r, t},
		}
	case wire.SceneShapePrism:
		out := make([]vec3, 0, len(s.Points)*2)
		for _, pt := range s.Points {
			if len(pt) >= 2 {
				out = append(out, vec3{pt[0], pt[1], b}, vec3{pt[0], pt[1], t})
			}
		}
		return out
	}
	return nil
}

type pointLight struct {
	pos   vec3
	color [3]float64
}

type sceneTri struct {
	v       [3]vec3
	n       vec3
	baseRGB [3]float64
	mat     string
	zBias   float64
	depth   float64
}

type sceneEdge struct {
	a, b vec3
	rgb  [3]float64
}

func renderScenePNG(comp wire.Component, width int) ([]byte, error) {
	w := clampPosterWidth(width)
	h := w * 3 / 4
	proj := newSceneProjector(comp, w, h)
	// framed on the whole model, drawn as the app draws it from this camera: the walls facing it cut away (#947)
	yaw := 225.0
	if comp.Camera != nil && comp.Camera.Yaw != nil && isFinite(*comp.Camera.Yaw) {
		yaw = *comp.Camera.Yaw
	}
	comp.Shapes = sceneCutaway(comp.Shapes, yaw)

	img := image.NewRGBA(image.Rect(0, 0, w, h))
	zbuf := make([]float32, w*h)

	// Architectural warm-paper vertical gradient (#F6F4EF at top to #EAE6DD at bottom).
	for y := range h {
		t := float64(y) / float64(max(h-1, 1))
		r := uint8(math.Round(246*(1-t) + 234*t))
		g := uint8(math.Round(244*(1-t) + 230*t))
		b := uint8(math.Round(239*(1-t) + 221*t))
		row := y * img.Stride
		for x := range w {
			off := row + x*4
			img.Pix[off] = r
			img.Pix[off+1] = g
			img.Pix[off+2] = b
			img.Pix[off+3] = 255
			zbuf[y*w+x] = math.MaxFloat32
		}
	}

	var lights []pointLight
	for _, pin := range comp.Pins {
		if pin.Light == nil || !pin.Light.On || len(pin.At) < 2 {
			continue
		}
		lz := 2.2
		if pin.Height != nil && isFinite(*pin.Height) {
			lz = *pin.Height
		}
		col := [3]float64{1.0, 0.851, 0.608} // #FFD99B warm lamp glow
		if pin.Light.Warmth == wire.SceneWarmthCool {
			col = [3]float64{0.863, 0.933, 1.0} // #DCEEFF cool white
		}
		lights = append(lights, pointLight{pos: vec3{pin.At[0], pin.At[1], lz}, color: col})
	}

	viewToCam := proj.fwd.scale(-1)
	var opaque, glass []sceneTri
	var edges []sceneEdge

	for _, s := range comp.Shapes {
		if s.Bottom == nil || s.Top == nil {
			continue
		}
		isGlass := s.Material == wire.SceneMaterialGlass
		tris, es := tessellateShape(s, viewToCam, isGlass)
		if isGlass {
			for i := range tris {
				c := tris[i].v[0].add(tris[i].v[1]).add(tris[i].v[2]).scale(1.0 / 3.0)
				tris[i].depth = c.dot(proj.fwd)
			}
			glass = append(glass, tris...)
		} else {
			opaque = append(opaque, tris...)
			edges = append(edges, es...)
		}
	}

	// Pass 1: opaque geometry with Gouraud-shaded directional + radial pin lighting.
	for _, tri := range opaque {
		var sx, sy, sz [3]float64
		var vc [3][3]float64
		for k := range 3 {
			sx[k], sy[k], sz[k] = proj.project(tri.v[k].x, tri.v[k].y, tri.v[k].z)
			sz[k] += tri.zBias
			vc[k] = shadeVertex(tri.v[k], tri.n, tri.baseRGB, tri.mat, lights)
		}
		rasterizeTri(img, zbuf, w, h, sx, sy, sz, vc, 1.0, true)
	}

	// Subtle top-crease outlines so adjacent walls, doors and furniture parts read crisply from above.
	for _, e := range edges {
		x0, y0, z0 := proj.project(e.a.x, e.a.y, e.a.z)
		x1, y1, z1 := proj.project(e.b.x, e.b.y, e.b.z)
		col := shadeVertex(e.a.add(e.b).scale(0.5), vec3{0, 0, 1}, e.rgb, wire.SceneMaterialMatte, lights)
		dark := [3]uint8{
			uint8(math.Round(col[0] * 255 * 0.84)),
			uint8(math.Round(col[1] * 255 * 0.84)),
			uint8(math.Round(col[2] * 255 * 0.84)),
		}
		drawDepthLine(img, zbuf, w, h, x0, y0, z0-0.015, x1, y1, z1-0.015, dark, 0.45)
	}

	// Pass 2: semi-transparent glass sorted back-to-front.
	sort.Slice(glass, func(i, j int) bool { return glass[i].depth > glass[j].depth })
	for _, tri := range glass {
		var sx, sy, sz [3]float64
		var vc [3][3]float64
		n := tri.n
		if n.dot(viewToCam) < 0 {
			n = n.scale(-1)
		}
		for k := range 3 {
			sx[k], sy[k], sz[k] = proj.project(tri.v[k].x, tri.v[k].y, tri.v[k].z)
			vc[k] = shadeVertex(tri.v[k], n, tri.baseRGB, tri.mat, lights)
		}
		rasterizeTri(img, zbuf, w, h, sx, sy, sz, vc, 0.36, false)
	}

	// Pass 3: projected device pin halos and crisp colored dot markers, back-to-front.
	type projPin struct {
		pin           wire.ScenePin
		sx, sy, depth float64
	}
	var pins []projPin
	for _, pin := range comp.Pins {
		if len(pin.At) < 2 || !isFinite(pin.At[0]) || !isFinite(pin.At[1]) {
			continue
		}
		pz := 0.0
		if pin.Height != nil && isFinite(*pin.Height) {
			pz = *pin.Height
		}
		sx, sy, d := proj.project(pin.At[0], pin.At[1], pz)
		pins = append(pins, projPin{pin: pin, sx: sx, sy: sy, depth: d})
	}
	sort.Slice(pins, func(i, j int) bool { return pins[i].depth > pins[j].depth })

	dotR := max(5.0, float64(w)/135.0)
	for _, pp := range pins {
		if pp.pin.Light != nil && pp.pin.Light.On {
			glow := [3]uint8{255, 210, 130}
			if pp.pin.Light.Warmth == wire.SceneWarmthCool {
				glow = [3]uint8{200, 232, 255}
			}
			drawSoftGlow(img, w, h, pp.sx, pp.sy, dotR*3.4, glow, 0.30)
		}
		drawPinDot(img, w, h, pp.sx, pp.sy, dotR, pinMarkerRGB(pp.pin))
	}

	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

var (
	keyLightDir  = vec3{0.38, -0.52, 0.76}.norm()
	fillLightDir = vec3{-0.60, 0.35, 0.72}.norm()
)

func shadeVertex(p, n vec3, base [3]float64, mat string, lights []pointLight) [3]float64 {
	ambient, keyGain, fillGain := 0.56, 0.30, 0.10
	if len(lights) > 0 {
		// Slightly calmer daylight base when lamps are on so lit rooms visibly glow warm.
		ambient, keyGain, fillGain = 0.48, 0.27, 0.09
	}
	k := max(0, n.dot(keyLightDir))
	f := max(0, n.dot(fillLightDir))
	hemi := ambient + 0.05*n.z
	ground := 0.96 + 0.04*min(max(p.z/1.2, 0), 1)
	dir := (hemi + keyGain*k + fillGain*f) * ground
	if mat == wire.SceneMaterialMetal {
		dir += 0.06 * math.Pow(k, 3)
	} else if mat == wire.SceneMaterialScreen {
		dir = 0.55 + 0.35*k
	}

	lr, lg, lb := dir, dir, dir
	var addR, addG, addB float64
	for _, l := range lights {
		dx, dy, dz := l.pos.x-p.x, l.pos.y-p.y, l.pos.z-p.z
		// Weight vertical ceiling height lightly so a 2.4 m ceiling lamp warmly bathes the floor below it.
		effDist2 := dx*dx + dy*dy + 0.12*dz*dz
		effDist := math.Sqrt(effDist2)
		if effDist >= 4.4 {
			continue
		}
		att := (1.0 / (1.0 + 0.30*effDist2)) * (1.0 - effDist/4.4)
		dist3 := math.Sqrt(dx*dx + dy*dy + dz*dz)
		ndotl := 0.35
		if dist3 > 1e-4 {
			ndotl = max(0.30, (n.x*dx+n.y*dy+n.z*dz)/dist3)
		}
		s := att * ndotl
		lr += 0.68 * s * l.color[0]
		lg += 0.62 * s * l.color[1]
		lb += 0.42 * s * l.color[2]
		addR += 0.13 * s * l.color[0]
		addG += 0.09 * s * l.color[1]
		addB += 0.03 * s * l.color[2]
	}

	soft := func(v float64) float64 {
		if v <= 0 {
			return 0
		}
		if v <= 0.85 {
			return v
		}
		// Smooth shoulder above 0.85 so bright warm floors keep R > G > B instead of hard-clipping white.
		return 0.85 + 0.15*(1.0-math.Exp(-(v-0.85)/0.22))
	}
	return [3]float64{
		min(1, soft(base[0]*lr+addR)),
		min(1, soft(base[1]*lg+addG)),
		min(1, soft(base[2]*lb+addB)),
	}
}

func tessellateShape(s wire.SceneShape, viewToCam vec3, isGlass bool) ([]sceneTri, []sceneEdge) {
	base := parseSceneRGB(s.Color, s.Material)
	b, t := *s.Bottom, *s.Top
	zBias := 0.0
	if t <= 0.002 {
		// Floor prisms sit at top=0; tiny positive depth bias prevents z-fighting with thresholds and furniture bases.
		zBias = 0.004
	}
	var tris []sceneTri
	var edges []sceneEdge

	emitQuad := func(v00, v10, v11, v01, n vec3, uLen, vLen float64) {
		if !isGlass && n.dot(viewToCam) < -0.01 {
			return
		}
		nu, nv := 1, 1
		if !isGlass {
			nu = min(20, max(1, int(math.Ceil(uLen/subdivideStep))))
			nv = min(12, max(1, int(math.Ceil(vLen/subdivideStep))))
		}
		lerp := func(a, b vec3, f float64) vec3 {
			return vec3{a.x + (b.x-a.x)*f, a.y + (b.y-a.y)*f, a.z + (b.z-a.z)*f}
		}
		grid := func(iu, iv int) vec3 {
			fu, fv := float64(iu)/float64(nu), float64(iv)/float64(nv)
			bot := lerp(v00, v10, fu)
			top := lerp(v01, v11, fu)
			return lerp(bot, top, fv)
		}
		for iu := range nu {
			for iv := range nv {
				p00 := grid(iu, iv)
				p10 := grid(iu+1, iv)
				p11 := grid(iu+1, iv+1)
				p01 := grid(iu, iv+1)
				tris = append(tris,
					sceneTri{v: [3]vec3{p00, p10, p11}, n: n, baseRGB: base, mat: s.Material, zBias: zBias},
					sceneTri{v: [3]vec3{p00, p11, p01}, n: n, baseRGB: base, mat: s.Material, zBias: zBias},
				)
			}
		}
	}

	switch s.Kind {
	case wire.SceneShapeBox:
		if len(s.Center) < 2 || len(s.Size) < 2 {
			return nil, nil
		}
		w, d := s.Size[0], s.Size[1]
		h := t - b
		yaw := 0.0
		if s.Yaw != nil {
			yaw = *s.Yaw
		}
		rad := yaw * math.Pi / 180
		cs, sn := math.Cos(rad), math.Sin(rad)
		ux := vec3{cs, sn, 0}
		uy := vec3{-sn, cs, 0}
		at := func(lx, ly, z float64) vec3 {
			return vec3{s.Center[0] + lx*cs - ly*sn, s.Center[1] + lx*sn + ly*cs, z}
		}
		hx, hy := w/2, d/2
		b00, b10, b11, b01 := at(-hx, -hy, b), at(hx, -hy, b), at(hx, hy, b), at(-hx, hy, b)
		t00, t10, t11, t01 := at(-hx, -hy, t), at(hx, -hy, t), at(hx, hy, t), at(-hx, hy, t)

		emitQuad(t00, t10, t11, t01, vec3{0, 0, 1}, w, d)
		emitQuad(b00, b10, t10, t00, uy.scale(-1), w, h)
		emitQuad(b10, b11, t11, t10, ux, d, h)
		emitQuad(b11, b01, t01, t11, uy, w, h)
		emitQuad(b01, b00, t00, t01, ux.scale(-1), d, h)

		if !isGlass && t > 0.03 {
			edges = append(edges,
				sceneEdge{a: t00, b: t10, rgb: base},
				sceneEdge{a: t10, b: t11, rgb: base},
				sceneEdge{a: t11, b: t01, rgb: base},
				sceneEdge{a: t01, b: t00, rgb: base},
			)
		}

	case wire.SceneShapeCylinder:
		if len(s.Center) < 2 || s.Radius <= 0 {
			return nil, nil
		}
		cx, cy, r := s.Center[0], s.Center[1], s.Radius
		h := t - b
		const segs = 18
		topC := vec3{cx, cy, t}
		for i := range segs {
			a0 := float64(i) * 2 * math.Pi / segs
			a1 := float64(i+1) * 2 * math.Pi / segs
			c0, s0 := math.Cos(a0), math.Sin(a0)
			c1, s1 := math.Cos(a1), math.Sin(a1)
			p0b := vec3{cx + r*c0, cy + r*s0, b}
			p1b := vec3{cx + r*c1, cy + r*s1, b}
			p0t := vec3{cx + r*c0, cy + r*s0, t}
			p1t := vec3{cx + r*c1, cy + r*s1, t}
			if isGlass || viewToCam.z >= -0.01 {
				tris = append(tris, sceneTri{v: [3]vec3{topC, p0t, p1t}, n: vec3{0, 0, 1}, baseRGB: base, mat: s.Material, zBias: zBias})
			}
			nSide := vec3{(c0 + c1) / 2, (s0 + s1) / 2, 0}.norm()
			emitQuad(p0b, p1b, p1t, p0t, nSide, 2*math.Pi*r/segs, h)
		}

	case wire.SceneShapePrism:
		if len(s.Points) < 3 {
			return nil, nil
		}
		poly := make([][2]float64, 0, len(s.Points))
		for _, pt := range s.Points {
			if len(pt) < 2 {
				continue
			}
			p := [2]float64{pt[0], pt[1]}
			if len(poly) == 0 || dist2D(poly[len(poly)-1], p) > 1e-5 {
				poly = append(poly, p)
			}
		}
		if len(poly) >= 2 && dist2D(poly[0], poly[len(poly)-1]) <= 1e-5 {
			poly = poly[:len(poly)-1]
		}
		if len(poly) < 3 {
			return nil, nil
		}
		if polygonSignedArea2D(poly) < 0 {
			for i, j := 0, len(poly)-1; i < j; i, j = i+1, j-1 {
				poly[i], poly[j] = poly[j], poly[i]
			}
		}
		for _, t2 := range subdivide2DTris(triangulatePolygon(poly), subdivideStep) {
			tris = append(tris, sceneTri{
				v:       [3]vec3{{t2[0][0], t2[0][1], t}, {t2[1][0], t2[1][1], t}, {t2[2][0], t2[2][1], t}},
				n:       vec3{0, 0, 1},
				baseRGB: base,
				mat:     s.Material,
				zBias:   zBias,
			})
		}
		h := t - b
		for i := range poly {
			a, q := poly[i], poly[(i+1)%len(poly)]
			edgeLen := dist2D(a, q)
			if edgeLen < 1e-5 {
				continue
			}
			nx, ny := (q[1]-a[1])/edgeLen, -(q[0]-a[0])/edgeLen
			emitQuad(
				vec3{a[0], a[1], b}, vec3{q[0], q[1], b},
				vec3{q[0], q[1], t}, vec3{a[0], a[1], t},
				vec3{nx, ny, 0}, edgeLen, h,
			)
		}
	}
	return tris, edges
}

func polygonSignedArea2D(poly [][2]float64) float64 {
	var a float64
	for i := range poly {
		p, q := poly[i], poly[(i+1)%len(poly)]
		a += p[0]*q[1] - q[0]*p[1]
	}
	return a / 2
}

// triangulatePolygon ear-clips a counter-clockwise simple 2D polygon into triangles.
func triangulatePolygon(poly [][2]float64) [][3][2]float64 {
	n := len(poly)
	if n < 3 {
		return nil
	}
	idx := make([]int, n)
	for i := range idx {
		idx[i] = i
	}
	cross := func(a, b, c [2]float64) float64 {
		return (b[0]-a[0])*(c[1]-a[1]) - (b[1]-a[1])*(c[0]-a[0])
	}
	inTri := func(p, a, b, c [2]float64) bool {
		return cross(a, b, p) >= -1e-9 && cross(b, c, p) >= -1e-9 && cross(c, a, p) >= -1e-9
	}

	var out [][3][2]float64
	for len(idx) > 3 {
		m := len(idx)
		earFound := false
		for i := range m {
			ia, ib, ic := idx[(i+m-1)%m], idx[i], idx[(i+1)%m]
			a, b, c := poly[ia], poly[ib], poly[ic]
			if cross(a, b, c) <= 1e-9 {
				continue
			}
			empty := true
			for j := range m {
				ij := idx[j]
				if ij == ia || ij == ib || ij == ic {
					continue
				}
				if inTri(poly[ij], a, b, c) {
					empty = false
					break
				}
			}
			if empty {
				out = append(out, [3][2]float64{a, b, c})
				idx = append(idx[:i], idx[i+1:]...)
				earFound = true
				break
			}
		}
		if !earFound {
			// Nearly collinear fallback: clip the most convex remaining corner so termination is guaranteed.
			best, bestCross := 0, math.Inf(-1)
			for i := range m {
				c := cross(poly[idx[(i+m-1)%m]], poly[idx[i]], poly[idx[(i+1)%m]])
				if c > bestCross {
					best, bestCross = i, c
				}
			}
			out = append(out, [3][2]float64{poly[idx[(best+m-1)%m]], poly[idx[best]], poly[idx[(best+1)%m]]})
			idx = append(idx[:best], idx[best+1:]...)
		}
	}
	if len(idx) == 3 {
		out = append(out, [3][2]float64{poly[idx[0]], poly[idx[1]], poly[idx[2]]})
	}
	return out
}

// subdivide2DTris splits triangles along their longest edge until edges are around maxEdge, keeping point-light pools smooth.
func subdivide2DTris(in [][3][2]float64, maxEdge float64) [][3][2]float64 {
	const capTris = 1024
	work := make([][3][2]float64, len(in))
	copy(work, in)
	var out [][3][2]float64
	for len(work) > 0 {
		t := work[len(work)-1]
		work = work[:len(work)-1]
		d01 := dist2D(t[0], t[1])
		d12 := dist2D(t[1], t[2])
		d20 := dist2D(t[2], t[0])
		longest := max(d01, max(d12, d20))
		if longest <= maxEdge || len(out)+len(work)+2 > capTris {
			out = append(out, t)
			continue
		}
		switch {
		case d01 >= d12 && d01 >= d20:
			m := [2]float64{(t[0][0] + t[1][0]) / 2, (t[0][1] + t[1][1]) / 2}
			work = append(work, [3][2]float64{t[0], m, t[2]}, [3][2]float64{m, t[1], t[2]})
		case d12 >= d01 && d12 >= d20:
			m := [2]float64{(t[1][0] + t[2][0]) / 2, (t[1][1] + t[2][1]) / 2}
			work = append(work, [3][2]float64{t[0], t[1], m}, [3][2]float64{t[0], m, t[2]})
		default:
			m := [2]float64{(t[2][0] + t[0][0]) / 2, (t[2][1] + t[0][1]) / 2}
			work = append(work, [3][2]float64{t[0], t[1], m}, [3][2]float64{m, t[1], t[2]})
		}
	}
	return out
}

func rasterizeTri(img *image.RGBA, zbuf []float32, w, h int, sx, sy, sz [3]float64, vc [3][3]float64, alpha float64, writeZ bool) {
	area := (sx[1]-sx[0])*(sy[2]-sy[0]) - (sy[1]-sy[0])*(sx[2]-sx[0])
	if math.Abs(area) < 1e-4 {
		return
	}
	if area < 0 {
		sx[1], sx[2] = sx[2], sx[1]
		sy[1], sy[2] = sy[2], sy[1]
		sz[1], sz[2] = sz[2], sz[1]
		vc[1], vc[2] = vc[2], vc[1]
		area = -area
	}
	invArea := 1.0 / area

	minX := max(0, int(math.Floor(min(sx[0], min(sx[1], sx[2])))))
	maxX := min(w-1, int(math.Ceil(max(sx[0], max(sx[1], sx[2])))))
	minY := max(0, int(math.Floor(min(sy[0], min(sy[1], sy[2])))))
	maxY := min(h-1, int(math.Ceil(max(sy[0], max(sy[1], sy[2])))))
	if minX > maxX || minY > maxY {
		return
	}

	dx12, dy12 := sx[2]-sx[1], sy[2]-sy[1]
	dx20, dy20 := sx[0]-sx[2], sy[0]-sy[2]
	dx01, dy01 := sx[1]-sx[0], sy[1]-sy[0]

	for py := minY; py <= maxY; py++ {
		fy := float64(py) + 0.5
		rowOff := py * img.Stride
		zRow := py * w
		for px := minX; px <= maxX; px++ {
			fx := float64(px) + 0.5
			w0 := ((fy-sy[1])*dx12 - (fx-sx[1])*dy12) * invArea
			w1 := ((fy-sy[2])*dx20 - (fx-sx[2])*dy20) * invArea
			w2 := ((fy-sy[0])*dx01 - (fx-sx[0])*dy01) * invArea
			if w0 < -1e-5 || w1 < -1e-5 || w2 < -1e-5 {
				continue
			}
			z := float32(w0*sz[0] + w1*sz[1] + w2*sz[2])
			zi := zRow + px
			if z >= zbuf[zi] {
				continue
			}
			r := (w0*vc[0][0] + w1*vc[1][0] + w2*vc[2][0]) * 255
			g := (w0*vc[0][1] + w1*vc[1][1] + w2*vc[2][1]) * 255
			b := (w0*vc[0][2] + w1*vc[1][2] + w2*vc[2][2]) * 255
			off := rowOff + px*4
			if writeZ && alpha >= 0.999 {
				zbuf[zi] = z
				img.Pix[off] = clampByte(r)
				img.Pix[off+1] = clampByte(g)
				img.Pix[off+2] = clampByte(b)
				img.Pix[off+3] = 255
			} else {
				inv := 1.0 - alpha
				img.Pix[off] = clampByte(float64(img.Pix[off])*inv + r*alpha)
				img.Pix[off+1] = clampByte(float64(img.Pix[off+1])*inv + g*alpha)
				img.Pix[off+2] = clampByte(float64(img.Pix[off+2])*inv + b*alpha)
			}
		}
	}
}

func drawDepthLine(img *image.RGBA, zbuf []float32, w, h int, x0, y0, z0, x1, y1, z1 float64, col [3]uint8, alpha float64) {
	steps := int(math.Ceil(math.Hypot(x1-x0, y1-y0)))
	if steps <= 0 || steps > 4000 {
		return
	}
	inv := 1.0 - alpha
	for i := 0; i <= steps; i++ {
		t := float64(i) / float64(steps)
		px := int(math.Round(x0 + (x1-x0)*t))
		py := int(math.Round(y0 + (y1-y0)*t))
		if px < 0 || px >= w || py < 0 || py >= h {
			continue
		}
		z := float32(z0 + (z1-z0)*t)
		zi := py*w + px
		if z <= zbuf[zi]+0.02 {
			off := py*img.Stride + px*4
			img.Pix[off] = clampByte(float64(img.Pix[off])*inv + float64(col[0])*alpha)
			img.Pix[off+1] = clampByte(float64(img.Pix[off+1])*inv + float64(col[1])*alpha)
			img.Pix[off+2] = clampByte(float64(img.Pix[off+2])*inv + float64(col[2])*alpha)
		}
	}
}

func drawSoftGlow(img *image.RGBA, w, h int, cx, cy, r float64, col [3]uint8, peakAlpha float64) {
	minX := max(0, int(math.Floor(cx-r)))
	maxX := min(w-1, int(math.Ceil(cx+r)))
	minY := max(0, int(math.Floor(cy-r)))
	maxY := min(h-1, int(math.Ceil(cy+r)))
	for py := minY; py <= maxY; py++ {
		dy := float64(py) + 0.5 - cy
		rowOff := py * img.Stride
		for px := minX; px <= maxX; px++ {
			dx := float64(px) + 0.5 - cx
			d := math.Hypot(dx, dy)
			if d >= r {
				continue
			}
			f := 1.0 - d/r
			a := peakAlpha * f * f
			inv := 1.0 - a
			off := rowOff + px*4
			img.Pix[off] = clampByte(float64(img.Pix[off])*inv + float64(col[0])*a)
			img.Pix[off+1] = clampByte(float64(img.Pix[off+1])*inv + float64(col[1])*a)
			img.Pix[off+2] = clampByte(float64(img.Pix[off+2])*inv + float64(col[2])*a)
		}
	}
}

func drawPinDot(img *image.RGBA, w, h int, cx, cy, r float64, fill [3]uint8) {
	// Soft drop shadow 1.5 px below the marker, then a white ring and crisp colored core.
	drawSoftCircle(img, w, h, cx, cy+1.5, r+0.8, [3]uint8{30, 28, 26}, 0.22)
	drawSoftCircle(img, w, h, cx, cy, r, [3]uint8{255, 255, 255}, 0.96)
	drawSoftCircle(img, w, h, cx, cy, max(2.2, r-2.0), fill, 1.0)
}

func drawSoftCircle(img *image.RGBA, w, h int, cx, cy, r float64, col [3]uint8, alpha float64) {
	minX := max(0, int(math.Floor(cx-r-1)))
	maxX := min(w-1, int(math.Ceil(cx+r+1)))
	minY := max(0, int(math.Floor(cy-r-1)))
	maxY := min(h-1, int(math.Ceil(cy+r+1)))
	for py := minY; py <= maxY; py++ {
		dy := float64(py) + 0.5 - cy
		rowOff := py * img.Stride
		for px := minX; px <= maxX; px++ {
			dx := float64(px) + 0.5 - cx
			d := math.Hypot(dx, dy)
			cov := min(1, max(0, r+0.5-d))
			if cov <= 0 {
				continue
			}
			a := alpha * cov
			inv := 1.0 - a
			off := rowOff + px*4
			img.Pix[off] = clampByte(float64(img.Pix[off])*inv + float64(col[0])*a)
			img.Pix[off+1] = clampByte(float64(img.Pix[off+1])*inv + float64(col[1])*a)
			img.Pix[off+2] = clampByte(float64(img.Pix[off+2])*inv + float64(col[2])*a)
		}
	}
}

func pinMarkerRGB(pin wire.ScenePin) [3]uint8 {
	if pin.Light != nil && pin.Light.On {
		if pin.Light.Warmth == wire.SceneWarmthCool {
			return [3]uint8{96, 178, 255}
		}
		return [3]uint8{245, 172, 42}
	}
	switch pin.Tone {
	case wire.TonePositive:
		if pin.Icon == wire.NavIconLight {
			return [3]uint8{245, 172, 42}
		}
		return [3]uint8{46, 176, 108}
	case wire.ToneCaution:
		return [3]uint8{238, 146, 40}
	case wire.ToneDanger:
		return [3]uint8{226, 72, 77}
	}
	switch pin.Color {
	case wire.ColorAmber, wire.ColorYellow:
		return [3]uint8{240, 170, 44}
	case wire.ColorOrange:
		return [3]uint8{236, 128, 50}
	case wire.ColorGreen:
		return [3]uint8{46, 176, 108}
	case wire.ColorTeal:
		return [3]uint8{42, 168, 162}
	case wire.ColorBlue:
		return [3]uint8{62, 136, 228}
	case wire.ColorIndigo:
		return [3]uint8{94, 106, 218}
	case wire.ColorPurple:
		return [3]uint8{148, 92, 212}
	case wire.ColorPink:
		return [3]uint8{222, 92, 152}
	case wire.ColorRed:
		return [3]uint8{226, 72, 77}
	}
	return [3]uint8{126, 122, 116}
}

func parseSceneRGB(hex, mat string) [3]float64 {
	if len(hex) == 7 && hex[0] == '#' {
		r, err1 := strconv.ParseUint(hex[1:3], 16, 8)
		g, err2 := strconv.ParseUint(hex[3:5], 16, 8)
		b, err3 := strconv.ParseUint(hex[5:7], 16, 8)
		if err1 == nil && err2 == nil && err3 == nil {
			return [3]float64{float64(r) / 255, float64(g) / 255, float64(b) / 255}
		}
	}
	switch mat {
	case wire.SceneMaterialTile:
		return [3]float64{0.918, 0.894, 0.855}
	case wire.SceneMaterialWood:
		return [3]float64{0.784, 0.659, 0.510}
	case wire.SceneMaterialMetal:
		return [3]float64{0.816, 0.831, 0.847}
	case wire.SceneMaterialScreen:
		return [3]float64{0.118, 0.125, 0.141}
	case wire.SceneMaterialGlass:
		return [3]float64{0.722, 0.847, 0.910}
	default:
		return [3]float64{0.925, 0.906, 0.871}
	}
}

func clampByte(v float64) uint8 {
	if v <= 0 {
		return 0
	}
	if v >= 255 {
		return 255
	}
	return uint8(math.Round(v))
}
