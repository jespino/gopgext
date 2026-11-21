// Package spgist writes the support functions of SP-GiST operator classes
// in Go: space-partitioned trees (quadtrees, k-d trees, radix trees, ...),
// the index method behind PostgreSQL's point, range, inet and text prefix
// operator classes. The operator class decides how values are partitioned;
// SP-GiST does the rest (pages, concurrency, WAL, vacuum).
//
// An SP-GiST index is a tree of inner tuples, each with an optional prefix
// and a list of nodes (child pointers, each with an optional label), and
// leaf tuples holding a leaf value per row. The extension registers an
// OpClass under the name of its operator class (in main or a package
// initializer):
//
//	spgist.Register("pt_quad_ops", spgist.OpClass{
//		Prefixes:        true,      // inner tuples have a prefix (a centroid)
//		CanReturnData:   true,      // leaves hold the values: index-only scans
//		Choose:          choose,    // which node of an inner tuple a value goes to
//		PickSplit:       pickSplit, // an inner tuple for a page full of leaves
//		InnerConsistent: inner,     // which nodes may hold matches (and their distances)
//		LeafConsistent:  leaf,      // does a leaf match (and its distance)?
//	})
//
// and its sql/ files declare the support functions, which all use the C
// symbols of this package, and the operator class:
//
//	CREATE FUNCTION pt_spg_config(internal, internal) RETURNS void
//		AS 'MODULE_PATHNAME', 'gopgext_spgist_config' LANGUAGE C STRICT;
//	CREATE FUNCTION pt_spg_choose(internal, internal) RETURNS void
//		AS 'MODULE_PATHNAME', 'gopgext_spgist_choose' LANGUAGE C STRICT;
//	CREATE FUNCTION pt_spg_picksplit(internal, internal) RETURNS void
//		AS 'MODULE_PATHNAME', 'gopgext_spgist_picksplit' LANGUAGE C STRICT;
//	CREATE FUNCTION pt_spg_inner_consistent(internal, internal) RETURNS void
//		AS 'MODULE_PATHNAME', 'gopgext_spgist_inner_consistent' LANGUAGE C STRICT;
//	CREATE FUNCTION pt_spg_leaf_consistent(internal, internal) RETURNS boolean
//		AS 'MODULE_PATHNAME', 'gopgext_spgist_leaf_consistent' LANGUAGE C STRICT;
//	-- only with Compress:
//	CREATE FUNCTION pt_spg_compress(pt) RETURNS bytea
//		AS 'MODULE_PATHNAME', 'gopgext_spgist_compress' LANGUAGE C STRICT;
//
//	CREATE OPERATOR CLASS pt_quad_ops DEFAULT FOR TYPE pt USING spgist AS
//		OPERATOR 6 ~= , OPERATOR 8 <@ (pt, rect),
//		OPERATOR 15 <-> (pt, pt) FOR ORDER BY float_ops,
//		FUNCTION 1 pt_spg_config(internal, internal),
//		FUNCTION 2 pt_spg_choose(internal, internal),
//		FUNCTION 3 pt_spg_picksplit(internal, internal),
//		FUNCTION 4 pt_spg_inner_consistent(internal, internal),
//		FUNCTION 5 pt_spg_leaf_consistent(internal, internal);
//
// The functions find their OpClass through the operator family they belong
// to (named like the operator class, unless it was created with FAMILY).
//
// Values: indexed values and queries are variable-length values (types
// stored like bytea, e.g. declared with //gopgext:type over pg.Varlena),
// and so are prefixes, labels, leaf values (the indexed value, or what
// Compress makes of it: then stored as bytea), reconstructed values and
// traversal values. The functions get their bytes, valid during the call
// only (copy them to keep them), and return new ones. A nil prefix or label
// list means none (an empty, non-nil []byte is an empty value).
//
// Searches: Keys are the WHERE conditions (strategy numbers of the
// operator class's operators, with their right-hand values) and OrderBys
// the ORDER BY ordering operators (k-NN). InnerConsistent returns the nodes
// to visit, each with an optional reconstructed value (e.g. the prefix of
// a text built so far) and traversal value (anything the operator class
// wants to carry down, e.g. the region the node covers) and, for OrderBys,
// lower bounds of the distances below it. LeafConsistent decides on a leaf,
// with exact distances (or lower bounds with RecheckDistances). Recheck
// makes PostgreSQL evaluate the operator on the row. NULLs never reach the
// functions: SP-GiST handles them.
package spgist

import (
	"fmt"
	"runtime"
	"unsafe"

	"gopgext/fmgr"
	"gopgext/pg"
)

// Inner is an inner tuple.
type Inner struct {
	// AllTheSame is set for inner tuples whose nodes are all equivalent
	// (SP-GiST made them so when PickSplit put every value in one node):
	// InnerConsistent should then visit all nodes or none, and Choose must
	// return MatchNode (SP-GiST picks the node) or SplitTuple.
	AllTheSame bool
	Prefix     []byte   // nil: no prefix
	NNodes     int      // number of nodes
	Labels     [][]byte // node labels (nil: the operator class has no labels)
}

// Action is what Choose decides.
type Action int

const (
	MatchNode  Action = 1 // descend into node Node
	AddNode    Action = 2 // add a node labeled Label at position Node, then retry
	SplitTuple Action = 3 // split the inner tuple (its prefix doesn't fit), then retry
)

// ChooseIn describes a value being inserted at an inner tuple.
type ChooseIn struct {
	Value []byte // the original indexed value
	Leaf  []byte // the leaf value so far (Value, compressed, minus what levels above consumed)
	Level int    // the level of the inner tuple (from 0)
	Inner Inner
}

// ChooseOut is the decision of Choose.
type ChooseOut struct {
	Action Action

	// MatchNode: the node, how much to add to the level, and the leaf value
	// to carry down (nil: Leaf unchanged).
	// AddNode: where to insert the new node.
	Node     int
	LevelAdd int
	Rest     []byte

	// AddNode: the new node's label (nil without labels).
	Label []byte

	// SplitTuple: a new upper inner tuple (prefix Prefix, NNodes nodes or
	// Labels) whose node ChildNode points to a new lower inner tuple with
	// prefix PostfixPrefix and all the old nodes.
	Prefix        []byte
	NNodes        int
	Labels        [][]byte
	ChildNode     int
	PostfixPrefix []byte
}

// PickSplitIn are the leaf values of a full page, to be placed under a new
// inner tuple.
type PickSplitIn struct {
	Leaves [][]byte
	Level  int
}

// PickSplitOut is the new inner tuple: its prefix, nodes (NNodes, or
// Labels with one label per node), the node of each leaf, and the leaf
// value stored for each under its node (nil: unchanged).
type PickSplitOut struct {
	Prefix []byte
	NNodes int
	Labels [][]byte
	Nodes  []int
	Leaves [][]byte
}

// Key is a search condition (WHERE col op Query) or an ordering
// (ORDER BY col op Query): the operator's strategy number and its
// right-hand value.
type Key struct {
	Strategy int
	Query    []byte
}

// Scan is the search state at a tuple.
type Scan struct {
	Keys          []Key
	OrderBys      []Key
	Reconstructed []byte // from the parent's Visit (nil at the root)
	Traversal     []byte // from the parent's Visit (nil at the root)
	Level         int
	ReturnData    bool // index-only scan: the original value must be returned
}

// Visit is a node InnerConsistent visits.
type Visit struct {
	Node          int
	LevelAdd      int
	Reconstructed []byte    // passed to the child (nil: none)
	Traversal     []byte    // passed to the child (nil: none)
	Distances     []float64 // lower bound of the distances below, one per OrderBys
}

// LeafOut is the result of LeafConsistent.
type LeafOut struct {
	Match            bool
	Recheck          bool      // the operators must be checked on the row
	Distances        []float64 // one per OrderBys (when Match)
	RecheckDistances bool      // Distances are lower bounds
	// Value is the original value, for index-only scans (nil: the leaf
	// value, right without Compress).
	Value []byte
}

// OpClass is an SP-GiST operator class. Choose, PickSplit,
// InnerConsistent and LeafConsistent are required.
type OpClass struct {
	Prefixes      bool // inner tuples can have prefixes
	Labels        bool // nodes have labels
	CanReturnData bool // leaves can give back the original value (index-only scans)
	LongValuesOK  bool // values can be longer than a page (suffixes split over levels)

	// Compress returns the leaf value of an indexed value (stored as
	// bytea). nil: the leaf value is the indexed value.
	Compress func(value []byte) ([]byte, error)

	Choose          func(in ChooseIn) ChooseOut
	PickSplit       func(in PickSplitIn) PickSplitOut
	InnerConsistent func(scan Scan, inner Inner) []Visit
	LeafConsistent  func(scan Scan, leaf []byte) LeafOut
}

var opclasses = map[string]*OpClass{}

// Register registers the operator class whose support functions belong to
// the operator family family (the name of the operator class, unless it
// was created with FAMILY).
func Register(family string, oc OpClass) {
	if oc.Choose == nil || oc.PickSplit == nil || oc.InnerConsistent == nil || oc.LeafConsistent == nil {
		panic(fmt.Sprintf("spgist.Register(%q): Choose, PickSplit, InnerConsistent and LeafConsistent are required", family))
	}
	opclasses[family] = &oc
}

// ---- C structures (frameworks/spgist/spgist.c: keep in sync) -------------

type cKey struct {
	strategy int64
	query    uint64
}

type cScan struct {
	keys          uintptr
	nkeys         int64
	orderbys      uintptr
	norderbys     int64
	reconstructed uint64
	traversal     uintptr
	level         int64
	returnData    int64
}

type cInner struct {
	allTheSame int64
	prefix     uint64
	nnodes     int64
	labels     uintptr
}

type cChoose struct {
	action           int64
	node             int64
	levelAdd         int64
	rest             uint64
	label            uint64
	prefixHasPrefix  int64
	prefixPrefix     uint64
	prefixNNodes     int64
	prefixLabels     *uint64
	childNode        int64
	postfixHasPrefix int64
	postfixPrefix    uint64
}

type cPickSplit struct {
	hasPrefix int64
	prefix    uint64
	nnodes    int64
	labels    *uint64
	nodes     *int64
	leaves    *uint64
}

type cVisits struct {
	n             int64
	nodes         *int64
	levelAdds     *int64
	reconstructed *uint64
	traversal     *uint64
	distances     *float64
}

type cLeaf struct {
	recheck          int64
	recheckDistances int64
	distances        *float64
	value            uint64
}

const (
	flagPrefixes   = 1
	flagLabels     = 2
	flagReturnData = 4
	flagLongValues = 8
	flagCompress   = 16
)

// ---- Conversions -------------------------------------------------------------

func lookup(c *fmgr.Call) *OpClass {
	d, _ := c.Datum(0)
	name := fmgr.FromDatum[fmgr.CString](d)
	if oc := opclasses[string(name)]; oc != nil {
		return oc
	}
	panic(&pg.Error{Code: "55000", Message: fmt.Sprintf("SP-GiST operator family %q has no spgist.OpClass", name),
		Hint: fmt.Sprintf("Call spgist.Register(%q, ...) in main.", name)})
}

func arg(c *fmgr.Call, i int) uint64 {
	d, _ := c.Datum(i)
	return d
}

func ptr[T any](d uint64) *T { return (*T)(unsafe.Pointer(uintptr(d))) }

// view returns the bytes of a varlena Datum (nil for 0).
func view(d uint64) []byte {
	if d == 0 {
		return nil
	}
	return fmgr.VarlenaView(d)
}

// datum returns a new varlena Datum for b (0 for nil).
func datum(b []byte) uint64 {
	if b == nil {
		return 0
	}
	return fmgr.NewVarlena(b)
}

func datums(bs [][]byte) []uint64 {
	if bs == nil {
		return nil
	}
	ds := make([]uint64, len(bs)+1)
	for i, b := range bs {
		if b == nil {
			b = []byte{}
		}
		ds[i] = fmgr.NewVarlena(b)
	}
	return ds
}

func first[T any](xs []T) *T {
	if len(xs) == 0 {
		return nil
	}
	return &xs[0]
}

func readInner(in *cInner) Inner {
	inner := Inner{AllTheSame: in.allTheSame != 0, Prefix: view(in.prefix), NNodes: int(in.nnodes)}
	if in.labels != 0 {
		ds := unsafe.Slice((*uint64)(unsafe.Pointer(in.labels)), in.nnodes)
		inner.Labels = make([][]byte, len(ds))
		for i, d := range ds {
			inner.Labels[i] = view(d)
		}
	}
	return inner
}

func readKeys(p uintptr, n int64) []Key {
	if n == 0 {
		return nil
	}
	cks := unsafe.Slice((*cKey)(unsafe.Pointer(p)), n)
	keys := make([]Key, n)
	for i, k := range cks {
		keys[i] = Key{Strategy: int(k.strategy), Query: view(k.query)}
	}
	return keys
}

func readScan(s *cScan) Scan {
	scan := Scan{
		Keys:          readKeys(s.keys, s.nkeys),
		OrderBys:      readKeys(s.orderbys, s.norderbys),
		Reconstructed: view(s.reconstructed),
		Level:         int(s.level),
		ReturnData:    s.returnData != 0,
	}
	if s.traversal != 0 {
		scan.Traversal = fmgr.VarlenaView(uint64(s.traversal))
	}
	return scan
}

func invalid(format string, args ...any) {
	panic(&pg.Error{Code: "XX000", Message: "spgist.OpClass: " + fmt.Sprintf(format, args...)})
}

// ---- Entry points (frameworks/spgist/spgist.c) -------------------------------

//export gopgext_go_spgist_config
func config(c *fmgr.Call) {
	defer c.End(c.Begin(true))
	oc := lookup(c)
	var flags int64
	for _, f := range []struct {
		set  bool
		flag int64
	}{{oc.Prefixes, flagPrefixes}, {oc.Labels, flagLabels}, {oc.CanReturnData, flagReturnData},
		{oc.LongValuesOK, flagLongValues}, {oc.Compress != nil, flagCompress}} {
		if f.set {
			flags |= f.flag
		}
	}
	c.SetDatum(uint64(flags))
}

//export gopgext_go_spgist_compress
func compress(c *fmgr.Call) {
	defer c.End(c.Begin(true))
	oc := lookup(c)
	if oc.Compress == nil {
		invalid("compress function declared without Compress")
	}
	leaf, err := oc.Compress(view(arg(c, 1)))
	if err != nil {
		c.Fail(err)
		return
	}
	if leaf == nil {
		leaf = []byte{}
	}
	c.SetDatum(fmgr.NewVarlena(leaf))
}

func setPrefix(b []byte) (int64, uint64) {
	if b == nil {
		return 0, 0
	}
	return 1, fmgr.NewVarlena(b)
}

//export gopgext_go_spgist_choose
func choose(c *fmgr.Call) {
	defer c.End(c.Begin(true))
	oc := lookup(c)
	inner := readInner(ptr[cInner](arg(c, 4)))
	out := oc.Choose(ChooseIn{Value: view(arg(c, 1)), Leaf: view(arg(c, 2)), Level: int(int64(arg(c, 3))), Inner: inner})
	res := ptr[cChoose](arg(c, 5))
	res.action = int64(out.Action)
	res.node = int64(out.Node)
	res.levelAdd = int64(out.LevelAdd)
	switch out.Action {
	case MatchNode:
		res.rest = datum(out.Rest)
	case AddNode:
		if oc.Labels {
			if out.Label == nil {
				out.Label = []byte{}
			}
			res.label = fmgr.NewVarlena(out.Label)
		}
	case SplitTuple:
		res.prefixHasPrefix, res.prefixPrefix = setPrefix(out.Prefix)
		res.postfixHasPrefix, res.postfixPrefix = setPrefix(out.PostfixPrefix)
		labels := datums(out.Labels)
		res.prefixNNodes = int64(out.NNodes)
		if out.Labels != nil {
			res.prefixNNodes = int64(len(out.Labels))
		}
		res.prefixLabels = first(labels)
		res.childNode = int64(out.ChildNode)
		runtime.KeepAlive(labels)
	default:
		invalid("Choose returned action %d", out.Action)
	}
}

//export gopgext_go_spgist_picksplit
func picksplit(c *fmgr.Call) {
	defer c.End(c.Begin(true))
	oc := lookup(c)
	n := int(int64(arg(c, 1)))
	ds := unsafe.Slice((*uint64)(unsafe.Pointer(uintptr(arg(c, 2)))), n)
	leaves := make([][]byte, n)
	for i, d := range ds {
		leaves[i] = view(d)
	}
	out := oc.PickSplit(PickSplitIn{Leaves: leaves, Level: int(int64(arg(c, 3)))})
	nnodes := out.NNodes
	if out.Labels != nil {
		nnodes = len(out.Labels)
	}
	if nnodes < 1 || len(out.Nodes) != n || (out.Leaves != nil && len(out.Leaves) != n) {
		invalid("PickSplit must return at least 1 node, and a node for each of the %d leaves", n)
	}
	nodes := make([]int64, n+1)
	newLeaves := make([]uint64, n+1)
	for i, node := range out.Nodes {
		if node < 0 || node >= nnodes {
			invalid("PickSplit put leaf %d in node %d of %d", i, node, nnodes)
		}
		nodes[i] = int64(node)
		newLeaves[i] = ds[i]
		if out.Leaves != nil && out.Leaves[i] != nil {
			newLeaves[i] = fmgr.NewVarlena(out.Leaves[i])
		}
	}
	labels := datums(out.Labels)
	res := ptr[cPickSplit](arg(c, 4))
	res.hasPrefix, res.prefix = setPrefix(out.Prefix)
	res.nnodes = int64(nnodes)
	res.labels = first(labels)
	res.nodes = &nodes[0]
	res.leaves = &newLeaves[0]
	runtime.KeepAlive(labels)
	runtime.KeepAlive(nodes)
	runtime.KeepAlive(newLeaves)
}

//export gopgext_go_spgist_inner_consistent
func innerConsistent(c *fmgr.Call) {
	defer c.End(c.Begin(true))
	oc := lookup(c)
	scan := readScan(ptr[cScan](arg(c, 1)))
	inner := readInner(ptr[cInner](arg(c, 2)))
	visits := oc.InnerConsistent(scan, inner)
	n, nob := len(visits), len(scan.OrderBys)
	nodes := make([]int64, n+1)
	levelAdds := make([]int64, n+1)
	var recon, trav []uint64
	dists := make([]float64, n*nob+1)
	for i, v := range visits {
		if v.Node < 0 || v.Node >= inner.NNodes {
			invalid("InnerConsistent visits node %d of %d", v.Node, inner.NNodes)
		}
		if len(v.Distances) != nob {
			invalid("InnerConsistent returned %d distances for %d ORDER BY operators", len(v.Distances), nob)
		}
		nodes[i], levelAdds[i] = int64(v.Node), int64(v.LevelAdd)
		copy(dists[i*nob:], v.Distances)
		if v.Reconstructed != nil {
			if recon == nil {
				recon = make([]uint64, n+1)
			}
			recon[i] = fmgr.NewVarlena(v.Reconstructed)
		}
		if v.Traversal != nil {
			if trav == nil {
				trav = make([]uint64, n+1)
			}
			trav[i] = fmgr.NewVarlena(v.Traversal)
		}
	}
	res := ptr[cVisits](arg(c, 3))
	res.n = int64(n)
	res.nodes = &nodes[0]
	res.levelAdds = &levelAdds[0]
	res.reconstructed = first(recon)
	res.traversal = first(trav)
	res.distances = &dists[0]
	runtime.KeepAlive(nodes)
	runtime.KeepAlive(levelAdds)
	runtime.KeepAlive(recon)
	runtime.KeepAlive(trav)
	runtime.KeepAlive(dists)
}

//export gopgext_go_spgist_leaf_consistent
func leafConsistent(c *fmgr.Call) {
	defer c.End(c.Begin(true))
	oc := lookup(c)
	scan := readScan(ptr[cScan](arg(c, 1)))
	out := oc.LeafConsistent(scan, view(arg(c, 2)))
	nob := len(scan.OrderBys)
	if out.Match && len(out.Distances) != nob {
		invalid("LeafConsistent returned %d distances for %d ORDER BY operators", len(out.Distances), nob)
	}
	dists := make([]float64, nob+1)
	copy(dists, out.Distances)
	res := ptr[cLeaf](arg(c, 3))
	res.recheck = boolInt(out.Recheck)
	res.recheckDistances = boolInt(out.RecheckDistances)
	res.distances = &dists[0]
	if scan.ReturnData && out.Value != nil {
		res.value = fmgr.NewVarlena(out.Value)
	}
	runtime.KeepAlive(dists)
	c.SetDatum(fmgr.ToDatum(out.Match))
}

func boolInt(b bool) int64 {
	if b {
		return 1
	}
	return 0
}
