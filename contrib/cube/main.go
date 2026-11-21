// cube is a Go port of PostgreSQL's contrib/cube (version 1.5): the cube
// data type for multi-dimensional boxes and points, with its functions,
// operators, and btree and GiST operator classes.
//
// Values have upstream's binary layout (the varlena of struct NDBOX: a
// header with the number of dimensions and the point flag, then float8
// coordinates), so data written by the C module reads back unchanged, and
// the text and binary (send/receive) formats are the same.
//
// The SQL objects other than the type and its I/O functions are upstream's
// install scripts merged into sql/cube.sql; they reference the functions
// below by upstream's C symbol names.
//
// Ported from PostgreSQL 18's contrib/cube (cube.c, cubeparse.y,
// cubescan.l).
package main

import (
	"encoding/binary"
	"math"

	"gopgext/gist"
	"gopgext/pg"
)

func main() {
	gist.Register("gist_cube_ops", gist.OpClass{
		Consistent: gistConsistent,
		Union:      gistUnion,
		Penalty:    gistPenalty,
		PickSplit:  gistPickSplit,
		Same:       gistSame,
		Distance:   gistDistance,
	})
}

// maxDim is CUBE_MAX_DIM.
const maxDim = 100

const (
	pointBit = 0x80000000
	dimMask  = 0x7fffffff
)

// Cube is a multi-dimensional cube (NDBOX without its varlena header): a
// uint32 header (bits 0-30 the number of dimensions, bit 31 the point
// flag), then the lower-left coordinates and, unless it's a point, the
// upper-right ones (float64, native byte order: little-endian).
//
//gopgext:type input=CubeIn output=CubeOut receive=CubeRecv send=CubeSend alignment=double
type Cube pg.Varlena

func (c Cube) header() uint32 { return binary.LittleEndian.Uint32(c) }

func (c Cube) dim() int { return int(c.header() & dimMask) }

func (c Cube) isPoint() bool { return c.header()&pointBit != 0 }

// x is the i-th stored coordinate (cube->x[i]).
func (c Cube) x(i int) float64 {
	return math.Float64frombits(binary.LittleEndian.Uint64(c[4+8*i:]))
}

func (c Cube) ll(i int) float64 { return c.x(i) }

func (c Cube) ur(i int) float64 {
	if c.isPoint() {
		return c.x(i)
	}
	return c.x(i + c.dim())
}

// newCube returns a cube of dim dimensions: a point (dim coordinates) or a
// box (2*dim), all zero.
func newCube(dim int, point bool) Cube {
	n := 2 * dim
	h := uint32(dim)
	if point {
		n, h = dim, h|pointBit
	}
	c := make(Cube, 4+8*n)
	binary.LittleEndian.PutUint32(c, h)
	return c
}

func (c Cube) set(i int, v float64) {
	binary.LittleEndian.PutUint64(c[4+8*i:], math.Float64bits(v))
}

// toPoint returns box c (2*dim coordinates) as a point if it is one: the
// point flag set and the upper-right coordinates dropped.
func (c Cube) toPoint() Cube {
	if !c.isPointInternal() {
		return c
	}
	d := c.dim()
	binary.LittleEndian.PutUint32(c, uint32(d)|pointBit)
	return c[:4+8*d]
}

// isPointInternal is cube_is_point_internal: the point flag, or all
// lower-left coordinates equal to the upper-right ones.
func (c Cube) isPointInternal() bool {
	if c.isPoint() {
		return true
	}
	for i := range c.dim() {
		if c.ll(i) != c.ur(i) {
			return false
		}
	}
	return true
}

// cmin and cmax are C's Min and Max macros (which differ from math.Min and
// math.Max for NaN and signed zeros).
func cmin(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

func cmax(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

func tooManyDims(msg string) error {
	return &pg.Error{Code: "54000", Message: msg,
		Detail: "A cube cannot have more than 100 dimensions."}
}

func arrayError(msg string) error { return &pg.Error{Code: "2202E", Message: msg} }

// ---- Input and output ------------------------------------------------

// CubeIn parses '(x1, ..., xN), (y1, ..., yN)' and the other forms.
//
//gopgext:function immutable parallel=safe symbol=cube_in
func CubeIn(s pg.CString) (Cube, error) { return parseCube(string(s)) }

// CubeOut formats a cube like float8 does its coordinates.
//
//gopgext:function immutable parallel=safe symbol=cube_out
func CubeOut(c Cube) pg.CString {
	dim := c.dim()
	buf := make([]byte, 0, 2+dim*8)
	buf = append(buf, '(')
	for i := range dim {
		if i > 0 {
			buf = append(buf, ", "...)
		}
		buf = append(buf, pg.FormatFloat8(c.ll(i))...)
	}
	buf = append(buf, ')')
	if !c.isPointInternal() {
		buf = append(buf, ",("...)
		for i := range dim {
			if i > 0 {
				buf = append(buf, ", "...)
			}
			buf = append(buf, pg.FormatFloat8(c.ur(i))...)
		}
		buf = append(buf, ')')
	}
	return pg.CString(buf)
}

// CubeSend writes the binary format: the header, then the stored
// coordinates (network byte order).
//
//gopgext:function immutable parallel=safe symbol=cube_send
func CubeSend(c Cube) []byte {
	n := c.dim()
	if !c.isPoint() {
		n += n
	}
	b := make([]byte, 0, 4+8*n)
	b = binary.BigEndian.AppendUint32(b, c.header())
	for i := range n {
		b = binary.BigEndian.AppendUint64(b, math.Float64bits(c.x(i)))
	}
	return b
}

// CubeRecv reads the binary format.
//
//gopgext:function immutable parallel=safe symbol=cube_recv
func CubeRecv(buf pg.StringInfo) (Cube, error) {
	h := uint32(buf.ReadInt32())
	n := int(h & dimMask)
	if n > maxDim {
		return nil, tooManyDims("cube dimension is too large")
	}
	if h&pointBit == 0 {
		n += n
	}
	c := make(Cube, 4+8*n)
	binary.LittleEndian.PutUint32(c, h)
	for i := range n {
		c.set(i, buf.ReadFloat64())
	}
	return c, nil
}

// ---- Constructors ----------------------------------------------------

// noNulls returns xs without NULLs, or upstream's error.
func noNulls(xs ...[]*float64) ([][]float64, error) {
	out := make([][]float64, len(xs))
	for k, a := range xs {
		out[k] = make([]float64, len(a))
		for i, x := range a {
			if x == nil {
				return nil, arrayError("cannot work with arrays containing NULLs")
			}
			out[k][i] = *x
		}
	}
	return out, nil
}

// CubeFromArrays is cube(float8[], float8[]): a box from its corners.
//
//gopgext:function symbol=cube_a_f8_f8 manual
func CubeFromArrays(urArg, llArg []*float64) (Cube, error) {
	xs, err := noNulls(urArg, llArg)
	if err != nil {
		return nil, err
	}
	ur, ll := xs[0], xs[1]
	dim := len(ur)
	if dim > maxDim {
		return nil, tooManyDims("can't extend cube")
	}
	if len(ll) != dim {
		return nil, arrayError("UR and LL arrays must be of same length")
	}
	point := true
	for i := range dim {
		if ur[i] != ll[i] {
			point = false
			break
		}
	}
	c := newCube(dim, point)
	for i := range dim {
		c.set(i, ur[i])
		if !point {
			c.set(i+dim, ll[i])
		}
	}
	return c, nil
}

// CubeFromArray is cube(float8[]): a point.
//
//gopgext:function symbol=cube_a_f8 manual
func CubeFromArray(urArg []*float64) (Cube, error) {
	xs, err := noNulls(urArg)
	if err != nil {
		return nil, err
	}
	ur := xs[0]
	if len(ur) > maxDim {
		return nil, tooManyDims("array is too long")
	}
	c := newCube(len(ur), true)
	for i, x := range ur {
		c.set(i, x)
	}
	return c, nil
}

// CubeSubset is cube_subset(cube, int4[]): the given dimensions, in order.
//
//gopgext:function symbol=cube_subset manual
func CubeSubset(c Cube, idxArg []*int32) (Cube, error) {
	idx := make([]int32, len(idxArg))
	for i, x := range idxArg {
		if x == nil {
			return nil, arrayError("cannot work with arrays containing NULLs")
		}
		idx[i] = *x
	}
	dim := len(idx)
	if dim > maxDim {
		return nil, tooManyDims("array is too long")
	}
	r := newCube(dim, c.isPoint())
	for i, d := range idx {
		if d <= 0 || int(d) > c.dim() {
			return nil, arrayError("Index out of bounds")
		}
		r.set(i, c.x(int(d)-1))
		if !c.isPoint() {
			r.set(i+dim, c.x(int(d)+c.dim()-1))
		}
	}
	return r, nil
}

// CubeF8 is cube(float8): a one-dimensional point.
//
//gopgext:function symbol=cube_f8 manual
func CubeF8(x float64) Cube {
	c := newCube(1, true)
	c.set(0, x)
	return c
}

// CubeF8F8 is cube(float8, float8): a one-dimensional box.
//
//gopgext:function symbol=cube_f8_f8 manual
func CubeF8F8(x0, x1 float64) Cube {
	if x0 == x1 {
		return CubeF8(x0)
	}
	c := newCube(1, false)
	c.set(0, x0)
	c.set(1, x1)
	return c
}

// CubeCF8 is cube(cube, float8): c with one more dimension, x to x.
//
//gopgext:function symbol=cube_c_f8 manual
func CubeCF8(c Cube, x float64) (Cube, error) {
	d := c.dim()
	if d+1 > maxDim {
		return nil, tooManyDims("can't extend cube")
	}
	if c.isPoint() {
		r := newCube(d+1, true)
		for i := range d {
			r.set(i, c.x(i))
		}
		r.set(d, x)
		return r, nil
	}
	r := newCube(d+1, false)
	for i := range d {
		r.set(i, c.x(i))
		r.set(d+1+i, c.x(d+i))
	}
	r.set(d, x)
	r.set(2*(d+1)-1, x)
	return r, nil
}

// CubeCF8F8 is cube(cube, float8, float8): c with one more dimension, x1
// to x2.
//
//gopgext:function symbol=cube_c_f8_f8 manual
func CubeCF8F8(c Cube, x1, x2 float64) (Cube, error) {
	d := c.dim()
	if d+1 > maxDim {
		return nil, tooManyDims("can't extend cube")
	}
	if c.isPoint() && x1 == x2 {
		r := newCube(d+1, true)
		for i := range d {
			r.set(i, c.x(i))
		}
		r.set(d, x1)
		return r, nil
	}
	r := newCube(d+1, false)
	for i := range d {
		r.set(i, c.ll(i))
		r.set(d+1+i, c.ur(i))
	}
	r.set(d, x1)
	r.set(2*(d+1)-1, x2)
	return r, nil
}

// ---- Accessors -------------------------------------------------------

// CubeDim is cube_dim(cube).
//
//gopgext:function symbol=cube_dim manual
func CubeDim(c Cube) int32 { return int32(c.dim()) }

// CubeIsPoint is cube_is_point(cube).
//
//gopgext:function symbol=cube_is_point manual
func CubeIsPoint(c Cube) bool { return c.isPointInternal() }

// CubeLLCoord is cube_ll_coord(cube, int4): the n-th lower coordinate.
//
//gopgext:function symbol=cube_ll_coord manual
func CubeLLCoord(c Cube, n int32) float64 {
	if int32(c.dim()) >= n && n > 0 {
		return cmin(c.ll(int(n-1)), c.ur(int(n-1)))
	}
	return 0
}

// CubeURCoord is cube_ur_coord(cube, int4): the n-th upper coordinate.
//
//gopgext:function symbol=cube_ur_coord manual
func CubeURCoord(c Cube, n int32) float64 {
	if int32(c.dim()) >= n && n > 0 {
		return cmax(c.ll(int(n-1)), c.ur(int(n-1)))
	}
	return 0
}

// CubeCoord is cube_coord(cube, int4) (the -> operator): coordinates 1 to
// N of the first corner, N+1 to 2N of the second.
//
//gopgext:function symbol=cube_coord manual
func CubeCoord(c Cube, coord int32) (float64, error) {
	d := int32(c.dim())
	if coord <= 0 || coord > 2*d {
		return 0, arrayError("cube index " + itoa(coord) + " is out of bounds")
	}
	if c.isPoint() {
		return c.x(int((coord - 1) % d)), nil
	}
	return c.x(int(coord - 1)), nil
}

// CubeCoordLLUR is cube_coord_llur(cube, int4) (the ~> operator): 2N-1 is
// the lower bound of dimension N, 2N its upper bound, negative ones their
// negatives; 0 beyond the cube's dimensions.
//
//gopgext:function symbol=cube_coord_llur manual
func CubeCoordLLUR(c Cube, coord int32) (float64, error) {
	if coord == 0 {
		return 0, arrayError("zero cube index is not defined")
	}
	return coordLLUR(c, coord, true), nil
}

// coordLLUR is the ~> value for coord != 0; on an inner GiST key (leaf
// false), the bound that is a lower bound of the values below.
func coordLLUR(c Cube, coord int32, leaf bool) float64 {
	inverse := false
	if coord < 0 {
		coord, inverse = -coord, true
	}
	var r float64
	if int(coord) <= 2*c.dim() {
		index := int(coord-1) / 2
		upper := (coord-1)%2 == 1
		switch {
		case c.isPoint():
			r = c.x(index)
		case leaf && upper, !leaf && inverse:
			r = cmax(c.x(index), c.x(index+c.dim()))
		default:
			r = cmin(c.x(index), c.x(index+c.dim()))
		}
	}
	if inverse {
		r = -r
	}
	return r
}

func itoa(n int32) string {
	if n == math.MinInt32 {
		return "-2147483648"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [11]byte
	i := len(b)
	for {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
		if n == 0 {
			break
		}
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

// CubeEnlarge is cube_enlarge(cube, float8, int4): c grown by r in each
// direction, in at least n dimensions (shrunk for r < 0).
//
//gopgext:function symbol=cube_enlarge manual
func CubeEnlarge(a Cube, r float64, n int32) Cube {
	if n > maxDim {
		n = maxDim
	}
	dim := 0
	if r > 0 && n > 0 {
		dim = int(n)
	}
	if a.dim() > dim {
		dim = a.dim()
	}
	res := newCube(dim, false)
	i, j := 0, dim
	for ; i < a.dim(); i, j = i+1, j+1 {
		if a.ll(i) >= a.ur(i) {
			res.set(i, a.ur(i)-r)
			res.set(j, a.ll(i)+r)
		} else {
			res.set(i, a.ll(i)-r)
			res.set(j, a.ur(i)+r)
		}
		if res.x(i) > res.x(j) {
			res.set(i, (res.x(i)+res.x(j))/2)
			res.set(j, res.x(i))
		}
	}
	for ; i < dim; i, j = i+1, j+1 {
		res.set(i, -r)
		res.set(j, r)
	}
	return res.toPoint()
}
