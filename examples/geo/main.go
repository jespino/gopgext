// geo is a small PostGIS-style extension: a shape data type (points, line
// strings and polygons in well-known text), measurement functions, bounding
// box operators, an exact distance, and a GiST index (gopgext/gist) that
// answers the bounding box operators and k-nearest-neighbor queries
// (ORDER BY shape <-> query), with the index's bounding boxes as lossy keys
// for the exact distance.
package main

import (
	"encoding/binary"
	"iter"
	"math"
	"strconv"
	"strings"

	"gopgext/gist"
	"gopgext/pg"
)

func main() {
	gist.Register("shape_gist_ops", gist.OpClass{
		Compress:   gistCompress,
		Consistent: gistConsistent,
		Union:      gistUnion,
		Penalty:    gistPenalty,
		PickSplit:  gistPickSplit,
		Distance:   gistDistance,
	})
}

// ---- The shape type --------------------------------------------------

type kind byte

const (
	point kind = iota + 1
	lineString
	polygon
)

var kindNames = map[kind]string{point: "POINT", lineString: "LINESTRING", polygon: "POLYGON"}

// Shape is a point, a line string or a polygon (one ring) in the plane.
// Format: kind byte, 3 bytes unused, uint32 number of points, then x, y
// float64 pairs (little-endian).
//
//gopgext:type input=ShapeIn output=ShapeOut storage=main
type Shape pg.Varlena

type xy struct{ x, y float64 }

func (s Shape) kind() kind { return kind(s[0]) }

func (s Shape) n() int { return int(binary.LittleEndian.Uint32(s[4:])) }

func (s Shape) at(i int) xy {
	b := s[8+16*i:]
	return xy{math.Float64frombits(binary.LittleEndian.Uint64(b)), math.Float64frombits(binary.LittleEndian.Uint64(b[8:]))}
}

func (s Shape) points() []xy {
	ps := make([]xy, s.n())
	for i := range ps {
		ps[i] = s.at(i)
	}
	return ps
}

func makeShape(k kind, ps []xy) Shape {
	s := make(Shape, 8+16*len(ps))
	s[0] = byte(k)
	binary.LittleEndian.PutUint32(s[4:], uint32(len(ps)))
	for i, p := range ps {
		binary.LittleEndian.PutUint64(s[8+16*i:], math.Float64bits(p.x))
		binary.LittleEndian.PutUint64(s[16+16*i:], math.Float64bits(p.y))
	}
	return s
}

func syntaxError(s, detail string) error {
	e := pg.Errorf(pg.CodeInvalidTextRepresentation, "invalid input syntax for type shape: %q", s)
	e.Detail = detail
	return e
}

// parsePoints parses "x y, x y, ...".
func parsePoints(s, list string) ([]xy, error) {
	var ps []xy
	for _, item := range strings.Split(list, ",") {
		f := strings.Fields(item)
		if len(f) != 2 {
			return nil, syntaxError(s, "Points are two numbers separated by spaces.")
		}
		x, err1 := strconv.ParseFloat(f[0], 64)
		y, err2 := strconv.ParseFloat(f[1], 64)
		if err1 != nil || err2 != nil || math.IsNaN(x) || math.IsNaN(y) || math.IsInf(x, 0) || math.IsInf(y, 0) {
			return nil, syntaxError(s, "Coordinates must be finite numbers.")
		}
		ps = append(ps, xy{x, y})
	}
	return ps, nil
}

// ShapeIn parses POINT(x y), LINESTRING(x y, ...) or POLYGON((x y, ...)).
//
//gopgext:function immutable parallel=safe
func ShapeIn(cs pg.CString) (Shape, error) {
	s := string(cs)
	name, rest, ok := strings.Cut(strings.TrimSpace(s), "(")
	if !ok || !strings.HasSuffix(rest, ")") {
		return nil, syntaxError(s, "Expected POINT(...), LINESTRING(...) or POLYGON((...)).")
	}
	body := strings.TrimSuffix(rest, ")")
	var k kind
	for kk, n := range kindNames {
		if strings.EqualFold(strings.TrimSpace(name), n) {
			k = kk
		}
	}
	if k == polygon {
		body = strings.TrimSpace(body)
		if !strings.HasPrefix(body, "(") || !strings.HasSuffix(body, ")") {
			return nil, syntaxError(s, "A polygon is one ring: POLYGON((x y, ...)).")
		}
		body = body[1 : len(body)-1]
	}
	ps, err := parsePoints(s, body)
	if err != nil {
		return nil, err
	}
	switch k {
	case point:
		if len(ps) != 1 {
			return nil, syntaxError(s, "A point has one position.")
		}
	case lineString:
		if len(ps) < 2 {
			return nil, syntaxError(s, "A line string has at least 2 points.")
		}
	case polygon:
		if len(ps) < 4 || ps[0] != ps[len(ps)-1] {
			return nil, syntaxError(s, "A polygon ring is closed and has at least 4 points.")
		}
	default:
		return nil, syntaxError(s, "Expected POINT(...), LINESTRING(...) or POLYGON((...)).")
	}
	return makeShape(k, ps), nil
}

func formatPoints(b []byte, ps []xy) []byte {
	for i, p := range ps {
		if i > 0 {
			b = append(b, ',')
		}
		b = strconv.AppendFloat(b, p.x, 'g', -1, 64)
		b = append(b, ' ')
		b = strconv.AppendFloat(b, p.y, 'g', -1, 64)
	}
	return b
}

// ShapeOut formats a shape as well-known text.
//
//gopgext:function immutable parallel=safe
func ShapeOut(s Shape) pg.CString {
	b := append([]byte(kindNames[s.kind()]), '(')
	if s.kind() == polygon {
		b = append(b, '(')
	}
	b = formatPoints(b, s.points())
	if s.kind() == polygon {
		b = append(b, ')')
	}
	return pg.CString(append(b, ')'))
}

// ---- Functions ---------------------------------------------------------

// StMakePoint returns the point (x, y).
//
//gopgext:function name=st_makepoint immutable parallel=safe
func StMakePoint(x, y float64) Shape { return makeShape(point, []xy{{x, y}}) }

// StGeometryType returns ST_Point, ST_LineString or ST_Polygon.
//
//gopgext:function name=st_geometrytype immutable parallel=safe
func StGeometryType(s Shape) string {
	return map[kind]string{point: "ST_Point", lineString: "ST_LineString", polygon: "ST_Polygon"}[s.kind()]
}

// StNPoints returns the number of points.
//
//gopgext:function name=st_npoints immutable parallel=safe
func StNPoints(s Shape) int32 { return int32(s.n()) }

// StX returns the x coordinate of a point (NULL for other shapes).
//
//gopgext:function name=st_x immutable parallel=safe
func StX(s Shape) *float64 {
	if s.kind() != point {
		return nil
	}
	x := s.at(0).x
	return &x
}

// StY returns the y coordinate of a point (NULL for other shapes).
//
//gopgext:function name=st_y immutable parallel=safe
func StY(s Shape) *float64 {
	if s.kind() != point {
		return nil
	}
	y := s.at(0).y
	return &y
}

// StArea returns the area of a polygon (0 for other shapes).
//
//gopgext:function name=st_area immutable parallel=safe
func StArea(s Shape) float64 {
	if s.kind() != polygon {
		return 0
	}
	ps := s.points()
	var a float64
	for i := 0; i+1 < len(ps); i++ {
		a += ps[i].x*ps[i+1].y - ps[i+1].x*ps[i].y
	}
	return math.Abs(a) / 2
}

func pathLength(ps []xy) float64 {
	var l float64
	for i := 0; i+1 < len(ps); i++ {
		l += math.Hypot(ps[i+1].x-ps[i].x, ps[i+1].y-ps[i].y)
	}
	return l
}

// StLength returns the length of a line string (0 for other shapes).
//
//gopgext:function name=st_length immutable parallel=safe
func StLength(s Shape) float64 {
	if s.kind() != lineString {
		return 0
	}
	return pathLength(s.points())
}

// StPerimeter returns the perimeter of a polygon (0 for other shapes).
//
//gopgext:function name=st_perimeter immutable parallel=safe
func StPerimeter(s Shape) float64 {
	if s.kind() != polygon {
		return 0
	}
	return pathLength(s.points())
}

// Box is a bounding box.
type Box struct {
	XMin float64 `pg:"xmin"`
	YMin float64 `pg:"ymin"`
	XMax float64 `pg:"xmax"`
	YMax float64 `pg:"ymax"`
}

func (s Shape) box() Box {
	b := Box{math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)}
	for i := range s.n() {
		p := s.at(i)
		b.XMin, b.YMin = min(b.XMin, p.x), min(b.YMin, p.y)
		b.XMax, b.YMax = max(b.XMax, p.x), max(b.YMax, p.y)
	}
	return b
}

// ShapeBBox returns the bounding box of a shape, as a row.
//
//gopgext:function name=shape_bbox immutable parallel=safe
func ShapeBBox(s Shape) Box { return s.box() }

// StEnvelope returns the bounding box of a shape as a shape (a point for a
// point).
//
//gopgext:function name=st_envelope immutable parallel=safe
func StEnvelope(s Shape) Shape {
	b := s.box()
	if b.XMin == b.XMax && b.YMin == b.YMax {
		return makeShape(point, []xy{{b.XMin, b.YMin}})
	}
	return makeShape(polygon, []xy{{b.XMin, b.YMin}, {b.XMin, b.YMax}, {b.XMax, b.YMax}, {b.XMax, b.YMin}, {b.XMin, b.YMin}})
}

// Coordinates is a point of a point.
type Coordinates struct {
	X float64
	Y float64
}

// StPointXY returns the coordinates of a point, NULL for other shapes.
//
//gopgext:function name=st_pointxy immutable parallel=safe
func StPointXY(s Shape) *Coordinates {
	if s.kind() != point {
		return nil
	}
	p := s.at(0)
	return &Coordinates{p.x, p.y}
}

// DumpedPoint is a row of st_dumppoints.
type DumpedPoint struct {
	Path int32
	X    float64
	Y    float64
}

// StDumpPoints returns the points of a shape, numbered from 1.
//
//gopgext:function name=st_dumppoints immutable parallel=safe
func StDumpPoints(s Shape) iter.Seq[DumpedPoint] {
	return func(yield func(DumpedPoint) bool) {
		for i := range s.n() {
			p := s.at(i)
			if !yield(DumpedPoint{int32(i + 1), p.x, p.y}) {
				return
			}
		}
	}
}

// ---- Distance ------------------------------------------------------------

// segments returns the segments of a shape (a point is a degenerate one).
func (s Shape) segments() [][2]xy {
	ps := s.points()
	if len(ps) == 1 {
		return [][2]xy{{ps[0], ps[0]}}
	}
	segs := make([][2]xy, 0, len(ps)-1)
	for i := 0; i+1 < len(ps); i++ {
		segs = append(segs, [2]xy{ps[i], ps[i+1]})
	}
	return segs
}

func pointSegmentDistance(p xy, s [2]xy) float64 {
	dx, dy := s[1].x-s[0].x, s[1].y-s[0].y
	t := 0.0
	if l := dx*dx + dy*dy; l > 0 {
		t = max(0, min(1, ((p.x-s[0].x)*dx+(p.y-s[0].y)*dy)/l))
	}
	return math.Hypot(p.x-(s[0].x+t*dx), p.y-(s[0].y+t*dy))
}

func cross(o, a, b xy) float64 { return (a.x-o.x)*(b.y-o.y) - (a.y-o.y)*(b.x-o.x) }

func segmentsIntersect(a, b [2]xy) bool {
	d1, d2 := cross(b[0], b[1], a[0]), cross(b[0], b[1], a[1])
	d3, d4 := cross(a[0], a[1], b[0]), cross(a[0], a[1], b[1])
	return ((d1 > 0 && d2 < 0) || (d1 < 0 && d2 > 0)) && ((d3 > 0 && d4 < 0) || (d3 < 0 && d4 > 0))
}

// inside reports whether p is inside polygon s (even-odd rule).
func (s Shape) inside(p xy) bool {
	ps := s.points()
	in := false
	for i, j := 0, len(ps)-1; i < len(ps); j, i = i, i+1 {
		if (ps[i].y > p.y) != (ps[j].y > p.y) &&
			p.x < (ps[j].x-ps[i].x)*(p.y-ps[i].y)/(ps[j].y-ps[i].y)+ps[i].x {
			in = !in
		}
	}
	return in
}

// StDistance returns the minimum distance between two shapes (0 if they
// intersect).
//
//gopgext:function name=st_distance immutable parallel=safe
func StDistance(a, b Shape) float64 {
	if (a.kind() == polygon && a.inside(b.at(0))) || (b.kind() == polygon && b.inside(a.at(0))) {
		return 0
	}
	d := math.Inf(1)
	for _, sa := range a.segments() {
		for _, sb := range b.segments() {
			if segmentsIntersect(sa, sb) {
				return 0
			}
			d = min(d, pointSegmentDistance(sa[0], sb), pointSegmentDistance(sa[1], sb),
				pointSegmentDistance(sb[0], sa), pointSegmentDistance(sb[1], sa))
		}
	}
	return d
}

// StDWithin reports whether two shapes are within distance d.
//
//gopgext:function name=st_dwithin immutable parallel=safe
func StDWithin(a, b Shape, d float64) bool { return StDistance(a, b) <= d }

// ---- Bounding box operators ------------------------------------------------

func (b Box) overlaps(o Box) bool {
	return b.XMin <= o.XMax && o.XMin <= b.XMax && b.YMin <= o.YMax && o.YMin <= b.YMax
}

func (b Box) contains(o Box) bool {
	return b.XMin <= o.XMin && o.XMax <= b.XMax && b.YMin <= o.YMin && o.YMax <= b.YMax
}

// distance is the distance between two boxes (0 if they overlap).
func (b Box) distance(o Box) float64 {
	dx := max(0, o.XMin-b.XMax, b.XMin-o.XMax)
	dy := max(0, o.YMin-b.YMax, b.YMin-o.YMax)
	return math.Hypot(dx, dy)
}

// ShapeOverlaps is the && operator: do the bounding boxes overlap?
//
//gopgext:function immutable parallel=safe
func ShapeOverlaps(a, b Shape) bool { return a.box().overlaps(b.box()) }

// ShapeContains is the ~ operator: does a's bounding box contain b's?
//
//gopgext:function immutable parallel=safe
func ShapeContains(a, b Shape) bool { return a.box().contains(b.box()) }

// ShapeWithin is the @ operator: is a's bounding box within b's?
//
//gopgext:function immutable parallel=safe
func ShapeWithin(a, b Shape) bool { return b.box().contains(a.box()) }

// ---- GiST: keys are bounding boxes (4 float64) -----------------------------

func boxKey(b Box) []byte {
	k := make([]byte, 32)
	for i, v := range []float64{b.XMin, b.YMin, b.XMax, b.YMax} {
		binary.LittleEndian.PutUint64(k[8*i:], math.Float64bits(v))
	}
	return k
}

func keyBox(k []byte) Box {
	f := func(i int) float64 { return math.Float64frombits(binary.LittleEndian.Uint64(k[8*i:])) }
	return Box{f(0), f(1), f(2), f(3)}
}

func (b Box) union(o Box) Box {
	return Box{min(b.XMin, o.XMin), min(b.YMin, o.YMin), max(b.XMax, o.XMax), max(b.YMax, o.YMax)}
}

func (b Box) area() float64   { return (b.XMax - b.XMin) * (b.YMax - b.YMin) }
func (b Box) margin() float64 { return (b.XMax - b.XMin) + (b.YMax - b.YMin) }

func gistCompress(value []byte) ([]byte, error) { return boxKey(Shape(value).box()), nil }

const (
	strategyOverlap     = 3  // &&
	strategyContains    = 7  // ~
	strategyContainedBy = 8  // @
	strategyDistance    = 15 // <->
)

func gistConsistent(key, query []byte, strategy int, leaf bool) (match, recheck bool) {
	k, q := keyBox(key), Shape(query).box()
	switch strategy {
	case strategyOverlap:
		return k.overlaps(q), false
	case strategyContains:
		return k.contains(q), false
	case strategyContainedBy:
		if leaf {
			return q.contains(k), false
		}
		return k.overlaps(q), false // some boxes below may be within q
	}
	return false, false
}

func gistUnion(keys [][]byte) []byte {
	u := keyBox(keys[0])
	for _, k := range keys[1:] {
		u = u.union(keyBox(k))
	}
	return boxKey(u)
}

// gistPenalty is the growth of the box's area (and of its margin, which
// tells boxes of points on a line apart).
func gistPenalty(key, added []byte) float64 {
	k := keyBox(key)
	u := k.union(keyBox(added))
	return (u.area() - k.area()) + (u.margin() - k.margin())
}

// gistPickSplit sorts the boxes along the axis where their centers spread
// most, and splits them in the middle.
func gistPickSplit(keys [][]byte) (left, right []int) {
	type entry struct {
		i      int
		cx, cy float64
	}
	es := make([]entry, len(keys))
	var lo, hi = xy{math.Inf(1), math.Inf(1)}, xy{math.Inf(-1), math.Inf(-1)}
	for i, k := range keys {
		b := keyBox(k)
		es[i] = entry{i, (b.XMin + b.XMax) / 2, (b.YMin + b.YMax) / 2}
		lo.x, lo.y = min(lo.x, es[i].cx), min(lo.y, es[i].cy)
		hi.x, hi.y = max(hi.x, es[i].cx), max(hi.y, es[i].cy)
	}
	byX := hi.x-lo.x >= hi.y-lo.y
	sortEntries(es, func(a, b entry) bool {
		if byX {
			return a.cx < b.cx
		}
		return a.cy < b.cy
	})
	for j, e := range es {
		if j < len(es)/2 {
			left = append(left, e.i)
		} else {
			right = append(right, e.i)
		}
	}
	return left, right
}

func sortEntries[T any](xs []T, less func(a, b T) bool) {
	// Insertion sort is enough for a page of keys, and avoids the reflection
	// of sort.Slice.
	for i := 1; i < len(xs); i++ {
		for j := i; j > 0 && less(xs[j], xs[j-1]); j-- {
			xs[j], xs[j-1] = xs[j-1], xs[j]
		}
	}
}

// gistDistance is the distance between bounding boxes: a lower bound of
// the exact distance, which PostgreSQL computes for the rows (recheck).
func gistDistance(key, query []byte, strategy int, leaf bool) (float64, bool) {
	return keyBox(key).distance(Shape(query).box()), true
}
