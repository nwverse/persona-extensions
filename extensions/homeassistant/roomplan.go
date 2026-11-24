package main

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path"
	"sort"
	"strconv"
	"strings"
)

// Apple RoomPlan USDZ reader (#936): reads an ASCII USD (.usda) export from a zip archive into plan coordinates
// ([x, y] in metres, x east, y north, heights up from the lowest wall bottom). Binary USD (.usdc, magic PXR-USDC) is
// refused with a plain message asking for ASCII USDZ.

const (
	usdzBytesMost   = 20 << 20 // 20 MB zip limit
	usdzEntriesMost = 500
	usdaBytesMost   = 2 << 20 // 2 MB per .usda entry
	usdcMagic       = "PXR-USDC"
)

var errBinaryUSD = errors.New("binary USD (.usdc) is not supported: please export ASCII USDZ from the scanning app")

// scan is one RoomPlan export in plan coordinates ([x, y], x east, y north; heights up from 0 at the lowest wall).
type scan struct {
	Walls    []scanWall
	Doors    []scanDoor
	Windows  []scanWindow
	Openings []scanOpening
	Objects  []scanObject
	Floor    [][2]float64
	Rooms    []scanRoom
}

type scanWall struct {
	Name        string
	Center      [2]float64
	Size        [2]float64 // [length, thickness]
	Yaw         float64    // degrees CCW from +x
	Bottom, Top float64
}

type scanDoor struct {
	Name        string
	Kind        string // "" or "sliding_glass"
	Open        bool
	Center      [2]float64
	Size        [2]float64
	Yaw         float64
	Bottom, Top float64
}

type scanWindow struct {
	Name        string
	Center      [2]float64
	Size        [2]float64
	Yaw         float64
	Bottom, Top float64
}

type scanOpening struct {
	Name        string
	Kind        string
	Center      [2]float64
	Size        [2]float64
	Yaw         float64
	Bottom, Top float64
}

type scanObject struct {
	Category    string
	Kind        string // override from home3d.json ("bar_table", "piano", …), else Category
	Name        string
	Center      [2]float64
	Size        [2]float64
	Yaw         float64
	FixedYaw    bool // true when yaw came explicitly from home3d.json "add"
	Bottom, Top float64
	Color       string
}

type scanRoom struct {
	Name   string
	Center [2]float64
}

// readRoomPlanFile reads a RoomPlan USDZ file from disk, checking its size before reading.
func readRoomPlanFile(file string) (*scan, error) {
	info, err := os.Stat(file)
	if err != nil {
		return nil, err
	}
	if info.Size() > usdzBytesMost {
		return nil, fmt.Errorf("usdz is %d bytes, max %d", info.Size(), usdzBytesMost)
	}
	f, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, usdzBytesMost+1))
	if err != nil {
		return nil, err
	}
	return readRoomPlan(raw)
}

// readRoomPlan parses a RoomPlan USDZ byte slice into plan coordinates.
func readRoomPlan(raw []byte) (*scan, error) {
	if bytes.HasPrefix(raw, []byte(usdcMagic)) {
		return nil, errBinaryUSD
	}
	if len(raw) > usdzBytesMost {
		return nil, fmt.Errorf("usdz is %d bytes, max %d", len(raw), usdzBytesMost)
	}
	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		return nil, fmt.Errorf("usdz zip: %w", err)
	}
	if len(zr.File) > usdzEntriesMost {
		return nil, fmt.Errorf("usdz has %d entries, max %d", len(zr.File), usdzEntriesMost)
	}

	sc := &scan{}
	seenPrims := map[string]bool{}
	var floorPts [][2]float64
	totalRead := 0

	// Read asset meshes before or after root files; sort so assets/Mesh/* come before root references if names collide.
	files := make([]*zip.File, len(zr.File))
	copy(files, zr.File)
	sort.SliceStable(files, func(i, j int) bool {
		ai := strings.Contains(files[i].Name, "/")
		aj := strings.Contains(files[j].Name, "/")
		if ai != aj {
			return ai
		}
		return files[i].Name < files[j].Name
	})

	for _, f := range files {
		if f.FileInfo().IsDir() {
			continue
		}
		clean := path.Clean(strings.TrimLeft(f.Name, "/"))
		lower := strings.ToLower(clean)
		if strings.HasSuffix(lower, ".usdc") {
			return nil, errBinaryUSD
		}
		if !strings.HasSuffix(lower, ".usda") && !strings.HasSuffix(lower, ".usd") {
			// Still check magic on unknown entries in case a binary USD was stored under another name.
			rc, err := f.Open()
			if err != nil {
				return nil, fmt.Errorf("%s: %w", clean, err)
			}
			var head [8]byte
			n, _ := io.ReadFull(rc, head[:])
			rc.Close()
			if bytes.Equal(head[:n], []byte(usdcMagic)) {
				return nil, errBinaryUSD
			}
			continue
		}
		if f.UncompressedSize64 > usdaBytesMost {
			return nil, fmt.Errorf("%s is %d bytes, max %d", clean, f.UncompressedSize64, usdaBytesMost)
		}
		rc, err := f.Open()
		if err != nil {
			return nil, fmt.Errorf("%s: %w", clean, err)
		}
		data, err := io.ReadAll(io.LimitReader(rc, usdaBytesMost+1))
		rc.Close()
		if err != nil {
			return nil, fmt.Errorf("%s: %w", clean, err)
		}
		if bytes.HasPrefix(data, []byte(usdcMagic)) {
			return nil, errBinaryUSD
		}
		if len(data) > usdaBytesMost {
			return nil, fmt.Errorf("%s exceeds %d bytes", clean, usdaBytesMost)
		}
		totalRead += len(data)
		if totalRead > usdzBytesMost*2 {
			return nil, fmt.Errorf("usdz uncompressed text exceeds limit")
		}

		pathCat := categoryFromAssetPath(clean)
		parsed, err := parseUSDA(clean, string(data))
		if err != nil {
			return nil, err
		}
		collectUSDA(parsed, pathCat, seenPrims, sc, &floorPts)
	}

	// Shift heights so the lowest wall bottom sits at 0.
	if len(sc.Walls) > 0 {
		floorY := sc.Walls[0].Bottom
		for _, w := range sc.Walls[1:] {
			floorY = min(floorY, w.Bottom)
		}
		for i := range sc.Walls {
			sc.Walls[i].Bottom -= floorY
			sc.Walls[i].Top -= floorY
		}
		shift := func(bottom, top *float64) {
			*bottom -= floorY
			*top -= floorY
			if *bottom < 0 {
				*bottom = 0
			}
			if *top < *bottom+0.02 {
				*top = *bottom + 0.02
			}
		}
		for i := range sc.Doors {
			shift(&sc.Doors[i].Bottom, &sc.Doors[i].Top)
		}
		for i := range sc.Windows {
			shift(&sc.Windows[i].Bottom, &sc.Windows[i].Top)
		}
		for i := range sc.Openings {
			shift(&sc.Openings[i].Bottom, &sc.Openings[i].Top)
		}
		for i := range sc.Objects {
			shift(&sc.Objects[i].Bottom, &sc.Objects[i].Top)
		}
	}

	sc.Floor = cleanFloorPolygon(floorPts)
	return sc, nil
}

// categoryFromAssetPath extracts <Category> from assets/Mesh/<Category>/<Name>.usda as a fallback.
func categoryFromAssetPath(clean string) string {
	parts := strings.Split(clean, "/")
	for i := 0; i+2 < len(parts); i++ {
		if strings.EqualFold(parts[i], "Mesh") {
			return parts[i+1]
		}
	}
	return ""
}

// ---- 4x4 row-vector matrix math (world = local · M) ----

type usdaMat [4][4]float64

func usdaIdentity() usdaMat {
	return usdaMat{
		{1, 0, 0, 0},
		{0, 1, 0, 0},
		{0, 0, 1, 0},
		{0, 0, 0, 1},
	}
}

func usdaMul(a, b usdaMat) usdaMat {
	var out usdaMat
	for i := range 4 {
		for j := range 4 {
			out[i][j] = a[i][0]*b[0][j] + a[i][1]*b[1][j] + a[i][2]*b[2][j] + a[i][3]*b[3][j]
		}
	}
	return out
}

func usdaApply(p [3]float64, m usdaMat) [3]float64 {
	return [3]float64{
		p[0]*m[0][0] + p[1]*m[1][0] + p[2]*m[2][0] + m[3][0],
		p[0]*m[0][1] + p[1]*m[1][1] + p[2]*m[2][1] + m[3][1],
		p[0]*m[0][2] + p[1]*m[1][2] + p[2]*m[2][2] + m[3][2],
	}
}

// ---- ASCII USD (.usda) parser ----

type usdaFile struct {
	metersPerUnit float64
	roots         []*usdaNode
}

type usdaNode struct {
	typ      string
	name     string
	kind     string
	category string
	mat      usdaMat
	points   [][3]float64
	children []*usdaNode
}

type usdaTokKind uint8

const (
	tokEOF usdaTokKind = iota
	tokPunct
	tokWord
	tokString
	tokOtherLit // @asset@ or </SdfPath>
)

type usdaTok struct {
	kind usdaTokKind
	val  string
}

type usdaLexer struct {
	src string
	pos int
}

func (l *usdaLexer) next() (usdaTok, error) {
	s := l.src
	for l.pos < len(s) {
		c := s[l.pos]
		if c == ' ' || c == '\t' || c == '\r' || c == '\n' {
			l.pos++
			continue
		}
		if c == '#' {
			for l.pos < len(s) && s[l.pos] != '\n' {
				l.pos++
			}
			continue
		}
		break
	}
	if l.pos >= len(s) {
		return usdaTok{kind: tokEOF}, nil
	}
	c := s[l.pos]
	switch c {
	case '(', ')', '{', '}', '[', ']', '=', ',', ';':
		l.pos++
		return usdaTok{kind: tokPunct, val: string(c)}, nil
	case '"', '\'':
		quote := c
		l.pos++
		// Triple-quoted strings """..."""
		if l.pos+1 < len(s) && s[l.pos] == quote && s[l.pos+1] == quote {
			l.pos += 2
			start := l.pos
			for l.pos+2 < len(s) {
				if s[l.pos] == quote && s[l.pos+1] == quote && s[l.pos+2] == quote {
					val := s[start:l.pos]
					l.pos += 3
					return usdaTok{kind: tokString, val: val}, nil
				}
				l.pos++
			}
			return usdaTok{}, errors.New("unterminated triple-quoted string")
		}
		var b strings.Builder
		for l.pos < len(s) {
			ch := s[l.pos]
			l.pos++
			if ch == quote {
				return usdaTok{kind: tokString, val: b.String()}, nil
			}
			if ch == '\\' && l.pos < len(s) {
				b.WriteByte(s[l.pos])
				l.pos++
				continue
			}
			b.WriteByte(ch)
		}
		return usdaTok{}, errors.New("unterminated string")
	case '@':
		l.pos++
		// @@@...@@@ or @...@
		triple := false
		if l.pos+1 < len(s) && s[l.pos] == '@' && s[l.pos+1] == '@' {
			triple = true
			l.pos += 2
		}
		start := l.pos
		for l.pos < len(s) {
			if triple {
				if l.pos+2 < len(s) && s[l.pos] == '@' && s[l.pos+1] == '@' && s[l.pos+2] == '@' {
					val := s[start:l.pos]
					l.pos += 3
					return usdaTok{kind: tokOtherLit, val: val}, nil
				}
			} else if s[l.pos] == '@' {
				val := s[start:l.pos]
				l.pos++
				return usdaTok{kind: tokOtherLit, val: val}, nil
			}
			l.pos++
		}
		return usdaTok{}, errors.New("unterminated asset path")
	case '<':
		l.pos++
		start := l.pos
		for l.pos < len(s) && s[l.pos] != '>' && s[l.pos] != '\n' {
			l.pos++
		}
		if l.pos >= len(s) || s[l.pos] != '>' {
			return usdaTok{}, errors.New("unterminated path reference")
		}
		val := s[start:l.pos]
		l.pos++
		return usdaTok{kind: tokOtherLit, val: val}, nil
	}
	start := l.pos
	for l.pos < len(s) {
		ch := s[l.pos]
		if ch == ' ' || ch == '\t' || ch == '\r' || ch == '\n' ||
			strings.IndexByte("(){}[]=,;#\"'@<", ch) >= 0 {
			break
		}
		l.pos++
	}
	return usdaTok{kind: tokWord, val: s[start:l.pos]}, nil
}

type usdaParser struct {
	file string
	lex  usdaLexer
	peek *usdaTok
}

func (p *usdaParser) next() (usdaTok, error) {
	if p.peek != nil {
		t := *p.peek
		p.peek = nil
		return t, nil
	}
	return p.lex.next()
}

func (p *usdaParser) unread(t usdaTok) { p.peek = &t }

func parseUSDAFloat(tok string) (float64, error) {
	v, err := strconv.ParseFloat(tok, 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, fmt.Errorf("non-finite or invalid number %q", tok)
	}
	return v, nil
}

// checkWordFinite refuses non-finite number literals (nan, inf, -inf, overflow) anywhere in a USDA value.
func checkWordFinite(tok string) error {
	switch strings.ToLower(tok) {
	case "nan", "+nan", "-nan", "inf", "+inf", "-inf", "infinity", "+infinity", "-infinity":
		return fmt.Errorf("non-finite number %q", tok)
	}
	if len(tok) == 0 {
		return nil
	}
	c := tok[0]
	if (c >= '0' && c <= '9') || ((c == '+' || c == '-' || c == '.') && len(tok) > 1 && (tok[1] >= '0' && tok[1] <= '9' || tok[1] == '.')) {
		if v, err := strconv.ParseFloat(tok, 64); err == nil {
			if math.IsNaN(v) || math.IsInf(v, 0) {
				return fmt.Errorf("non-finite number %q", tok)
			}
		} else if errors.Is(err, strconv.ErrRange) {
			return fmt.Errorf("non-finite number %q", tok)
		}
	}
	return nil
}

func parseUSDA(file, text string) (*usdaFile, error) {
	trimmed := strings.TrimLeft(text, " \t\r\n")
	if !strings.HasPrefix(trimmed, "#usda") {
		return nil, fmt.Errorf("%s: missing #usda header", file)
	}
	p := &usdaParser{file: file, lex: usdaLexer{src: text}}
	out := &usdaFile{metersPerUnit: 1}

	tok, err := p.next()
	if err != nil {
		return nil, fmt.Errorf("%s: %w", file, err)
	}
	if tok.kind == tokPunct && tok.val == "(" {
		if err := p.readHeaderMeta(out); err != nil {
			return nil, fmt.Errorf("%s: %w", file, err)
		}
	} else {
		p.unread(tok)
	}

	for {
		tok, err := p.next()
		if err != nil {
			return nil, fmt.Errorf("%s: %w", file, err)
		}
		if tok.kind == tokEOF {
			break
		}
		if tok.kind == tokWord && (tok.val == "def" || tok.val == "over" || tok.val == "class") {
			n, err := p.readPrim(0)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", file, err)
			}
			if n != nil {
				out.roots = append(out.roots, n)
			}
		} else if err := checkWordFinite(tok.val); err != nil {
			return nil, fmt.Errorf("%s: %w", file, err)
		}
	}
	return out, nil
}

func (p *usdaParser) readHeaderMeta(out *usdaFile) error {
	depth := 1
	var prev usdaTok
	for depth > 0 {
		t, err := p.next()
		if err != nil {
			return err
		}
		if t.kind == tokEOF {
			return errors.New("unterminated header metadata")
		}
		if t.kind == tokPunct {
			switch t.val {
			case "(":
				depth++
			case ")":
				depth--
			case "=":
				if depth == 1 && prev.kind == tokWord && prev.val == "metersPerUnit" {
					valTok, err := p.next()
					if err != nil {
						return err
					}
					v, err := parseUSDAFloat(valTok.val)
					if err != nil || v <= 0 {
						return fmt.Errorf("invalid metersPerUnit %q", valTok.val)
					}
					out.metersPerUnit = v
					continue
				}
			}
		} else if t.kind == tokWord {
			if err := checkWordFinite(t.val); err != nil {
				return err
			}
		}
		prev = t
	}
	return nil
}

func (p *usdaParser) readPrim(level int) (*usdaNode, error) {
	if level > 32 {
		return nil, errors.New("prim nesting too deep")
	}
	n := &usdaNode{mat: usdaIdentity()}
	t1, err := p.next()
	if err != nil {
		return nil, err
	}
	switch t1.kind {
	case tokWord:
		n.typ = t1.val
		t2, err := p.next()
		if err != nil {
			return nil, err
		}
		if t2.kind == tokString {
			n.name = t2.val
		} else {
			p.unread(t2)
		}
	case tokString:
		n.name = t1.val
	default:
		return nil, errors.New("expected prim type or name")
	}

	tok, err := p.next()
	if err != nil {
		return nil, err
	}
	if tok.kind == tokPunct && tok.val == "(" {
		if err := p.readPrimMeta(n); err != nil {
			return nil, err
		}
		tok, err = p.next()
		if err != nil {
			return nil, err
		}
	}
	if tok.kind != tokPunct || tok.val != "{" {
		p.unread(tok)
		return n, nil
	}

	var prevWord string
	for {
		t, err := p.next()
		if err != nil {
			return nil, err
		}
		if t.kind == tokEOF {
			return nil, fmt.Errorf("unterminated prim %q", n.name)
		}
		if t.kind == tokPunct && t.val == "}" {
			break
		}
		if t.kind == tokWord && (t.val == "def" || t.val == "over" || t.val == "class") {
			child, err := p.readPrim(level + 1)
			if err != nil {
				return nil, err
			}
			if child != nil {
				n.children = append(n.children, child)
			}
			prevWord = ""
			continue
		}
		if t.kind == tokPunct && t.val == "=" {
			switch {
			case prevWord == "points":
				pts, err := p.readPoints()
				if err != nil {
					return nil, err
				}
				n.points = pts
			case strings.HasPrefix(prevWord, "xformOp:transform"):
				m, err := p.readMatrix()
				if err != nil {
					return nil, err
				}
				n.mat = m
			case strings.HasPrefix(prevWord, "xformOp:translate"):
				tr, err := p.readVec3()
				if err != nil {
					return nil, err
				}
				n.mat[3][0], n.mat[3][1], n.mat[3][2] = tr[0], tr[1], tr[2]
			default:
				if err := p.skipValue(); err != nil {
					return nil, err
				}
			}
			prevWord = ""
			continue
		}
		if t.kind == tokPunct && t.val == "(" {
			if err := p.skipBalanced("(", ")"); err != nil {
				return nil, err
			}
			continue
		}
		if t.kind == tokWord {
			if err := checkWordFinite(t.val); err != nil {
				return nil, err
			}
			prevWord = t.val
		}
	}
	return n, nil
}

func (p *usdaParser) readPrimMeta(n *usdaNode) error {
	depth := 1
	var prev string
	for depth > 0 {
		t, err := p.next()
		if err != nil {
			return err
		}
		if t.kind == tokEOF {
			return errors.New("unterminated prim metadata")
		}
		if t.kind == tokPunct {
			switch t.val {
			case "(":
				depth++
			case ")":
				depth--
			case "=":
				if prev == "Category" || prev == "kind" {
					vt, err := p.next()
					if err != nil {
						return err
					}
					if vt.kind == tokString {
						if prev == "Category" {
							n.category = vt.val
						} else if depth == 1 {
							n.kind = vt.val
						}
					} else {
						p.unread(vt)
					}
					prev = ""
					continue
				}
			}
			prev = ""
			continue
		}
		if t.kind == tokWord {
			if err := checkWordFinite(t.val); err != nil {
				return err
			}
			prev = t.val
		} else {
			prev = ""
		}
	}
	return nil
}

func (p *usdaParser) skipBalanced(open, closeCh string) error {
	depth := 1
	for depth > 0 {
		t, err := p.next()
		if err != nil {
			return err
		}
		if t.kind == tokEOF {
			return fmt.Errorf("unterminated %s", open)
		}
		if t.kind == tokPunct {
			if t.val == open {
				depth++
			} else if t.val == closeCh {
				depth--
			}
		} else if t.kind == tokWord {
			if err := checkWordFinite(t.val); err != nil {
				return err
			}
		}
	}
	return nil
}

func (p *usdaParser) skipValue() error {
	t, err := p.next()
	if err != nil {
		return err
	}
	if t.kind == tokPunct {
		switch t.val {
		case "[":
			return p.skipBalanced("[", "]")
		case "(":
			return p.skipBalanced("(", ")")
		case "{":
			return p.skipBalanced("{", "}")
		}
	}
	if t.kind == tokWord {
		return checkWordFinite(t.val)
	}
	return nil
}

func (p *usdaParser) readPoints() ([][3]float64, error) {
	t, err := p.next()
	if err != nil {
		return nil, err
	}
	if t.kind == tokWord && t.val == "None" {
		return nil, nil
	}
	if t.kind != tokPunct || t.val != "[" {
		return nil, fmt.Errorf("points: expected '[', got %q", t.val)
	}
	var pts [][3]float64
	for {
		tok, err := p.next()
		if err != nil {
			return nil, err
		}
		if tok.kind == tokEOF {
			return nil, errors.New("unterminated points array")
		}
		if tok.kind == tokPunct && tok.val == "]" {
			break
		}
		if tok.kind == tokPunct && tok.val == "," {
			continue
		}
		if tok.kind != tokPunct || tok.val != "(" {
			return nil, fmt.Errorf("points: expected '(', got %q", tok.val)
		}
		v, err := p.readTuple3()
		if err != nil {
			return nil, err
		}
		pts = append(pts, v)
	}
	return pts, nil
}

func (p *usdaParser) readVec3() ([3]float64, error) {
	t, err := p.next()
	if err != nil {
		return [3]float64{}, err
	}
	if t.kind != tokPunct || t.val != "(" {
		return [3]float64{}, fmt.Errorf("expected '(', got %q", t.val)
	}
	return p.readTuple3()
}

func (p *usdaParser) readTuple3() ([3]float64, error) {
	var out [3]float64
	i := 0
	for {
		t, err := p.next()
		if err != nil {
			return out, err
		}
		if t.kind == tokEOF {
			return out, errors.New("unterminated tuple")
		}
		if t.kind == tokPunct && t.val == ")" {
			if i != 3 {
				return out, fmt.Errorf("expected 3 numbers in tuple, got %d", i)
			}
			return out, nil
		}
		if t.kind == tokPunct && t.val == "," {
			continue
		}
		if i >= 3 {
			return out, errors.New("more than 3 numbers in point tuple")
		}
		v, err := parseUSDAFloat(t.val)
		if err != nil {
			return out, err
		}
		out[i] = v
		i++
	}
}

func (p *usdaParser) readMatrix() (usdaMat, error) {
	var m usdaMat
	t, err := p.next()
	if err != nil {
		return m, err
	}
	if t.kind != tokPunct || t.val != "(" {
		return m, fmt.Errorf("matrix: expected '(', got %q", t.val)
	}
	var nums []float64
	depth := 1
	for depth > 0 {
		tok, err := p.next()
		if err != nil {
			return m, err
		}
		if tok.kind == tokEOF {
			return m, errors.New("unterminated matrix")
		}
		if tok.kind == tokPunct {
			switch tok.val {
			case "(":
				depth++
			case ")":
				depth--
			case ",":
			default:
				return m, fmt.Errorf("matrix: unexpected %q", tok.val)
			}
			continue
		}
		v, err := parseUSDAFloat(tok.val)
		if err != nil {
			return m, err
		}
		nums = append(nums, v)
	}
	if len(nums) != 16 {
		return m, fmt.Errorf("matrix: expected 16 numbers, got %d", len(nums))
	}
	for i := range 4 {
		for j := range 4 {
			m[i][j] = nums[i*4+j]
		}
	}
	return m, nil
}

// collectUSDA walks parsed nodes, extracting rooms under Section_grp and components with meshes.
func collectUSDA(f *usdaFile, pathCat string, seen map[string]bool, sc *scan, floorPts *[][2]float64) {
	scale := f.metersPerUnit
	var walk func(n *usdaNode, parent usdaMat, inSection bool)
	walk = func(n *usdaNode, parent usdaMat, inSection bool) {
		world := usdaMul(n.mat, parent)
		if inSection && n.typ == "Xform" && n.kind == "assembly" && n.name != "" {
			c := usdaApply([3]float64{0, 0, 0}, world)
			sc.Rooms = append(sc.Rooms, scanRoom{
				Name:   n.name,
				Center: [2]float64{c[0] * scale, -c[2] * scale},
			})
		}
		cat := n.category
		if cat == "" && pathCat != "" && (n.kind == "component" || len(f.roots) == 1 && n == f.roots[0]) {
			cat = pathCat
		}
		if cat != "" {
			pts, meshWorld := findMeshPoints(n, world)
			if len(pts) > 0 && (n.name == "" || !seen[n.name]) {
				if n.name != "" {
					seen[n.name] = true
				}
				addComponent(sc, floorPts, n.name, cat, pts, meshWorld, scale)
				return
			}
		}
		nextSection := inSection || n.name == "Section_grp"
		for _, ch := range n.children {
			walk(ch, world, nextSection)
		}
	}
	for _, r := range f.roots {
		walk(r, usdaIdentity(), false)
	}
}

func findMeshPoints(n *usdaNode, world usdaMat) ([][3]float64, usdaMat) {
	if len(n.points) > 0 {
		return n.points, world
	}
	for _, ch := range n.children {
		cw := usdaMul(ch.mat, world)
		if pts, mw := findMeshPoints(ch, cw); len(pts) > 0 {
			return pts, mw
		}
	}
	return nil, world
}

func addComponent(sc *scan, floorPts *[][2]float64, name, rawCat string, pts [][3]float64, m usdaMat, scale float64) {
	cat := strings.TrimSpace(rawCat)
	lower := strings.ToLower(cat)
	if lower == "floor" {
		for _, p := range pts {
			w := usdaApply(p, m)
			*floorPts = append(*floorPts, [2]float64{w[0] * scale, -w[2] * scale})
		}
		return
	}

	xmin, xmax := pts[0][0], pts[0][0]
	ymin, ymax := pts[0][1], pts[0][1]
	zmin, zmax := pts[0][2], pts[0][2]
	for _, p := range pts[1:] {
		xmin, xmax = min(xmin, p[0]), max(xmax, p[0])
		ymin, ymax = min(ymin, p[1]), max(ymax, p[1])
		zmin, zmax = min(zmin, p[2]), max(zmax, p[2])
	}
	lc := [3]float64{(xmin + xmax) / 2, (ymin + ymax) / 2, (zmin + zmax) / 2}
	wc := usdaApply(lc, m)
	center := [2]float64{wc[0] * scale, -wc[2] * scale}

	sx := hypot3(m[0][0], m[0][1], m[0][2]) * scale
	sy := hypot3(m[1][0], m[1][1], m[1][2]) * scale
	sz := hypot3(m[2][0], m[2][1], m[2][2]) * scale
	lx := (xmax - xmin) * sx
	ly := (ymax - ymin) * sy
	lz := (zmax - zmin) * sz

	// Plan x is world x, plan y is -world z; local +x transforms to (m00, -m02).
	yaw := math.Atan2(-m[0][2], m[0][0]) * 180 / math.Pi
	bottom := wc[1]*scale - ly/2
	top := wc[1]*scale + ly/2
	if top-bottom < 0.02 {
		top = bottom + 0.02
	}

	switch {
	case lower == "wall":
		if lx < 0.02 {
			lx = 0.02
		}
		if lz < 0.02 {
			lz = 0.12
		}
		sc.Walls = append(sc.Walls, scanWall{
			Name: name, Center: center, Size: [2]float64{lx, lz}, Yaw: yaw, Bottom: bottom, Top: top,
		})
	case strings.HasPrefix(lower, "door"):
		open := strings.Contains(lower, "true")
		if lx < 0.04 {
			lx = 0.04
		}
		if lz < 0.04 {
			lz = 0.04
		}
		sc.Doors = append(sc.Doors, scanDoor{
			Name: name, Open: open, Center: center, Size: [2]float64{lx, lz}, Yaw: yaw, Bottom: bottom, Top: top,
		})
	case lower == "window":
		if lx < 0.04 {
			lx = 0.04
		}
		if lz < 0.04 {
			lz = 0.04
		}
		sc.Windows = append(sc.Windows, scanWindow{
			Name: name, Center: center, Size: [2]float64{lx, lz}, Yaw: yaw, Bottom: bottom, Top: top,
		})
	case lower == "opening":
		if lx < 0.04 {
			lx = 0.04
		}
		if lz < 0.04 {
			lz = 0.04
		}
		sc.Openings = append(sc.Openings, scanOpening{
			Name: name, Center: center, Size: [2]float64{lx, lz}, Yaw: yaw, Bottom: bottom, Top: top,
		})
	default:
		oc := normalizeObjectCategory(cat)
		if lx < 0.05 {
			lx = 0.05
		}
		if lz < 0.05 {
			lz = 0.05
		}
		sc.Objects = append(sc.Objects, scanObject{
			Category: oc, Kind: oc, Name: name, Center: center, Size: [2]float64{lx, lz}, Yaw: yaw, Bottom: bottom, Top: top,
		})
	}
}

func hypot3(x, y, z float64) float64 { return math.Sqrt(x*x + y*y + z*z) }

func normalizeObjectCategory(cat string) string {
	switch strings.ToLower(strings.TrimSpace(cat)) {
	case "storage":
		return "storage"
	case "refrigerator":
		return "refrigerator"
	case "stove":
		return "stove"
	case "bed":
		return "bed"
	case "sink":
		return "sink"
	case "washerdryer":
		return "washerdryer"
	case "toilet":
		return "toilet"
	case "bathtub":
		return "bathtub"
	case "oven":
		return "oven"
	case "dishwasher":
		return "dishwasher"
	case "table":
		return "table"
	case "sofa":
		return "sofa"
	case "chair":
		return "chair"
	case "fireplace":
		return "fireplace"
	case "television":
		return "television"
	case "stairs":
		return "stairs"
	default:
		return "storage"
	}
}

// cleanFloorPolygon turns scanned floor points into a simple polygon of 3..64 vertices suitable for ScenePrism.
func cleanFloorPolygon(raw [][2]float64) [][2]float64 {
	if len(raw) < 3 {
		return nil
	}
	// Drop consecutive near-duplicates and collinear fold-backs while preserving concave outlines.
	var ring [][2]float64
	for _, p := range raw {
		if len(ring) > 0 && dist2D(ring[len(ring)-1], p) < 0.01 {
			continue
		}
		ring = append(ring, p)
	}
	for len(ring) >= 3 && dist2D(ring[0], ring[len(ring)-1]) < 0.01 {
		ring = ring[:len(ring)-1]
	}
	ring = pruneCollinear(ring)
	if len(ring) > 64 {
		ring = decimatePolygon(ring, 64)
	}
	if simplePolygon(ring) {
		return ring
	}
	// Extruded floor meshes or triangle soups repeat vertices; fall back to the 2D convex hull.
	hull := convexHull2D(raw)
	if len(hull) > 64 {
		hull = decimatePolygon(hull, 64)
	}
	if simplePolygon(hull) {
		return hull
	}
	return nil
}

func dist2D(a, b [2]float64) float64 { return math.Hypot(b[0]-a[0], b[1]-a[1]) }

func pruneCollinear(pts [][2]float64) [][2]float64 {
	changed := true
	for changed && len(pts) >= 3 {
		changed = false
		var next [][2]float64
		n := len(pts)
		for i := range n {
			a := pts[(i+n-1)%n]
			b := pts[i]
			c := pts[(i+1)%n]
			if dist2D(a, b) < 0.01 {
				changed = true
				continue
			}
			ux, uy := b[0]-a[0], b[1]-a[1]
			vx, vy := c[0]-b[0], c[1]-b[1]
			cross := math.Abs(ux*vy - uy*vx)
			if cross <= 1e-6*math.Hypot(ux, uy)*math.Hypot(vx, vy) {
				// Either straight continuation or a zero-area spike folding back: drop the middle vertex.
				changed = true
				continue
			}
			next = append(next, b)
		}
		pts = next
	}
	return pts
}

func decimatePolygon(pts [][2]float64, limit int) [][2]float64 {
	for len(pts) > limit {
		bestIdx := 0
		bestErr := math.MaxFloat64
		n := len(pts)
		for i := range n {
			a, b, c := pts[(i+n-1)%n], pts[i], pts[(i+1)%n]
			err := math.Abs((b[0]-a[0])*(c[1]-a[1]) - (b[1]-a[1])*(c[0]-a[0]))
			if err < bestErr {
				bestErr, bestIdx = err, i
			}
		}
		pts = append(pts[:bestIdx], pts[bestIdx+1:]...)
	}
	return pts
}

func convexHull2D(raw [][2]float64) [][2]float64 {
	pts := make([][2]float64, len(raw))
	copy(pts, raw)
	sort.Slice(pts, func(i, j int) bool {
		if pts[i][0] != pts[j][0] {
			return pts[i][0] < pts[j][0]
		}
		return pts[i][1] < pts[j][1]
	})
	var uniq [][2]float64
	for _, p := range pts {
		if len(uniq) == 0 || dist2D(uniq[len(uniq)-1], p) >= 0.01 {
			uniq = append(uniq, p)
		}
	}
	if len(uniq) < 3 {
		return nil
	}
	cross := func(o, a, b [2]float64) float64 {
		return (a[0]-o[0])*(b[1]-o[1]) - (a[1]-o[1])*(b[0]-o[0])
	}
	var lower [][2]float64
	for _, p := range uniq {
		for len(lower) >= 2 && cross(lower[len(lower)-2], lower[len(lower)-1], p) <= 1e-6 {
			lower = lower[:len(lower)-1]
		}
		lower = append(lower, p)
	}
	var upper [][2]float64
	for i := len(uniq) - 1; i >= 0; i-- {
		p := uniq[i]
		for len(upper) >= 2 && cross(upper[len(upper)-2], upper[len(upper)-1], p) <= 1e-6 {
			upper = upper[:len(upper)-1]
		}
		upper = append(upper, p)
	}
	return append(lower[:len(lower)-1], upper[:len(upper)-1]...)
}

// simplePolygon checks the exact polygon invariants required by wire.CheckScene (ScenePrism).
func simplePolygon(pts [][2]float64) bool {
	n := len(pts)
	if n < 3 || n > 64 {
		return false
	}
	for _, p := range pts {
		if math.IsNaN(p[0]) || math.IsInf(p[0], 0) || math.IsNaN(p[1]) || math.IsInf(p[1], 0) ||
			p[0] < -500 || p[0] > 500 || p[1] < -500 || p[1] > 500 {
			return false
		}
	}
	at := func(k int) [2]float64 { return pts[((k%n)+n)%n] }
	area := 0.0
	for k := range n {
		a, b, c := at(k), at(k+1), at(k+2)
		if dist2D(a, b) < 0.006 {
			return false
		}
		ux, uy, vx, vy := b[0]-a[0], b[1]-a[1], c[0]-b[0], c[1]-b[1]
		if cross := ux*vy - uy*vx; math.Abs(cross) <= 1e-11*math.Hypot(ux, uy)*math.Hypot(vx, vy) && ux*vx+uy*vy < 0 {
			return false
		}
		area += a[0]*b[1] - b[0]*a[1]
	}
	for i := range n {
		for j := i + 2; j < n; j++ {
			if i == 0 && j == n-1 {
				continue
			}
			if polySegmentsTouch(at(i), at(i+1), at(j), at(j+1)) {
				return false
			}
		}
	}
	return math.Abs(area)/2 >= 2e-4
}

func polySegmentsTouch(a, b, c, d [2]float64) bool {
	orient := func(p, q, r [2]float64) float64 { return (q[0]-p[0])*(r[1]-p[1]) - (q[1]-p[1])*(r[0]-p[0]) }
	on := func(p, q, r [2]float64) bool {
		return min(p[0], q[0]) <= r[0] && r[0] <= max(p[0], q[0]) && min(p[1], q[1]) <= r[1] && r[1] <= max(p[1], q[1])
	}
	sign := func(v float64) int {
		switch {
		case v > 0:
			return 1
		case v < 0:
			return -1
		}
		return 0
	}
	o1, o2, o3, o4 := sign(orient(a, b, c)), sign(orient(a, b, d)), sign(orient(c, d, a)), sign(orient(c, d, b))
	if o1 != o2 && o3 != o4 {
		return true
	}
	return o1 == 0 && on(a, b, c) || o2 == 0 && on(a, b, d) || o3 == 0 && on(c, d, a) || o4 == 0 && on(c, d, b)
}
