---
title: "gopgext/agg"
linkTitle: "agg"
description: "Aggregates whose state is a Go value."
weight: 8
---

API reference: [gopgext/agg](/api/agg/).

Aggregates whose state is a Go value (transition type `internal`): a slice
for a median, a map for a mode, a sketch. The state is an `*agg.State`
(its `Value` is any Go value), and the aggregate's functions are ordinary
SQL functions taking and returning one:

```go
//gopgext:function immutable parallel=safe
func MedianAdd(s *agg.State, x *float64) *agg.State {
	if s == nil {
		s = agg.New(&values{}) // first row of the group
	}
	if x != nil {
		s.Value.(*values).add(*x)
	}
	return s
}

//gopgext:function immutable parallel=safe
func MedianFinal(s *agg.State) *float64 { ... }
```

```sql
CREATE AGGREGATE median(float8) (
	SFUNC = median_add, STYPE = internal, FINALFUNC = median_final,
	-- optional, for parallel aggregation: merge states, and pass them between processes as bytea
	COMBINEFUNC = median_combine, SERIALFUNC = median_serialize, DESERIALFUNC = median_deserialize,
	PARALLEL = SAFE);
```

A state lives in the aggregate's memory context: when PostgreSQL frees it
(after a group, the query, or an error), gopgext/agg drops the Go value.
Functions with an `*agg.State` parameter aren't `STRICT` (the first call
gets `nil`): use pointer parameters to see NULL inputs. `examples/stats`
has `median` (parallel), `percentile` and `mode_of`.
