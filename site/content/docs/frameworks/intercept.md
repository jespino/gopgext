---
title: "gopgext/intercept"
linkTitle: "intercept"
description: "UPDATE statements handed to Go instead of executed: write-behind buffers, queues, routing writes."
weight: 18
---

API reference: [gopgext/intercept](/api/intercept/).

`intercept` takes over `UPDATE` statements of chosen tables and columns.
An intercepted `UPDATE` doesn't lock or write rows. It runs as a query of
the rows it would update (their primary key and new values), Go receives
them, and the statement reports them as updated (`UPDATE n`, `FOUND`,
`GET DIAGNOSTICS ... ROW_COUNT`). Uses include:

- write-behind buffers that coalesce frequent updates of timestamps or
  counters and write them later;
- queues;
- routing writes elsewhere.

[autobatch](https://github.com/jespino/autobatch) is built on it.

```go
func main() {
	intercept.Register(intercept.Handler{
		// Asked once per table and backend, then cached until the table's
		// relcache entry is invalidated (pg.InvalidateRelation, DDL...).
		Table: func(rel *pg.Relation) *intercept.Policy {
			t := rel.Trigger("track_seen")
			if t == nil {
				return nil
			}
			seen := rel.Column("seen_at").Num
			return &intercept.Policy{
				Groups:  [][]int16{{seen}}, // UPDATEs setting only seen_at
				Watch:   []int16{seen},     // tell Written about other writes of it
				Trigger: "track_seen",      // only while this trigger fires
			}
		},
		Rows: func(b *intercept.Batch) error {
			for i := 0; i < b.Len(); i++ {
				r := b.Row(i)
				remember(r.Rel(), string(r.Key(0)), r.Value(0))
			}
			return nil
		},
		Written: func(w *intercept.Write) error { forgetStale(w); return nil },
	})
}
```

## Which UPDATEs are intercepted

The table's policy has column groups. An `UPDATE` is intercepted when all
of the following hold:

- every column it sets belongs to one group;
- it has no `RETURNING`, `WHERE CURRENT OF`, multiple-column assignment or
  data-modifying CTE;
- its new values don't read columns of the groups (`SET n = n + 1`: the
  table may not have the latest value);
- the table is a plain or partitioned table with a primary key;
- the table has no row-level security and no inheritance children;
- the table has no enabled `UPDATE` trigger other than the policy's
  `Trigger` (intercepted UPDATEs don't fire triggers);
- `Handler.Setting` is on, and `Policy.Trigger` fires in the session
  (`ALTER TABLE ... DISABLE TRIGGER` and `session_replication_role` turn
  it off).

`UPDATE` permission is checked as usual. `FROM`, joins, subqueries and
parameters work: the rows are those the `UPDATE` would update, each counted
once. Prepared statements and cached plans work too. Everything else
runs normally.

With a `Keyed` policy, an `UPDATE` whose `WHERE` is exactly
`pk = constant or parameter` doesn't read the table at all. The row counts
as updated whether or not it exists.

Policies are cached per backend. To change one (e.g. to stop intercepting a
table for a while), change what `Table` returns and call
`pg.InvalidateRelation(table)`: every backend asks again, and replans
its statements on the table.

## Rows

A statement's rows come in batches of up to 1000, all before the statement
ends. `Batch.First` and `Last` mark the first and last batch. Each row has:

- its table (`Rel`: the statement's, or a partition);
- its primary key columns as text (`Key`);
- its new values (`Value`), in the `SET` list's order, each with its
  column's attribute number in the row's table (partitions can number
  them differently).

A value is NULL, its binary image, or text. The binary image is what
PostgreSQL keeps in memory (see [images](/docs/pg/#binary-images-of-values)),
which can be stored, for example in shared memory, and turned back into a
value later. Values are passed as text when their image is longer than
`Handler.MaxImage`.

An error from `Rows` fails the statement, but nothing Go already did is
undone. To keep rows only if the transaction commits, collect them and
apply them in a [gopgext/xact](/docs/frameworks/xact/) callback. Go's own writes of
intercepted tables (eventually writing the rows) run in `intercept.Suspend`,
so they aren't intercepted.

## Watched columns

`Policy.Watch` columns are reported to `Written` when a statement that
isn't intercepted writes them, e.g. so that Go can drop values it holds
that are now stale:

- a plain `UPDATE` reports its rows (their keys), read from a hidden
  `RETURNING` the client doesn't get;
- `MERGE`, `INSERT ... ON CONFLICT DO UPDATE`, `UPDATE ... RETURNING` and
  data-modifying CTEs report the table and columns only.

## Limits

- `Table` runs inside the planner, so it can't run SQL. It can read
  the catalog with `pg.LookupRelation`, which it's given.
- The callbacks run in the executor of every intercepted statement, so
  keep them fast.
- `EXPLAIN` shows the plan of the query that replaces the `UPDATE`.
  `EXPLAIN ANALYZE` runs it, so Go gets its rows.
- Each extension registers at most one handler. Several extensions can
  intercept different tables.
