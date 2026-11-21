# tablefunc, in Go

A port of PostgreSQL's
[`tablefunc`](https://www.postgresql.org/docs/current/tablefunc.html)
(version 1.0, from PostgreSQL 18): `normal_rand`, `crosstab` (by position:
`crosstab(text)`, `crosstab2/3/4`, the obsolete `crosstab(text, int)`; and
with a categories query: `crosstab(text, text)`) and `connectby` (with or
without a branch column and sibling order). The functions returning `SETOF
record` return `pg.Record`s: `pg.ResultColumns()` gives them the caller's
column definition list (or the composite type of `crosstab2/3/4`, or `OUT`
parameters), checked like the original, and values are converted by the
columns' input functions. It has no C code of its own.

| File           | Upstream (`tablefunc.c`, `tablefunc--1.0.sql`)                         |
|----------------|------------------------------------------------------------------------|
| `main.go`      | `normal_rand`, `get_normal_pair`, `crosstab`, `crosstab_hash`, `load_categories_hash`, `get_crosstab_tuplestore`, `compatCrosstabTupleDescs` |
| `connectby.go` | `connectby_text`, `connectby_text_serial`, `build_tuplestore_recursively`, `validateConnectbyTupleDesc`, `compatConnectbyTupleDescs` |
| `sql/`         | `tablefunc--1.0.sql` (the C symbols of the 5- and 6-argument `connectby` variants differ, see below) |

## Install

```sh
gopgext install -trusted contrib/tablefunc
```

## Tests

`test.sql` (run by `make pgtest`): typed values (floats, `real`, `bytea`
in their text forms), `crosstab3`, mismatched column definition lists,
typmods (`varchar(2)`), `connectby` with order and branch, quoting of the
start value.

Upstream's regression test passes unmodified on PostgreSQL 18, and
PostgreSQL 18's test passes on PostgreSQL 14 too:

```sh
gopgext install -trusted contrib/tablefunc
cd <postgresql 18 source>/contrib/tablefunc
$(pg_config --pkglibdir)/pgxs/src/test/regress/pg_regress --bindir=$(pg_config --bindir) \
    --inputdir=. --temp-instance=/tmp/tf tablefunc
# ok 1 - tablefunc
```

PostgreSQL 14's own expected output differs in 5 error messages that
PostgreSQL 15+ reworded (`provided "categories" SQL must return 1 column of
at least one row`, `invalid return type` for `connectby`, ...): the port
has PostgreSQL 18's messages on every version.

**Differential test**: `differential.sql` runs the port and the C original
(installed as `tablefunc_c.so`) on generated tables and compares results
and errors: `crosstab` (330 queries: 11 value types and typmods, 1 to 6
categories, NULL row names and categories), `crosstab(text, text)` (330,
with an extra column) and `connectby` (280: integer and text keys, cycles,
depth limits, orderings, mismatched types). PostgreSQL 18: 0 mismatches.
On PostgreSQL 14 all results agree; 118 errors differ, being PostgreSQL
18's (the port's) checks and messages: the 14 C library reports "return
and sql tuple descriptions are incompatible" and "invalid return type",
and rejects `connectby` key columns of different types before looking at
the data.

## Differences from the original

- The 5- and 6-argument `connectby` (and the 6- and 7-argument ordered
  ones) use different C symbols (`connectby_text`/`connectby_text_nobranch`,
  `connectby_text_serial`/`connectby_text_serial_nobranch`): the original
  tells them apart with `PG_NARGS()`. The SQL interface is the same, and
  `crosstab`, `crosstab_hash` keep their symbols (user functions declared
  with `AS '$libdir/tablefunc', 'crosstab'` work). A function declared
  with fewer arguments than a symbol's Go function gets an error, not a
  crash.
- Query results are read through gopgext's SPI (each query in a
  subtransaction) and formatted like `SPI_getvalue` in Go (floats with
  the default `extra_float_digits`, `bytea` in hex).
- `normal_rand` draws from Go's `math/rand` instead of PostgreSQL's
  `pg_prng`.
- Called in a `SELECT` list (`SELECT crosstab_hash(...)`), the functions
  report "function returning record called in context that cannot accept
  type record" instead of "materialize mode required".
