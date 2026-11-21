---
title: "gopgext/am"
linkTitle: "am"
description: "Index access methods: new kinds of indexes, written in Go."
weight: 7
---

API reference: [gopgext/am](/api/am/).

New index access methods in Go (`CREATE ACCESS METHOD ... TYPE INDEX`), like
pgvector's IVFFlat. The extension registers an `am.Method` under the access
method's name, and creates the access method with its own handler function
(all with this package's C symbol) and its operator classes in `sql/`:

```go
func main() {
	am.Register("ivfflat", am.Method{
		Options: []am.Option{{Name: "lists", Default: 100, Min: 1, Max: 32768}}, // WITH (lists = 100)
		OrderBy: true,   // ORDER BY col <-> query
		Build:   build,  // func(idx *am.Index, rows am.Rows) (int, error): CREATE INDEX
		Insert:  insert, // func(idx *am.Index, tid am.TID, values []am.Value) error
		Vacuum:  vacuum, // func(idx *am.Index, dead func(am.TID) bool) (removed, remaining int, err error)
		Cost:    cost,   // fraction of the index a scan reads (planner)
		Scan:    scan,   // func(idx *am.Index, keys, orderBys []am.ScanKey) (am.Scanner, error)
	})
}
```

```sql
CREATE FUNCTION ivfflat_handler(internal) RETURNS index_am_handler
	AS 'MODULE_PATHNAME', 'gopgext_am_handler' LANGUAGE C;
CREATE ACCESS METHOD ivfflat TYPE INDEX HANDLER ivfflat_handler;
CREATE OPERATOR CLASS vector_l2_ops DEFAULT FOR TYPE vector USING ivfflat AS
	OPERATOR 1 <-> (vector, vector) FOR ORDER BY float_ops;
```

Go never touches pages: an `am.Index` is a meta blob (`Meta`, `SetMeta`)
and chains of records (`NewChain`, `Append`, `Records`, `Get`, `Update`,
`Delete`), stored in the index's pages and WAL-logged (GenericXLog), so
indexes survive crashes and replicate. Build, Insert and Vacuum are
serialized per index; scans run concurrently and see each storage
operation atomically. Scans return heap TIDs with their distances
(`am.Result`), nearest first, and can ask PostgreSQL to recheck
conditions or distances. A record holds up to about 8,100 bytes.
`examples/vector` has a complete IVFFlat (k-means lists,
`vector.ivfflat_probes`, L2/inner product/cosine operator classes).
