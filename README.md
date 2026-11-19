# gopgext — PostgreSQL extensions in Go

`gopgext` builds PostgreSQL extensions from plain Go code, using TinyGo. A
project is just Go files: functions marked with `//gopgext:function` become
SQL functions, and gopgext supplies everything else: the C glue, the
`CREATE FUNCTION` script and the control file. Go's standard library works
(`fmt`, `strings`, `regexp`, `encoding/json`, `crypto/*`, maps, generics,
`defer`/`recover`, iterators...), and Go code can run queries through SPI.

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

It's the same approach as [gokm](../gokm) (Linux kernel modules in Go):
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
    -name name         extension name
    -version v         extension version (default 1.0)
    -comment text      control file comment
    -trusted           mark the extension trusted (installable by non-superusers)
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
files compiled into the library. Their `PG_FUNCTION_INFO_V1` functions are
exported (so `sql/` files can declare them, e.g. a foreign data wrapper's
handler), and Go can call their other functions through body-less
`//export` declarations. `//gopgext:ldflags -lcurl` in a Go file adds
linker flags (system libraries the C code uses). `../victoria_fdw` is a
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
| `pg.CString`              | `cstring` (input/output functions of types) |
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

### Data types, operators and index support

Data types, operators, operator classes and casts are SQL: put them in
`sql/*.sql`, and implement the functions they need in Go with `manual`
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
representation (a type `LIKE int8` is an `int64` in Go). An error returned
by an input function called by `pg_input_is_valid()` or
`pg_input_error_info()` is reported softly (PostgreSQL 16+), as input
functions should. `examples/isn` is a complete example: 8 types with
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
| `CheckInterrupts() error`                            | has the query been canceled? (call in long loops) |
| `SetConfig(name, value, local)`                      | change a setting, like `SET` or `set_config()`  |
| `CString`                                            | the SQL `cstring` type                          |
| `NewBoolSetting`, `NewIntSetting`, `NewFloatSetting`, `NewStringSetting` | custom settings (GUCs) `<extension>.<name>` |
| `ExtensionName()`                                    | the extension's name                            |

Queries go through SPI. Each statement runs in its own subtransaction, like
`plpy.execute` in PL/Python: if it fails, its effects are rolled back and the
error is returned, so Go code can handle it and go on (like an `EXCEPTION`
block). Returning that error unchanged re-raises the original PostgreSQL
error with all its fields (position, context...). Results are copied into Go
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
`SET`, `ALTER SYSTEM`, ...

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

### Frameworks

`gopgext/http` and `gopgext/fdw` are frameworks, like gokm's: importing one
compiles its C glue into the library (and adds its linker flags and install
SQL), so extensions using them are pure Go.

## Examples

| Example              | Shows                                                                        |
|----------------------|------------------------------------------------------------------------------|
| `examples/hello`     | the `gopgext init` template                                                  |
| `examples/types`     | every type mapping, NULLs, arrays, sets, errors, panics, GC under load       |
| `examples/inventory` | SPI queries and error handling, settings, logging, nested calls, cancellation, `sql/` files |
| `examples/textutil`  | the standard library: `regexp`, `encoding/json`, `crypto/sha256`, Unicode    |
| `examples/numbers_fdw` | a foreign data wrapper with gopgext/fdw: pushdown, required conditions, joins, IMPORT FOREIGN SCHEMA, user mappings (offline) |
| `examples/isn`       | a port of PostgreSQL's `contrib/isn`: 8 data types with upstream's SQL (operators, btree/hash operator classes, casts) over Go input/output functions; passes the upstream regression test and matches the C original on 100,000 inputs |
| `examples/fuzzystrmatch` | a port of PostgreSQL's `contrib/fuzzystrmatch`: passes the upstream regression tests and matches the C original on 100,000 strings (see its README) |

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
  `pg_usleep`. A backend is a single-threaded process, so there is no
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
- Function signatures: the scalars above, NULL-able pointers, arrays and
  sets of scalars. Data types whose representation is one of those (e.g.
  `LIKE int8`, or varlena types as `[]byte`) work through `sql/` files; no
  composite types or `RETURNS TABLE`, triggers, or index support functions
  taking `internal` arguments (GiST, GIN, ...) yet.
- gopgext/fdw: read-only scans (no INSERT/UPDATE/DELETE), no
  aggregate/sort/LIMIT pushdown; `IN (...)` lists and `LIKE` aren't offered
  as pushable conditions (they're checked locally).
- Long-running Go code must call `pg.CheckInterrupts()` to be cancelable.
- Deep recursion in Go isn't checked (the backend's stack is the limit,
  usually 8 MB).
- Linux x86-64 only; `os` file APIs are not available (TinyGo's baremetal
  `os`).
