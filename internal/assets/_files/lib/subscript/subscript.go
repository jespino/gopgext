// Package subscript implements subscripting of data types in Go:
// value[key] to read an element and SET col[key] = x (or x[key] := ... in
// PL/pgSQL) to replace one, like hstore's h['key'] or jsonb's j['a'].
//
// The extension registers a Type under the name of its SQL type (in main
// or a package initializer):
//
//	subscript.Register("kv", subscript.Type{
//		Subscript: "text", // subscripts are coerced to text
//		Result:    "text", // kv[...] is text
//		Fetch:     kvGet,  // the element, or ok=false for NULL
//		Assign:    kvSet,  // the container with the element replaced
//	})
//
// and its sql/ files give the type this package's handler:
//
//	CREATE FUNCTION kv_subscript_handler(internal) RETURNS internal
//		AS 'MODULE_PATHNAME', 'gopgext_subscript_handler' LANGUAGE C STRICT IMMUTABLE;
//	ALTER TYPE kv SET (SUBSCRIPT = kv_subscript_handler);
//
// Containers, subscripts and elements are variable-length values (types
// stored like bytea or text): the functions get their bytes, valid during
// the call, and return new ones. Slices (a[1:2]) aren't supported.
package subscript

import (
	"fmt"
	"unsafe"

	"gopgext/fmgr"
	"gopgext/internal/core"
	"gopgext/pg"
)

// Type is the subscripting of a data type. Fetch and Assign are required.
type Type struct {
	// Subscript is the SQL type subscripts are coerced to (assignment
	// casts), e.g. "text".
	Subscript string
	// Result is the SQL type of an element, e.g. "text".
	Result string
	// Subscripts is the number of subscripts allowed (0: one).
	Subscripts int

	// Fetch returns the element at subscripts (one per subscript of the
	// expression, never NULL: a NULL container or subscript makes the
	// element NULL without calling Fetch); ok=false makes it NULL.
	Fetch func(container []byte, subscripts [][]byte) (element []byte, ok bool, err error)

	// Assign returns the container with the element at subscripts set to
	// element. A nil container, subscript or element is NULL.
	Assign func(container []byte, subscripts [][]byte, element []byte) ([]byte, error)
}

var types = map[string]*Type{}

// Register registers the subscripting of the SQL type named typeName.
func Register(typeName string, t Type) {
	if t.Fetch == nil || t.Assign == nil || t.Subscript == "" || t.Result == "" {
		panic(fmt.Sprintf("subscript.Register(%q): Subscript, Result, Fetch and Assign are required", typeName))
	}
	if t.Subscripts <= 0 {
		t.Subscripts = 1
	}
	types[typeName] = &t
}

// ---- Entry points (frameworks/subscript/subscript.c) ----------------------

func lookup(c *fmgr.Call) (*Type, string) {
	d, _ := c.Datum(0)
	name := string(fmgr.FromDatum[fmgr.CString](d))
	if t := types[name]; t != nil {
		return t, name
	}
	panic(&pg.Error{Code: "55000", Message: fmt.Sprintf("type %q has no subscript.Type", name),
		Hint: fmt.Sprintf("Call subscript.Register(%q, ...) in main.", name)})
}

func arg(c *fmgr.Call, i int) uint64 {
	d, _ := c.Datum(i)
	return d
}

// subscripts reads n subscript Datums (nil for NULL ones, if nulls is set).
func subscripts(n int, datums, nulls uintptr) [][]byte {
	ds := unsafe.Slice((*uint64)(unsafe.Pointer(datums)), n)
	var ns []bool
	if nulls != 0 {
		ns = unsafe.Slice((*bool)(unsafe.Pointer(nulls)), n)
	}
	out := make([][]byte, n)
	for i, d := range ds {
		if ns != nil && ns[i] {
			continue
		}
		out[i] = nonNil(fmgr.VarlenaView(d))
	}
	return out
}

func nonNil(b []byte) []byte {
	if b == nil {
		return []byte{}
	}
	return b
}

//export gopgext_subscript_set_config
func setConfig(cfg uintptr, subtype, restype *byte, max int64) uintptr

//export gopgext_go_subscript_config
func config(c *fmgr.Call) {
	defer c.End(c.Begin(true))
	t, _ := lookup(c)
	core.Check(setConfig(uintptr(arg(c, 1)), core.CBytes(t.Subscript), core.CBytes(t.Result), int64(t.Subscripts)))
}

//export gopgext_go_subscript_fetch
func fetch(c *fmgr.Call) {
	defer c.End(c.Begin(true))
	t, _ := lookup(c)
	subs := subscripts(int(int64(arg(c, 2))), uintptr(arg(c, 3)), 0)
	v, ok, err := t.Fetch(nonNil(fmgr.VarlenaView(arg(c, 1))), subs)
	if err != nil {
		c.Fail(err)
		return
	}
	if !ok {
		c.SetNull()
		return
	}
	c.SetDatum(fmgr.NewVarlena(v))
}

//export gopgext_go_subscript_assign
func assign(c *fmgr.Call) {
	defer c.End(c.Begin(true))
	t, _ := lookup(c)
	var container, elem []byte
	if arg(c, 2) == 0 {
		container = nonNil(fmgr.VarlenaView(arg(c, 1)))
	}
	if arg(c, 7) == 0 {
		elem = nonNil(fmgr.VarlenaView(arg(c, 6)))
	}
	subs := subscripts(int(int64(arg(c, 3))), uintptr(arg(c, 4)), uintptr(arg(c, 5)))
	v, err := t.Assign(container, subs, elem)
	if err != nil {
		c.Fail(err)
		return
	}
	c.SetDatum(fmgr.NewVarlena(nonNil(v)))
}
