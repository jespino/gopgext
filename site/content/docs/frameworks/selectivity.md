---
title: "gopgext/selectivity"
linkTitle: "selectivity"
description: "Selectivity estimators for your operators."
weight: 9
---

API reference: [gopgext/selectivity](/api/selectivity/).

Selectivity estimators in Go: the `RESTRICT` and `JOIN` functions of
operators, which tell the planner what fraction of rows a condition keeps.
Estimators get the operator, the constant (a `pg.AnyElement`) and the
column's statistics from ANALYZE (NULL fraction, distinct values, most
common values with their frequencies, histogram):

```go
func main() {
	selectivity.RegisterRestriction("near_sel", func(r *selectivity.Restriction) float64 {
		if r.Value == nil || r.Column == nil || !r.Column.HasStats {
			return 0.005 // not "column op constant", or no statistics
		}
		... // r.Column.MCV, r.Column.MCVFreqs, r.Column.Histogram, r.Column.NullFrac
	})
	selectivity.RegisterJoin("near_joinsel", func(j *selectivity.Join) float64 { ... })
}
```

```sql
CREATE FUNCTION near_sel(internal, oid, internal, integer) RETURNS float8
	AS 'MODULE_PATHNAME', 'gopgext_restriction_selectivity' LANGUAGE C STABLE STRICT;
CREATE FUNCTION near_joinsel(internal, oid, internal, smallint, internal) RETURNS float8
	AS 'MODULE_PATHNAME', 'gopgext_join_selectivity' LANGUAGE C STABLE STRICT;
CREATE OPERATOR ~~= (LEFTARG = bigint, RIGHTARG = bigint, PROCEDURE = nearly_equal,
	RESTRICT = near_sel, JOIN = near_joinsel);
```

Estimators are found by the name of the SQL function. Results are clamped
to [0, 1]; a NULL constant matches nothing (0, without calling Go).
`examples/stats` estimates "within 10 of" from the most common values and
the histogram.
