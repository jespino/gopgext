// Package tablesample writes TABLESAMPLE methods in Go: which blocks of a
// table a sample scan reads and which tuples of each block it returns
// (SELECT ... FROM t TABLESAMPLE system_rows (100)).
//
// The extension registers a Method under the name of the method (in main
// or a package initializer):
//
//	tablesample.Register("system_rows", tablesample.Method{
//		Params:     []pg.Oid{tablesample.Int8}, // system_rows (n)
//		SampleSize: estimate,  // planner: pages read, rows returned
//		Begin:      begin,     // a scan (or rescan) starts: check the parameters
//		NextBlock:  nextBlock, // optional: by default every block, in order
//		NextTuple:  nextTuple, // the next tuple offset of a block
//	})
//
// and its sql/ files declare the method's handler function, named like it,
// with this package's C symbol:
//
//	CREATE FUNCTION system_rows(internal) RETURNS tsm_handler
//		AS 'MODULE_PATHNAME', 'gopgext_tablesample_handler' LANGUAGE C STRICT;
//
// A Scan carries the method's State across the calls of a scan and its
// rescans (e.g. the block order chosen at the first scan of a query), and
// how many tuples the scan has returned so far. PostgreSQL checks the
// visibility of the tuples: a method only picks positions (offsets past
// the last item of a block, or of unused items, are skipped).
package tablesample

import (
	"fmt"
	"unsafe"

	"gopgext/fmgr"
	"gopgext/internal/core"
	"gopgext/pg"
)

// Parameter types (Method.Params).
const (
	Bool   pg.Oid = 16
	Int8   pg.Oid = 20
	Int2   pg.Oid = 21
	Int4   pg.Oid = 23
	Text   pg.Oid = 25
	Float4 pg.Oid = 700
	Float8 pg.Oid = 701
)

// Method is a TABLESAMPLE method.
type Method struct {
	// Params are the types of the method's parameters (at most 16).
	// Arguments are converted to them; they are never NULL.
	Params []pg.Oid

	// Can the method return the same sample for the same REPEATABLE seed
	// in different queries (REPEATABLE is rejected otherwise), and in
	// successive scans of the same query (else the planner materializes
	// the scan where it could be rescanned)?
	RepeatableAcrossQueries, RepeatableAcrossScans bool

	// SampleSize estimates the pages the scan reads and the rows it
	// returns, for the planner. params holds the parameters whose value is
	// known at planning time (constants), nil for others. Required.
	SampleSize func(rel Relation, params []any) (pages, tuples float64)

	// Begin starts a scan, or a rescan: params are the parameters' values
	// (int64 for integers, float64 for floats, bool, string, else a
	// pg.AnyElement) and seed the random seed (from REPEATABLE, or random).
	// An error (e.g. a pg.Error with CodeInvalidTablesampleArgument) aborts
	// the query. Required.
	Begin func(s *Scan, params []any, seed uint32) error

	// NextBlock returns the next block to read (of nblocks), or false when
	// the scan is done. Optional: without it, every block is read in order.
	NextBlock func(s *Scan, nblocks uint32) (block uint32, ok bool)

	// NextTuple returns the next tuple offset (1 to maxOffset) to return
	// from block, or false to go to the next block. Required.
	NextTuple func(s *Scan, block uint32, maxOffset uint16) (offset uint16, ok bool)
}

// CodeInvalidTablesampleArgument is the SQLSTATE of invalid parameters.
const CodeInvalidTablesampleArgument = "2202H"

// Relation is what the planner knows about the sampled table.
type Relation struct {
	Pages, Tuples  float64 // size estimates (from VACUUM/ANALYZE)
	RandomPageCost float64 // random_page_cost of its tablespace
}

// Scan is a sample scan.
type Scan struct {
	// State is the method's: it lives as long as the scan, across its
	// rescans.
	State any
	// DoneTuples is the number of tuples the scan has returned so far
	// (visible ones).
	DoneTuples int64

	m *Method
}

var (
	methods = map[string]*Method{}
	scans   = map[uint64]*Scan{}
	lastID  uint64
)

// Register registers the method whose handler function is named name.
func Register(name string, m Method) {
	if m.SampleSize == nil || m.Begin == nil || m.NextTuple == nil {
		panic(fmt.Sprintf("tablesample.Register(%q): SampleSize, Begin and NextTuple are required", name))
	}
	if len(m.Params) > maxParams {
		panic(fmt.Sprintf("tablesample.Register(%q): at most %d parameters", name, maxParams))
	}
	methods[name] = &m
}

// ClampRows is the planner's clamp_row_est: at least 1, rounded.
func ClampRows(rows float64) float64 { return clampRowEst(rows) }

// Random is PostgreSQL's random generator for sampling methods
// (utils/sampling.h), so a method samples like the built-in ones.
type Random struct{ state [2]uint64 }

// NewRandom returns a generator seeded with seed.
func NewRandom(seed uint32) *Random {
	r := &Random{}
	randomInit(unsafe.Pointer(&r.state), seed)
	return r
}

// Fract returns a random number in (0, 1) (sampler_random_fract).
func (r *Random) Fract() float64 { return randomFract(unsafe.Pointer(&r.state)) }

// ---- Entry points (frameworks/tablesample/tablesample.c) -------------------

const maxParams = 16

// cmethod mirrors struct tsm_method.
type cmethod struct {
	nparams int64
	flags   int64
	types   [maxParams]uint32
}

// csize mirrors struct tsm_size.
type csize struct {
	pages, tuples, randomPageCost float64
	nparams                       int64
	values                        [maxParams]uint64
	types                         [maxParams]uint32
	known                         [maxParams]bool
}

//export gopgext_tsm_freed
func tsmFreed(buf *uint64, max int64) int64

//export gopgext_tsm_random_init
func randomInit(state unsafe.Pointer, seed uint32)

//export gopgext_tsm_random_fract
func randomFract(state unsafe.Pointer) float64

//export gopgext_tsm_clamp_row_est
func clampRowEst(rows float64) float64

func arg(c *fmgr.Call, i int) uint64 {
	d, _ := c.Datum(i)
	return d
}

func lookup(c *fmgr.Call) *Method {
	name := string(fmgr.FromDatum[fmgr.CString](arg(c, 0)))
	if m := methods[name]; m != nil {
		return m
	}
	panic(&pg.Error{Code: "55000", Message: fmt.Sprintf("TABLESAMPLE method %q has no tablesample.Method", name),
		Hint: fmt.Sprintf("Call tablesample.Register(%q, ...) in main.", name)})
}

// drain drops the scans whose memory PostgreSQL freed.
func drain() {
	var buf [64]uint64
	for {
		n := int(tsmFreed(&buf[0], int64(len(buf))))
		for _, id := range buf[:n] {
			delete(scans, id)
		}
		if n < len(buf) {
			return
		}
	}
}

func scan(c *fmgr.Call) *Scan {
	s := scans[arg(c, 0)]
	if s == nil {
		panic(&pg.Error{Code: "XX000", Message: "sample scan state was already freed"})
	}
	return s
}

// value converts a parameter.
func value(d uint64, typ uint32) any {
	switch pg.Oid(typ) {
	case Bool:
		return fmgr.FromDatum[bool](d)
	case Int2:
		return int64(fmgr.FromDatum[int16](d))
	case Int4:
		return int64(fmgr.FromDatum[int32](d))
	case Int8:
		return fmgr.FromDatum[int64](d)
	case Float4:
		return float64(fmgr.FromDatum[float32](d))
	case Float8:
		return fmgr.FromDatum[float64](d)
	case Text:
		return fmgr.FromDatum[string](d)
	}
	return core.MakeAnyElement(d, typ)
}

//export gopgext_go_tsm_describe
func describe(c *fmgr.Call) {
	defer c.End(c.Begin(true))
	m := lookup(c)
	out := (*cmethod)(unsafe.Pointer(uintptr(arg(c, 1))))
	out.nparams = int64(len(m.Params))
	for i, t := range m.Params {
		out.types[i] = uint32(t)
	}
	if m.RepeatableAcrossQueries {
		out.flags |= 1
	}
	if m.RepeatableAcrossScans {
		out.flags |= 2
	}
	if m.NextBlock != nil {
		out.flags |= 4
	}
}

//export gopgext_go_tsm_samplesize
func sampleSize(c *fmgr.Call) {
	defer c.End(c.Begin(true))
	m := lookup(c)
	s := (*csize)(unsafe.Pointer(uintptr(arg(c, 1))))
	params := make([]any, s.nparams)
	for i := range params {
		if s.known[i] {
			params[i] = value(s.values[i], s.types[i])
		}
	}
	s.pages, s.tuples = m.SampleSize(Relation{Pages: s.pages, Tuples: s.tuples, RandomPageCost: s.randomPageCost}, params)
}

//export gopgext_go_tsm_init
func initScan(c *fmgr.Call) {
	defer c.End(c.Begin(true))
	drain()
	m := lookup(c)
	lastID++
	scans[lastID] = &Scan{m: m}
	c.SetDatum(lastID)
}

//export gopgext_go_tsm_begin
func begin(c *fmgr.Call) {
	defer c.End(c.Begin(true))
	s := scan(c)
	n := int(int64(arg(c, 3)))
	params := make([]any, n)
	if n > 0 {
		ds := unsafe.Slice((*uint64)(unsafe.Pointer(uintptr(arg(c, 1)))), n)
		ts := unsafe.Slice((*uint32)(unsafe.Pointer(uintptr(arg(c, 2)))), n)
		for i := range params {
			params[i] = value(ds[i], ts[i])
		}
	}
	s.DoneTuples = 0
	if err := s.m.Begin(s, params, uint32(arg(c, 4))); err != nil {
		c.Fail(err)
	}
}

//export gopgext_go_tsm_nextblock
func nextBlock(c *fmgr.Call) {
	defer c.End(c.Begin(true))
	s := scan(c)
	s.DoneTuples = int64(arg(c, 2))
	b, ok := s.m.NextBlock(s, uint32(arg(c, 1)))
	if !ok {
		c.SetDatum(^uint64(0)) // -1
		return
	}
	c.SetDatum(uint64(b))
}

//export gopgext_go_tsm_nexttuple
func nextTuple(c *fmgr.Call) {
	defer c.End(c.Begin(true))
	s := scan(c)
	s.DoneTuples = int64(arg(c, 3))
	off, ok := s.m.NextTuple(s, uint32(arg(c, 1)), uint16(arg(c, 2)))
	if !ok {
		off = 0
	}
	c.SetDatum(uint64(off))
}

//export gopgext_go_tsm_end
func end(c *fmgr.Call) {
	defer c.End(c.Begin(true))
	delete(scans, arg(c, 0))
}
