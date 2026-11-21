---
title: "gopgext/brin"
linkTitle: "brin"
description: "BRIN operator classes: block range summaries."
weight: 6
---

API reference: [gopgext/brin](/api/brin/).

BRIN operator classes in Go: one small summary per block range
(consecutive table pages), so scans skip the ranges that can't match. Good
for large tables whose values follow the physical order of rows
(timestamps, log data). The extension registers a `brin.OpClass` under the
name of its operator class, and declares the support functions (all with
this package's C symbols) and the operator class in `sql/`:

```go
func main() {
	brin.Register("epoch_minmax_ops", brin.OpClass{
		Add:        func(summary, value []byte) ([]byte, bool) { ... }, // summary nil: empty range
		Consistent: func(summary []byte, strategy int, query []byte) bool { ... },
		Union:      func(a, b []byte) []byte { ... },
	})
}
```

```sql
CREATE FUNCTION epoch_brin_opcinfo(internal) RETURNS internal
	AS 'MODULE_PATHNAME', 'gopgext_brin_opcinfo' LANGUAGE C STRICT;
-- ... add_value, consistent (4 arguments), union: see gopgext/brin
CREATE OPERATOR CLASS epoch_minmax_ops DEFAULT FOR TYPE epoch USING brin AS
	OPERATOR 1 < , OPERATOR 2 <= , OPERATOR 3 = , OPERATOR 4 >= , OPERATOR 5 > ,
	FUNCTION 1 epoch_brin_opcinfo(internal), ...;
```

A summary is bytes the operator class defines (stored as `bytea`, one per
range); values and queries are bytes too (a fixed-size value's bytes, e.g.
a type over `int64` is 8 bytes little-endian). NULLs never reach Go: BRIN
tracks them (`IS NULL` works by itself). `Consistent` may report false
positives (rows are rechecked), never false negatives. One set of SQL
support functions can serve several operator classes. `examples/eventlog`
has a min/max and a Bloom filter operator class.
