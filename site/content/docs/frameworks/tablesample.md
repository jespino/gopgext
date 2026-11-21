---
title: "gopgext/tablesample"
linkTitle: "tablesample"
description: "TABLESAMPLE methods."
weight: 16
---

API reference: [gopgext/tablesample](/api/tablesample/).

`TABLESAMPLE` methods in Go: which blocks of a table a sample scan reads,
and which tuple offsets of each block it returns. The extension registers
a `tablesample.Method` under the method's name, and declares its handler
function (with this package's C symbol) in `sql/`:

```go
func main() {
	tablesample.Register("system_rows", tablesample.Method{
		Params:                []pg.Oid{tablesample.Int8}, // TABLESAMPLE system_rows (100)
		RepeatableAcrossScans: true,
		SampleSize: func(rel tablesample.Relation, params []any) (pages, tuples float64) { ... }, // planner
		Begin:      func(s *tablesample.Scan, params []any, seed uint32) error { ... },        // (re)scan starts
		NextBlock:  func(s *tablesample.Scan, nblocks uint32) (uint32, bool) { ... },          // optional
		NextTuple:  func(s *tablesample.Scan, block uint32, maxOffset uint16) (uint16, bool) { ... },
	})
}
```

```sql
CREATE FUNCTION system_rows(internal) RETURNS tsm_handler
	AS 'MODULE_PATHNAME', 'gopgext_tablesample_handler' LANGUAGE C STRICT;
```

A `Scan` keeps the method's `State` across a scan's rescans and counts the
tuples returned so far (`DoneTuples`, visible ones); PostgreSQL reads the
pages and checks visibility. `SampleSize` gets the table's size estimates,
its tablespace's `random_page_cost` and the parameters known at planning
time; `tablesample.ClampRows` is the planner's `clamp_row_est`, and
`tablesample.NewRandom(seed)` PostgreSQL's sampling random generator (the
same samples as the built-in methods for a seed). A scan's Go state is
dropped when the query ends, also after errors. `contrib/tsm_system_rows`
and `contrib/tsm_system_time` are built on it.
