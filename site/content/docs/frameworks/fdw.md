---
title: "gopgext/fdw"
linkTitle: "fdw"
description: "Foreign data wrappers: tables whose rows come from Go."
weight: 2
---

API reference: [gopgext/fdw](/api/fdw/).

Foreign data wrappers in Go: the extension registers a `fdw.Wrapper`, and
gopgext provides the PostgreSQL side (planning, pushdown, parameterized
paths for joins, EXPLAIN, IMPORT FOREIGN SCHEMA, option validation). The
install script creates the wrapper, named like the extension.

```go
func main() {
	fdw.Register(fdw.Wrapper{
		Options: []fdw.Option{{Name: "url", Where: fdw.OnServer, Required: fdw.OnServer}},
		// Which conditions "column op value" to evaluate remotely (and
		// whether a scan is possible with them).
		Plan: func(t *fdw.Table, conds []fdw.Condition) (fdw.Plan, error) { ... },
		// The rows, given the values of those conditions: constants,
		// stable expressions (now() - interval '1 hour'), parameters, or
		// columns of another table in a nested loop (called per outer row).
		Scan: func(t *fdw.Table, vals []fdw.Value) (fdw.Rows, error) { ... },
	})
}
```

Rows return Go values (`string`, numbers, `bool`, `time.Time`,
`time.Duration`, `[]byte`, JSON-marshalable values), converted to each
column's type. PostgreSQL checks every condition itself too, so pushdown
only has to never drop matching rows. If `Plan` rejects a scan and no other
plan provides what it needs, its error is raised. `examples/numbers_fdw` is
a small offline example.
