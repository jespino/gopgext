# tsm_system_time, in Go

A port of PostgreSQL's
[`tsm_system_time`](https://www.postgresql.org/docs/current/tsm-system-time.html)
(version 1.0): the `SYSTEM_TIME` `TABLESAMPLE` method (`TABLESAMPLE system_time (ms)`), which
reads whole blocks in a random order until the given number of milliseconds has passed. It's built on `gopgext/tablesample` and has no C code of its own.

| File      | Upstream                                                       |
|-----------|----------------------------------------------------------------|
| `main.go` | `tsm_system_time.c`: sample size estimate, begin, next block, next tuple |
| `sql/`    | `tsm_system_time--1.0.sql` (the handler's C symbol is gopgext/tablesample's) |

## Install

```sh
gopgext install -trusted contrib/tsm_system_time
```

## Tests

`test.sql` (run by `make pgtest`). Upstream's regression test passes
unmodified on PostgreSQL 14 and 18:

```sh
cd <postgresql source>/contrib/tsm_system_time
$(pg_config --pkglibdir)/pgxs/src/test/regress/pg_regress --bindir=$(pg_config --bindir) \
    --inputdir=. --temp-instance=/tmp/tsm tsm_system_time
# ok 1 - tsm_system_time
```

There's no differential test: the method isn't repeatable (`REPEATABLE`
is rejected), so samples can't be compared row for row. The block order
uses PostgreSQL's own sampling random generator
(`tablesample.NewRandom`, i.e. `sampler_random_fract`), as the
original does.

## Differences from the original

- The handler function `system_time(internal)` uses the C symbol
  `gopgext_tablesample_handler` instead of `tsm_system_time_handler`.
- Each block and tuple position is chosen by a call into Go.
