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

// CString is the SQL type cstring.
type CString = core.CString

const callReturnedError = 1 // GOPGEXT_CALL_RETURNED_ERROR

// Saved is the state Begin saves and End restores.
type Saved struct {
	seq      uint64
	readOnly bool
}

// Begin starts a call. readOnly is set for STABLE and IMMUTABLE functions.
func (c *Call) Begin(readOnly bool) Saved {
	s := Saved{core.Seq, core.ReadOnly}
	core.Seq = core.NextSeq()
	core.ReadOnly = readOnly
	return s
}

// End finishes a call (deferred): a panic becomes the call's error.
func (c *Call) End(s Saved) {
	if r := recover(); r != nil {
		c.panicked(r)
	}
	core.Seq, core.ReadOnly = s.seq, s.readOnly
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
	bool | int16 | int32 | int64 | int | float32 | float64 | string | []byte | CString
}

func (c *Call) arg(i int) (uint64, bool) {
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
	}
	return 17 // []byte: bytea
}

//export gopgext_array_get
func arrayGet(d uint64, elemType uint32, n *int64, elems *unsafe.Pointer, nulls *unsafe.Pointer) uintptr

// ArgArray returns array argument i (any number of dimensions, flattened).
// NULL arrays and NULL elements raise an error.
func ArgArray[T Scalar](c *Call, i int, name string) []T {
	d, null := c.arg(i)
	if null {
		nullArg(name)
	}
	var n int64
	var elems, nulls unsafe.Pointer
	core.Check(arrayGet(d, typeOID[T](), &n, &elems, &nulls))
	ds := unsafe.Slice((*uint64)(elems), n)
	ns := unsafe.Slice((*bool)(nulls), n)
	out := make([]T, n)
	for j := range out {
		if ns[j] {
			panic(&core.Error{Code: "22004", Message: fmt.Sprintf("array argument %q must not contain NULLs", name)})
		}
		out[j] = fromDatum[T](ds[j])
	}
	return out
}

// ArgArrayNullable returns array argument i, with nil for NULL elements.
func ArgArrayNullable[T Scalar](c *Call, i int, name string) []*T {
	d, null := c.arg(i)
	if null {
		nullArg(name)
	}
	var n int64
	var elems, nulls unsafe.Pointer
	core.Check(arrayGet(d, typeOID[T](), &n, &elems, &nulls))
	ds := unsafe.Slice((*uint64)(elems), n)
	ns := unsafe.Slice((*bool)(nulls), n)
	out := make([]*T, n)
	for j := range out {
		if !ns[j] {
			v := fromDatum[T](ds[j])
			out[j] = &v
		}
	}
	return out
}

// ArgArrayPtr returns array argument i, nil for NULL.
func ArgArrayPtr[T Scalar](c *Call, i int, name string) *[]T {
	if _, null := c.arg(i); null {
		return nil
	}
	a := ArgArray[T](c, i, name)
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
	case CString:
		p := strdup(core.StringData(string(x)), int64(len(x)))
		if p == nil {
			panic(&core.Error{Code: "54000", Message: "cstring result too large"})
		}
		return uint64(uintptr(unsafe.Pointer(p))), true
	}
	return 0, false
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

// ReturnArray sets a one-dimensional array result.
func ReturnArray[T Scalar](c *Call, v []T) {
	elems := make([]uint64, len(v)+1)
	nulls := make([]bool, len(v)+1)
	for i, x := range v {
		elems[i], _ = toDatum(x)
	}
	core.Check(arrayNew(typeOID[T](), int64(len(v)), &elems[0], &nulls[0], &c.result))
}

// ReturnArrayNullable sets a one-dimensional array result whose nil
// elements are NULL.
func ReturnArrayNullable[T Scalar](c *Call, v []*T) {
	elems := make([]uint64, len(v)+1)
	nulls := make([]bool, len(v)+1)
	for i, x := range v {
		if x == nil {
			nulls[i] = true
		} else {
			elems[i], _ = toDatum(*x)
		}
	}
	core.Check(arrayNew(typeOID[T](), int64(len(v)), &elems[0], &nulls[0], &c.result))
}

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
