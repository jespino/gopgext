# tsm_system_rows, in Go

A port of PostgreSQL's
[`tsm_system_rows`](https://www.postgresql.org/docs/current/tsm-system-rows.html)
(version 1.0): the `SYSTEM_ROWS` `TABLESAMPLE` method (`TABLESAMPLE system_rows (n)`), which
reads whole blocks in a random order (linear probing from a random block with a random step) until it has returned the requested number of rows. It's built on `gopgext/tablesample` and has no C code of its own.

| File      | Upstream                                                       |
|-----------|----------------------------------------------------------------|
| `main.go` | `tsm_system_rows.c`: sample size estimate, begin, next block, next tuple |
| `sql/`    | `tsm_system_rows--1.0.sql` (the handler's C symbol is gopgext/tablesample's) |

## Install

```sh
gopgext install -trusted contrib/tsm_system_rows
```

## Tests

`test.sql` (run by `make pgtest`). Upstream's regression test passes
unmodified on PostgreSQL 14 and 18:

```sh
cd <postgresql source>/contrib/tsm_system_rows
$(pg_config --pkglibdir)/pgxs/src/test/regress/pg_regress --bindir=$(pg_config --bindir) \
    --inputdir=. --temp-instance=/tmp/tsm tsm_system_rows
# ok 1 - tsm_system_rows
```

There's no differential test: the method isn't repeatable (`REPEATABLE`
is rejected), so samples can't be compared row for row. The block order
uses PostgreSQL's own sampling random generator
(`tablesample.NewRandom`, i.e. `sampler_random_fract`), as the
original does.

## Differences from the original

- The handler function `system_rows(internal)` uses the C symbol
  `gopgext_tablesample_handler` instead of `tsm_system_rows_handler`.
- Each block and tuple position is chosen by a call into Go.
