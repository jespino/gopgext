// Package fmgr connects SQL functions written in Go to PostgreSQL's function
// manager. gopgext generates the calls to it: extension code doesn't use it
// directly.
//
// For every //gopgext:function, gopgext generates a C function PostgreSQL
// calls (V1 calling convention), which calls a generated Go wrapper through
// gopgext_invoke:
//
//	//export gopgext_go_Add
//	func _gopgext_go_Add(c *fmgr.Call) {
//		defer c.End(c.Begin(true))
//		r0 := Add(fmgr.Arg[int32](c, 0, "a"), fmgr.Arg[int32](c, 1, "b"))
//		fmgr.Return(c, r0)
//	}
package fmgr

import (
	"errors"
	"fmt"
	"iter"
	"math"
	"strings"
	"time"
	"unsafe"

	"gopgext/internal/core"
)

// Call is one call from PostgreSQL into Go (struct gopgext_call in
// gopgext.h: keep in sync).
type Call struct {
	fcinfo   uintptr
	result   uint64
	isnull   int32
	sqlstate int32
	message  *byte
	detail   *byte
	hint     *byte
	edata    uintptr
	srf      uintptr
	flags    int32
	_        int32
}

// The gopgext types of SQL function arguments and results (see package pg).
type (
	CString    = core.CString
	Oid        = core.Oid
	Internal   = core.Internal
	Varlena    = core.Varlena
	StringInfo = core.StringInfo
	Numeric    = core.Numeric
	UUID       = core.UUID
	Point      = core.Point
	// Time is time.Time (timestamptz): generated code spells it this way.
	Time = time.Time
)

const callReturnedError = 1 // GOPGEXT_CALL_RETURNED_ERROR

// Saved is the state Begin saves and End restores.
type Saved struct {
	seq      uint64
	readOnly bool
	fcinfo   uintptr
	call     uintptr
}

// Begin starts a call. readOnly is set for STABLE and IMMUTABLE functions.
func (c *Call) Begin(readOnly bool) Saved {
	s := Saved{core.Seq, core.ReadOnly, core.FCInfo, core.Call}
	core.Seq = core.NextSeq()
	core.ReadOnly = readOnly
	core.FCInfo = c.fcinfo
	core.Call = uintptr(unsafe.Pointer(c))
	return s
}

// End finishes a call (deferred): a panic becomes the call's error.
func (c *Call) End(s Saved) {
	if r := recover(); r != nil {
		c.panicked(r)
	}
	core.Seq, core.ReadOnly, core.FCInfo, core.Call = s.seq, s.readOnly, s.fcinfo, s.call
}

func (c *Call) panicked(r any) {
	switch v := r.(type) {
	case core.PendingError:
		// The C side re-throws the original error.
		return
	case *core.Error:
		c.fail(v)
		return
	}
	var msg string
	switch v := r.(type) {
	case error:
		msg = v.Error()
	case string:
		msg = v
	case fmt.Stringer:
		msg = v.String()
	default:
		msg = fmt.Sprint(v)
	}
	hint := ""
	if strings.Contains(msg, "scheduler is disabled") {
		hint = "Extensions run Go without goroutines: blocking on a channel, select or sync.WaitGroup can never resume."
	}
	c.setError("XX000", "Go panic: "+msg, "", hint)
}

//export gopgext_strdup
func strdup(p *byte, n int64) *byte

func (c *Call) setError(code, msg, detail, hint string) {
	c.sqlstate = core.PackSQLState(code)
	if c.sqlstate == 0 {
		c.sqlstate = core.PackSQLState("P0001")
	}
	c.message = strdup(core.StringData(msg), int64(len(msg)))
	if detail != "" {
		c.detail = strdup(core.StringData(detail), int64(len(detail)))
	}
	if hint != "" {
		c.hint = strdup(core.StringData(hint), int64(len(hint)))
	}
}

// Fail makes the call raise err (softly, for input functions called with an
// ErrorSaveContext).
func (c *Call) Fail(err error) {
	c.flags |= callReturnedError
	c.fail(err)
}

func (c *Call) fail(err error) {
	var pe *core.Error
	if errors.As(err, &pe) {
		if ed := core.ErrorData(pe); ed != 0 && error(pe) == err {
			c.edata = ed
			return
		}
		c.setError(pe.Code, err.Error(), pe.Detail, pe.Hint)
		return
	}
	c.setError("P0001", err.Error(), "", "")
}

// Init runs package initialization and main, when the library loads.
func Init(c *Call) { runMain() }

//go:linkname runMain runtime.pgextRunMain
func runMain()

// ---- Arguments ------------------------------------------------------

// Scalar are the Go types of SQL function arguments and results.
type Scalar interface {
	bool | int16 | int32 | int64 | int | float32 | float64 | string | []byte |
		CString | Oid | Internal | Varlena | StringInfo | Numeric | Time | UUID | Point
}

func (c *Call) arg(i int) (uint64, bool) {
	// FunctionCallInfoBaseData.nargs: int16 at offset 30 (a SQL
	// declaration with fewer arguments than the Go function must not make
	// it read past them).
	if n := int(*(*int16)(unsafe.Pointer(c.fcinfo + 30))); i >= n {
		panic(&core.Error{Code: "42P13", Message: fmt.Sprintf("function called with %d arguments, but its Go function reads argument %d", n, i+1),
			Hint: "Check the CREATE FUNCTION declaration against the Go function's parameters."})
	}
	// FunctionCallInfoBaseData.args: NullableDatum[] at offset 32.
	p := unsafe.Pointer(c.fcinfo + 32 + uintptr(i)*16)
	return *(*uint64)(p), *(*bool)(unsafe.Add(p, 8))
}

func nullArg(name string) {
	panic(&core.Error{
		Code:    "22004",
		Message: fmt.Sprintf("argument %q must not be NULL", name),
		Hint:    "Declare the Go parameter as a pointer to accept NULL.",
	})
}

// Arg returns argument i (NULL raises an error).
func Arg[T Scalar](c *Call, i int, name string) T {
	d, null := c.arg(i)
	if null {
		nullArg(name)
	}
	return fromDatum[T](d)
}

// ArgPtr returns argument i, nil for NULL.
func ArgPtr[T Scalar](c *Call, i int) *T {
	d, null := c.arg(i)
	if null {
		return nil
	}
	v := fromDatum[T](d)
	return &v
}

//export gopgext_varlena_get
func varlenaGet(d uint64, ptr *unsafe.Pointer, n *int64) uintptr

func varlena(d uint64) []byte {
	var p unsafe.Pointer
	var n int64
	core.Check(varlenaGet(d, &p, &n))
	return unsafe.Slice((*byte)(p), n)
}

func fromDatum[T Scalar](d uint64) (v T) {
	switch p := any(&v).(type) {
	case *bool:
		*p = d != 0
	case *int16:
		*p = int16(d)
	case *int32:
		*p = int32(d)
	case *int64:
		*p = int64(d)
	case *int:
		*p = int(int64(d))
	case *float32:
		*p = math.Float32frombits(uint32(d))
	case *float64:
		*p = math.Float64frombits(d)
	case *string:
		*p = string(varlena(d))
	case *[]byte:
		*p = append([]byte{}, varlena(d)...)
	case *CString:
		// Not copied: valid during the call (see pg.CString).
		*p = CString(core.StringView((*byte)(unsafe.Pointer(uintptr(d)))))
	case *Oid:
		*p = Oid(uint32(d))
	case *Internal:
		*p = Internal(uintptr(d))
	case *Varlena:
		// Not copied: valid during the call (see pg.Varlena).
		*p = Varlena(varlena(d))
	case *StringInfo:
		*p = core.MakeStringInfo(uintptr(d))
	case *Numeric:
		*p = core.NumericFromDatum(d)
	case *Time:
		*p = core.TimeFromDatum(d)
	case *UUID:
		*p = *(*UUID)(unsafe.Pointer(uintptr(d)))
	case *Point:
		*p = *(*Point)(unsafe.Pointer(uintptr(d)))
	}
	return
}

// typeOID returns the PostgreSQL type of T.
func typeOID[T Scalar]() uint32 {
	var v T
	switch any(v).(type) {
	case bool:
		return 16
	case int16:
		return 21
	case int32:
		return 23
	case int64, int:
		return 20
	case float32:
		return 700
	case float64:
		return 701
	case string:
		return 25
	case CString:
		return 2275
	case Oid:
		return 26
	case Internal, StringInfo:
		return 2281
	case Numeric:
		return 1700
	case Time:
		return 1184
	case UUID:
		return 2950
	case Point:
		return 600
	}
	return 17 // []byte, Varlena: bytea
}

//export gopgext_array_get
func arrayGet(d uint64, elemType uint32, n *int64, elems *unsafe.Pointer, nulls *unsafe.Pointer) uintptr

// arrayElems returns the elements of an array Datum (flattened). elemType 0
// accepts any element type (the type of the argument is checked by
// PostgreSQL: arrays of extension-defined types).
func arrayElems(d uint64, elemType uint32) ([]uint64, []bool) {
	var n int64
	var elems, nulls unsafe.Pointer
	core.Check(arrayGet(d, elemType, &n, &elems, &nulls))
	return unsafe.Slice((*uint64)(elems), n), unsafe.Slice((*bool)(nulls), n)
}

func arrayOf[T Scalar](d uint64, elemType uint32, name string) []T {
	ds, ns := arrayElems(d, elemType)
	out := make([]T, len(ds))
	for j := range out {
		if ns[j] {
			panic(&core.Error{Code: "22004", Message: fmt.Sprintf("array argument %q must not contain NULLs", name)})
		}
		out[j] = fromDatum[T](ds[j])
	}
	return out
}

func arrayNullableOf[T Scalar](d uint64, elemType uint32) []*T {
	ds, ns := arrayElems(d, elemType)
	out := make([]*T, len(ds))
	for j := range out {
		if !ns[j] {
			v := fromDatum[T](ds[j])
			out[j] = &v
		}
	}
	return out
}

// ArgArray returns array argument i (any number of dimensions, flattened).
// NULL arrays and NULL elements raise an error.
func ArgArray[T Scalar](c *Call, i int, name string) []T {
	return getArray[T](Value{c: c, i: i}.load(), name, typeOID[T]())
}

// ArgArrayNullable returns array argument i, with nil for NULL elements.
func ArgArrayNullable[T Scalar](c *Call, i int, name string) []*T {
	return getArrayNullable[T](Value{c: c, i: i}.load(), name, typeOID[T]())
}

// ArgArrayPtr returns array argument i, nil for NULL.
func ArgArrayPtr[T Scalar](c *Call, i int, name string) *[]T {
	return getArrayPtr[T](Value{c: c, i: i}.load(), name, typeOID[T]())
}

// ArgArrayOf, ArgArrayNullableOf and ArgArrayPtrOf read arrays of an
// extension-defined type whose representation is T.
func ArgArrayOf[T Scalar](c *Call, i int, name string) []T {
	return getArray[T](Value{c: c, i: i}.load(), name, 0)
}

func ArgArrayNullableOf[T Scalar](c *Call, i int, name string) []*T {
	return getArrayNullable[T](Value{c: c, i: i}.load(), name, 0)
}

func ArgArrayPtrOf[T Scalar](c *Call, i int, name string) *[]T {
	return getArrayPtr[T](Value{c: c, i: i}.load(), name, 0)
}

// Value is an argument, or a column of a composite argument.
type Value struct {
	c    *Call
	i    int // argument index (-1: a column)
	d    uint64
	null bool
	typ  uint32 // a column's type
}

func (v Value) load() Value {
	if v.i >= 0 && v.c != nil {
		v.d, v.null = v.c.arg(v.i)
	}
	return v
}

func nullValue(name string) {
	if name == "" {
		name = "value"
	}
	nullArg(name)
}

func getArray[T Scalar](v Value, name string, elemType uint32) []T {
	if v.null {
		nullValue(name)
	}
	return arrayOf[T](v.d, elemType, name)
}

func getArrayNullable[T Scalar](v Value, name string, elemType uint32) []*T {
	if v.null {
		nullValue(name)
	}
	return arrayNullableOf[T](v.d, elemType)
}

func getArrayPtr[T Scalar](v Value, name string, elemType uint32) *[]T {
	if v.null {
		return nil
	}
	a := arrayOf[T](v.d, elemType, name)
	return &a
}

// ---- Results --------------------------------------------------------

//export gopgext_varlena_new
func varlenaNew(p *byte, n int64, out *uint64) uintptr

// toDatum converts v; varlena reports whether the datum was allocated.
func toDatum[T Scalar](v T) (d uint64, varlena bool) {
	switch x := any(v).(type) {
	case bool:
		if x {
			return 1, false
		}
		return 0, false
	case int16:
		return uint64(int64(x)), false
	case int32:
		return uint64(int64(x)), false
	case int64:
		return uint64(x), false
	case int:
		return uint64(int64(x)), false
	case float32:
		return uint64(int64(int32(math.Float32bits(x)))), false
	case float64:
		return math.Float64bits(x), false
	case string:
		core.Check(varlenaNew(core.StringData(x), int64(len(x)), &d))
		return d, true
	case []byte:
		core.Check(varlenaNew(unsafe.SliceData(x), int64(len(x)), &d))
		return d, true
	case Varlena:
		core.Check(varlenaNew(unsafe.SliceData(x), int64(len(x)), &d))
		return d, true
	case Oid:
		return uint64(x), false
	case Internal:
		return uint64(x), false
	case CString:
		p := strdup(core.StringData(string(x)), int64(len(x)))
		if p == nil {
			panic(&core.Error{Code: "54000", Message: "cstring result too large"})
		}
		return uint64(uintptr(unsafe.Pointer(p))), true
	case Numeric:
		return core.NumericToDatum(x), true
	case Time:
		return core.TimeToDatum(x), false
	case UUID:
		// uuid is passed by reference: 16 bytes in palloc'd memory.
		p := strdup(&x[0], int64(len(x)))
		if p == nil {
			panic(&core.Error{Code: "53200", Message: "out of memory"})
		}
		return uint64(uintptr(unsafe.Pointer(p))), true
	case Point:
		return fixedDatum(unsafe.Pointer(&x), unsafe.Sizeof(x)), true
	}
	return 0, false
}

// fixedDatum copies the n bytes at p into PostgreSQL memory: a value of a
// fixed-size type passed by reference.
func fixedDatum(p unsafe.Pointer, n uintptr) uint64 {
	q := strdup((*byte)(p), int64(n))
	if q == nil {
		panic(&core.Error{Code: "53200", Message: "out of memory"})
	}
	return uint64(uintptr(unsafe.Pointer(q)))
}

// ArgFixed returns argument i of a fixed-size type passed by reference (a
// //gopgext:type over [N]byte): a copy of its bytes. NULL raises an error.
func ArgFixed[T any](c *Call, i int, name string) T {
	d, null := c.arg(i)
	if null {
		nullArg(name)
	}
	return *(*T)(unsafe.Pointer(uintptr(d)))
}

// ArgFixedPtr is ArgFixed, nil for NULL.
func ArgFixedPtr[T any](c *Call, i int) *T {
	d, null := c.arg(i)
	if null {
		return nil
	}
	v := *(*T)(unsafe.Pointer(uintptr(d)))
	return &v
}

// ReturnFixed sets a result of a fixed-size type passed by reference.
func ReturnFixed[T any](c *Call, v T) {
	c.result = fixedDatum(unsafe.Pointer(&v), unsafe.Sizeof(v))
}

// ReturnFixedPtr is ReturnFixed, NULL for nil.
func ReturnFixedPtr[T any](c *Call, v *T) {
	if v == nil {
		c.isnull = 1
		return
	}
	ReturnFixed(c, *v)
}

// Return sets the result.
func Return[T Scalar](c *Call, v T) {
	c.result, _ = toDatum(v)
}

// ReturnPtr sets the result, NULL for nil.
func ReturnPtr[T Scalar](c *Call, v *T) {
	if v == nil {
		c.isnull = 1
		return
	}
	Return(c, *v)
}

//export gopgext_array_new
func arrayNew(elemType uint32, n int64, elems *uint64, nulls *bool, out *uint64) uintptr

func arrayDatum[T Scalar](v []T) uint64 { return arrayDatumOf(v, typeOID[T]()) }

func arrayDatumOf[T Scalar](v []T, elemType uint32) (d uint64) {
	elems := make([]uint64, len(v)+1)
	nulls := make([]bool, len(v)+1)
	for i, x := range v {
		elems[i], _ = toDatum(x)
	}
	core.Check(arrayNew(elemType, int64(len(v)), &elems[0], &nulls[0], &d))
	return d
}

func arrayNullableDatum[T Scalar](v []*T) uint64 { return arrayNullableDatumOf(v, typeOID[T]()) }

func arrayNullableDatumOf[T Scalar](v []*T, elemType uint32) (d uint64) {
	elems := make([]uint64, len(v)+1)
	nulls := make([]bool, len(v)+1)
	for i, x := range v {
		if x == nil {
			nulls[i] = true
		} else {
			elems[i], _ = toDatum(*x)
		}
	}
	core.Check(arrayNew(elemType, int64(len(v)), &elems[0], &nulls[0], &d))
	return d
}

// ReturnArray sets a one-dimensional array result.
func ReturnArray[T Scalar](c *Call, v []T) { c.result = arrayDatum(v) }

// ReturnArrayNullable sets a one-dimensional array result whose nil
// elements are NULL.
func ReturnArrayNullable[T Scalar](c *Call, v []*T) { c.result = arrayNullableDatum(v) }

// ReturnArrayPtr sets an array result, NULL for nil.
func ReturnArrayPtr[T Scalar](c *Call, v *[]T) {
	if v == nil {
		c.isnull = 1
		return
	}
	ReturnArray(c, *v)
}

//export gopgext_srf_begin
func srfBegin(c *Call) uintptr

//export gopgext_srf_put
func srfPut(c *Call, d uint64, isnull bool, free bool) uintptr

// ReturnSet returns the values of seq as the rows of a set-returning
// function (RETURNS SETOF).
func ReturnSet[T Scalar](c *Call, seq iter.Seq[T]) {
	core.Check(srfBegin(c))
	for v := range seq {
		d, alloc := toDatum(v)
		core.Check(srfPut(c, d, false, alloc))
	}
}

// ReturnSetNullable returns the values of seq as the rows of a
// set-returning function, NULL for nil.
func ReturnSetNullable[T Scalar](c *Call, seq iter.Seq[*T]) {
	core.Check(srfBegin(c))
	for v := range seq {
		if v == nil {
			core.Check(srfPut(c, 0, true, false))
			continue
		}
		d, alloc := toDatum(*v)
		core.Check(srfPut(c, d, false, alloc))
	}
}

// ---- Composite results (rows) -----------------------------------------

// Row is a composite result being built: one value per column (the fields
// of a Go struct, in order). Generated code fills it with the SetField
// functions.
type Row struct {
	c      *Call
	values []uint64
	nulls  []bool
	alloc  []bool // values[i] was allocated (freed once stored in a set)
}

func (r *Row) reset() {
	r.values, r.nulls, r.alloc = r.values[:0], r.nulls[:0], r.alloc[:0]
}

func (r *Row) set(i int, d uint64, null, alloc bool) {
	for len(r.values) <= i {
		r.values = append(r.values, 0)
		r.nulls = append(r.nulls, true)
		r.alloc = append(r.alloc, false)
	}
	r.values[i], r.nulls[i], r.alloc[i] = d, null, alloc
}

// SetField sets column i.
func SetField[T Scalar](r *Row, i int, v T) {
	d, alloc := toDatum(v)
	r.set(i, d, false, alloc)
}

// SetFieldPtr sets column i, NULL for nil.
func SetFieldPtr[T Scalar](r *Row, i int, v *T) {
	if v == nil {
		r.set(i, 0, true, false)
		return
	}
	SetField(r, i, *v)
}

// SetFieldArray sets array column i.
func SetFieldArray[T Scalar](r *Row, i int, v []T) { r.set(i, arrayDatum(v), false, true) }

// SetFieldArrayPtr sets array column i, NULL for nil.
func SetFieldArrayPtr[T Scalar](r *Row, i int, v *[]T) {
	if v == nil {
		r.set(i, 0, true, false)
		return
	}
	SetFieldArray(r, i, *v)
}

// SetFieldArrayNullable sets array column i, whose nil elements are NULL.
func SetFieldArrayNullable[T Scalar](r *Row, i int, v []*T) {
	r.set(i, arrayNullableDatum(v), false, true)
}

//export gopgext_row_form
func rowForm(c *Call, n int64, values *uint64, nulls *bool, out *uint64) uintptr

//export gopgext_srf_put_row
func srfPutRow(c *Call, n int64, values *uint64, nulls *bool, alloc *bool) uintptr

func (r *Row) ptrs() (*uint64, *bool, *bool) {
	if len(r.values) == 0 {
		return nil, nil, nil
	}
	return &r.values[0], &r.nulls[0], &r.alloc[0]
}

// ReturnRow sets a composite result (a function with OUT parameters): fill
// sets its columns.
func ReturnRow[S any](c *Call, v S, fill func(S, *Row)) {
	r := Row{c: c}
	fill(v, &r)
	values, nulls, _ := r.ptrs()
	core.Check(rowForm(c, int64(len(r.values)), values, nulls, &c.result))
}

// ReturnRowPtr sets a composite result, NULL for nil.
func ReturnRowPtr[S any](c *Call, v *S, fill func(S, *Row)) {
	if v == nil {
		c.isnull = 1
		return
	}
	ReturnRow(c, *v, fill)
}

// ReturnRows returns the values of seq as the rows of a function returning
// a table (RETURNS TABLE): fill sets the columns of each.
func ReturnRows[S any](c *Call, seq iter.Seq[S], fill func(S, *Row)) {
	core.Check(srfBegin(c))
	r := Row{c: c}
	for v := range seq {
		r.reset()
		fill(v, &r)
		values, nulls, alloc := r.ptrs()
		core.Check(srfPutRow(c, int64(len(r.values)), values, nulls, alloc))
	}
}

// ---- For framework packages (gopgext/gist, ...) ------------------------

// Datum returns argument i as a raw Datum, and whether it is NULL.
func (c *Call) Datum(i int) (uint64, bool) { return c.arg(i) }

// SetDatum sets the result to a raw Datum.
func (c *Call) SetDatum(d uint64) { c.result = d }

// SetNull makes the result NULL.
func (c *Call) SetNull() { c.isnull = 1 }

// VarlenaView returns the contents of a varlena Datum (detoasted if needed,
// not copied: valid during the call).
func VarlenaView(d uint64) []byte { return varlena(d) }

// NewVarlena returns a new varlena Datum (palloc'd in the current memory
// context) holding a copy of b.
func NewVarlena(b []byte) uint64 {
	var d uint64
	core.Check(varlenaNew(unsafe.SliceData(b), int64(len(b)), &d))
	return d
}

// ToDatum converts a value to a Datum.
func ToDatum[T Scalar](v T) uint64 {
	d, _ := toDatum(v)
	return d
}

// FromDatum converts a Datum to a value.
func FromDatum[T Scalar](d uint64) T { return fromDatum[T](d) }

// ---- Arrays of extension-defined types (results) ----------------------

//export gopgext_result_elemtype
func resultElemType(c *Call, column int64, out *uint32) uintptr

// elemTypeOf returns the element type of the result (column -1) or of a
// column of the row result.
func (c *Call) elemTypeOf(column int) uint32 {
	var oid uint32
	core.Check(resultElemType(c, int64(column), &oid))
	return oid
}

// ReturnArrayOf, ReturnArrayNullableOf and ReturnArrayPtrOf set an array
// result of an extension-defined type whose representation is T.
func ReturnArrayOf[T Scalar](c *Call, v []T) { c.result = arrayDatumOf(v, c.elemTypeOf(-1)) }

func ReturnArrayNullableOf[T Scalar](c *Call, v []*T) {
	c.result = arrayNullableDatumOf(v, c.elemTypeOf(-1))
}

func ReturnArrayPtrOf[T Scalar](c *Call, v *[]T) {
	if v == nil {
		c.isnull = 1
		return
	}
	ReturnArrayOf(c, *v)
}

// SetFieldArrayOf, SetFieldArrayNullableOf and SetFieldArrayPtrOf set an
// array column of an extension-defined type.
func SetFieldArrayOf[T Scalar](r *Row, i int, v []T) {
	r.set(i, arrayDatumOf(v, r.c.elemTypeOf(i)), false, true)
}

func SetFieldArrayNullableOf[T Scalar](r *Row, i int, v []*T) {
	r.set(i, arrayNullableDatumOf(v, r.c.elemTypeOf(i)), false, true)
}

func SetFieldArrayPtrOf[T Scalar](r *Row, i int, v *[]T) {
	if v == nil {
		r.set(i, 0, true, false)
		return
	}
	SetFieldArrayOf(r, i, *v)
}

// Conv converts the elements of a slice (generated code: between an
// extension-defined type and its representation).
func Conv[T, U any](xs []T, f func(T) U) []U {
	if xs == nil {
		return nil
	}
	out := make([]U, len(xs))
	for i, x := range xs {
		out[i] = f(x)
	}
	return out
}

// ConvPtr is Conv for a NULL-able slice.
func ConvPtr[T, U any](xs *[]T, f func(T) U) *[]U {
	if xs == nil {
		return nil
	}
	out := Conv(*xs, f)
	return &out
}

// ---- Composite arguments -----------------------------------------------

//export gopgext_row_get
func rowGet(d uint64, n int64, values *uint64, nulls *bool, types *uint32) uintptr

// RowArg is a composite argument: generated code reads its columns.
type RowArg struct {
	c      *Call
	values []uint64
	nulls  []bool
	types  []uint32
}

func (r *RowArg) at(i int) Value {
	return Value{c: r.c, i: -1, d: r.values[i], null: r.nulls[i], typ: r.types[i]}
}

func readRow[S any](c *Call, d uint64, n int, read func(*RowArg) S) S {
	r := &RowArg{c: c, values: make([]uint64, n+1), nulls: make([]bool, n+1), types: make([]uint32, n+1)}
	core.Check(rowGet(d, int64(n), &r.values[0], &r.nulls[0], &r.types[0]))
	return read(r)
}

// ArgRow returns composite argument i (n columns), read by read.
func ArgRow[S any](c *Call, i int, name string, n int, read func(*RowArg) S) S {
	d, null := c.arg(i)
	if null {
		nullArg(name)
	}
	return readRow(c, d, n, read)
}

// ArgRowPtr returns composite argument i, nil for NULL.
func ArgRowPtr[S any](c *Call, i int, n int, read func(*RowArg) S) *S {
	d, null := c.arg(i)
	if null {
		return nil
	}
	v := readRow(c, d, n, read)
	return &v
}

func nullField(name string) {
	panic(&core.Error{
		Code:    "22004",
		Message: fmt.Sprintf("column %q of a composite argument must not be NULL", name),
		Hint:    "Declare the Go field as a pointer to accept NULL.",
	})
}

// Field returns column i of a composite argument (NULL raises an error).
func Field[T Scalar](r *RowArg, i int, name string) T {
	v := r.at(i)
	if v.null {
		nullField(name)
	}
	return fromDatum[T](v.d)
}

// FieldPtr returns column i, nil for NULL.
func FieldPtr[T Scalar](r *RowArg, i int) *T {
	v := r.at(i)
	if v.null {
		return nil
	}
	x := fromDatum[T](v.d)
	return &x
}

func fieldArray[T Scalar](r *RowArg, i int, name string, elemType uint32) []T {
	v := r.at(i)
	if v.null {
		nullField(name)
	}
	return arrayOf[T](v.d, elemType, name)
}

// FieldArray returns array column i.
func FieldArray[T Scalar](r *RowArg, i int, name string) []T {
	return fieldArray[T](r, i, name, typeOID[T]())
}

// FieldArrayOf returns array column i of an extension-defined type.
func FieldArrayOf[T Scalar](r *RowArg, i int, name string) []T {
	return fieldArray[T](r, i, name, 0)
}

// FieldArrayPtr returns array column i, nil for NULL.
func FieldArrayPtr[T Scalar](r *RowArg, i int, name string) *[]T {
	if r.at(i).null {
		return nil
	}
	a := FieldArray[T](r, i, name)
	return &a
}

// FieldArrayPtrOf is FieldArrayPtr for an extension-defined type.
func FieldArrayPtrOf[T Scalar](r *RowArg, i int, name string) *[]T {
	if r.at(i).null {
		return nil
	}
	a := FieldArrayOf[T](r, i, name)
	return &a
}

// FieldArrayNullable returns array column i, nil for NULL elements.
func FieldArrayNullable[T Scalar](r *RowArg, i int, name string) []*T {
	v := r.at(i)
	if v.null {
		nullField(name)
	}
	return arrayNullableOf[T](v.d, typeOID[T]())
}

// FieldArrayNullableOf is FieldArrayNullable for an extension-defined type.
func FieldArrayNullableOf[T Scalar](r *RowArg, i int, name string) []*T {
	v := r.at(i)
	if v.null {
		nullField(name)
	}
	return arrayNullableOf[T](v.d, 0)
}

// ---- Polymorphic arguments and results ---------------------------------

// AnyElement and AnyArray are the SQL types anyelement and anyarray.
type (
	AnyElement = core.AnyElement
	AnyArray   = core.AnyArray
)

//export gopgext_arg_type
func argType(c *Call, i int64, out *uint32) uintptr

func (c *Call) argType(i int) uint32 {
	var oid uint32
	core.Check(argType(c, int64(i), &oid))
	return oid
}

// ArgAny returns polymorphic argument i.
func ArgAny(c *Call, i int, name string) AnyElement {
	d, null := c.arg(i)
	if null {
		nullArg(name)
	}
	return core.MakeAnyElement(d, c.argType(i))
}

// ArgAnyPtr returns polymorphic argument i, nil for NULL.
func ArgAnyPtr(c *Call, i int) *AnyElement {
	d, null := c.arg(i)
	if null {
		return nil
	}
	v := core.MakeAnyElement(d, c.argType(i))
	return &v
}

// ArgAnyArray returns anyarray argument i.
func ArgAnyArray(c *Call, i int, name string) AnyArray {
	d, null := c.arg(i)
	if null {
		nullArg(name)
	}
	return core.MakeAnyArray(d)
}

// ArgAnyArrayPtr returns anyarray argument i, nil for NULL.
func ArgAnyArrayPtr(c *Call, i int) *AnyArray {
	d, null := c.arg(i)
	if null {
		return nil
	}
	v := core.MakeAnyArray(d)
	return &v
}

// ReturnAny sets a polymorphic result.
func ReturnAny(c *Call, v AnyElement) { c.result = v.Datum() }

// ReturnAnyPtr sets a polymorphic result, NULL for nil.
func ReturnAnyPtr(c *Call, v *AnyElement) {
	if v == nil {
		c.isnull = 1
		return
	}
	ReturnAny(c, *v)
}

// ReturnAnyArray sets an anyarray result.
func ReturnAnyArray(c *Call, v AnyArray) { c.result = v.Datum() }

// ReturnAnyArrayPtr sets an anyarray result, NULL for nil.
func ReturnAnyArrayPtr(c *Call, v *AnyArray) {
	if v == nil {
		c.isnull = 1
		return
	}
	ReturnAnyArray(c, *v)
}

// ReturnAnySet returns the values of seq as the rows of SETOF anyelement.
func ReturnAnySet(c *Call, seq iter.Seq[AnyElement]) {
	core.Check(srfBegin(c))
	for v := range seq {
		core.Check(srfPut(c, v.Datum(), false, false))
	}
}
