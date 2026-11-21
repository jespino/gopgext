---
title: "SQL functions"
description: "Turn Go functions into SQL functions: type mapping, options, NULLs, sets, rows and errors."
weight: 4
---

Any function in package `main` whose doc comment has a `//gopgext:function`
line becomes a SQL function. Names are converted to snake_case
(`ReverseString` → `reverse_string`, parameters too), and the rest of the
doc comment becomes the function's `COMMENT`.

| Go                        | SQL                                        |
|---------------------------|--------------------------------------------|
| `bool`                    | `boolean`                                  |
| `int16`, `int32`          | `smallint`, `integer`                      |
| `int64`, `int`            | `bigint`                                   |
| `float32`, `float64`      | `real`, `double precision`                 |
| `string`                  | `text`                                     |
| `[]byte`                  | `bytea`                                    |
| `*T` (any of the above)   | the same type, `nil` ↔ `NULL`              |
| `[]T` (any of the above)  | `T[]` (multi-dimensional arrays are flattened) |
| `*[]T`                    | `T[]`, `nil` ↔ `NULL`                      |
| `[]*T`                    | `T[]` with NULL elements (`nil`)           |
| `iter.Seq[T]` (results)   | `SETOF T`                                  |
| `iter.Seq[*T]` (results)  | `SETOF T` with NULL rows (`nil`)           |
| `pg.CString`              | `cstring` (input/output functions of types; arguments aren't copied: valid during the call) |
| `pg.Oid`                  | `oid`                                      |
| `time.Time`               | `timestamp with time zone` (microseconds; infinite values raise an error) |
| `pg.Numeric`              | `numeric`, in its text form (`"12.50"`; use `math/big` for arithmetic) |
| `pg.UUID`                 | `uuid` (`[16]byte`; `String()`, `pg.ParseUUID`) |
| `pg.Point`                | `point` (`X`, `Y float64`)                 |
| `pg.Varlena`              | `bytea`, or any type stored like it (arguments aren't copied: valid during the call, read-only) |
| `pg.StringInfo` (parameters) | `internal`: the buffer of a type's receive function |
| `pg.Internal`             | `internal` (a pointer for C code or a framework package) |
| a `//gopgext:type`        | that data type, and its arrays (see below) |
| a `//gopgext:type` struct | that composite type (arguments and results) |
| other structs (results)   | a row: `OUT` parameters (one column per field) |
| `iter.Seq[S]`, S a struct | `RETURNS TABLE (...)` (`SETOF` for a composite type) |
| `pg.Record`, `iter.Seq[pg.Record]` (results) | `record`, `SETOF record`: columns defined by the caller (see below) |
| `pg.AnyElement`, `pg.AnyArray` | `anyelement`, `anyarray` (polymorphic functions) |
| `*agg.State`              | `internal`: an aggregate state that is a Go value (gopgext/agg) |
| `*trigger.Data` (the only parameter) | a trigger function: no SQL parameters, `RETURNS trigger` (gopgext/trigger) |
| `*trigger.Event` (the only parameter) | an event trigger function: `RETURNS event_trigger` (gopgext/trigger) |
| no result                 | `void`                                     |
| last result `error`       | raises the error                           |

NULL handling: a function without pointer parameters is `STRICT`: a NULL
argument returns NULL without calling Go. With pointer parameters, NULL
arrives as `nil`, and NULL for a non-pointer parameter raises an error. A
nil pointer result returns NULL (a nil or empty `[]T` result is an empty
array: use `*[]T` to return NULL).

Options after `//gopgext:function`:

| Option                                  | Meaning                                        |
|-----------------------------------------|------------------------------------------------|
| `name=sql_name`                         | SQL name (default: snake_case of the Go name)  |
| `immutable`, `stable`, `volatile`       | volatility (default `volatile`); queries in `stable`/`immutable` functions run read-only |
| `strict`                                | `STRICT` even with pointer parameters          |
| `parallel=safe\|restricted\|unsafe`     | `PARALLEL ...` (PostgreSQL's default: unsafe)  |
| `cost=N`, `rows=N`                      | planner estimates (`rows` for sets)            |
| `leakproof`, `security_definer`         | the corresponding attributes                   |
| `symbol=c_name`                         | the C symbol PostgreSQL calls (default `gopgext_fn_<GoName>`) |
| `manual`                                | don't generate `CREATE FUNCTION`: it's in the project's `sql/` files |

```go
// Fib returns the first n Fibonacci numbers.
//
//gopgext:function immutable parallel=safe rows=20
func Fib(n int32) iter.Seq[int64] { ... }      // SELECT * FROM fib(10)

//gopgext:function stable
func PriceOf(name string) (*float64, error) { ... }   // NULL-able numeric result, errors
```

## Rows and tables

A struct result is a row, and `iter.Seq` of a struct a table. Each field
(of the types above, at least two) is a column, named in snake_case or by a
`pg:"name"` tag:

```go
type Box struct {
	XMin float64 `pg:"xmin"`
	YMin float64 `pg:"ymin"`
	Tag  *string // NULL-able
}

//gopgext:function immutable
func BBox(s Shape) Box { ... }                   // bbox(s shape, OUT xmin float8, ...) RETURNS record

//gopgext:function immutable
func DumpPoints(s Shape) iter.Seq[Point] { ... } // RETURNS TABLE (path int, x float8, y float8)
```

A `*S` result returns NULL for `nil`.

## Rows whose columns the caller defines

A `pg.Record` result is SQL `record` (`iter.Seq[pg.Record]`: `SETOF
record`): the caller gives the columns, with a column definition list
(`SELECT * FROM f(...) AS t(a int, b text)`), or the function is declared
in `sql/` (`manual`) to return a composite type or with `OUT` parameters.
`pg.ResultColumns()` tells the running function which columns (names,
types, type modifiers) are expected, and a record has one value per
column: `nil` for NULL, or a Go value converted to the column's type,
directly when the types match (`bool`, integers, floats, `string`,
`[]byte`, `pg.Numeric`, `pg.UUID`, `time.Time`, a `pg.AnyElement`) and
otherwise through its text form and the column type's input function
(`"42"` fills an `integer` column, `1.5` a `numeric` one). A record with
the wrong number of values raises PostgreSQL's error for a mismatched
column definition list; called where no columns are known (`SELECT f()`),
`ResultColumns` returns PostgreSQL's "function returning record called in
context that cannot accept type record".

```go
//gopgext:function stable
func Pivot(sql string) (iter.Seq[pg.Record], error) {
	cols, err := pg.ResultColumns() // AS t(row_name text, c1 int, c2 int, ...)
	if err != nil {
		return nil, err
	}
	... // e.g. reject cols[i].Type that doesn't fit, naming cols[i].TypeName
	return func(yield func(pg.Record) bool) {
		yield(pg.Record{"row1", 1, nil})
	}, nil
}
```

`contrib/tablefunc` (crosstab, connectby) is built on it.

## Errors and panics

A returned error is raised as a PostgreSQL error: a `*pg.Error` with its
SQLSTATE `Code`, `Message`, `Detail` and `Hint`; any other error with SQLSTATE
`P0001` (`raise_exception`, like PL/pgSQL's `RAISE`). A panic (index out of
range, nil dereference, `panic(...)`) is raised as `internal_error`
(`XX000`) with message `Go panic: ...`: the backend and the session go on.
`recover` works as usual inside Go code.
