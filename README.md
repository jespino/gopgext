# gopgext — PostgreSQL extensions in Go

> [!WARNING]
> gopgext is **experimental**. Don't use it in production: a bug in the
> generated code or the Go runtime runs inside the server, where it can
> crash it or corrupt data, and the API and generated SQL may change
> without notice.

`gopgext` builds PostgreSQL extensions from plain Go code, using TinyGo. A
project is just Go files: functions marked with `//gopgext:function` become
SQL functions, and gopgext supplies everything else: the C glue, the
`CREATE FUNCTION` script and the control file. Go's standard library works
(`fmt`, `strings`, `regexp`, `encoding/json`, `crypto/*`, maps, generics,
`defer`/`recover`, iterators...), and Go code can run queries through SPI.

**New to gopgext?** [TUTORIAL.md](TUTORIAL.md) walks you through building,
installing and calling a "hello world" extension in a few minutes.

```sh
gopgext init hello       # creates hello/go.mod and hello/main.go
gopgext install hello    # builds hello.so, hello.control, hello--1.0.sql and installs them
psql -c 'CREATE EXTENSION hello' -c "SELECT hello('world')"
#     hello
# ---------------
#  Hello, world!
```

```go
package main

import (
	"strings"

	"gopgext/pg"
)

// Hello returns a greeting for name.
//
//gopgext:function immutable parallel=safe
func Hello(name string) string {
	return "Hello, " + name + "!"
}

// Shout upper-cases s. An empty string raises an error.
//
//gopgext:function immutable parallel=safe
func Shout(s string) (string, error) {
	if s == "" {
		return "", pg.Errorf(pg.CodeInvalidParameterValue, "nothing to shout")
	}
	return strings.ToUpper(s) + "!", nil
}

func main() {} // runs once per backend, when PostgreSQL loads the library
```

It's the same approach as [gokm](https://github.com/jespino/gokm) (Linux kernel modules in Go):
a custom TinyGo target and runtime layer for the host, a C runtime compiled
by the host's own build system (PGXS here, kbuild there), and generated glue.

## Install

Requirements: Go, TinyGo (tested with 0.43.0), PostgreSQL 14 or later with
its server development files (`pg_config`, headers, PGXS; tested with 17 and
19), `make`, a C compiler and binutils (`objcopy`). Linux x86-64.

```sh
go install .          # or: make  (builds ./gopgext)
```

## Commands

```
gopgext init [-module path] [dir]
    Creates go.mod and main.go in dir (default "."). The extension name is the
    last element of the module path. Also writes a go.work so editors resolve
    gopgext's packages (machine-specific: don't commit it).

gopgext build [flags] [dir]
gopgext install [flags] [dir]
    -pg-config path    PostgreSQL to build for (default: $PG_CONFIG, else pg_config in PATH)
    -o dir             where to write the .so, .control and .sql (default: the project dir)
    -name name         extension name (letters, digits, _ and -, as in uuid-ossp)
    -version v         extension version (default 1.0)
    -comment text      control file comment
    -trusted           mark the extension trusted (installable by non-superusers)
    -requires ext,...  extensions this one depends on (control file requires;
                       CREATE EXTENSION ... CASCADE installs them)
    -heap-size size    initial size of each backend's Go heap (default 1M)
    -heap-max size     limit the Go heap can grow to (default 1G of reserved address space)
    -v                 show the commands run and their output
```

`install` builds, then copies the library to `pg_config --pkglibdir` and the
control and SQL files to `pg_config --sharedir`/extension (use `sudo` for a
system PostgreSQL). Everything but the outputs goes into `~/.cache/gopgext`
(or `$GOPGEXTCACHE`). A library is built for one PostgreSQL major version,
like any C extension.

A project can also have `sql/*.sql` files, appended to the install script
(tables, views, aggregates built on the Go functions...), and `csrc/*.c`
files compiled into the library, and `tsearch_data/` files that `install`
copies into `pg_config --sharedir`/tsearch_data (text search dictionaries'
data files, see gopgext/tsearch). `csrc/` files' `PG_FUNCTION_INFO_V1` functions are
exported (so `sql/` files can declare them, e.g. a foreign data wrapper's
handler), and Go can call their other functions through body-less
`//export` declarations. `//gopgext:ldflags -lcurl` in a Go file adds
linker flags (system libraries the C code uses).
`//gopgext:alias name=target` exports the library's V1 function `target`
(e.g. a framework's `gopgext_gist_consistent`) under another C symbol too,
so install scripts written for a C extension (`AS 'MODULE_PATHNAME'`, with
the function's name as symbol), and databases created with them, find it. `../victoria_fdw` is a
complete example: a foreign data wrapper whose planner glue is C and whose
requests and parsing are Go.

## SQL functions

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

### Rows and tables

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

### Rows whose columns the caller defines

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

### Data types

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

### Composite types

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

### Polymorphic functions

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

### Types defined in SQL, and existing extensions' scripts

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

### Errors and panics

A returned error is raised as a PostgreSQL error: a `*pg.Error` with its
SQLSTATE `Code`, `Message`, `Detail` and `Hint`; any other error with SQLSTATE
`P0001` (`raise_exception`, like PL/pgSQL's `RAISE`). A panic (index out of
range, nil dereference, `panic(...)`) is raised as `internal_error`
(`XX000`) with message `Go panic: ...`: the backend and the session go on.
`recover` works as usual inside Go code.

## Package gopgext/pg

| API                                                  | Purpose                                         |
|------------------------------------------------------|-------------------------------------------------|
| `Exec(sql, args...) (int64, error)`                  | run a statement ($1, $2, ... parameters)        |
| `Query(sql, args...) (*Rows, error)`                 | rows: `Next`, `Scan`, `Values`, `Columns`       |
| `QueryRow(sql, args...).Scan(&a, &b)`                | one row, or `ErrNoRows`                         |
| `Error`, `Errorf(code, format, ...)`, `Code...`      | PostgreSQL errors with SQLSTATE codes           |
| `Debug`, `Log`, `Info`, `Notice`, `Warning` (+`f`)   | `ereport` at that level (`Notice` = `RAISE NOTICE`) |
| `Message{Level, Text, Detail, Hint, HideStatement, HideContext}.Report()` | `ereport` at any level from `LevelDebug5` to `LevelWarning`, with a detail and hint, optionally without the statement (`errhidestmt`) or context in the server log |
| `CheckInterrupts() error`                            | has the query been canceled? (call in long loops) |
| `SetConfig(name, value, local)`                      | change a setting, like `SET` or `set_config()`  |
| `CString`, `Oid`, `Varlena`, `StringInfo`, `Internal`, `UUID`, `Record` | SQL types of function signatures (see above)  |
| `ResultColumns() ([]Column, error)`                  | the columns a function returning `record` must return |
| `FormatType(typ, typmod)`, `Rows.ColumnTypmods()`    | a type's SQL name (`character varying(10)`); type modifiers of query results |
| `ParseUUID(s)`, `UUID.String()`                      | the text form of a `uuid` (queries take and return uuids as text) |
| `NewBoolSetting`, `NewIntSetting`, `NewFloatSetting`, `NewStringSetting`, `NewEnumSetting` | custom settings (GUCs) `<extension>.<name>` |
| `ExtensionName()`                                    | the extension's name                            |
| `Preloading()`                                       | is the library being loaded by `shared_preload_libraries`? (in `main`) |
| `UserID()`, `DatabaseID()`, `HasPrivilegesOf(role)`  | current user and database; role membership (`has_privs_of_role`) |
| `ReadFile`, `WriteFile`, `RemoveFile`                | server files (relative to the data directory; `WriteFile` is atomic and durable) |
| `Notify(channel, payload)`                           | `NOTIFY` (`pg_notify()`): delivered when the transaction commits |
| `Collation()`, `DefaultCollation`                    | the input collation of the call (`PG_GET_COLLATION()`, 0 if none) |
| `ToLower(s, coll)`, `ToUpper(s, coll)`, `CompareStrings(a, b, coll)` | `lower()`/`upper()` and text comparison with a collation (`str_tolower`, `varstr_cmp`) |
| `HashBytes(b)`, `HashBytesExtended(b, seed)`         | PostgreSQL's `hash_any`/`hash_any_extended` (hash operator classes) |
| `ArgType(i)`                                         | the actual type of argument i, even when it is NULL (`get_fn_expr_argtype`) |
| `AnyElement.Bytes()`, `FromBytes(typ, b)`            | contents of a variable-length value (text, json...), and a value from bytes |
| `Input(typ, typmod, s)`                              | the input function as for a column (typmod; `nil` checks a domain's constraints on NULL) |
| `IsComposite`, `Columns`, `AnyElement.RowType()`, `AnyElement.Fields()`, `NewRecord` | composite values of any row type: `record` and `anyelement` arguments (columns by name, values, building rows; domains over composites are checked) |
| `AnyArray.LowerBounds()`, `NewAnyArrayDims(elem, xs, dims, lbounds)` | multi-dimensional arrays with their bounds |
| `FormatFloat8(x)`                                    | `x` as `float8` outputs it (shortest exact form, or per `extra_float_digits`): for types that print floats |

Queries go through SPI. Each statement runs in its own subtransaction, like
`plpy.execute` in PL/Python: if it fails, its effects are rolled back and the
error is returned, so Go code can handle it and go on (like an `EXCEPTION`
block). Returning that error unchanged re-raises the original PostgreSQL
error with all its fields (position, context...). Parameters can also be
`pg.AnyElement` values (e.g. a trigger's row values), passed with their own
type. Results are copied into Go
values: `Scan` converts between numeric types, `numeric`, `json`,
timestamps and other types come as their text form, and `**T` destinations
accept NULL.

```go
var lowStock = pg.NewIntSetting("low_stock", 5, 0, 1000000, "Quantity below which items are low on stock.")

//gopgext:function
func Restock(name string, qty int32) (string, error) {
	_, err := pg.Exec("INSERT INTO items (name, qty) VALUES ($1, $2)", name, qty)
	var pe *pg.Error
	if errors.As(err, &pe) && pe.Code == pg.CodeUniqueViolation {
		_, err = pg.Exec("UPDATE items SET qty = qty + $2 WHERE name = $1", name, qty)
		return "restocked " + name, err
	}
	return "new item " + name, err
}
```

Settings must be defined while the library loads (package-level variables or
`main`). Avoid SQL keywords as setting names (`SET ext.verbose` is a syntax
error). Settings are read with `Get()`, which sees the current value of
`SET`, `ALTER SYSTEM`, ... Options choose who can change them and when:
`pg.Superuser` (`PGC_SUSET`), `pg.Sighup` (configuration file, on reload)
and `pg.Postmaster` (server start: e.g. sizes of shared memory). Enum
settings take one of a list of names:
`pg.NewEnumSetting("track", "top", []string{"none", "top", "all"}, "...")`.
Integer and floating-point settings can have a unit (`pg.UnitBytes`,
`UnitKilobytes`, `UnitMilliseconds`, `UnitSeconds`, `UnitMinutes`): values
are then given and shown with one (`SET ext.timeout = '2s'`).

## Package gopgext/http

An HTTP(S) client on libcurl (TinyGo has no `net/http` here), with
`net/http`'s client API: `http.Get`, `http.Post`, `Client{Timeout, Header}`,
`Client.Do(&Request{...})`, `Response{StatusCode, Header, Body}`. Bodies
are streamed (read them with `bufio`, `json.Decoder`, `io.ReadAll`), waiting
for the server notices query cancellation, only `http://` and `https://`
URLs are allowed (redirects included), and a request is closed when the
transaction ends. Importing it links the extension with libcurl (install
it with its headers to build).

```go
resp, err := http.Get("https://api.example.com/rates")
if err != nil {
	return err
}
defer resp.Body.Close()
err = json.NewDecoder(resp.Body).Decode(&rates)
```

## Package gopgext/fdw

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
a small offline example; [victoria_fdw](../victoria_fdw) a real one
(VictoriaMetrics and VictoriaLogs, with gopgext/http).

## Package gopgext/gist

GiST operator classes in Go: the index method of PostGIS, ltree, cube,
pg_trgm and range types. The extension registers a `gist.OpClass` under the
name of its operator class, and declares the support functions (all with
this package's C symbols) and the operator class in `sql/`:

```go
func main() {
	gist.Register("shape_gist_ops", gist.OpClass{
		Compress:   func(value []byte) ([]byte, error) { ... },  // indexed value -> key (a bounding box)
		Consistent: func(key, query []byte, strategy int, leaf bool) (match, recheck bool) { ... },
		Union:      func(keys [][]byte) []byte { ... },
		Penalty:    func(key, added []byte) float64 { ... },
		PickSplit:  func(keys [][]byte) (left, right []int) { ... },
		Distance:   func(key, query []byte, strategy int, leaf bool) (float64, bool) { ... }, // k-NN, optional
	})
}
```

```sql
CREATE FUNCTION shape_gist_consistent(internal, shape, smallint, oid, internal)
	RETURNS boolean AS 'MODULE_PATHNAME', 'gopgext_gist_consistent' LANGUAGE C STRICT;
-- ... union, compress, penalty, picksplit, same, distance: see gopgext/gist
CREATE OPERATOR CLASS shape_gist_ops DEFAULT FOR TYPE shape USING gist AS
	OPERATOR 3 && , OPERATOR 15 <-> FOR ORDER BY float_ops,
	FUNCTION 1 shape_gist_consistent (internal, shape, smallint, oid, internal), ...
	STORAGE bytea;
```

Values, keys and queries are bytes: variable-size values as they are,
fixed-size ones copied (a `[N]byte` type's N bytes; a by-value type's,
little-endian: an `int4` query of `ORDER BY cube ~> 3` is 4 bytes). A
query's type is the operator's right-hand type. Keys are usually stored as
`bytea`, or as the indexed type itself (no `Compress`, no `STORAGE`). For
scripts written for C operator classes there's also a no-op
`gopgext_gist_decompress`. Operator class parameters
(`CREATE INDEX ... (col shape_gist_ops(siglen = 32))`) are declared in
`OpClass.Options`, read with `gist.Option(name)` during calls, and need the
`gopgext_gist_options` support function (10). A key can be lossy (a bounding box for
a polygon): `recheck` makes PostgreSQL check the operator on the row, or
compute the exact distance for `ORDER BY`. `examples/geo` is a complete
example (a small PostGIS: shapes, bounding box operators, exact distance,
k-NN).

## Package gopgext/gin

GIN operator classes in Go: the inverted index of full text search,
pg_trgm, jsonb and arrays. The extension registers a `gin.OpClass` under
the name of its operator class, and declares the support functions (all
with this package's C symbols) and the operator class in `sql/`:

```go
func main() {
	gin.Register("trgm_gin_ops", gin.OpClass{
		ExtractValue:  func(value []byte) ([][]byte, error) { ... },                // value -> keys
		ExtractQuery:  func(query []byte, strategy int) (gin.Query, error) { ... }, // keys to look for, search mode
		Consistent:    func(check []bool, strategy int, query []byte) (match, recheck bool) { ... },
		TriConsistent: func(check []gin.Ternary, strategy int, query []byte) gin.Ternary { ... }, // optional
		// Compare, ComparePartial (prefix-style partial matches): optional
	})
}
```

```sql
CREATE FUNCTION trgm_gin_extract_value(text, internal, internal)
	RETURNS internal AS 'MODULE_PATHNAME', 'gopgext_gin_extract_value' LANGUAGE C STRICT;
-- ... extract_query, consistent, tri_consistent, compare, compare_partial: see gopgext/gin
CREATE OPERATOR CLASS trgm_gin_ops FOR TYPE text USING gin AS
	OPERATOR 1 % (text, text), OPERATOR 3 ~~ (text, text),
	FUNCTION 2 trgm_gin_extract_value (text, internal, internal), ...
	STORAGE bytea;
```

Values, queries and keys are variable-size values (Go sees their bytes);
keys are usually stored as `bytea` and ordered by byte (or by `Compare`).
Lossy keys (trigrams for LIKE) set `recheck`; queries without useful keys
use `gin.All`; partial-match keys (prefixes) use `ComparePartial`.
`examples/trgm` is a complete example (a small pg_trgm).

## Package gopgext/spgist

SP-GiST operator classes in Go: space-partitioned trees (quadtrees, k-d
trees, radix trees), the index method of PostgreSQL's point, range, inet
and text prefix operator classes. The extension registers a
`spgist.OpClass` under the name of its operator class, and declares the
support functions (all with this package's C symbols) and the operator
class in `sql/`:

```go
func main() {
	spgist.Register("pt_quad_ops", spgist.OpClass{
		Prefixes:        true, // inner tuples have a prefix (a centroid)
		CanReturnData:   true, // leaves hold the values: index-only scans
		Choose:          func(in spgist.ChooseIn) spgist.ChooseOut { ... },           // which node a new value goes to
		PickSplit:       func(in spgist.PickSplitIn) spgist.PickSplitOut { ... },     // an inner tuple for a full page
		InnerConsistent: func(s spgist.Scan, in spgist.Inner) []spgist.Visit { ... }, // nodes to visit, k-NN bounds
		LeafConsistent:  func(s spgist.Scan, leaf []byte) spgist.LeafOut { ... },
	})
}
```

```sql
CREATE FUNCTION pt_spg_config(internal, internal) RETURNS void
	AS 'MODULE_PATHNAME', 'gopgext_spgist_config' LANGUAGE C STRICT;
-- ... choose, picksplit, inner_consistent, leaf_consistent (and compress): see gopgext/spgist
CREATE OPERATOR CLASS pt_quad_ops DEFAULT FOR TYPE pt USING spgist AS
	OPERATOR 8 <@ (pt, rect), OPERATOR 15 <-> (pt, pt) FOR ORDER BY float_ops,
	FUNCTION 1 pt_spg_config (internal, internal), ...;
```

Values, prefixes, node labels and leaf values are variable-size values
(Go sees their bytes; prefixes and labels are stored as bytea). Searches
carry reconstructed and traversal values down the tree (e.g. the region a
node covers), and return lower bounds of distances for k-NN `ORDER BY`.
`examples/quadtree` is a complete example (a point quadtree with
containment, equality, k-NN and index-only scans).

## Package gopgext/brin

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

## Package gopgext/am

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

## Package gopgext/agg

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

## Package gopgext/selectivity

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

## Package gopgext/hooks

Observes the statements a backend processes through PostgreSQL's query
processing hooks (post parse analysis, planner, executor, utility): for
statistics, slow query logs, auditing. Decision callbacks choose what to
measure; result callbacks get the measurements:

```go
func main() {
	hooks.EnableQueryID() // compute_query_id = auto computes identifiers
	hooks.Register(hooks.Hooks{
		Parsed:   func(s *hooks.Statement) { ... s.Constants() ... }, // normalize the text
		Execute:  func(s *hooks.Statement) bool { return s.Nesting == 0 },
		Executed: func(s *hooks.Statement, u *hooks.Usage) { ... },  // u.Time, u.Rows, buffers, WAL, JIT
		Utility:  func(s *hooks.Statement) bool { return s.Command != "EXECUTE" },
		UtilityDone: func(s *hooks.Statement, u *hooks.Usage) { ... },
	})
}
```

A `Statement` has the query identifier, `Text()` (the statement, out of a
multi-statement string), its nesting level (0 for the client's statements;
functions, DO blocks, utility statements and planning add levels) and its
command tag. `Parsed` can change the query identifier, and gets the
statement's constants with their lengths (`Constants()`), to replace them
with `$n`. PostgreSQL code (planner, executor) only runs in C: Go is called
before and after. Load the library with `shared_preload_libraries` to see
every backend's statements. `contrib/pg_stat_statements` is built on it.

`Statement.ExecutorNesting` counts only the executions in progress, as
`auto_explain` does: statements of a `DO` block or the query of `CREATE
TABLE AS` are at executor level 0. `Execute` can ask for per-node
instrumentation of the execution, and `Executed` can then get its plan as
EXPLAIN prints it (`contrib/auto_explain` is built on this):

```go
hooks.Register(hooks.Hooks{
	Execute: func(s *hooks.Statement) bool {
		s.Instrument(hooks.InstrumentTimer | hooks.InstrumentBuffers) // or InstrumentRows, InstrumentWAL
		return s.ExecutorNesting == 0
	},
	Executed: func(s *hooks.Statement, u *hooks.Usage) {
		if u.Time > 100 {
			plan := s.Explain(hooks.ExplainOptions{Format: "json", Analyze: true, Buffers: true, QueryText: true})
			(&pg.Message{Level: pg.LevelLog, Text: plan, HideStatement: true}).Report()
		}
	},
})
```

`ExplainOptions` has EXPLAIN's `Format` (`text`, `xml`, `json`, `yaml`),
`Analyze`, `Timing`, `Buffers`, `WAL`, `Triggers` (these need the matching
instrumentation; without any, `Analyze` is ignored, e.g. under `EXPLAIN`
without `ANALYZE`), `Verbose`, `Settings`, `NoCosts`, `QueryText` and
`Parameters` (the values of `$n` parameters: 0 none, -1 in full, n bytes
each; PostgreSQL 16+). Instrumentation is requested before the executor
starts (the decision callback now runs before `ExecutorStart`).

## Package gopgext/auth

Hooks into client authentication (`ClientAuthentication_hook`) and password
changes (`check_password_hook`):

```go
func main() {
	auth.OnAuthenticate(func(c *auth.Connection) error {
		if c.Status != auth.StatusOK {
			time.Sleep(time.Second) // slow down password guessing
		}
		return nil // an error rejects the connection (FATAL)
	})
	auth.OnPasswordCheck(func(p *auth.Password) error {
		if p.Type == auth.PasswordPlain && len(p.Password) < 12 {
			return pg.Errorf(pg.CodeInvalidParameterValue, "password is too short")
		}
		if p.Type != auth.PasswordPlain && p.Matches(p.Role) { // encrypted: only guesses
			return pg.Errorf(pg.CodeInvalidParameterValue, "password must not equal user name")
		}
		return nil // an error rejects the password
	})
}
```

A `Connection` has the user, database, application name, remote host and
port, the pg_hba.conf method and the outcome (`StatusOK`, `StatusError`,
`StatusEOF`: the client hung up when asked for a password). It runs before
the backend connects to its database: no SQL. It needs the library in
`shared_preload_libraries`. A `Password` has the role, the password as
given (plain, or encrypted: `PasswordMD5`, `PasswordSCRAM`), and `ValidUntil`
(nil if none or infinity); `Matches(plain)` compares it with a plain text
guess. Password checks run once the library is loaded in the session
(`LOAD`, `session_preload_libraries`...). `contrib/auth_delay` and
`contrib/passwordcheck` are built on it.

## Package gopgext/shmem

Shared memory and locks for libraries in `shared_preload_libraries`:

```go
type Key struct {
	DB    pg.Oid
	_     [4]byte // keys can't have implicit padding
	Query int64
}
type Counters struct {
	Mutex shmem.SpinLock // for updates under a shared Lock
	Calls int64
	Time  float64
}

var (
	lock  *shmem.Lock
	stats *shmem.Table[Key, Counters]
)

func main() {
	if !pg.Preloading() {
		return // functions using them will say the library must be preloaded
	}
	lock = shmem.NewLock("myext")
	stats = shmem.NewTable[Key, Counters]("myext stats", maxSetting.Get())
	shmem.OnStartup(load)  // postmaster, new memory: e.g. pg.ReadFile
	shmem.OnShutdown(save) // postmaster, clean shutdown: e.g. pg.WriteFile
}
```

| API                                     | Purpose                                                    |
|-----------------------------------------|------------------------------------------------------------|
| `NewLock(name)`: `Lock`, `RLock`, `Unlock` | an LWLock (its name is the wait event); released on errors |
| `NewTable[K, V](name, size)`: `Get`, `Insert`, `Delete`, `All`, `Len` | a shared hash table (dynahash) of at most `size` entries |
| `NewVar[T](name)`: `Ptr`                | a shared value                                             |
| `NewBytes(name, size)`: `Slice`         | a shared byte area                                         |
| `SpinLock`                              | a spinlock to embed in shared values                       |
| `OnStartup`, `OnShutdown`, `Available`  | lifecycle; is the shared memory there?                     |

Shared memory isn't the Go heap: keys, values and variables are checked to
hold only numbers, booleans and arrays/structs of them (no pointers,
strings or slices). Pointers returned point into shared memory, so access
them under a lock. Objects are created while loading in the postmaster
(their space is requested before the server allocates shared memory); if
the library isn't preloaded, using them raises "`<ext>` must be loaded via
shared_preload_libraries".

## Package gopgext/trigger

Trigger and event trigger functions. A `//gopgext:function` whose only
parameter is a `*trigger.Data` is a trigger function (`RETURNS trigger`,
no SQL arguments); its result is the row the operation goes on with:

```go
//gopgext:function
func Stamp(t *trigger.Data) (*trigger.Row, error) {
	if t.When != trigger.Before || t.New == nil || len(t.Args) != 1 {
		return nil, pg.Errorf(trigger.CodeProtocolViolated, "stamp: BEFORE INSERT OR UPDATE FOR EACH ROW, with a column")
	}
	if err := trigger.Set(t.New, t.Args[0], time.Now()); err != nil { // a timestamptz column
		return nil, err
	}
	return t.New, nil // nil: skip the row
}
```

```sql
CREATE TRIGGER stamp BEFORE INSERT OR UPDATE ON items
	FOR EACH ROW EXECUTE FUNCTION stamp('changed_at');
```

| API                                       | Purpose                                                    |
|-------------------------------------------|------------------------------------------------------------|
| `Data.When`, `Op`, `ForEachRow`           | `Before`/`After`/`InsteadOf`, `Insert`/`Update`/`Delete`/`Truncate`, row or statement level |
| `Data.Name`, `Args`                       | the trigger's name and arguments (`EXECUTE FUNCTION f('a', 'b')`) |
| `Data.Table`, `Schema`, `TableOID`        | the table or view                                          |
| `Data.Old`, `Data.New`                    | the rows before and after the change (row-level; nil when absent) |
| `Data.Columns()`, `Column(name)`          | the columns: name, number, type (and a domain's base type), typmod, type name |
| `Data.UpdatedColumns()`, `Changed(name)`  | the columns an UPDATE assigns; whether a value changed      |
| `Row.Any`, `Text`, `IsNull`; `trigger.Get[T]` | a column as a `pg.AnyElement`, its text form, or a Go value of its type |
| `Row.SetText`, `SetAny`, `SetNull`; `trigger.Set` | change a column of the returned row (`SetText` applies typmods and domain checks) |
| `*trigger.Event`: `Event`, `Tag`          | an event trigger's event (`ddl_command_start`, ...) and command tag |

A function without a row result lets the operation go on unchanged (New,
or Old for DELETE); AFTER and statement-level triggers' results are
ignored, and an INSTEAD OF trigger returns nil when it did nothing. Rows
are only valid during the call: the tuples stay in C, Go gets their
values (`pg.AnyElement` values can be passed to `pg.Exec`/`Query` as is).
A function whose only parameter is a `*trigger.Event` is an event trigger
function, returning nothing or an error. `examples/triggers` shows them all;
`contrib/tcn`, `lo`, `autoinc`, `insert_username`, `moddatetime` and
`refint` are ports of PostgreSQL's trigger modules.

## Package gopgext/tsearch

Full text search dictionary templates and parsers in Go, and text search
services for Go code. A dictionary template is registered under the name of
its init function; the `Dictionary` it returns from the options of
`CREATE TEXT SEARCH DICTIONARY` is the dictionary's state (PostgreSQL caches
it per backend; it's dropped when PostgreSQL drops it, e.g. after `ALTER`):

```go
type intDict struct{ maxLen int }

func (d *intDict) Lexize(word string) ([]tsearch.Lexeme, error) {
	if len(word) > d.maxLen {
		return tsearch.StopWord, nil // drop it; nil: unknown word, for the next dictionary
	}
	return []tsearch.Lexeme{{Text: word}}, nil
}

func main() {
	tsearch.RegisterDictionary("dintdict_init", func(opts []tsearch.Option) (tsearch.Dictionary, error) {
		d := &intDict{maxLen: 6}
		for _, o := range opts { ... } // o.Name, o.Value, o.Text(), o.Bool() (defGetString/defGetBoolean)
		return d, nil
	})
}
```

```sql
CREATE FUNCTION dintdict_init(internal) RETURNS internal
	AS 'MODULE_PATHNAME', 'gopgext_tsearch_init' LANGUAGE C STRICT;
CREATE FUNCTION dintdict_lexize(internal, internal, internal, internal) RETURNS internal
	AS 'MODULE_PATHNAME', 'gopgext_tsearch_lexize' LANGUAGE C STRICT;
CREATE TEXT SEARCH TEMPLATE intdict_template (LEXIZE = dintdict_lexize, INIT = dintdict_init);
```

| API                                      | Purpose                                                    |
|------------------------------------------|------------------------------------------------------------|
| `RegisterDictionary(init, f)`            | a dictionary template (symbols `gopgext_tsearch_init`, `gopgext_tsearch_lexize`) |
| `Lexeme{Text, Variant, Flags}`, `Prefix`, `Filter`, `AddPos`, `StopWord` | `Lexize` results: variants (`nvariant`), `TSL_*` flags; nil is an unknown word, an empty slice a stop word |
| `RegisterParser(start, lextype, p)`      | a parser: `TokenTypes()` and `Parse(text)` returning a `Tokenizer` (`Next() (type, token)`, 0 at the end); symbols `gopgext_tsearch_start`, `_gettoken`, `_end`, `_lextype` (headlines: `prsd_headline`) |
| `ConfigFile(name, ext)`, `ReadConfigFile(path)` | data files in `$SHAREDIR/tsearch_data` (`get_tsearch_config_filename`, `tsearch_readline`: lines in the server encoding, a per-line error for characters it lacks, the "line N of configuration file" context while reading) |
| `Lexize(dict, word)`, `FindDictionary(name)` | call any dictionary (like `ts_lexize`); the dictionary `name` in the schema of the SQL function being called |
| `Lower(s)`, `CharLen(s)`                 | lower-casing as text search does (`str_tolower`, default collation); character length in the server encoding |

A project's `tsearch_data/` files (rules, synonyms, stop words) are
installed into `$SHAREDIR/tsearch_data` by `gopgext install`. Dictionaries
don't get the thesaurus' multi-word state (`DictSubState`). `examples/textsearch`
has a parser and a compound-word dictionary; `contrib/dict_int`,
`contrib/dict_xsyn` and `contrib/unaccent` are ports of the contrib
dictionaries.

## Package gopgext/subscript

Subscripting for data types: `value['key']` and `SET col['key'] = x`, like
hstore and jsonb. The extension registers a `subscript.Type` under the
name of its SQL type, and gives the type this package's handler in `sql/`:

```go
func main() {
	subscript.Register("kv", subscript.Type{
		Subscript: "text", // subscripts are coerced to text
		Result:    "text", // kv['a'] is text
		Fetch: func(container []byte, subs [][]byte) ([]byte, bool, error) { ... }, // false: NULL
		Assign: func(container []byte, subs [][]byte, elem []byte) ([]byte, error) { ... }, // nil: NULL
	})
}
```

```sql
CREATE FUNCTION kv_subscript_handler(internal) RETURNS internal
	AS 'MODULE_PATHNAME', 'gopgext_subscript_handler' LANGUAGE C STRICT IMMUTABLE;
ALTER TYPE kv SET (SUBSCRIPT = kv_subscript_handler);
```

Containers, subscripts and elements are variable-size values (Go sees
their bytes). A NULL container or subscript reads as NULL without calling
Go. `Subscripts` sets how many subscripts are allowed (one by default);
slices (`a[1:2]`) aren't supported. `contrib/hstore` uses it.

## Package gopgext/tablesample

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

### Frameworks

`gopgext/http`, `gopgext/fdw`, `gopgext/gist`, `gopgext/gin`,
`gopgext/spgist`, `gopgext/brin`, `gopgext/am`, `gopgext/agg`,
`gopgext/selectivity`, `gopgext/hooks`, `gopgext/auth`, `gopgext/shmem`, `gopgext/trigger`, `gopgext/tsearch`, `gopgext/subscript` and `gopgext/tablesample` are frameworks, like gokm's: importing one compiles its C glue into the
library (and adds its linker flags and install SQL), so extensions using
them are pure Go.

## Examples and contrib ports

`contrib/` holds ports of PostgreSQL's contrib modules, written only with
the public gopgext packages (see `contrib/README.md`); `examples/` shows
features.

| Project              | Shows                                                                        |
|----------------------|------------------------------------------------------------------------------|
| `examples/hello`     | the `gopgext init` template                                                  |
| `examples/types`     | every type mapping, NULLs, arrays, sets, composite types, `uuid`, `record` results with column definition lists, arrays of extension-defined types, polymorphic functions, errors, panics, GC under load |
| `examples/inventory` | SPI queries and error handling, settings, logging, nested calls, cancellation, `sql/` files |
| `examples/textutil`  | the standard library: `regexp`, `encoding/json`, `crypto/sha256`, Unicode    |
| `examples/numbers_fdw` | a foreign data wrapper with gopgext/fdw: pushdown, required conditions, joins, IMPORT FOREIGN SCHEMA, user mappings (offline) |
| `contrib/pg_stat_statements` | a port of `pg_stat_statements` 1.12 with gopgext/hooks and gopgext/shmem: passes upstream's PostgreSQL 18 regression suite unmodified |
| `contrib/auto_explain` | a port of `auto_explain` with gopgext/hooks (instrumentation, EXPLAIN output): passes upstream's tests (its TAP test as SQL) on 14 and 18 and logs the same plans as the C original |
| `contrib/passwordcheck` | a port of `passwordcheck` with gopgext/auth: passes upstream's regression test |
| `contrib/auth_delay` | a port of `auth_delay` with gopgext/auth: delays failed logins |
| `contrib/pgcrypto`   | a port of `pgcrypto` 1.4 with the standard library: digests, HMAC, `crypt`/`gen_salt` (DES, extended DES, MD5, bcrypt, SHA-256/512 crypt), `encrypt`/`decrypt` (AES, Blowfish, DES, 3DES, CAST5), OpenPGP symmetric and public-key encryption, armor; passes upstream's regression tests on 14 and 18 and matches the C original (see its README) |
| `contrib/isn`        | a port of PostgreSQL's `contrib/isn`: 8 data types with upstream's SQL (operators, btree/hash operator classes, casts) over Go input/output functions; passes the upstream regression test and matches the C original on 100,000 inputs |
| `contrib/hstore`     | a port of `hstore` 1.8: upstream's binary format, I/O, operators, records (`populate_record`, `#=`, `hstore(record)`) with the composite APIs of gopgext/pg, JSON, btree/hash/GiST (`siglen`)/GIN operator classes, subscripting with gopgext/subscript; passes upstream's regression tests on 14 and 18 and matches the C original (see its README) |
| `contrib/citext`     | a port of `citext` 1.8 with collation-aware lowercasing and comparison (`pg.Collation`, `pg.ToLower`, `pg.CompareStrings`): passes upstream's regression tests on 14 and 18 |
| `contrib/cube`       | a port of `cube` 1.5: the cube type (upstream's binary layout, text and binary I/O), a hand-written parser matching Bison's errors, btree and GiST operator classes with k-NN (`<->`, `<#>`, `<=>`, `~>` on an `int4` query); passes upstream's regression tests on 14 and 18 and matches the C original |
| `contrib/earthdistance` | a port of `earthdistance` 1.2 (`requires = 'cube'`, `-requires`): the earth domain over cube and `<@>` on the built-in `point` (`pg.Point`); passes upstream's regression test on 14 and 18 |
| `contrib/seg`        | a port of `seg` 1.4: a 12-byte fixed-size type (`//gopgext:type` over `[12]byte`) with significant digits, its parser and GiST operator class; passes upstream's regression tests on 14 and 18 and matches the C original |
| `contrib/fuzzystrmatch` | a port of PostgreSQL's `contrib/fuzzystrmatch`: passes the upstream regression tests and matches the C original on 100,000 strings (see its README) |
| `contrib/dict_int`   | a port of PostgreSQL's `contrib/dict_int` with gopgext/tsearch: passes the upstream regression test on 14 and 18 and matches the C original |
| `contrib/dict_xsyn`  | a port of PostgreSQL's `contrib/dict_xsyn` with gopgext/tsearch (rules files in `tsearch_data/`): passes the upstream regression test on 14 and 18 and matches the C original |
| `contrib/unaccent`   | a port of PostgreSQL's `contrib/unaccent` 1.1 with gopgext/tsearch: the dictionary, `unaccent()` and `unaccent.rules`; passes the upstream regression test on 14 and 18 and matches the C original on 100,000 strings |
| `contrib/tablefunc`  | a port of `tablefunc` 1.0 with `pg.Record` results: `crosstab` (all variants), `connectby`, `normal_rand`; passes upstream's PostgreSQL 18 regression test on 14 and 18, matches the C original |
| `contrib/uuid-ossp`  | a port of `uuid-ossp` 1.1 with `pg.UUID`: versions 1, 3, 4 and 5, nil and namespace UUIDs; passes upstream's regression test, matches the C original |
| `contrib/tsm_system_rows`, `contrib/tsm_system_time` | ports of the `SYSTEM_ROWS` and `SYSTEM_TIME` `TABLESAMPLE` methods with gopgext/tablesample; pass upstream's regression tests on 14 and 18 |
| `examples/vector`    | the core of pgvector with `//gopgext:type`: the `vector` type (same format), `vector(n)` typmod, text and binary I/O, distances and operators, btree operator class, casts, `avg`/`sum`, and an IVFFlat index access method with gopgext/am (k-means lists, probes, L2/inner product/cosine operator classes) |
| `examples/geo`       | a small PostGIS: a `shape` type (WKT points, line strings, polygons), measurements, rows and tables (`OUT` parameters, `RETURNS TABLE`), bounding box operators, exact distance, and a GiST index with gopgext/gist (`&&`, `~`, `@`, k-NN `ORDER BY <->` with recheck) |
| `examples/trgm`      | a small pg_trgm with gopgext/gin: `similarity`, `show_trgm`, `%` with a threshold setting, GIN indexes for `%`/`LIKE`/`ILIKE` (trigrams, recheck, triConsistent) and word prefixes (`^@@`, partial-match keys) |
| `examples/quadtree`  | an SP-GiST quadtree with gopgext/spgist: `pt`/`rect` types, `~=`, `<@`, k-NN `ORDER BY <->` (traversal values carry each node's region), index-only scans |
| `examples/eventlog`  | BRIN operator classes with gopgext/brin: a min/max summary for a timestamp type over `int64` and a Bloom filter for a name type over `string`; index results match sequential scans through inserts, (de)summarization, VACUUM and REINDEX |
| `examples/triggers`  | trigger functions with gopgext/trigger: BEFORE row changes (typmods, domains), skipping rows, returning the old row, AFTER and statement-level auditing, TRUNCATE, INSTEAD OF on a view, changed columns, event triggers |
| `contrib/tcn`, `contrib/lo`, `contrib/autoinc`, `contrib/insert_username`, `contrib/moddatetime`, `contrib/refint` | ports of PostgreSQL's trigger modules with gopgext/trigger: pass upstream's regression tests (lo, autoinc, refint; tcn's isolation spec) on 14 and 18 |
| `examples/textsearch` | a text search parser (words, numbers, #tags) and a compound-word dictionary template with gopgext/tsearch: variants, prefix lexemes, stop words, a `tsearch_data/` file, headlines, calling dictionaries from Go |
| `examples/stats`     | aggregates with Go states (gopgext/agg): `median` (parallel: combine, serialize), `percentile`, `mode_of`; selectivity estimators in Go (gopgext/selectivity) from MCVs, histograms and distinct counts |

Each has a `test.sql`; `make pgtest PG_CONFIG=...` builds and installs them
(and the failure-mode fixtures in `test/fixtures`) and runs them in a
throwaway cluster, then again with the extensions in
`shared_preload_libraries`.

## How it works

```
project *.go + generated wrappers ──tinygo (pgext target, runtime overlay)──▶ go.o
go.o ──objcopy (strip debug, keep only the entry points global)──▶ ext/go.o
ext/{runtime.c, spi.c, functions.c, go.o} ──PGXS──▶ <name>.so (exports: Pg_magic_func, _PG_init, functions)
signatures ──▶ <name>.control, <name>--<version>.sql
```

- **Target and runtime.** A custom TinyGo target (x86-64, PIC) with a
  `pgext` runtime layer (`overlay/src/runtime/runtime_pgext.go`): output goes
  to the server log, time comes from `clock_gettime`, sleeping is
  `pg_usleep`, and `crypto/rand` (and the runtime's random seeds) read
  PostgreSQL's `pg_strong_random` (OpenSSL's `RAND_bytes` or
  `/dev/urandom`). A backend is a single-threaded process, so there is no
  scheduler: Go code runs on the backend's own stack (which keeps
  `check_stack_depth()` and Go → SPI → Go recursion working), and only while
  PostgreSQL is calling into the extension.
- **Memory.** TinyGo's conservative collector, over a heap reserved with
  `mmap` at load (`-heap-max` of address space; only touched pages use
  memory) that grows on demand. Each backend has its own heap; it scans the
  stack between the current position and the outermost entry from C.
  Results are copied into PostgreSQL memory (`palloc`) before returning.
- **Calls.** For every SQL function gopgext generates a V1 C function and a
  Go wrapper. `gopgext_invoke` (runtime.c) enters Go, and once Go has
  returned it raises the error Go reported, if any: a PostgreSQL error never
  `longjmp`s through Go frames. C helpers that may raise one catch it; if it
  can't be handled in Go (say, a corrupt toast pointer), Go unwinds with a
  panic and the original error is re-thrown, and queries are refused
  meanwhile.
- **Symbols.** All Go symbols are local to the library and it exports only
  what PostgreSQL looks up, so several Go extensions can be loaded in one
  backend.
- **Fatal errors.** If the Go runtime can't continue (heap exhausted beyond
  `-heap-max`), the backend exits with `FATAL` and the message goes to the
  server log. The session ends; the server and other sessions are unaffected.

## Limitations

- No goroutines: a backend is single-threaded and Go only runs during calls.
  (`sync.Mutex` and friends work; blocking on a channel deadlocks, which is
  reported as a panic.)
- Function signatures: no arrays of composite types, composite columns of
  composite types, `record` arguments (except as a `pg.AnyElement` in
  `manual` functions, read with `Fields`), `anycompatible`/`anyrange`-style
  polymorphic types or `VARIADIC`. Trigger functions (gopgext/trigger)
  don't see transition tables (`REFERENCING OLD TABLE ...`) and can't change
  system columns; `Set` doesn't apply typmods or domain checks (`SetText` does).
- Index operator classes (gopgext/gist, gin, spgist, brin): gin, spgist
  and brin take variable-size values, keys and queries (built-in fixed-size
  types like `point` aren't readable as bytes; gist reads them), strategies
  get no subtype, and
  operator class parameters are supported for GiST only (integers).
  `typanalyze` functions (custom statistics, like PostGIS's) and
  planner support functions (`SUPPORT`) aren't supported either.
- gopgext/am: writers to an index are serialized (readers aren't blocked),
  records fit in a page, freed pages aren't recycled (REINDEX compacts),
  builds write one WAL record per row; no parallel builds, bitmap scans,
  index-only scans or non-integer options. `examples/vector` has IVFFlat
  but not HNSW.
- gopgext/fdw: read-only scans (no INSERT/UPDATE/DELETE), no
  aggregate/sort/LIMIT pushdown; `IN (...)` lists and `LIKE` aren't offered
  as pushable conditions (they're checked locally).
- gopgext/shmem: fixed-size objects created at server start only (no
  dynamic shared memory segments or DSA areas); gopgext/hooks: no
  `emit_log_hook`; EXPLAIN output only of finished executions, with
  auto_explain's options (no custom EXPLAIN options or `MEMORY`/`SERIALIZE`).
- Standard library packages that draw randomness from Go's internal
  generator instead of `crypto/rand.Reader` (`crypto/ecdh`, `crypto/ecdsa`,
  `crypto/rsa` key generation, `crypto/mlkem`, ...) don't build: it starts
  a timer goroutine. Use `crypto/rand` and `math/big`.
- Long-running Go code must call `pg.CheckInterrupts()` to be cancelable.
- Deep recursion in Go isn't checked (the backend's stack is the limit,
  usually 8 MB).
- Linux x86-64 only; `os` file APIs are not available (TinyGo's baremetal
  `os`): use `pg.ReadFile`/`pg.WriteFile`.
