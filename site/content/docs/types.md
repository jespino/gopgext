---
title: "Data types"
description: "New SQL data types backed by Go types, composite types, polymorphic functions and SQL-defined types."
weight: 5
---

## Data types

A Go type marked with `//gopgext:type` is a SQL data type. Its underlying
type is its representation: a fixed-size scalar (`bool`, `int16`, `int32`,
`int64`/`int`, `float32`, `float64`, `pg.Oid`: passed by value), `[N]byte`
(a fixed-size type passed by reference, `INTERNALLENGTH = N`, like `point`
or `uuid`: Go gets a copy of its bytes; no arrays, sets or columns of it)
or a variable-size one (`[]byte`, `string`, or `pg.Varlena` to avoid copying
arguments). The type's I/O functions are `//gopgext:function`s named by the
options, and gopgext creates the type (a shell type, its functions, then
`CREATE TYPE`) before the other functions, which can then use the Go type
in their signatures:

```go
// Vector is a vector of float4.
//
//gopgext:type input=VectorIn output=VectorOut typmod_in=VectorTypmodIn receive=VectorRecv send=VectorSend storage=external
type Vector pg.Varlena

//gopgext:function immutable parallel=safe
func VectorIn(s pg.CString, typ pg.Oid, typmod int32) (Vector, error) { ... }   // '[1,2,3]', vector(3)

//gopgext:function immutable parallel=safe
func VectorOut(v Vector) pg.CString { ... }

//gopgext:function immutable parallel=safe
func VectorTypmodIn(mods []pg.CString) (int32, error) { ... }                   // vector(3)

//gopgext:function immutable parallel=safe
func VectorRecv(buf pg.StringInfo, typ pg.Oid, typmod int32) (Vector, error) { ... }  // binary COPY, protocol

//gopgext:function immutable parallel=safe
func VectorSend(v Vector) []byte { ... }

//gopgext:function immutable parallel=safe
func L2Distance(a, b Vector) (float64, error) { ... }                          // l2_distance(vector, vector)
```

| Option                                  | Meaning                                        |
|-----------------------------------------|------------------------------------------------|
| `name=sql_name`                         | SQL name (default: snake_case of the Go name)  |
| `input=`, `output=` (required), `receive=`, `send=`, `typmod_in=`, `typmod_out=`, `analyze=` | the type's functions (Go names) |
| `storage=plain\|external\|extended\|main` | `STORAGE` (variable-size types)                |
| `alignment=char\|int2\|int4\|double`    | `ALIGNMENT` (default: from the representation) |
| `category=X`, `preferred`               | `CATEGORY`, `PREFERRED`                        |

Arrays of these types work like arrays of scalars (`[]Vector` is
`vector[]`). Operators, operator classes, casts and aggregates are SQL in
`sql/*.sql` (appended after the generated objects, so they can use them):
`examples/vector` is a complete example (the core of pgvector).

## Composite types

`//gopgext:type` on a struct creates a composite type (`CREATE TYPE ... AS`)
with a column per field (names in snake_case or from `pg:"name"` tags),
usable as arguments and results, and for tables (`CREATE TABLE t OF
reading`). Columns can be any of the types above but polymorphic ones and
other composites; a NULL column needs a pointer field.

```go
// Reading is a sensor's temperatures.
//
//gopgext:type
type Reading struct {
	Sensor string
	Temps  []Celsius // an array of a //gopgext:type
	Note   *string
}

//gopgext:function immutable
func Calibrate(r Reading, offset float64) Reading { ... } // calibrate(reading, float8) RETURNS reading
```

## Polymorphic functions

`pg.AnyElement` and `pg.AnyArray` are `anyelement` and `anyarray`: a
value of the type the function is called with (`Type()`), which Go can
format (`String()`, the type's output function), create from text
(`pg.ParseAny(typ, s)`), return as is, or collect (`AnyArray.Elements()`, flattened; `Dims()`,
`pg.NewAnyArray(typ, values)`). Results must have the type PostgreSQL
resolves from the arguments.

```go
//gopgext:function immutable
func FirstNonNull(xs pg.AnyArray) *pg.AnyElement { ... } // first_non_null(anyarray) RETURNS anyelement

//gopgext:function immutable
func Elements(xs pg.AnyArray) iter.Seq[pg.AnyElement] { ... } // RETURNS SETOF anyelement
```

## Types defined in SQL, and existing extensions' scripts

Data types, operators, operator classes and casts can also be entirely SQL:
put them in `sql/*.sql`, and implement the functions they need in Go with `manual`
(the `CREATE FUNCTION` is yours, so one Go function can back several SQL
declarations, e.g. the output function of several types) and, to keep an
existing extension's SQL unchanged, `symbol=`:

```go
// sql/types.sql: CREATE FUNCTION isbn13_in(cstring) RETURNS isbn13
//                    AS 'MODULE_PATHNAME', 'isbn_in' LANGUAGE C IMMUTABLE STRICT; ...
//                CREATE TYPE isbn13 (INPUT = isbn13_in, OUTPUT = ean13_out, LIKE = int8);

//gopgext:function symbol=isbn_in manual
func IsbnIn(s pg.CString) (int64, error) { ... }
```

Go parameter and result types must match the SQL declaration's
representation (a type `LIKE int8` is an `int64` in Go); a declaration
with fewer arguments than the Go function raises an error when Go reads
the missing one. An error returned
by an input function called by `pg_input_is_valid()` or
`pg_input_error_info()` is reported softly (PostgreSQL 16+), as input
functions should. `contrib/isn` is a complete example: 8 types with
operators, btree and hash operator classes and casts.
