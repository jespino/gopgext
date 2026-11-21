// Package gin writes the support functions of GIN operator classes in Go:
// the inverted index of full text search, pg_trgm, jsonb and arrays. An
// operator class decides which keys a value is indexed under, which keys a
// query looks for, and whether an item with some of those keys matches;
// GIN does the rest (the key tree, posting lists, the pending list, WAL,
// vacuum).
//
// The extension registers an OpClass under the name of its operator class
// (in main or a package initializer):
//
//	gin.Register("trgm_gin_ops", gin.OpClass{
//		ExtractValue: trigrams,      // indexed value -> keys
//		ExtractQuery: queryTrigrams, // query -> keys to look for (and a search mode)
//		Consistent:   matches,       // does an item with these keys match?
//	})
//
// and its sql/ files declare the support functions, which all use the C
// symbols of this package, and the operator class itself:
//
//	CREATE FUNCTION trgm_gin_extract_value(text, internal, internal)
//		RETURNS internal AS 'MODULE_PATHNAME', 'gopgext_gin_extract_value' LANGUAGE C STRICT;
//	CREATE FUNCTION trgm_gin_extract_query(text, internal, smallint, internal, internal, internal, internal)
//		RETURNS internal AS 'MODULE_PATHNAME', 'gopgext_gin_extract_query' LANGUAGE C STRICT;
//	CREATE FUNCTION trgm_gin_consistent(internal, smallint, text, integer, internal, internal, internal, internal)
//		RETURNS boolean AS 'MODULE_PATHNAME', 'gopgext_gin_consistent' LANGUAGE C STRICT;
//	-- optional:
//	CREATE FUNCTION trgm_gin_compare(bytea, bytea) RETURNS integer
//		AS 'MODULE_PATHNAME', 'gopgext_gin_compare' LANGUAGE C STRICT;
//	CREATE FUNCTION trgm_gin_compare_partial(bytea, bytea, smallint, internal) RETURNS integer
//		AS 'MODULE_PATHNAME', 'gopgext_gin_compare_partial' LANGUAGE C STRICT;
//	CREATE FUNCTION trgm_gin_tri_consistent(internal, smallint, text, integer, internal, internal, internal)
//		RETURNS "char" AS 'MODULE_PATHNAME', 'gopgext_gin_tri_consistent' LANGUAGE C STRICT;
//
//	CREATE OPERATOR CLASS trgm_gin_ops FOR TYPE text USING gin AS
//		OPERATOR 1 % (text, text),
//		OPERATOR 3 ~~ (text, text),
//		FUNCTION 2 trgm_gin_extract_value (text, internal, internal),
//		FUNCTION 3 trgm_gin_extract_query (text, internal, smallint, internal, internal, internal, internal),
//		FUNCTION 4 trgm_gin_consistent (internal, smallint, text, integer, internal, internal, internal, internal),
//		FUNCTION 6 trgm_gin_tri_consistent (internal, smallint, text, integer, internal, internal, internal),
//		STORAGE bytea;
//
// The functions find their OpClass through the operator family they belong
// to (named like the operator class, unless it was created with FAMILY): a
// support function must belong to one family only.
//
// Indexed values, queries and keys are variable-length values (types stored
// like bytea): the functions get their bytes, valid only during the call,
// and return new ones. Keys are usually stored as bytea (STORAGE bytea).
// Without a compare function (support function 1), keys are ordered by the
// default btree operator class of the key type (byte order for bytea);
// with one, it's OpClass.Compare. NULL values and queries never reach the
// functions (GIN indexes NULLs itself), and keys are never NULL.
//
// Strategy numbers are the operator numbers of the operator class. Matches
// are exact unless Consistent asks for a recheck: PostgreSQL then evaluates
// the operator on the row (for keys that don't capture the whole operator,
// like trigrams for LIKE).
package gin

import (
	"bytes"
	"fmt"
	"unsafe"

	"gopgext/fmgr"
	"gopgext/internal/core"
	"gopgext/pg"
)

// SearchMode is how GIN searches when a query has few or no keys.
type SearchMode int

const (
	// Default: items having at least one of the keys (a query without
	// keys matches nothing).
	Default SearchMode = 0
	// IncludeEmpty: also items indexed without keys.
	IncludeEmpty SearchMode = 1
	// All: every item (without NULLs), checked with Consistent: for
	// queries whose keys can't narrow the search, e.g. LIKE '%a%' for
	// trigrams.
	All SearchMode = 2
)

// Query is what a query looks for in the index.
type Query struct {
	// Keys are the keys to look for.
	Keys [][]byte
	// Partial marks the keys that are partial matches (nil: none): GIN
	// then scans the indexed keys from that key on, asking
	// OpClass.ComparePartial which ones match (e.g. prefixes).
	Partial []bool
	// Mode is the search mode.
	Mode SearchMode
}

// Ternary is a check value of TriConsistent.
type Ternary int8

const (
	False Ternary = 0 // the item doesn't have the key / doesn't match
	True  Ternary = 1 // it has it / it matches
	Maybe Ternary = 2 // unknown / it may match (recheck)
)

// OpClass is a GIN operator class. ExtractValue, ExtractQuery and
// Consistent are required.
type OpClass struct {
	// Compare orders keys (support function 1): used only if the operator
	// class declares it. nil: bytes.Compare.
	Compare func(a, b []byte) int

	// ExtractValue returns the keys an indexed value is indexed under
	// (duplicates are fine).
	ExtractValue func(value []byte) ([][]byte, error)

	// ExtractQuery returns what a query (the right operand of the operator
	// of the given strategy number) looks for.
	ExtractQuery func(query []byte, strategy int) (Query, error)

	// Consistent reports whether an item matches the query, given which of
	// the query's keys it has (check[i] for Query.Keys[i]). recheck asks
	// PostgreSQL to evaluate the operator on the row to confirm a match.
	Consistent func(check []bool, strategy int, query []byte) (match, recheck bool)

	// ComparePartial (support function 5) compares an indexed key with a
	// partial-match query key: 0 if it matches, < 0 if it doesn't but
	// later keys may, > 0 to end the scan. Required if ExtractQuery
	// returns partial keys.
	ComparePartial func(partial, key []byte, strategy int) int

	// TriConsistent (support function 6, optional) is Consistent when some
	// keys may or may not be present (Maybe): GIN uses it to skip items
	// faster. It returns True for a match that needs no recheck, Maybe for
	// a possible match (recheck), False for no match.
	TriConsistent func(check []Ternary, strategy int, query []byte) Ternary
}

var opclasses = map[string]*OpClass{}

// Register registers the operator class whose support functions belong to
// the operator family family (the name of the operator class, unless it
// was created with FAMILY).
func Register(family string, oc OpClass) {
	if oc.ExtractValue == nil || oc.ExtractQuery == nil || oc.Consistent == nil {
		panic(fmt.Sprintf("gin.Register(%q): ExtractValue, ExtractQuery and Consistent are required", family))
	}
	if oc.Compare == nil {
		oc.Compare = bytes.Compare
	}
	opclasses[family] = &oc
}

// ---- Entry points (frameworks/gin/gin.c) -------------------------------

func lookup(c *fmgr.Call) *OpClass {
	d, _ := c.Datum(0)
	name := fmgr.FromDatum[fmgr.CString](d)
	if oc := opclasses[string(name)]; oc != nil {
		return oc
	}
	panic(&pg.Error{Code: "55000", Message: fmt.Sprintf("GIN operator family %q has no gin.OpClass", name),
		Hint: fmt.Sprintf("Call gin.Register(%q, ...) in main.", name)})
}

func arg(c *fmgr.Call, i int) uint64 {
	d, _ := c.Datum(i)
	return d
}

func bytesArg(c *fmgr.Call, i int) []byte { return fmgr.VarlenaView(arg(c, i)) }

func ptrArg[T any](c *fmgr.Call, i int) *T { return (*T)(unsafe.Pointer(uintptr(arg(c, i)))) }

//export gopgext_gin_copy
func ginCopy(src unsafe.Pointer, size int64, out *unsafe.Pointer) uintptr

// palloc copies s into PostgreSQL memory.
func palloc[T any](s []T) unsafe.Pointer {
	var out unsafe.Pointer
	var zero T
	var src unsafe.Pointer
	if len(s) > 0 {
		src = unsafe.Pointer(&s[0])
	}
	core.Check(ginCopy(src, int64(len(s))*int64(unsafe.Sizeof(zero)), &out))
	return out
}

// keyDatums converts keys to a palloc'd array of key Datums.
func keyDatums(keys [][]byte) unsafe.Pointer {
	ds := make([]uint64, len(keys))
	for i, k := range keys {
		if k == nil {
			k = []byte{}
		}
		ds[i] = fmgr.NewVarlena(k)
	}
	return palloc(ds)
}

//export gopgext_go_gin_compare
func compare(c *fmgr.Call) {
	defer c.End(c.Begin(true))
	oc := lookup(c)
	c.SetDatum(fmgr.ToDatum(int64(sign(oc.Compare(bytesArg(c, 1), bytesArg(c, 2))))))
}

func sign(x int) int {
	switch {
	case x < 0:
		return -1
	case x > 0:
		return 1
	}
	return 0
}

//export gopgext_go_gin_extract_value
func extractValue(c *fmgr.Call) {
	defer c.End(c.Begin(true))
	oc := lookup(c)
	keys, err := oc.ExtractValue(bytesArg(c, 1))
	if err != nil {
		c.Fail(err)
		return
	}
	*ptrArg[int64](c, 3) = int64(len(keys))
	if len(keys) > 0 {
		*ptrArg[unsafe.Pointer](c, 2) = keyDatums(keys)
	}
}

//export gopgext_go_gin_extract_query
func extractQuery(c *fmgr.Call) {
	defer c.End(c.Begin(true))
	oc := lookup(c)
	q, err := oc.ExtractQuery(bytesArg(c, 1), int(int64(arg(c, 2))))
	if err != nil {
		c.Fail(err)
		return
	}
	if q.Partial != nil && len(q.Partial) != len(q.Keys) {
		panic(fmt.Errorf("gin: ExtractQuery returned %d keys and %d partial flags", len(q.Keys), len(q.Partial)))
	}
	*ptrArg[int64](c, 4) = int64(len(q.Keys))
	if len(q.Keys) > 0 {
		*ptrArg[unsafe.Pointer](c, 3) = keyDatums(q.Keys)
	}
	for _, p := range q.Partial {
		if p {
			if oc.ComparePartial == nil {
				panic(fmt.Errorf("gin: ExtractQuery returned partial keys, but the OpClass has no ComparePartial"))
			}
			*ptrArg[unsafe.Pointer](c, 5) = palloc(q.Partial)
			break
		}
	}
	*ptrArg[int64](c, 6) = int64(q.Mode)
}

//export gopgext_go_gin_consistent
func consistent(c *fmgr.Call) {
	defer c.End(c.Begin(true))
	oc := lookup(c)
	n := int(int64(arg(c, 2)))
	check := make([]bool, n)
	copy(check, unsafe.Slice(ptrArg[bool](c, 1), n))
	match, recheck := oc.Consistent(check, int(int64(arg(c, 3))), bytesArg(c, 4))
	*ptrArg[bool](c, 5) = match && recheck
	c.SetDatum(fmgr.ToDatum(match))
}

//export gopgext_go_gin_compare_partial
func comparePartial(c *fmgr.Call) {
	defer c.End(c.Begin(true))
	oc := lookup(c)
	if oc.ComparePartial == nil {
		panic(&pg.Error{Code: "0A000", Message: "this GIN operator class has no ComparePartial function"})
	}
	c.SetDatum(fmgr.ToDatum(int64(sign(oc.ComparePartial(bytesArg(c, 1), bytesArg(c, 2), int(int64(arg(c, 3))))))))
}

//export gopgext_go_gin_tri_consistent
func triConsistent(c *fmgr.Call) {
	defer c.End(c.Begin(true))
	oc := lookup(c)
	if oc.TriConsistent == nil {
		panic(&pg.Error{Code: "0A000", Message: "this GIN operator class has no TriConsistent function"})
	}
	n := int(int64(arg(c, 2)))
	check := make([]Ternary, n)
	copy(check, unsafe.Slice(ptrArg[Ternary](c, 1), n))
	r := oc.TriConsistent(check, int(int64(arg(c, 3))), bytesArg(c, 4))
	if r != False && r != True && r != Maybe {
		r = Maybe
	}
	c.SetDatum(uint64(r))
}
