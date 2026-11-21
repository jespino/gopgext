---
title: "gopgext/spgist"
linkTitle: "spgist"
description: "SP-GiST operator classes: space-partitioned indexes."
weight: 5
---

API reference: [gopgext/spgist](/api/spgist/).

SP-GiST operator classes in Go: space-partitioned trees (quadtrees, k-d
trees, radix trees), the index method of PostgreSQL's point, range, inet
and text prefix operator classes. The extension registers a
`spgist.OpClass` under the name of its operator class, and declares the
support functions (all with this package's C symbols) and the operator
class in `sql/`:

```go
func main() {
	spgist.Register("pt_quad_ops", spgist.OpClass{
		Prefixes:        true, // inner tuples have a prefix (a centroid)
		CanReturnData:   true, // leaves hold the values: index-only scans
		Choose:          func(in spgist.ChooseIn) spgist.ChooseOut { ... },           // which node a new value goes to
		PickSplit:       func(in spgist.PickSplitIn) spgist.PickSplitOut { ... },     // an inner tuple for a full page
		InnerConsistent: func(s spgist.Scan, in spgist.Inner) []spgist.Visit { ... }, // nodes to visit, k-NN bounds
		LeafConsistent:  func(s spgist.Scan, leaf []byte) spgist.LeafOut { ... },
	})
}
```

```sql
CREATE FUNCTION pt_spg_config(internal, internal) RETURNS void
	AS 'MODULE_PATHNAME', 'gopgext_spgist_config' LANGUAGE C STRICT;
-- ... choose, picksplit, inner_consistent, leaf_consistent (and compress): see gopgext/spgist
CREATE OPERATOR CLASS pt_quad_ops DEFAULT FOR TYPE pt USING spgist AS
	OPERATOR 8 <@ (pt, rect), OPERATOR 15 <-> (pt, pt) FOR ORDER BY float_ops,
	FUNCTION 1 pt_spg_config (internal, internal), ...;
```

Values, prefixes, node labels and leaf values are variable-size values
(Go sees their bytes; prefixes and labels are stored as bytea). Searches
carry reconstructed and traversal values down the tree (e.g. the region a
node covers), and return lower bounds of distances for k-NN `ORDER BY`.
`examples/quadtree` is a complete example (a point quadtree with
containment, equality, k-NN and index-only scans).
