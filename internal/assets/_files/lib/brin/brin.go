// Package brin writes BRIN operator classes in Go. A BRIN index keeps one
// small summary per block range (a group of consecutive table pages, 128 by
// default): scans skip the ranges whose summary can't match. It suits large
// tables whose values correlate with their physical order (timestamps,
// sequences, data loaded in order), at a tiny fraction of a btree's size.
//
// The extension registers an OpClass under the name of its operator class
// (in main or a package initializer):
//
//	brin.Register("epoch_minmax_ops", brin.OpClass{
//		Add:        addValue,   // summary of a range, with one more value
//		Consistent: mayMatch,   // can a range with this summary match the query?
//		Union:      merge,      // summary of two ranges
//	})
//
// and its sql/ files declare the support functions, which all use the C
// symbols of this package, and the operator class itself:
//
//	CREATE FUNCTION epoch_brin_opcinfo(internal) RETURNS internal
//		AS 'MODULE_PATHNAME', 'gopgext_brin_opcinfo' LANGUAGE C STRICT;
//	CREATE FUNCTION epoch_brin_add_value(internal, internal, internal, internal) RETURNS boolean
//		AS 'MODULE_PATHNAME', 'gopgext_brin_add_value' LANGUAGE C STRICT;
//	CREATE FUNCTION epoch_brin_consistent(internal, internal, internal, integer) RETURNS boolean
//		AS 'MODULE_PATHNAME', 'gopgext_brin_consistent' LANGUAGE C STRICT;
//	CREATE FUNCTION epoch_brin_union(internal, internal, internal) RETURNS boolean
//		AS 'MODULE_PATHNAME', 'gopgext_brin_union' LANGUAGE C STRICT;
//
//	CREATE OPERATOR CLASS epoch_minmax_ops DEFAULT FOR TYPE epoch USING brin AS
//		OPERATOR 1 < , OPERATOR 2 <= , OPERATOR 3 = , OPERATOR 4 >= , OPERATOR 5 > ,
//		FUNCTION 1 epoch_brin_opcinfo(internal),
//		FUNCTION 2 epoch_brin_add_value(internal, internal, internal, internal),
//		FUNCTION 3 epoch_brin_consistent(internal, internal, internal, integer),
//		FUNCTION 4 epoch_brin_union(internal, internal, internal);
//
// The functions find their OpClass through the operator family of the index
// column (named like the operator class, unless it was created with FAMILY).
//
// A summary is a []byte the OpClass defines (stored as bytea: keep it
// small, it's stored once per range). Values and queries are bytes too: the
// contents of a variable-size value (a type stored like bytea, e.g. a
// //gopgext:type over pg.Varlena or string), or the bytes of a fixed-size
// one (a //gopgext:type over int64 is its 8 bytes, little-endian). All of
// them are only valid during the call: copy them to keep them. NULLs never
// reach the functions: BRIN tracks them itself (IS NULL and IS NOT NULL
// work without the OpClass).
//
// Strategy numbers are the operator numbers of the operator class (3 for =
// above). Consistent may say a range matches when it doesn't (a lossy
// summary, like a Bloom filter): PostgreSQL checks every row of the ranges
// it returns. It must never say a range doesn't match when it might: rows
// would be missing from the results.
package brin

import (
	"bytes"
	"fmt"
	"unsafe"

	"gopgext/fmgr"
	"gopgext/pg"
)

// OpClass is a BRIN operator class. All functions are required.
type OpClass struct {
	// Add returns the summary of a block range with value added: summary
	// is nil for a range without values yet. changed is false if summary
	// already covers value (the range isn't rewritten).
	Add func(summary, value []byte) (newSummary []byte, changed bool)

	// Consistent reports whether a range with this summary may hold rows
	// matching query with the operator of the given strategy number. False
	// positives are fine (rows are rechecked); false negatives lose rows.
	Consistent func(summary []byte, strategy int, query []byte) bool

	// Union returns a summary covering the ranges of two summaries.
	Union func(a, b []byte) []byte
}

var opclasses = map[string]*OpClass{}

// Register registers the operator class of the operator family family (the
// name of the operator class, unless it was created with FAMILY).
func Register(family string, oc OpClass) {
	if oc.Add == nil || oc.Consistent == nil || oc.Union == nil {
		panic(fmt.Sprintf("brin.Register(%q): Add, Consistent and Union are required", family))
	}
	opclasses[family] = &oc
}

// ---- Entry points (frameworks/brin/brin.c) ------------------------------

func lookup(c *fmgr.Call) *OpClass {
	d, _ := c.Datum(0)
	name := fmgr.FromDatum[fmgr.CString](d)
	if oc := opclasses[string(name)]; oc != nil {
		return oc
	}
	panic(&pg.Error{Code: "55000", Message: fmt.Sprintf("BRIN operator family %q has no brin.OpClass", name),
		Hint: fmt.Sprintf("Call brin.Register(%q, ...) in main.", name)})
}

func arg(c *fmgr.Call, i int) uint64 {
	d, _ := c.Datum(i)
	return d
}

// bytesArg returns the bytes at the pointer and length of arguments i, i+1.
func bytesArg(c *fmgr.Call, i int) []byte {
	p, n := uintptr(arg(c, i)), int(int64(arg(c, i+1)))
	if n == 0 {
		return []byte{}
	}
	return unsafe.Slice((*byte)(unsafe.Pointer(p)), n)
}

func newSummary(b []byte) uint64 {
	if b == nil {
		b = []byte{}
	}
	return fmgr.NewVarlena(b)
}

//export gopgext_go_brin_add
func add(c *fmgr.Call) {
	defer c.End(c.Begin(true))
	oc := lookup(c)
	var summary []byte
	if arg(c, 3) != 0 {
		summary = bytesArg(c, 1)
	}
	s, changed := oc.Add(summary, bytesArg(c, 4))
	if !changed && summary != nil {
		c.SetNull()
		return
	}
	c.SetDatum(newSummary(s))
}

//export gopgext_go_brin_consistent
func consistent(c *fmgr.Call) {
	defer c.End(c.Begin(true))
	oc := lookup(c)
	summary := bytesArg(c, 1)
	n := int(int64(arg(c, 3)))
	strategies := unsafe.Slice((*int32)(unsafe.Pointer(uintptr(arg(c, 4)))), n)
	ptrs := unsafe.Slice((*uintptr)(unsafe.Pointer(uintptr(arg(c, 5)))), n)
	lens := unsafe.Slice((*int64)(unsafe.Pointer(uintptr(arg(c, 6)))), n)
	match := true
	for i := 0; i < n && match; i++ {
		var q []byte
		if lens[i] > 0 {
			q = unsafe.Slice((*byte)(unsafe.Pointer(ptrs[i])), lens[i])
		} else {
			q = []byte{}
		}
		match = oc.Consistent(summary, int(strategies[i]), q)
	}
	c.SetDatum(fmgr.ToDatum(match))
}

//export gopgext_go_brin_union
func union(c *fmgr.Call) {
	defer c.End(c.Begin(true))
	oc := lookup(c)
	a, b := bytesArg(c, 1), bytesArg(c, 3)
	if bytes.Equal(a, b) {
		c.SetDatum(newSummary(a))
		return
	}
	c.SetDatum(newSummary(oc.Union(a, b)))
}
