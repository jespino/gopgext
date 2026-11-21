// Package selectivity writes the selectivity estimators of operators in
// Go: the RESTRICT function (the fraction of a table's rows "column op
// value" matches) and the JOIN function (the fraction of the pairs of rows
// "a op b" matches), which the planner uses to choose plans.
//
// The extension registers estimators under the names of SQL functions
// (in main or a package initializer):
//
//	selectivity.RegisterRestriction("shape_overlaps_sel", func(r *selectivity.Restriction) float64 {
//		if r.Value == nil || r.Column == nil {
//			return 0.01 // not "column op constant": a default
//		}
//		...
//	})
//
// and its sql/ files declare those functions, with this package's C
// symbols, and use them in operators:
//
//	CREATE FUNCTION shape_overlaps_sel(internal, oid, internal, integer) RETURNS float8
//		AS 'MODULE_PATHNAME', 'gopgext_restriction_selectivity' LANGUAGE C STABLE STRICT;
//	CREATE FUNCTION shape_overlaps_joinsel(internal, oid, internal, smallint, internal) RETURNS float8
//		AS 'MODULE_PATHNAME', 'gopgext_join_selectivity' LANGUAGE C STABLE STRICT;
//	CREATE OPERATOR && (..., RESTRICT = shape_overlaps_sel, JOIN = shape_overlaps_joinsel);
//
// Estimators get the column's statistics, from ANALYZE: the fraction of
// NULLs, the number of distinct values, and, for types with the default
// "=" and "<" operators, the most common values with their frequencies and
// a histogram of the others. Values (the constant, statistics) are
// pg.AnyElement values, valid during the call. Results are clamped to
// [0, 1]; a NULL constant never matches (0, without calling the estimator).
package selectivity

import (
	"fmt"
	"unsafe"

	"gopgext/fmgr"
	"gopgext/internal/core"
	"gopgext/pg"
)

// Column is a column (or an expression with statistics, e.g. of an
// expression index) a clause compares.
type Column struct {
	Type pg.Oid
	Rows float64 // estimated number of rows of its table (0: unknown)

	// Statistics (HasStats false: not analyzed yet).
	HasStats bool
	NullFrac float64 // fraction of NULLs
	// Distinct is the number of distinct non-NULL values: > 0 a count, < 0
	// minus a fraction of the rows (-1: all distinct), 0 unknown.
	Distinct float64
	Width    int // average width in bytes

	// Most common values and their frequencies (fractions of all rows), and
	// histogram bounds of the remaining values (sorted, equal-frequency
	// buckets). Empty if the type has no default btree operator class, or
	// the user can't read the column.
	MCV       []pg.AnyElement
	MCVFreqs  []float64
	Histogram []pg.AnyElement
}

// Restriction is "column op value" (or "value op column": ColumnOnLeft).
type Restriction struct {
	Operator     pg.Oid
	Column       *Column        // nil: no column (e.g. an expression without statistics)
	Value        *pg.AnyElement // nil: not a constant (a parameter, another column...)
	ColumnOnLeft bool
}

// Join is "a op b" with a and b of different tables.
type Join struct {
	Operator    pg.Oid
	Left, Right *Column // nil: not a column
	JoinType    int     // JOIN_INNER (0), JOIN_LEFT, ... (nodes.h)
}

var (
	restrictions = map[string]func(*Restriction) float64{}
	joins        = map[string]func(*Join) float64{}
)

// RegisterRestriction registers the estimator of SQL function name, a
// RESTRICT function with symbol gopgext_restriction_selectivity.
func RegisterRestriction(name string, f func(*Restriction) float64) { restrictions[name] = f }

// RegisterJoin registers the estimator of SQL function name, a JOIN
// function with symbol gopgext_join_selectivity.
func RegisterJoin(name string, f func(*Join) float64) { joins[name] = f }

// ---- Entry points (frameworks/selectivity/selectivity.c) -----------------

// cstats is struct sel_stats.
type cstats struct {
	present, typ  int64
	rows          float64
	hasStats      int64
	nullfrac, ndv float64
	width         int64
	nMCV          int64
	mcv           *uint64
	mcvFreq       *float64
	mcvType       int64
	nHist         int64
	hist          *uint64
	histType      int64
}

// cinput is struct sel_input.
type cinput struct {
	name        *byte
	op          int64
	hasConst    int64
	constType   int64
	constValue  uint64
	varOnLeft   int64
	jointype    int64
	left, right cstats
}

func values(p *uint64, n int64, typ int64) []pg.AnyElement {
	if n == 0 {
		return nil
	}
	out := make([]pg.AnyElement, n)
	for i, d := range unsafe.Slice(p, n) {
		out[i] = core.MakeAnyElement(d, uint32(typ))
	}
	return out
}

func (s *cstats) column() *Column {
	if s.present == 0 {
		return nil
	}
	c := &Column{Type: pg.Oid(s.typ), Rows: s.rows, HasStats: s.hasStats != 0, NullFrac: s.nullfrac,
		Distinct: s.ndv, Width: int(s.width)}
	c.MCV = values(s.mcv, s.nMCV, s.mcvType)
	if s.nMCV > 0 {
		c.MCVFreqs = append([]float64(nil), unsafe.Slice(s.mcvFreq, s.nMCV)...)
	}
	c.Histogram = values(s.hist, s.nHist, s.histType)
	return c
}

func input(c *fmgr.Call) *cinput {
	d, _ := c.Datum(0)
	return (*cinput)(unsafe.Pointer(uintptr(d)))
}

func missing(kind, name string) *core.Error {
	return &core.Error{Code: "55000", Message: fmt.Sprintf("no %s selectivity estimator %q", kind, name),
		Hint: fmt.Sprintf("Call selectivity.Register%s(%q, ...) in main.", map[string]string{"restriction": "Restriction", "join": "Join"}[kind], name)}
}

//export gopgext_go_selectivity_restriction
func restriction(c *fmgr.Call) {
	defer c.End(c.Begin(true))
	in := input(c)
	name := core.GoString(in.name)
	f := restrictions[name]
	if f == nil {
		panic(missing("restriction", name))
	}
	r := &Restriction{Operator: pg.Oid(in.op), Column: in.left.column(), ColumnOnLeft: in.varOnLeft != 0}
	if in.hasConst != 0 {
		v := core.MakeAnyElement(in.constValue, uint32(in.constType))
		r.Value = &v
	}
	c.SetDatum(fmgr.ToDatum(f(r)))
}

//export gopgext_go_selectivity_join
func join(c *fmgr.Call) {
	defer c.End(c.Begin(true))
	in := input(c)
	name := core.GoString(in.name)
	f := joins[name]
	if f == nil {
		panic(missing("join", name))
	}
	j := &Join{Operator: pg.Oid(in.op), Left: in.left.column(), Right: in.right.column(), JoinType: int(in.jointype)}
	c.SetDatum(fmgr.ToDatum(f(j)))
}
