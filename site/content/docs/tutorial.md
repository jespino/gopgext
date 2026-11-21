---
title: "Tutorial"
description: "Build, install and call a \"hello world\" PostgreSQL extension written in Go, then add a function of your own."
weight: 2
---

This tutorial builds a tiny PostgreSQL extension with gopgext, installs it
and calls it from SQL, then adds a function of your own. It takes about ten
minutes. The rest of the [documentation](/docs/) is the full reference.

## 1. What you need

- Linux x86-64
- Go and [TinyGo](https://tinygo.org/getting-started/install/) (tested with
  TinyGo 0.43.0)
- PostgreSQL 14 or later, with its server development files: `pg_config`,
  the headers and PGXS. On Debian/Ubuntu that's the
  `postgresql-server-dev-NN` package, and on Fedora it's `postgresql-server-devel`.
- `make`, a C compiler and binutils (`objcopy`)

Check that the tools can be found:

```sh
go version
tinygo version
pg_config --version
```

## 2. Install gopgext

```sh
git clone https://github.com/jespino/gopgext
cd gopgext
go install .            # puts gopgext in $(go env GOPATH)/bin
```

Make sure `$(go env GOPATH)/bin` is in your `PATH`, then run `gopgext` with
no arguments to see its commands.

## 3. Create a project

```sh
gopgext init hello
cd hello
```

This writes three files:

- `go.mod`: the module name (`hello`) is also the extension's name.
- `main.go`: the extension's code.
- `go.work`: lets your editor find gopgext's packages. It contains paths
  for your machine only, so don't commit it.

## 4. Read the code

`main.go` is plain Go:

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

func main() {}
```

Here's how gopgext reads it:

- **`//gopgext:function`** turns the Go function below it into a SQL
  function. The name becomes snake_case (`Hello` → `hello`), Go types map
  to SQL types (`string` → `text`, `int32` → `integer`, ...), and the rest
  of the doc comment becomes the function's SQL `COMMENT`.
- **`immutable parallel=safe`** are the function's SQL options. They're
  optional: leave them out and the function is `VOLATILE`.
- **Returning an `error`** raises a PostgreSQL error. `pg.Errorf` sets its
  SQLSTATE, so SQL code can catch it by name (`invalid_parameter_value`).
- **`main`** runs once per database connection, when PostgreSQL loads the
  extension. Here it does nothing.

## 5. Build and install

```sh
gopgext install
```

This compiles the code with TinyGo and links `hello.so`. It then writes
`hello.control` and `hello--1.0.sql`, and copies all three into
PostgreSQL's directories (`pg_config --pkglibdir` and
`pg_config --sharedir`/extension). The first build takes longer because
gopgext prepares its TinyGo files in `~/.cache/gopgext`.

Some things can go wrong at this step:

- **Permission denied.** PostgreSQL's directories usually belong to root.
  Run `gopgext build` instead, which only writes the files into the project
  directory, and copy them by hand:

  ```sh
  gopgext build
  sudo cp hello.so "$(pg_config --pkglibdir)/"
  sudo cp hello.control hello--1.0.sql "$(pg_config --sharedir)/extension/"
  ```

- **Several PostgreSQL versions installed.** Pick one with
  `-pg-config /usr/lib/postgresql/17/bin/pg_config`, or set `$PG_CONFIG`.

Open `hello--1.0.sql` to see the SQL that gopgext generated from your Go
functions.

## 6. Use it from SQL

```sh
psql
```

```sql
CREATE EXTENSION hello;

SELECT hello('world');
--      hello
-- ---------------
--  Hello, world!

SELECT shout('hey');
--  shout
-- -------
--  HEY!

SELECT shout('');
-- ERROR:  nothing to shout

\df hello
```

`hello(NULL)` returns `NULL` without calling Go, because a parameter that
isn't a pointer can't hold `NULL`, so gopgext makes the function `STRICT`.

## 7. Add your own function

Add this to `main.go`:

```go
// Stars returns a bar of n stars, for drawing quick charts in psql.
//
//gopgext:function immutable parallel=safe
func Stars(n int32) (string, error) {
	if n < 0 {
		return "", pg.Errorf(pg.CodeInvalidParameterValue, "n must not be negative, got %d", n)
	}
	return strings.Repeat("*", int(n)), nil
}
```

Then rebuild and reinstall:

```sh
gopgext install
```

PostgreSQL loads a library once per connection and never unloads it. To
pick up the new code, **open a new connection** and recreate the extension,
which also creates the new function:

```sql
DROP EXTENSION hello;
CREATE EXTENSION hello;

SELECT stars(5);
--  stars
-- -------
--  *****

SELECT relname, stars(relpages / 10) FROM pg_class ORDER BY relpages DESC LIMIT 3;
```

## 8. Where to go next

- [SQL functions](/docs/functions/): all type mappings, function options,
  `NULL` handling, set-returning functions and row results.
- [Data types](/docs/types/): your own SQL types, backed by Go types.
- [The pg package](/docs/pg/): running queries through SPI, logging,
  settings (GUCs) and errors.
- [Frameworks](/docs/frameworks/): triggers, aggregates, index operator
  classes, foreign data wrappers, hooks and more.
- [Limitations](/docs/limitations/): what doesn't work, such as goroutines.

The [examples](/docs/examples/) are complete extensions to learn from,
including ports of PostgreSQL's own contrib modules.
