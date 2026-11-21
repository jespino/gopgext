// Package am writes index access methods in Go: new kinds of indexes, like
// pgvector's IVFFlat and HNSW, created with CREATE ACCESS METHOD. The
// extension registers a Method under the access method's name (in main):
//
//	am.Register("ivfflat", am.Method{
//		Options: []am.Option{{Name: "lists", Default: 100, Min: 1, Max: 32768}},
//		OrderBy: true,  // ORDER BY col <-> query uses the index
//		Build:   build,  // CREATE INDEX: reads the table's rows, writes records
//		Insert:  insert, // a new row
//		Vacuum:  vacuum, // removes the records of deleted rows
//		Scan:    scan,   // the rows matching scan keys, or nearest first
//	})
//
// and its sql/ files create the access method, with one handler function
// per access method (all with this package's C symbol), and its operator
// classes:
//
//	CREATE FUNCTION ivfflat_handler(internal) RETURNS index_am_handler
//		AS 'MODULE_PATHNAME', 'gopgext_am_handler' LANGUAGE C;
//	CREATE ACCESS METHOD ivfflat TYPE INDEX HANDLER ivfflat_handler;
//	CREATE OPERATOR CLASS vector_l2_ops FOR TYPE vector USING ivfflat AS
//		OPERATOR 1 <-> (vector, vector) FOR ORDER BY float_ops;
//
// Storage: an Index is a meta blob (Meta, SetMeta) and chains of records
// (NewChain, Append, Records, Get, Update, Delete): gopgext keeps them in
// the index's pages, WAL-logged (so they survive crashes and replicate).
// Each of those operations is atomic. Modifications only happen in Build,
// Insert and Vacuum, which never run concurrently for one index (they are
// serialized by a lock), while scans run concurrently with them and see
// each operation either entirely or not at all. A record holds at most
// about 8,100 bytes, the meta blob about 8,000.
//
// Go errors (and panics) become PostgreSQL errors, as in SQL functions.
package am

import (
	"encoding/binary"
	"fmt"
	"iter"
	"math"
	"unsafe"

	"gopgext/fmgr"
	"gopgext/internal/core"
	"gopgext/pg"
)

// TID identifies a table row (a heap tuple).
type TID struct {
	Block  uint32
	Offset uint16
}

func (t TID) pack() int64 { return int64(t.Block)<<16 | int64(t.Offset) }

func unpackTID(v int64) TID { return TID{uint32(v >> 16), uint16(v & 0xFFFF)} }

// Bytes returns t as 6 bytes, to store it in a record.
func (t TID) Bytes() []byte {
	var b [6]byte
	binary.LittleEndian.PutUint32(b[:], t.Block)
	binary.LittleEndian.PutUint16(b[4:], t.Offset)
	return b[:]
}

// TIDFromBytes is the inverse of TID.Bytes.
func TIDFromBytes(b []byte) TID {
	return TID{binary.LittleEndian.Uint32(b), binary.LittleEndian.Uint16(b[4:])}
}

// Value is an indexed value or the argument of a scan key.
type Value struct {
	d    uint64
	null bool
}

// IsNull reports whether the value is NULL.
func (v Value) IsNull() bool { return v.null }

// Datum returns the raw Datum.
func (v Value) Datum() uint64 { return v.d }

// View returns the contents of a variable-length value (bytea, or a type
// stored like it), not copied: valid during the call (scan keys: until the
// scan ends).
func (v Value) View() []byte { return fmgr.VarlenaView(v.d) }

// Bytes returns a copy of the contents of a variable-length value.
func (v Value) Bytes() []byte { return append([]byte(nil), v.View()...) }

// Int64 returns a bigint (or a smaller integer type) value.
func (v Value) Int64() int64 { return int64(v.d) }

// Float64 returns a double precision value.
func (v Value) Float64() float64 { return math.Float64frombits(v.d) }

// Option is an integer option of the indexes (CREATE INDEX ... WITH
// (lists = 100)).
type Option struct {
	Name     string
	Desc     string
	Default  int
	Min, Max int
}

// ScanKey is a condition (WHERE col op Arg) or an ordering (ORDER BY col op
// Arg) of a scan: Strategy is the operator's number in the operator class.
type ScanKey struct {
	Column   int // 0-based
	Strategy int
	Arg      Value
}

// Result is a row a scan returns.
type Result struct {
	TID TID
	// Distances are the values of the ORDER BY operators for the row (one
	// per ordering key), in ascending order across results.
	Distances []float64
	// Recheck asks PostgreSQL to check the conditions on the row.
	Recheck bool
	// RecheckOrder means Distances are lower bounds: PostgreSQL computes
	// the exact ones (with the operators) and reorders.
	RecheckOrder bool
	// NullDistance: the distances are NULL (a NULL value, or a NULL
	// query): such rows come last.
	NullDistance bool
}

// Scanner returns the results of a scan, one at a time, until ok is false.
type Scanner interface {
	Next() (r Result, ok bool, err error)
}

type sliceScanner struct{ rs []Result }

func (s *sliceScanner) Next() (Result, bool, error) {
	if len(s.rs) == 0 {
		return Result{}, false, nil
	}
	r := s.rs[0]
	s.rs = s.rs[1:]
	return r, true, nil
}

// Results returns a Scanner over rs.
func Results(rs []Result) Scanner { return &sliceScanner{rs} }

// Rows are the rows of the table being indexed (Build): their TID and the
// values of the index's columns (NULL ones included). Ranging over it
// scans the table; it can be scanned more than once.
type Rows = iter.Seq2[TID, []Value]

// Method is an index access method.
type Method struct {
	Options []Option
	// SupportFunctions is the number of support functions operator
	// classes can declare (FUNCTION n).
	SupportFunctions int
	// OrderBy: scans can return rows ordered by an operator (ORDER BY col
	// <-> value): operator classes declare it FOR ORDER BY.
	OrderBy bool
	// MultiColumn: indexes can have several columns.
	MultiColumn bool

	// Build fills a new, empty index with the rows of the table, and
	// returns the number of index entries.
	Build func(idx *Index, rows Rows) (int, error)
	// Insert indexes a new row.
	Insert func(idx *Index, tid TID, values []Value) error
	// Vacuum removes the entries of the rows for which dead is true, and
	// returns how many it removed and how many remain.
	Vacuum func(idx *Index, dead func(TID) bool) (removed, remaining int, err error)
	// Cost returns the fraction of the index a scan reads (for the
	// planner); nil: all of it.
	Cost func(idx *Index) float64
	// Scan starts a scan: keys are conditions, orderBys orderings.
	Scan func(idx *Index, keys, orderBys []ScanKey) (Scanner, error)
}

var methods = map[string]*Method{}

//export gopgext_am_register
func amRegister(name *byte, nopts int64, names, descs **byte, defaults, mins, maxs *int64,
	support int64, orderby, multicol bool) uintptr

// Register registers the access method named name (call it in main).
func Register(name string, m Method) {
	if m.Build == nil || m.Insert == nil || m.Vacuum == nil || m.Scan == nil {
		panic(fmt.Sprintf("am.Register(%q): Build, Insert, Vacuum and Scan are required", name))
	}
	n := len(m.Options)
	names := make([]*byte, n+1)
	descs := make([]*byte, n+1)
	defs := make([]int64, n+1)
	mins := make([]int64, n+1)
	maxs := make([]int64, n+1)
	for i, o := range m.Options {
		names[i], descs[i] = core.CBytes(o.Name), core.CBytes(o.Desc)
		defs[i], mins[i], maxs[i] = int64(o.Default), int64(o.Min), int64(o.Max)
	}
	core.Check(amRegister(core.CBytes(name), int64(n), &names[0], &descs[0], &defs[0], &mins[0], &maxs[0],
		int64(m.SupportFunctions), m.OrderBy, m.MultiColumn))
	methods[name] = &m
}

// ---- Index storage ------------------------------------------------------

// Index is an index of the access method.
type Index struct {
	rel  uintptr
	ctx  uintptr
	m    *Method
	name string
}

// RecordID identifies a record. IDs of deleted records may be reused.
type RecordID int64

// ChainID identifies a chain of records.
type ChainID int64

//export gopgext_am_meta
func cMeta(rel uintptr, data *unsafe.Pointer, n *int64) uintptr

//export gopgext_am_set_meta
func cSetMeta(rel uintptr, data *byte, n int64) uintptr

//export gopgext_am_new_chain
func cNewChain(rel uintptr, chain *int64) uintptr

//export gopgext_am_append
func cAppend(rel uintptr, chain int64, data *byte, n int64, rid *int64) uintptr

//export gopgext_am_get
func cGet(rel uintptr, rid int64, data *unsafe.Pointer, n *int64) uintptr

//export gopgext_am_update
func cUpdate(rel uintptr, rid int64, data *byte, n int64) uintptr

//export gopgext_am_delete
func cDelete(rel uintptr, rid int64) uintptr

//export gopgext_am_read_page
func cReadPage(rel uintptr, blk int64, data *unsafe.Pointer, n *int64) uintptr

//export gopgext_am_pfree
func cPfree(p unsafe.Pointer)

//export gopgext_am_option
func cOption(rel uintptr, i, def int64) int64

//export gopgext_am_opfamily
func cOpfamily(rel uintptr, col int64) *byte

//export gopgext_am_natts
func cNatts(rel uintptr) int64

//export gopgext_am_relfilenode
func cRelfilenode(rel uintptr) int64

func copied(p unsafe.Pointer, n int64) []byte {
	b := make([]byte, n)
	copy(b, unsafe.Slice((*byte)(p), n))
	cPfree(p)
	return b
}

func dataPtr(b []byte) *byte {
	if len(b) == 0 {
		return nil
	}
	return &b[0]
}

// Name returns the access method's name.
func (idx *Index) Name() string { return idx.name }

// ID identifies the index's storage: it changes when the index is rebuilt
// (REINDEX, TRUNCATE), so it can key caches of its contents.
func (idx *Index) ID() int64 { return cRelfilenode(idx.rel) }

// Columns returns the number of key columns.
func (idx *Index) Columns() int { return int(cNatts(idx.rel)) }

// OpFamily returns the name of the operator family of column col (0-based):
// its operator class's name, unless created with FAMILY.
func (idx *Index) OpFamily(col int) string { return core.GoString(cOpfamily(idx.rel, int64(col))) }

// Option returns the value of an option of the index.
func (idx *Index) Option(name string) int {
	for i, o := range idx.m.Options {
		if o.Name == name {
			return int(cOption(idx.rel, int64(i), int64(o.Default)))
		}
	}
	panic(fmt.Sprintf("access method %q has no option %q", idx.name, name))
}

// Meta returns the meta blob (empty in a new index).
func (idx *Index) Meta() []byte {
	var p unsafe.Pointer
	var n int64
	core.Check(cMeta(idx.rel, &p, &n))
	return copied(p, n)
}

// SetMeta replaces the meta blob.
func (idx *Index) SetMeta(b []byte) { core.Check(cSetMeta(idx.rel, dataPtr(b), int64(len(b)))) }

// NewChain creates an empty chain of records.
func (idx *Index) NewChain() ChainID {
	var c int64
	core.Check(cNewChain(idx.rel, &c))
	return ChainID(c)
}

// Append adds a record at the end of a chain.
func (idx *Index) Append(chain ChainID, b []byte) RecordID {
	var rid int64
	core.Check(cAppend(idx.rel, int64(chain), dataPtr(b), int64(len(b)), &rid))
	return RecordID(rid)
}

// Get returns a copy of a record.
func (idx *Index) Get(id RecordID) []byte {
	var p unsafe.Pointer
	var n int64
	core.Check(cGet(idx.rel, int64(id), &p, &n))
	return copied(p, n)
}

// Update replaces a record with one of the same size.
func (idx *Index) Update(id RecordID, b []byte) {
	core.Check(cUpdate(idx.rel, int64(id), dataPtr(b), int64(len(b))))
}

// Delete deletes a record.
func (idx *Index) Delete(id RecordID) { core.Check(cDelete(idx.rel, int64(id))) }

// Records returns the records of a chain, in order (copies). Records
// appended while ranging may or may not be seen.
func (idx *Index) Records(chain ChainID) iter.Seq2[RecordID, []byte] {
	return func(yield func(RecordID, []byte) bool) {
		blk := int64(chain)
		for blk >= 0 {
			var p unsafe.Pointer
			var n int64
			core.Check(cReadPage(idx.rel, blk, &p, &n))
			page := copied(p, n)
			blk = int64(binary.LittleEndian.Uint64(page))
			count := int(binary.LittleEndian.Uint64(page[8:]))
			off := 16
			for range count {
				id := RecordID(binary.LittleEndian.Uint64(page[off:]))
				l := int(binary.LittleEndian.Uint64(page[off+8:]))
				rec := page[off+16 : off+16+l : off+16+l]
				off += 16 + (l+7)&^7
				if !yield(id, rec) {
					return
				}
				if pg.CheckInterrupts() != nil {
					return // the cancel is raised when the call returns
				}
			}
		}
	}
}

// ---- Entry points (frameworks/am/am.c) --------------------------------------

func arg(c *fmgr.Call, i int) uint64 {
	d, _ := c.Datum(i)
	return d
}

func lookup(c *fmgr.Call) *Index {
	name := string(fmgr.FromDatum[fmgr.CString](arg(c, 0)))
	m := methods[name]
	if m == nil {
		panic(&pg.Error{Code: "55000", Message: fmt.Sprintf("access method %q has no am.Method", name),
			Hint: fmt.Sprintf("Call am.Register(%q, ...) in main.", name)})
	}
	return &Index{rel: uintptr(arg(c, 1)), m: m, name: name}
}

func values(d, n uint64, count int) []Value {
	ds := unsafe.Slice((*uint64)(unsafe.Pointer(uintptr(d))), count)
	ns := unsafe.Slice((*bool)(unsafe.Pointer(uintptr(n))), count)
	vs := make([]Value, count)
	for i := range vs {
		vs[i] = Value{ds[i], ns[i]}
	}
	return vs
}

//export gopgext_am_build_scan
func cBuildScan(ctx uintptr, heapTuples *int64) uintptr

// buildYield receives the rows of the table scan running for Build.
var buildYield func(TID, []Value) bool

//export gopgext_go_am_build
func build(c *fmgr.Call) {
	defer c.End(c.Begin(false))
	idx := lookup(c)
	idx.ctx = uintptr(arg(c, 2))
	var heapTuples int64
	rows := func(yield func(TID, []Value) bool) {
		if buildYield != nil {
			panic("am: Rows can't be scanned while they are being scanned")
		}
		stopped := false
		buildYield = func(t TID, vs []Value) bool {
			if !stopped {
				stopped = !yield(t, vs)
			}
			return !stopped
		}
		defer func() { buildYield = nil }()
		core.Check(cBuildScan(idx.ctx, &heapTuples))
	}
	n, err := idx.m.Build(idx, rows)
	if err != nil {
		c.Fail(err)
		return
	}
	c.SetDatum(uint64(heapTuples)<<32 | uint64(uint32(n)))
}

//export gopgext_go_am_build_row
func buildRow(c *fmgr.Call) {
	defer c.End(c.Begin(false))
	if buildYield != nil {
		buildYield(unpackTID(int64(arg(c, 0))), values(arg(c, 1), arg(c, 2), int(arg(c, 3))))
	}
}

//export gopgext_go_am_insert
func insert(c *fmgr.Call) {
	defer c.End(c.Begin(false))
	idx := lookup(c)
	if err := idx.m.Insert(idx, unpackTID(int64(arg(c, 2))), values(arg(c, 3), arg(c, 4), int(arg(c, 5)))); err != nil {
		c.Fail(err)
	}
}

//export gopgext_am_is_dead
func cIsDead(ctx uintptr, tid int64) bool

//export gopgext_go_am_vacuum
func vacuum(c *fmgr.Call) {
	defer c.End(c.Begin(false))
	idx := lookup(c)
	ctx := uintptr(arg(c, 2))
	removed, remaining, err := idx.m.Vacuum(idx, func(t TID) bool { return cIsDead(ctx, t.pack()) })
	if err != nil {
		c.Fail(err)
		return
	}
	c.SetDatum(uint64(uint32(removed))<<32 | uint64(uint32(remaining)))
}

//export gopgext_go_am_cost
func cost(c *fmgr.Call) {
	defer c.End(c.Begin(true))
	idx := lookup(c)
	f := 1.0
	if idx.m.Cost != nil {
		f = idx.m.Cost(idx)
	}
	c.SetDatum(fmgr.ToDatum(f))
}

// Scans in progress, by id. Scans a failed transaction didn't end are
// dropped once a new one starts.
type scanState struct {
	s   Scanner
	gen int64
}

var scans = map[int64]*scanState{}

//export gopgext_am_xact_gen
func cXactGen() int64

type cKey struct {
	attno, strategy int64
	arg             uint64
	isnull          int64
}

func scanKeys(p uint64, n int) []ScanKey {
	if n == 0 {
		return nil
	}
	ks := unsafe.Slice((*cKey)(unsafe.Pointer(uintptr(p))), n)
	out := make([]ScanKey, n)
	for i, k := range ks {
		out[i] = ScanKey{Column: int(k.attno) - 1, Strategy: int(k.strategy), Arg: Value{k.arg, k.isnull != 0}}
	}
	return out
}

//export gopgext_go_am_scan_begin
func scanBegin(c *fmgr.Call) {
	defer c.End(c.Begin(true))
	idx := lookup(c)
	gen := cXactGen()
	for id, s := range scans {
		if s.gen != gen {
			delete(scans, id)
		}
	}
	s, err := idx.m.Scan(idx, scanKeys(arg(c, 3), int(arg(c, 4))), scanKeys(arg(c, 5), int(arg(c, 6))))
	if err != nil {
		c.Fail(err)
		return
	}
	scans[int64(arg(c, 2))] = &scanState{s, gen}
}

//export gopgext_go_am_scan_next
func scanNext(c *fmgr.Call) {
	defer c.End(c.Begin(true))
	st := scans[int64(arg(c, 0))]
	if st == nil {
		c.SetDatum(fmgr.ToDatum(false))
		return
	}
	r, ok, err := st.s.Next()
	if err != nil {
		c.Fail(err)
		return
	}
	if ok {
		*(*int64)(unsafe.Pointer(uintptr(arg(c, 1)))) = r.TID.pack()
		dist := unsafe.Slice((*float64)(unsafe.Pointer(uintptr(arg(c, 2)))), int(arg(c, 3)))
		for i := range dist {
			dist[i] = 0
			if i < len(r.Distances) {
				dist[i] = r.Distances[i]
			}
		}
		*(*bool)(unsafe.Pointer(uintptr(arg(c, 4)))) = r.Recheck
		*(*bool)(unsafe.Pointer(uintptr(arg(c, 5)))) = r.RecheckOrder
		*(*bool)(unsafe.Pointer(uintptr(arg(c, 6)))) = r.NullDistance
	}
	c.SetDatum(fmgr.ToDatum(ok))
}

//export gopgext_go_am_scan_end
func scanEnd(c *fmgr.Call) {
	defer c.End(c.Begin(true))
	delete(scans, int64(arg(c, 0)))
}
