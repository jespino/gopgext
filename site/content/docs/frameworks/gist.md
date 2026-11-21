---
title: "gopgext/gist"
linkTitle: "gist"
description: "GiST operator classes: indexes for your data types."
weight: 3
---

API reference: [gopgext/gist](/api/gist/).

GiST operator classes in Go: the index method of PostGIS, ltree, cube,
pg_trgm and range types. The extension registers a `gist.OpClass` under the
name of its operator class, and declares the support functions (all with
this package's C symbols) and the operator class in `sql/`:

```go
func main() {
	gist.Register("shape_gist_ops", gist.OpClass{
		Compress:   func(value []byte) ([]byte, error) { ... },  // indexed value -> key (a bounding box)
		Consistent: func(key, query []byte, strategy int, leaf bool) (match, recheck bool) { ... },
		Union:      func(keys [][]byte) []byte { ... },
		Penalty:    func(key, added []byte) float64 { ... },
		PickSplit:  func(keys [][]byte) (left, right []int) { ... },
		Distance:   func(key, query []byte, strategy int, leaf bool) (float64, bool) { ... }, // k-NN, optional
	})
}
```

```sql
CREATE FUNCTION shape_gist_consistent(internal, shape, smallint, oid, internal)
	RETURNS boolean AS 'MODULE_PATHNAME', 'gopgext_gist_consistent' LANGUAGE C STRICT;
-- ... union, compress, penalty, picksplit, same, distance: see gopgext/gist
CREATE OPERATOR CLASS shape_gist_ops DEFAULT FOR TYPE shape USING gist AS
	OPERATOR 3 && , OPERATOR 15 <-> FOR ORDER BY float_ops,
	FUNCTION 1 shape_gist_consistent (internal, shape, smallint, oid, internal), ...
	STORAGE bytea;
```

Values, keys and queries are bytes: variable-size values as they are,
fixed-size ones copied (a `[N]byte` type's N bytes; a by-value type's,
little-endian: an `int4` query of `ORDER BY cube ~> 3` is 4 bytes). A
query's type is the operator's right-hand type. Keys are usually stored as
`bytea`, or as the indexed type itself (no `Compress`, no `STORAGE`). For
scripts written for C operator classes there's also a no-op
`gopgext_gist_decompress`. Operator class parameters
(`CREATE INDEX ... (col shape_gist_ops(siglen = 32))`) are declared in
`OpClass.Options`, read with `gist.Option(name)` during calls, and need the
`gopgext_gist_options` support function (10). A key can be lossy (a bounding box for
a polygon): `recheck` makes PostgreSQL check the operator on the row, or
compute the exact distance for `ORDER BY`. `examples/geo` is a complete
example (a small PostGIS: shapes, bounding box operators, exact distance,
k-NN).
