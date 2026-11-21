// Package gist writes the support functions of GiST operator classes in Go:
// the index method PostGIS, ltree, cube, pg_trgm, range types and many
// others use. An operator class decides how its keys summarize the values
// below them; GiST does the rest (the tree, concurrency, WAL, vacuum).
//
// The extension registers an OpClass under the name of its operator class
// (in main or a package initializer):
//
//	gist.Register("box_ops", gist.OpClass{
//		Compress:   toBox,      // indexed value -> key (nil: the value is the key)
//		Consistent: boxMatches, // can key (or keys below it) match the query?
//		Union:      boxUnion,   // the key covering some keys
//		Penalty:    boxGrowth,  // the cost of adding a key to a subtree
//		PickSplit:  boxSplit,   // splits the keys of a full page in two
//		Distance:   boxDist,    // optional: ORDER BY col <-> query (k-NN)
//	})
//
// and its sql/ files declare the support functions, which all use the C
// symbols of this package, and the operator class itself:
//
//	CREATE FUNCTION shape_gist_consistent(internal, shape, smallint, oid, internal)
//		RETURNS boolean AS 'MODULE_PATHNAME', 'gopgext_gist_consistent' LANGUAGE C STRICT;
//	CREATE FUNCTION shape_gist_union(internal, internal) RETURNS bytea
//		AS 'MODULE_PATHNAME', 'gopgext_gist_union' LANGUAGE C STRICT;
//	CREATE FUNCTION shape_gist_compress(internal) RETURNS internal
//		AS 'MODULE_PATHNAME', 'gopgext_gist_compress' LANGUAGE C STRICT;
//	CREATE FUNCTION shape_gist_penalty(internal, internal, internal) RETURNS internal
//		AS 'MODULE_PATHNAME', 'gopgext_gist_penalty' LANGUAGE C STRICT;
//	CREATE FUNCTION shape_gist_picksplit(internal, internal) RETURNS internal
//		AS 'MODULE_PATHNAME', 'gopgext_gist_picksplit' LANGUAGE C STRICT;
//	CREATE FUNCTION shape_gist_same(bytea, bytea, internal) RETURNS internal
//		AS 'MODULE_PATHNAME', 'gopgext_gist_same' LANGUAGE C STRICT;
//	CREATE FUNCTION shape_gist_distance(internal, shape, smallint, oid, internal)
//		RETURNS float8 AS 'MODULE_PATHNAME', 'gopgext_gist_distance' LANGUAGE C STRICT;
//
//	CREATE OPERATOR CLASS box_ops DEFAULT FOR TYPE shape USING gist AS
//		OPERATOR 3 && , OPERATOR 7 ~ , OPERATOR 8 @ ,
//		OPERATOR 15 <-> FOR ORDER BY float_ops,
//		FUNCTION 1 shape_gist_consistent (internal, shape, smallint, oid, internal),
//		FUNCTION 2 shape_gist_union (internal, internal),
//		FUNCTION 3 shape_gist_compress (internal),
//		FUNCTION 5 shape_gist_penalty (internal, internal, internal),
//		FUNCTION 6 shape_gist_picksplit (internal, internal),
//		FUNCTION 7 shape_gist_same (bytea, bytea, internal),
//		FUNCTION 8 shape_gist_distance (internal, shape, smallint, oid, internal),
//		STORAGE bytea;
//
// The functions find their OpClass through the operator family they belong
// to (named like the operator class, unless it was created with FAMILY): a
// support function must belong to one family only.
//
// Indexed values, keys and queries are bytes, which are only valid during
// the call (copy them to keep them): variable-length values (types stored
// like bytea, e.g. declared with //gopgext:type over pg.Varlena) as they
// are, fixed-size ones copied (a //gopgext:type over [N]byte: N bytes; a
// by-value type: its bytes, little-endian, e.g. 4 for an int4 query). A
// query has the type of the operator's right-hand argument. The functions
// return new keys, of the key type: STORAGE (usually bytea), or the indexed
// type without one (no Compress). NULLs never reach the functions: GiST
// handles them. gopgext_gist_decompress, which does nothing, is there for
// install scripts written for a C operator class (see //gopgext:alias).
//
// An operator class with parameters (OpClass.Options, e.g. a signature
// length) adds the options support function, and one that declares a
// decompress function (support function 4) can use the identity:
//
//	CREATE FUNCTION shape_gist_options(internal) RETURNS void
//		AS 'MODULE_PATHNAME', 'gopgext_gist_options' LANGUAGE C;
//	CREATE FUNCTION shape_gist_decompress(internal) RETURNS internal
//		AS 'MODULE_PATHNAME', 'gopgext_gist_decompress' LANGUAGE C STRICT;
//	-- FUNCTION 4 shape_gist_decompress (internal), FUNCTION 10 shape_gist_options (internal)
//
// Strategy numbers are the operator numbers of the operator class (3 for
// && above). Results are exact unless the function asks for a recheck:
// PostgreSQL then evaluates the operator on the row (so a lossy key, e.g. a
// bounding box of a polygon, can still answer exact operators).
package gist

import (
	"bytes"
	"fmt"
	"math"
	"unsafe"

	"gopgext/fmgr"
	"gopgext/internal/core"
	"gopgext/pg"
)

// OpClass is a GiST operator class. Consistent, Union, Penalty and
// PickSplit are required.
type OpClass struct {
	// Compress returns the key stored for an indexed value (a leaf key).
	// nil: the value itself is the key (STORAGE must then be its type).
	Compress func(value []byte) ([]byte, error)

	// Consistent reports whether key may match query with the operator of
	// the given strategy number: on a leaf, key is the key of one row; on
	// an inner page, it covers all the keys below it, and match means some
	// of them may match. recheck (leaves only) asks PostgreSQL to evaluate
	// the operator on the row to confirm a match.
	Consistent func(key, query []byte, strategy int, leaf bool) (match, recheck bool)

	// Union returns a key that covers all the keys.
	Union func(keys [][]byte) []byte

	// Penalty returns how much worse key gets as a cover for added (e.g. the
	// growth of a bounding box): new rows go where the penalty is lowest.
	// It must be 0 or more.
	Penalty func(key, added []byte) float64

	// PickSplit splits the keys of a full page in two non-empty groups:
	// the indexes of the keys going left and right (each index once).
	PickSplit func(keys [][]byte) (left, right []int)

	// Same reports whether two keys are equal (nil: bytes.Equal).
	Same func(a, b []byte) bool

	// Distance returns the distance from key to query for the ordering
	// operator of the given strategy (ORDER BY col <-> query uses the
	// index): on an inner page, a lower bound of the distance of the rows
	// below. recheck (leaves only) means the distance is a lower bound:
	// PostgreSQL then computes the exact one with the operator. nil: the
	// operator class has no ordering operators.
	Distance func(key, query []byte, strategy int, leaf bool) (distance float64, recheck bool)

	// Options are the operator class's integer parameters, set per index:
	// CREATE INDEX ... USING gist (col opclass(name = value)). The
	// functions read them with Option. They need the options support
	// function (10) in the operator family (see the package doc).
	Options []IntOption
}

// IntOption is an integer parameter of an operator class.
type IntOption struct {
	Name, Desc        string
	Default, Min, Max int
}

var opclasses = map[string]*OpClass{}

// current is the OpClass of the support function being called.
var current *OpClass

//export gopgext_gist_option
func gistOption(i int64, out *int64) bool

// Option returns the value of the operator class parameter name for the
// index the current support function works on (its Default if the index
// has none, e.g. it was created before the parameter existed). It must be
// called from the OpClass's functions.
func Option(name string) int {
	if current == nil {
		panic("gist.Option called outside a support function")
	}
	for i, o := range current.Options {
		if o.Name == name {
			var v int64
			if gistOption(int64(i), &v) {
				return int(v)
			}
			return o.Default
		}
	}
	panic(fmt.Sprintf("gist.Option: the operator class has no option %q", name))
}

// Register registers the operator class whose support functions belong to
// the operator family family (the name of the operator class, unless it
// was created with FAMILY).
func Register(family string, oc OpClass) {
	if oc.Consistent == nil || oc.Union == nil || oc.Penalty == nil || oc.PickSplit == nil {
		panic(fmt.Sprintf("gist.Register(%q): Consistent, Union, Penalty and PickSplit are required", family))
	}
	if oc.Same == nil {
		oc.Same = bytes.Equal
	}
	opclasses[family] = &oc
}

// ---- Entry points (frameworks/gist/gist.c) ------------------------------

func lookup(c *fmgr.Call) *OpClass {
	d, _ := c.Datum(0)
	name := fmgr.FromDatum[fmgr.CString](d)
	if oc := opclasses[string(name)]; oc != nil {
		current = oc
		return oc
	}
	panic(&pg.Error{Code: "55000", Message: fmt.Sprintf("GiST operator family %q has no gist.OpClass", name),
		Hint: fmt.Sprintf("Call gist.Register(%q, ...) in main.", name)})
}

func arg(c *fmgr.Call, i int) uint64 {
	d, _ := c.Datum(i)
	return d
}

func bytesArg(c *fmgr.Call, i int) []byte { return fmgr.VarlenaView(arg(c, i)) }

// keysArg reads n key Datums from a C array.
func keysArg(c *fmgr.Call, ni, pi int) [][]byte {
	n := int(int64(arg(c, ni)))
	ds := unsafe.Slice((*uint64)(unsafe.Pointer(uintptr(arg(c, pi)))), n)
	keys := make([][]byte, n)
	for i, d := range ds {
		keys[i] = fmgr.VarlenaView(d)
	}
	return keys
}

func setBool(c *fmgr.Call, i int, v bool) {
	*(*bool)(unsafe.Pointer(uintptr(arg(c, i)))) = v
}

func newKey(b []byte) uint64 {
	if b == nil {
		b = []byte{}
	}
	return fmgr.NewVarlena(b)
}

//export gopgext_go_gist_compress
func compress(c *fmgr.Call) {
	defer c.End(c.Begin(true))
	oc := lookup(c)
	if oc.Compress == nil {
		c.SetNull()
		return
	}
	key, err := oc.Compress(bytesArg(c, 1))
	if err != nil {
		c.Fail(err)
		return
	}
	c.SetDatum(newKey(key))
}

//export gopgext_go_gist_consistent
func consistent(c *fmgr.Call) {
	defer c.End(c.Begin(true))
	oc := lookup(c)
	leaf := arg(c, 4) != 0
	match, recheck := oc.Consistent(bytesArg(c, 1), bytesArg(c, 2), int(int64(arg(c, 3))), leaf)
	setBool(c, 5, match && recheck && leaf)
	c.SetDatum(fmgr.ToDatum(match))
}

//export gopgext_go_gist_union
func union(c *fmgr.Call) {
	defer c.End(c.Begin(true))
	oc := lookup(c)
	c.SetDatum(newKey(oc.Union(keysArg(c, 1, 2))))
}

//export gopgext_go_gist_penalty
func penalty(c *fmgr.Call) {
	defer c.End(c.Begin(true))
	oc := lookup(c)
	p := oc.Penalty(bytesArg(c, 1), bytesArg(c, 2))
	if math.IsNaN(p) || p < 0 {
		p = 0
	}
	c.SetDatum(fmgr.ToDatum(p))
}

//export gopgext_go_gist_picksplit
func picksplit(c *fmgr.Call) {
	defer c.End(c.Begin(true))
	oc := lookup(c)
	keys := keysArg(c, 1, 2)
	n := len(keys)
	sides := unsafe.Slice((*int32)(unsafe.Pointer(uintptr(arg(c, 3)))), n)
	left, right := oc.PickSplit(keys)
	seen := make([]int8, n)
	ok := len(left) > 0 && len(right) > 0 && len(left)+len(right) == n
	for side, idx := range [][]int{left, right} {
		for _, i := range idx {
			if !ok || i < 0 || i >= n || seen[i] != 0 {
				ok = false
				break
			}
			seen[i] = int8(side + 1)
		}
	}
	if !ok {
		// Not a valid split: fall back to halves, so the index stays usable.
		left, right = nil, nil
		for i := range keys {
			if i < n/2 {
				left = append(left, i)
			} else {
				right = append(right, i)
			}
		}
	}
	lkeys := make([][]byte, 0, len(left))
	for _, i := range left {
		sides[i] = 0
		lkeys = append(lkeys, keys[i])
	}
	rkeys := make([][]byte, 0, len(right))
	for _, i := range right {
		sides[i] = 1
		rkeys = append(rkeys, keys[i])
	}
	*(*uint64)(unsafe.Pointer(uintptr(arg(c, 4)))) = newKey(oc.Union(lkeys))
	*(*uint64)(unsafe.Pointer(uintptr(arg(c, 5)))) = newKey(oc.Union(rkeys))
}

//export gopgext_go_gist_same
func same(c *fmgr.Call) {
	defer c.End(c.Begin(true))
	oc := lookup(c)
	c.SetDatum(fmgr.ToDatum(oc.Same(bytesArg(c, 1), bytesArg(c, 2))))
}

//export gopgext_go_gist_distance
func distance(c *fmgr.Call) {
	defer c.End(c.Begin(true))
	oc := lookup(c)
	if oc.Distance == nil {
		panic(&pg.Error{Code: "0A000", Message: "this GiST operator class has no Distance function"})
	}
	leaf := arg(c, 4) != 0
	d, recheck := oc.Distance(bytesArg(c, 1), bytesArg(c, 2), int(int64(arg(c, 3))), leaf)
	setBool(c, 5, recheck && leaf)
	c.SetDatum(fmgr.ToDatum(d))
}

//export gopgext_gist_init_options
func initOptions(relopts uintptr, size int64) uintptr

//export gopgext_gist_add_option
func addOption(relopts uintptr, name, desc *byte, def, min, max, offset int64) uintptr

//export gopgext_go_gist_options
func options(c *fmgr.Call) {
	defer c.End(c.Begin(true))
	oc := lookup(c)
	relopts := uintptr(arg(c, 1))
	core.Check(initOptions(relopts, int64(4+4*len(oc.Options))))
	for i, o := range oc.Options {
		core.Check(addOption(relopts, core.CBytes(o.Name), core.CBytes(o.Desc),
			int64(o.Default), int64(o.Min), int64(o.Max), int64(4+4*i)))
	}
}
