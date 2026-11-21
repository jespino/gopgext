// vector is a Go port of the core of pgvector (https://github.com/pgvector/pgvector):
// the vector data type (with the same on-disk format, so a dimension
// typmod, vector(3)), its text and binary I/O, distance functions and
// operators, arithmetic, comparisons with a btree operator class, casts
// from and to arrays, and the avg and sum aggregates.
//
// It also has pgvector's IVFFlat approximate nearest neighbor index, an
// index access method written with gopgext/am (ivfflat.go); HNSW isn't
// ported.
package main

import (
	"encoding/binary"
	"fmt"
	"math"
	"strconv"
	"strings"

	"gopgext/pg"
)

func main() { registerIVFFlat() }

// maxDim is the maximum number of dimensions (VECTOR_MAX_DIM).
const maxDim = 16000

// Vector is a vector of float4 values (pgvector's format: int16 dimensions,
// int16 unused, then the values in native byte order).
//
//gopgext:type input=VectorIn output=VectorOut typmod_in=VectorTypmodIn receive=VectorRecv send=VectorSend storage=external
type Vector pg.Varlena

// Dim returns the number of dimensions.
func (v Vector) Dim() int { return int(int16(binary.LittleEndian.Uint16(v))) }

// At returns element i.
func (v Vector) At(i int) float32 {
	return math.Float32frombits(binary.LittleEndian.Uint32(v[4+4*i:]))
}

// Values returns the elements.
func (v Vector) Values() []float32 {
	xs := make([]float32, v.Dim())
	for i := range xs {
		xs[i] = v.At(i)
	}
	return xs
}

// newVector returns a vector holding xs.
func newVector(xs []float32) Vector {
	v := make(Vector, 4+4*len(xs))
	binary.LittleEndian.PutUint16(v, uint16(len(xs)))
	for i, x := range xs {
		binary.LittleEndian.PutUint32(v[4+4*i:], math.Float32bits(x))
	}
	return v
}

func dataError(format string, args ...any) error {
	return pg.Errorf(pg.CodeDataException, format, args...)
}

func checkDim(dim int) error {
	if dim < 1 {
		return dataError("vector must have at least 1 dimension")
	}
	if dim > maxDim {
		return pg.Errorf("54000", "vector cannot have more than %d dimensions", maxDim)
	}
	return nil
}

func checkExpectedDim(typmod int32, dim int) error {
	if typmod != -1 && int(typmod) != dim {
		return dataError("expected %d dimensions, not %d", typmod, dim)
	}
	return nil
}

func checkElement(x float32) error {
	switch {
	case x != x:
		return dataError("NaN not allowed in vector")
	case math.IsInf(float64(x), 0):
		return dataError("infinite value not allowed in vector")
	}
	return nil
}

func checkDims(a, b Vector) error {
	if a.Dim() != b.Dim() {
		return dataError("different vector dimensions %d and %d", a.Dim(), b.Dim())
	}
	return nil
}

// ---- I/O -------------------------------------------------------------

func isSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\v' || c == '\f'
}

func syntaxError(s string, detail string) error {
	e := pg.Errorf(pg.CodeInvalidTextRepresentation, "invalid input syntax for type vector: %q", s)
	e.Detail = detail
	return e
}

// VectorIn parses '[1,2,3]'.
//
//gopgext:function immutable strict parallel=safe
func VectorIn(cs pg.CString, _ pg.Oid, typmod int32) (Vector, error) {
	s := string(cs)
	p := strings.TrimLeftFunc(s, func(r rune) bool { return r < 128 && isSpace(byte(r)) })
	if !strings.HasPrefix(p, "[") {
		return nil, syntaxError(s, `Vector contents must start with "[".`)
	}
	body, rest, ok := strings.Cut(p[1:], "]")
	if !ok {
		return nil, syntaxError(s, `Unexpected end of input.`)
	}
	if strings.TrimLeftFunc(rest, func(r rune) bool { return r < 128 && isSpace(byte(r)) }) != "" {
		return nil, syntaxError(s, "Junk after closing right brace.")
	}
	var xs []float32
	if strings.TrimSpace(body) != "" {
		for _, f := range strings.Split(body, ",") {
			f = strings.TrimFunc(f, func(r rune) bool { return r < 128 && isSpace(byte(r)) })
			if f == "" {
				return nil, syntaxError(s, "")
			}
			if len(xs) == maxDim {
				return nil, pg.Errorf("54000", "vector cannot have more than %d dimensions", maxDim)
			}
			x, err := strconv.ParseFloat(f, 32)
			if err != nil {
				if ne, ok := err.(*strconv.NumError); ok && ne.Err == strconv.ErrRange {
					return nil, pg.Errorf(pg.CodeNumericValueOutOfRange, "%q is out of range for type vector", f)
				}
				return nil, syntaxError(s, "")
			}
			if err := checkElement(float32(x)); err != nil {
				return nil, err
			}
			xs = append(xs, float32(x))
		}
	}
	if err := checkDim(len(xs)); err != nil {
		return nil, err
	}
	if err := checkExpectedDim(typmod, len(xs)); err != nil {
		return nil, err
	}
	return newVector(xs), nil
}

// VectorOut formats a vector as '[1,2,3]', with the shortest exact
// representation of each element (like float4 output).
//
//gopgext:function immutable strict parallel=safe
func VectorOut(v Vector) pg.CString {
	b := make([]byte, 0, 2+v.Dim()*8)
	b = append(b, '[')
	for i := range v.Dim() {
		if i > 0 {
			b = append(b, ',')
		}
		b = strconv.AppendFloat(b, float64(v.At(i)), 'g', -1, 32)
	}
	b = append(b, ']')
	return pg.CString(b)
}

// VectorTypmodIn checks the dimensions of vector(n).
//
//gopgext:function immutable strict parallel=safe
func VectorTypmodIn(mods []pg.CString) (int32, error) {
	if len(mods) != 1 {
		return 0, pg.Errorf(pg.CodeInvalidParameterValue, "invalid type modifier")
	}
	n, err := strconv.ParseInt(string(mods[0]), 10, 32)
	if err != nil {
		return 0, pg.Errorf(pg.CodeInvalidTextRepresentation, "invalid input syntax for type integer: %q", string(mods[0]))
	}
	if n < 1 {
		return 0, pg.Errorf(pg.CodeInvalidParameterValue, "dimensions for type vector must be at least 1")
	}
	if n > maxDim {
		return 0, pg.Errorf(pg.CodeInvalidParameterValue, "dimensions for type vector cannot exceed %d", maxDim)
	}
	return int32(n), nil
}

// VectorRecv reads the binary format: int16 dimensions, int16 unused,
// float4 values (network byte order).
//
//gopgext:function immutable strict parallel=safe
func VectorRecv(buf pg.StringInfo, _ pg.Oid, typmod int32) (Vector, error) {
	dim := int(buf.ReadInt16())
	unused := buf.ReadInt16()
	if err := checkDim(dim); err != nil {
		return nil, err
	}
	if err := checkExpectedDim(typmod, dim); err != nil {
		return nil, err
	}
	if unused != 0 {
		return nil, dataError("expected unused to be 0, not %d", unused)
	}
	xs := make([]float32, dim)
	for i := range xs {
		xs[i] = buf.ReadFloat32()
		if err := checkElement(xs[i]); err != nil {
			return nil, err
		}
	}
	return newVector(xs), nil
}

// VectorSend writes the binary format.
//
//gopgext:function immutable strict parallel=safe
func VectorSend(v Vector) []byte {
	b := make([]byte, 0, 4+4*v.Dim())
	b = binary.BigEndian.AppendUint16(b, uint16(v.Dim()))
	b = binary.BigEndian.AppendUint16(b, 0)
	for i := range v.Dim() {
		b = binary.BigEndian.AppendUint32(b, math.Float32bits(v.At(i)))
	}
	return b
}

// ---- Casts -------------------------------------------------------------

// VectorTypmod checks a vector against the dimensions of vector(n) (the
// length coercion cast).
//
//gopgext:function name=vector immutable strict parallel=safe
func VectorTypmod(v Vector, typmod int32, _ bool) (Vector, error) {
	if err := checkExpectedDim(typmod, v.Dim()); err != nil {
		return nil, err
	}
	return v, nil
}

func fromFloats[T float32 | float64](xs []T, typmod int32) (Vector, error) {
	if err := checkDim(len(xs)); err != nil {
		return nil, err
	}
	if err := checkExpectedDim(typmod, len(xs)); err != nil {
		return nil, err
	}
	out := make([]float32, len(xs))
	for i, x := range xs {
		out[i] = float32(x)
		if math.IsInf(float64(out[i]), 0) && !math.IsInf(float64(x), 0) {
			return nil, pg.Errorf(pg.CodeNumericValueOutOfRange, "value out of range: overflow")
		}
		if err := checkElement(out[i]); err != nil {
			return nil, err
		}
	}
	return newVector(out), nil
}

// IntArrayToVector converts integer[] (cast).
//
//gopgext:function name=array_to_vector immutable strict parallel=safe
func IntArrayToVector(a []int32, typmod int32, _ bool) (Vector, error) {
	xs := make([]float32, len(a))
	for i, x := range a {
		xs[i] = float32(x)
	}
	return fromFloats(xs, typmod)
}

// RealArrayToVector converts real[] (cast).
//
//gopgext:function name=array_to_vector immutable strict parallel=safe
func RealArrayToVector(a []float32, typmod int32, _ bool) (Vector, error) {
	return fromFloats(a, typmod)
}

// DoubleArrayToVector converts double precision[] (cast).
//
//gopgext:function name=array_to_vector immutable strict parallel=safe
func DoubleArrayToVector(a []float64, typmod int32, _ bool) (Vector, error) {
	return fromFloats(a, typmod)
}

// VectorToFloat4 converts to real[] (cast).
//
//gopgext:function immutable strict parallel=safe
func VectorToFloat4(v Vector, _ int32, _ bool) []float32 { return v.Values() }

// ---- Distances ---------------------------------------------------------

// L2Distance returns the Euclidean distance.
//
//gopgext:function immutable strict parallel=safe
func L2Distance(a, b Vector) (float64, error) {
	d, err := l2Squared(a, b)
	return math.Sqrt(float64(d)), err
}

// VectorL2SquaredDistance returns the squared Euclidean distance.
//
//gopgext:function immutable strict parallel=safe
func VectorL2SquaredDistance(a, b Vector) (float64, error) {
	d, err := l2Squared(a, b)
	return float64(d), err
}

func l2Squared(a, b Vector) (float32, error) {
	if err := checkDims(a, b); err != nil {
		return 0, err
	}
	var sum float32
	for i := range a.Dim() {
		d := a.At(i) - b.At(i)
		sum += d * d
	}
	return sum, nil
}

func dot(a, b Vector) (float32, error) {
	if err := checkDims(a, b); err != nil {
		return 0, err
	}
	var sum float32
	for i := range a.Dim() {
		sum += a.At(i) * b.At(i)
	}
	return sum, nil
}

// InnerProduct returns the dot product.
//
//gopgext:function immutable strict parallel=safe
func InnerProduct(a, b Vector) (float64, error) {
	d, err := dot(a, b)
	return float64(d), err
}

// VectorNegativeInnerProduct returns minus the dot product (operator <#>:
// smaller is closer).
//
//gopgext:function immutable strict parallel=safe
func VectorNegativeInnerProduct(a, b Vector) (float64, error) {
	d, err := dot(a, b)
	return -float64(d), err
}

// CosineDistance returns 1 - the cosine similarity.
//
//gopgext:function immutable strict parallel=safe
func CosineDistance(a, b Vector) (float64, error) {
	if err := checkDims(a, b); err != nil {
		return 0, err
	}
	var sim, na, nb float32
	for i := range a.Dim() {
		x, y := a.At(i), b.At(i)
		sim += x * y
		na += x * x
		nb += y * y
	}
	similarity := float64(sim) / math.Sqrt(float64(na)*float64(nb))
	if math.IsNaN(similarity) {
		return math.NaN(), nil
	}
	return 1 - max(-1, min(1, similarity)), nil
}

// L1Distance returns the taxicab distance.
//
//gopgext:function immutable strict parallel=safe
func L1Distance(a, b Vector) (float64, error) {
	if err := checkDims(a, b); err != nil {
		return 0, err
	}
	var sum float32
	for i := range a.Dim() {
		sum += float32(math.Abs(float64(a.At(i) - b.At(i))))
	}
	return float64(sum), nil
}

// VectorDims returns the number of dimensions.
//
//gopgext:function immutable strict parallel=safe
func VectorDims(v Vector) int32 { return int32(v.Dim()) }

// VectorNorm returns the Euclidean norm.
//
//gopgext:function immutable strict parallel=safe
func VectorNorm(v Vector) float64 {
	var norm float64
	for i := range v.Dim() {
		norm += float64(v.At(i)) * float64(v.At(i))
	}
	return math.Sqrt(norm)
}

// L2Normalize returns v scaled to norm 1 (a zero vector stays zero).
//
//gopgext:function immutable strict parallel=safe
func L2Normalize(v Vector) (Vector, error) {
	norm := VectorNorm(v)
	xs := make([]float32, v.Dim())
	if norm > 0 {
		for i := range xs {
			xs[i] = float32(float64(v.At(i)) / norm)
			if math.IsInf(float64(xs[i]), 0) {
				return nil, pg.Errorf(pg.CodeNumericValueOutOfRange, "value out of range: overflow")
			}
		}
	}
	return newVector(xs), nil
}

// Subvector returns count elements from start (1-based).
//
//gopgext:function immutable strict parallel=safe
func Subvector(v Vector, start, count int32) (Vector, error) {
	from, to := int64(start), int64(start)+int64(count)
	if count < 1 {
		return nil, dataError("vector must have at least 1 dimension")
	}
	from = max(from, 1)
	to = min(to, int64(v.Dim())+1)
	if to <= from {
		return nil, dataError("vector must have at least 1 dimension")
	}
	return newVector(v.Values()[from-1 : to-1]), nil
}

// ---- Arithmetic ----------------------------------------------------------

func elementwise(a, b Vector, op func(x, y float32) float32) (Vector, error) {
	if err := checkDims(a, b); err != nil {
		return nil, err
	}
	xs := make([]float32, a.Dim())
	for i := range xs {
		xs[i] = op(a.At(i), b.At(i))
		if math.IsInf(float64(xs[i]), 0) {
			return nil, pg.Errorf(pg.CodeNumericValueOutOfRange, "value out of range: overflow")
		}
	}
	return newVector(xs), nil
}

// VectorAdd adds element by element.
//
//gopgext:function immutable strict parallel=safe
func VectorAdd(a, b Vector) (Vector, error) {
	return elementwise(a, b, func(x, y float32) float32 { return x + y })
}

// VectorSub subtracts element by element.
//
//gopgext:function immutable strict parallel=safe
func VectorSub(a, b Vector) (Vector, error) {
	return elementwise(a, b, func(x, y float32) float32 { return x - y })
}

// VectorMul multiplies element by element.
//
//gopgext:function immutable strict parallel=safe
func VectorMul(a, b Vector) (Vector, error) {
	return elementwise(a, b, func(x, y float32) float32 { return x * y })
}

// VectorConcat concatenates two vectors.
//
//gopgext:function immutable strict parallel=safe
func VectorConcat(a, b Vector) (Vector, error) {
	if err := checkDim(a.Dim() + b.Dim()); err != nil {
		return nil, err
	}
	return newVector(append(a.Values(), b.Values()...)), nil
}

// ---- Comparisons ---------------------------------------------------------

// VectorCmp orders vectors element by element, then by dimensions.
//
//gopgext:function immutable strict parallel=safe
func VectorCmp(a, b Vector) int32 {
	for i := range min(a.Dim(), b.Dim()) {
		switch x, y := a.At(i), b.At(i); {
		case x < y:
			return -1
		case x > y:
			return 1
		}
	}
	switch {
	case a.Dim() < b.Dim():
		return -1
	case a.Dim() > b.Dim():
		return 1
	}
	return 0
}

//gopgext:function immutable strict parallel=safe
func VectorLt(a, b Vector) bool { return VectorCmp(a, b) < 0 }

//gopgext:function immutable strict parallel=safe
func VectorLe(a, b Vector) bool { return VectorCmp(a, b) <= 0 }

//gopgext:function immutable strict parallel=safe
func VectorEq(a, b Vector) bool { return VectorCmp(a, b) == 0 }

//gopgext:function immutable strict parallel=safe
func VectorNe(a, b Vector) bool { return VectorCmp(a, b) != 0 }

//gopgext:function immutable strict parallel=safe
func VectorGe(a, b Vector) bool { return VectorCmp(a, b) >= 0 }

//gopgext:function immutable strict parallel=safe
func VectorGt(a, b Vector) bool { return VectorCmp(a, b) > 0 }

// ---- Aggregates ----------------------------------------------------------

// The state of avg(vector): {count, sum of each dimension...}.

// VectorAccum adds a vector to the state of avg.
//
//gopgext:function immutable strict parallel=safe
func VectorAccum(state []float64, v Vector) ([]float64, error) {
	if len(state) == 0 {
		return nil, dataError("invalid avg(vector) state")
	}
	if state[0] == 0 {
		state = make([]float64, 1+v.Dim())
	} else if len(state)-1 != v.Dim() {
		return nil, checkDims(newVector(make([]float32, len(state)-1)), v)
	}
	state[0]++
	for i := range v.Dim() {
		state[1+i] += float64(v.At(i))
		if math.IsInf(state[1+i], 0) {
			return nil, pg.Errorf(pg.CodeNumericValueOutOfRange, "value out of range: overflow")
		}
	}
	return state, nil
}

// VectorCombine merges two states of avg (parallel aggregation).
//
//gopgext:function immutable strict parallel=safe
func VectorCombine(a, b []float64) ([]float64, error) {
	switch {
	case len(a) == 0 || a[0] == 0:
		return b, nil
	case len(b) == 0 || b[0] == 0:
		return a, nil
	case len(a) != len(b):
		return nil, dataError("different vector dimensions %d and %d", len(a)-1, len(b)-1)
	}
	out := make([]float64, len(a))
	for i := range a {
		out[i] = a[i] + b[i]
	}
	return out, nil
}

// VectorAvg finishes avg: NULL for no rows.
//
//gopgext:function immutable strict parallel=safe
func VectorAvg(state []float64) (*Vector, error) {
	if len(state) == 0 || state[0] == 0 {
		return nil, nil
	}
	xs := make([]float32, len(state)-1)
	for i := range xs {
		xs[i] = float32(state[1+i] / state[0])
		if err := checkElement(xs[i]); err != nil {
			return nil, fmt.Errorf("avg: %w", err)
		}
	}
	v := newVector(xs)
	return &v, nil
}
