// quadtree is a point type with an SP-GiST quadtree index written in Go
// (gopgext/spgist), like PostgreSQL's quad_point_ops: each inner tuple's
// prefix is a centroid splitting its points into four quadrants. The index
// answers point equality (~=), containment in a rectangle (<@) and k-nearest
// neighbor queries (ORDER BY p <-> q), and supports index-only scans (the
// leaves hold the points).
package main

import (
	"encoding/binary"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"gopgext/pg"
	"gopgext/spgist"
)

func main() {
	spgist.Register("pt_quad_ops", spgist.OpClass{
		Prefixes:        true,
		CanReturnData:   true,
		Choose:          choose,
		PickSplit:       pickSplit,
		InnerConsistent: innerConsistent,
		LeafConsistent:  leafConsistent,
	})
}

// ---- Types -------------------------------------------------------------

// Pt is a point: x, y (float64, little-endian).
//
//gopgext:type input=PtIn output=PtOut storage=plain
type Pt pg.Varlena

// Rect is a rectangle: xmin, ymin, xmax, ymax.
//
//gopgext:type input=RectIn output=RectOut storage=plain
type Rect pg.Varlena

type point struct{ x, y float64 }

type box struct{ xmin, ymin, xmax, ymax float64 }

func f64(b []byte, i int) float64 { return math.Float64frombits(binary.LittleEndian.Uint64(b[8*i:])) }

func encode(fs ...float64) []byte {
	b := make([]byte, 8*len(fs))
	for i, f := range fs {
		binary.LittleEndian.PutUint64(b[8*i:], math.Float64bits(f))
	}
	return b
}

func toPoint(b []byte) point { return point{f64(b, 0), f64(b, 1)} }
func toBox(b []byte) box     { return box{f64(b, 0), f64(b, 1), f64(b, 2), f64(b, 3)} }

func (p point) bytes() []byte { return encode(p.x, p.y) }
func (b box) bytes() []byte   { return encode(b.xmin, b.ymin, b.xmax, b.ymax) }

func syntaxError(typ, s string) error {
	return pg.Errorf(pg.CodeInvalidTextRepresentation, "invalid input syntax for type %s: %q", typ, s)
}

// parseNumbers parses "(a,b)" pairs separated by commas.
func parseNumbers(typ, s string, n int) ([]float64, error) {
	clean := strings.NewReplacer("(", " ", ")", " ", ",", " ").Replace(s)
	f := strings.Fields(clean)
	if len(f) != n || strings.Count(s, "(") != n/2 || strings.Count(s, ")") != n/2 {
		return nil, syntaxError(typ, s)
	}
	out := make([]float64, n)
	for i, x := range f {
		v, err := strconv.ParseFloat(x, 64)
		if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
			return nil, syntaxError(typ, s)
		}
		out[i] = v
	}
	return out, nil
}

func formatFloat(f float64) string { return strconv.FormatFloat(f, 'g', -1, 64) }

// PtIn parses "(x,y)".
//
//gopgext:function immutable parallel=safe
func PtIn(s pg.CString) (Pt, error) {
	f, err := parseNumbers("pt", string(s), 2)
	if err != nil {
		return nil, err
	}
	return Pt(encode(f...)), nil
}

// PtOut formats "(x,y)".
//
//gopgext:function immutable parallel=safe
func PtOut(p Pt) pg.CString {
	q := toPoint(p)
	return pg.CString("(" + formatFloat(q.x) + "," + formatFloat(q.y) + ")")
}

// RectIn parses "(x1,y1),(x2,y2)" (any two opposite corners).
//
//gopgext:function immutable parallel=safe
func RectIn(s pg.CString) (Rect, error) {
	f, err := parseNumbers("rect", string(s), 4)
	if err != nil {
		return nil, err
	}
	return Rect(box{min(f[0], f[2]), min(f[1], f[3]), max(f[0], f[2]), max(f[1], f[3])}.bytes()), nil
}

// RectOut formats "(xmin,ymin),(xmax,ymax)".
//
//gopgext:function immutable parallel=safe
func RectOut(r Rect) pg.CString {
	b := toBox(r)
	return pg.CString(fmt.Sprintf("(%s,%s),(%s,%s)", formatFloat(b.xmin), formatFloat(b.ymin), formatFloat(b.xmax), formatFloat(b.ymax)))
}

// MakePt returns the point (x, y).
//
//gopgext:function name=pt immutable parallel=safe
func MakePt(x, y float64) Pt { return Pt(encode(x, y)) }

// ---- Operators -------------------------------------------------------------

func (b box) contains(p point) bool {
	return b.xmin <= p.x && p.x <= b.xmax && b.ymin <= p.y && p.y <= b.ymax
}

func (b box) overlaps(o box) bool {
	return b.xmin <= o.xmax && o.xmin <= b.xmax && b.ymin <= o.ymax && o.ymin <= b.ymax
}

// distance from p to the box (0 inside).
func (b box) distance(p point) float64 {
	dx := max(0, b.xmin-p.x, p.x-b.xmax)
	dy := max(0, b.ymin-p.y, p.y-b.ymax)
	return math.Hypot(dx, dy)
}

func (p point) distance(q point) float64 { return math.Hypot(p.x-q.x, p.y-q.y) }

// PtSame is the ~= operator.
//
//gopgext:function immutable parallel=safe
func PtSame(a, b Pt) bool { return toPoint(a) == toPoint(b) }

// PtWithin is the <@ operator: is the point in the rectangle (borders
// included)?
//
//gopgext:function immutable parallel=safe
func PtWithin(p Pt, r Rect) bool { return toBox(r).contains(toPoint(p)) }

// PtDistance is the <-> operator.
//
//gopgext:function immutable parallel=safe
func PtDistance(a, b Pt) float64 { return toPoint(a).distance(toPoint(b)) }

// ---- SP-GiST quadtree --------------------------------------------------------

const (
	strategySame        = 6  // ~=
	strategyContainedBy = 8  // <@
	strategyDistance    = 15 // <->
)

// quadrant returns the node of p under centroid c: bit 0 is right of c,
// bit 1 above it (points on a dividing line go left/below).
func quadrant(c, p point) int {
	q := 0
	if p.x > c.x {
		q |= 1
	}
	if p.y > c.y {
		q |= 2
	}
	return q
}

func choose(in spgist.ChooseIn) spgist.ChooseOut {
	if in.Inner.AllTheSame {
		return spgist.ChooseOut{Action: spgist.MatchNode} // SP-GiST picks the node
	}
	return spgist.ChooseOut{Action: spgist.MatchNode, Node: quadrant(toPoint(in.Inner.Prefix), toPoint(in.Leaf))}
}

// pickSplit uses the median point of the coordinates as the centroid.
func pickSplit(in spgist.PickSplitIn) spgist.PickSplitOut {
	xs := make([]float64, len(in.Leaves))
	ys := make([]float64, len(in.Leaves))
	for i, l := range in.Leaves {
		p := toPoint(l)
		xs[i], ys[i] = p.x, p.y
	}
	sort.Float64s(xs)
	sort.Float64s(ys)
	c := point{xs[len(xs)/2], ys[len(ys)/2]}
	nodes := make([]int, len(in.Leaves))
	for i, l := range in.Leaves {
		nodes[i] = quadrant(c, toPoint(l))
	}
	return spgist.PickSplitOut{Prefix: c.bytes(), NNodes: 4, Nodes: nodes}
}

var everywhere = box{math.Inf(-1), math.Inf(-1), math.Inf(1), math.Inf(1)}

// child returns the region of quadrant q of region r split at c (borders
// included on both sides: a superset is fine).
func child(r box, c point, q int) box {
	if q&1 != 0 {
		r.xmin = max(r.xmin, c.x)
	} else {
		r.xmax = min(r.xmax, c.x)
	}
	if q&2 != 0 {
		r.ymin = max(r.ymin, c.y)
	} else {
		r.ymax = min(r.ymax, c.y)
	}
	return r
}

// innerConsistent visits the quadrants whose region can hold matches,
// carrying the region down as the traversal value.
func innerConsistent(scan spgist.Scan, inner spgist.Inner) []spgist.Visit {
	region := everywhere
	if scan.Traversal != nil {
		region = toBox(scan.Traversal)
	}
	c := toPoint(inner.Prefix)
	var visits []spgist.Visit
	for n := 0; n < inner.NNodes; n++ {
		r := region
		if !inner.AllTheSame {
			r = child(region, c, n)
		}
		ok := true
		for _, k := range scan.Keys {
			switch k.Strategy {
			case strategySame:
				ok = ok && r.contains(toPoint(k.Query))
			case strategyContainedBy:
				ok = ok && r.overlaps(toBox(k.Query))
			}
		}
		if !ok {
			continue
		}
		v := spgist.Visit{Node: n, Traversal: r.bytes()}
		for _, o := range scan.OrderBys {
			v.Distances = append(v.Distances, r.distance(toPoint(o.Query)))
		}
		visits = append(visits, v)
	}
	return visits
}

func leafConsistent(scan spgist.Scan, leaf []byte) spgist.LeafOut {
	p := toPoint(leaf)
	for _, k := range scan.Keys {
		switch k.Strategy {
		case strategySame:
			if p != toPoint(k.Query) {
				return spgist.LeafOut{}
			}
		case strategyContainedBy:
			if !toBox(k.Query).contains(p) {
				return spgist.LeafOut{}
			}
		}
	}
	out := spgist.LeafOut{Match: true}
	for _, o := range scan.OrderBys {
		out.Distances = append(out.Distances, p.distance(toPoint(o.Query)))
	}
	return out
}
