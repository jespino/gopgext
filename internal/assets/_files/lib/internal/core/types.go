package core

import (
	"encoding/binary"
	"math"
	"time"
	"unsafe"
)

// Oid is an object identifier (pg.Oid, SQL type oid).
type Oid uint32

// Internal is a pointer to PostgreSQL memory (pg.Internal, SQL type
// internal).
type Internal uintptr

// Varlena is the contents of a variable-length value, not copied (pg.Varlena).
type Varlena []byte

// StringInfo is a StringInfo the receive function of a data type reads its
// binary input from (pg.StringInfo, SQL type internal).
type StringInfo struct{ p uintptr }

// MakeStringInfo wraps a StringInfo pointer.
func MakeStringInfo(p uintptr) StringInfo { return StringInfo{p} }

// StringInfoData layout (lib/stringinfo.h; checked in runtime.c):
// char *data; int len; int maxlen; int cursor.
func (s StringInfo) fields() (data *byte, length, cursor *int32) {
	base := unsafe.Pointer(s.p)
	return *(**byte)(base), (*int32)(unsafe.Add(base, 8)), (*int32)(unsafe.Add(base, 16))
}

// Remaining returns the unread bytes, without consuming them. They refer to
// PostgreSQL's memory: valid during the call.
func (s StringInfo) Remaining() []byte {
	data, n, cursor := s.fields()
	if data == nil || *cursor >= *n {
		return nil
	}
	return unsafe.Slice((*byte)(unsafe.Add(unsafe.Pointer(data), *cursor)), *n-*cursor)
}

// Read consumes the next n bytes (not copied: valid during the call). Fewer
// than n remaining bytes raise an error, like pq_getmsgbytes.
func (s StringInfo) Read(n int) []byte {
	rest := s.Remaining()
	if n < 0 || n > len(rest) {
		panic(&Error{Code: "08P01", Message: "insufficient data left in message"})
	}
	_, _, cursor := s.fields()
	*cursor += int32(n)
	return rest[:n:n]
}

// ReadAll consumes the remaining bytes (not copied: valid during the call).
func (s StringInfo) ReadAll() []byte { return s.Read(len(s.Remaining())) }

// ReadInt16 consumes a big-endian int16 (pq_getmsgint(msg, 2)).
func (s StringInfo) ReadInt16() int16 { return int16(binary.BigEndian.Uint16(s.Read(2))) }

// ReadInt32 consumes a big-endian int32 (pq_getmsgint(msg, 4)).
func (s StringInfo) ReadInt32() int32 { return int32(binary.BigEndian.Uint32(s.Read(4))) }

// ReadInt64 consumes a big-endian int64 (pq_getmsgint64).
func (s StringInfo) ReadInt64() int64 { return int64(binary.BigEndian.Uint64(s.Read(8))) }

// ReadFloat32 consumes a big-endian float4 (pq_getmsgfloat4).
func (s StringInfo) ReadFloat32() float32 {
	return math.Float32frombits(binary.BigEndian.Uint32(s.Read(4)))
}

// ReadFloat64 consumes a big-endian float8 (pq_getmsgfloat8).
func (s StringInfo) ReadFloat64() float64 {
	return math.Float64frombits(binary.BigEndian.Uint64(s.Read(8)))
}

// AnyElement is a value of a polymorphic argument or result (pg.AnyElement,
// SQL type anyelement): a Datum and its type.
type AnyElement struct {
	d   uint64
	typ uint32
}

// MakeAnyElement wraps a Datum of type typ.
func MakeAnyElement(d uint64, typ uint32) AnyElement { return AnyElement{d, typ} }

// Type returns the value's type.
func (a AnyElement) Type() Oid { return Oid(a.typ) }

// Datum returns the raw Datum (valid during the call).
func (a AnyElement) Datum() uint64 { return a.d }

//export gopgext_any_out
func anyOut(typ uint32, d uint64, out **byte) uintptr

// String returns the value's text form (its type's output function).
func (a AnyElement) String() string {
	var p *byte
	Check(anyOut(a.typ, a.d, &p))
	return GoString(p)
}

//export gopgext_any_in
func anyIn(typ uint32, s *byte, out *uint64) uintptr

// ParseAny returns a value of type typ from its text form (the type's
// input function); an invalid input is returned as the PostgreSQL error.
func ParseAny(typ Oid, s string) (AnyElement, error) {
	var d uint64
	if ed := anyIn(uint32(typ), CBytes(s), &d); ed != 0 {
		return AnyElement{}, FromErrorData(ed)
	}
	return AnyElement{d, uint32(typ)}, nil
}

// AnyArray is an array of any type (pg.AnyArray, SQL type anyarray).
type AnyArray struct {
	d    uint64
	elem uint32
}

//export gopgext_array_elemtype
func arrayElemType(d uint64, out *uint32) uintptr

// MakeAnyArray wraps an array Datum.
func MakeAnyArray(d uint64) AnyArray {
	var elem uint32
	Check(arrayElemType(d, &elem))
	return AnyArray{d, elem}
}

// ElemType returns the type of the elements.
func (a AnyArray) ElemType() Oid { return Oid(a.elem) }

// Datum returns the raw Datum (valid during the call).
func (a AnyArray) Datum() uint64 { return a.d }

//export gopgext_array_get
func arrayGet(d uint64, elemType uint32, n *int64, elems *unsafe.Pointer, nulls *unsafe.Pointer) uintptr

// Elements returns the elements (flattened), nil for NULL ones.
func (a AnyArray) Elements() []*AnyElement {
	var n int64
	var elems, nulls unsafe.Pointer
	Check(arrayGet(a.d, a.elem, &n, &elems, &nulls))
	ds, ns := unsafe.Slice((*uint64)(elems), n), unsafe.Slice((*bool)(nulls), n)
	out := make([]*AnyElement, n)
	for i := range out {
		if !ns[i] {
			out[i] = &AnyElement{ds[i], a.elem}
		}
	}
	return out
}

//export gopgext_array_dims
func arrayDims(d uint64, ndim *int64, dims *int32) uintptr

// Dims returns the length of each dimension (none for an empty array).
func (a AnyArray) Dims() []int {
	var n int64
	var dims [6]int32 // MAXDIM
	Check(arrayDims(a.d, &n, &dims[0]))
	out := make([]int, n)
	for i := range out {
		out[i] = int(dims[i])
	}
	return out
}

//export gopgext_array_type
func arrayType(elem uint32, out *uint32) uintptr

// String returns the array's text form.
func (a AnyArray) String() string {
	var typ uint32
	Check(arrayType(a.elem, &typ))
	return AnyElement{a.d, typ}.String()
}

//export gopgext_array_new
func arrayNew(elemType uint32, n int64, elems *uint64, nulls *bool, out *uint64) uintptr

// NewAnyArray returns a one-dimensional array of elements of type elem
// (nil elements are NULL).
func NewAnyArray(elem Oid, xs []*AnyElement) AnyArray {
	ds := make([]uint64, len(xs)+1)
	ns := make([]bool, len(xs)+1)
	for i, x := range xs {
		if x == nil {
			ns[i] = true
		} else {
			ds[i] = x.d
		}
	}
	var d uint64
	Check(arrayNew(uint32(elem), int64(len(xs)), &ds[0], &ns[0], &d))
	return AnyArray{d, uint32(elem)}
}

// Point is a value of SQL type point (pg.Point).
type Point struct{ X, Y float64 }

// Numeric is a value of SQL type numeric in its text form (pg.Numeric):
// "12.50", "-3", "NaN", "Infinity". Results are parsed by numeric's input
// function, which raises an error for invalid text.
type Numeric string

const numericOID = 1700

// NumericFromDatum returns the text form of a numeric Datum.
func NumericFromDatum(d uint64) Numeric {
	var p *byte
	Check(anyOut(numericOID, d, &p))
	return Numeric(GoString(p))
}

// NumericToDatum returns n as a numeric Datum (palloc'd).
func NumericToDatum(n Numeric) uint64 {
	var d uint64
	Check(anyIn(numericOID, CBytes(string(n)), &d))
	return d
}

// timestamptz is microseconds since 2000-01-01 00:00:00 UTC; the minimum
// and maximum int64 are -infinity and infinity. Valid values are from
// 4714-11-24 BC to 294276-12-31 AD.
const (
	pgEpochUnix  = 946684800
	minTimestamp = -211813488000000000 // 4714-11-24 00:00:00 BC (MIN_TIMESTAMP)
	endTimestamp = 9223371331200000000 // 294277-01-01 00:00:00 (END_TIMESTAMP)
)

// TimeFromDatum converts a timestamptz Datum; infinite timestamps raise an
// error (time.Time can't represent them).
func TimeFromDatum(d uint64) time.Time {
	us := int64(d)
	if us == math.MinInt64 || us == math.MaxInt64 {
		panic(&Error{Code: "22008", Message: "infinite timestamp can't be converted to a time.Time",
			Hint: "Exclude infinite timestamps (isfinite()) or pass them as text."})
	}
	sec, rem := us/1e6, us%1e6
	if rem < 0 {
		sec, rem = sec-1, rem+1e6
	}
	return time.Unix(sec+pgEpochUnix, rem*1000)
}

// TimeToDatum converts t to a timestamptz Datum (microsecond precision,
// truncated); times out of PostgreSQL's range raise an error.
func TimeToDatum(t time.Time) uint64 {
	sec := t.Unix() - pgEpochUnix
	const limit = endTimestamp / 1e6
	if sec < minTimestamp/1e6 || sec >= limit {
		panic(&Error{Code: "22008", Message: "timestamp out of range: " + t.String()})
	}
	return uint64(sec*1e6 + int64(t.Nanosecond()/1000))
}
