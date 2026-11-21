# auto_explain, in Go

A port of PostgreSQL's
[`auto_explain`](https://www.postgresql.org/docs/current/auto-explain.html)
(PostgreSQL 18) on `gopgext/hooks`: the executor hooks, per-node
instrumentation (`Statement.Instrument`) and EXPLAIN output
(`Statement.Explain`), with `pg.Message` to log at the configured level
without the statement. It has no C code of its own.

| File      | Upstream (`auto_explain.c`)                                         |
|-----------|---------------------------------------------------------------------|
| `main.go` | `_PG_init` (settings), `explain_ExecutorStart` (sampling, instrumentation), `explain_ExecutorEnd` (logging); nesting is `Statement.ExecutorNesting` |

## Install

```sh
gopgext install contrib/auto_explain
```

then `LOAD 'auto_explain'` in a session (superuser), or add it to
`session_preload_libraries` or `shared_preload_libraries`. The settings
are the original's: `auto_explain.log_min_duration`,
`.log_parameter_max_length`, `.log_analyze`, `.log_settings`,
`.log_verbose`, `.log_buffers`, `.log_wal`, `.log_triggers`,
`.log_format`, `.log_level`, `.log_nested_statements`, `.log_timing` and
`.sample_rate`.

## Tests

`test.sql` (run by `make pgtest`) holds upstream's TAP test
(`t/001_auto_explain.pl`) as SQL checks: it runs statements in new psql
sessions (`\!`) with `session_preload_libraries=auto_explain` and
`auto_explain.log_level=notice`, and matches their output. It also covers
the formats, timing/buffers/WAL/triggers/settings, nested statements
(functions, `DO`, `CREATE TABLE AS`), `EXPLAIN`, sampling, thresholds with
units, log levels, `GRANT SET ON PARAMETER` for non-superusers (15+), and
the reserved prefix. Query parameters are checked on 16+ and query
identifiers on 16+, as upstream prints them.

Upstream's regression test `alter_reset` passes on PostgreSQL 18 (14 has
no reserved prefixes and no test):

```sh
$(pg_config --pkglibdir)/pgxs/src/test/regress/pg_regress --bindir=$(pg_config --bindir) \
    --inputdir=<postgresql 18 source>/contrib/auto_explain --temp-instance=/tmp/ae alter_reset
# ok 1 - alter_reset
```

Differential check against the C original (copied to `auto_explain_c.so`):
9 plans (index/seq scans, aggregates, CTE, subplans, UPDATE, a prepared
statement with parameters, a DO block, LATERAL) logged with 7 option sets
(text/json/xml/yaml, analyze without timing, verbose, settings, triggers,
WAL, parameter truncation, nested statements) are identical after removing
durations and times, on PostgreSQL 18 and 14 (on 14 the C module lacks
`log_parameter_max_length`, which the port has).

## Differences from the original

- `pg_get_loaded_modules()` (18) reports the gopgext extension version
  (1.0), not the server version; `gopgext install` also installs a
  control file, so `CREATE EXTENSION auto_explain` creates an empty
  extension (upstream has none).
- `auto_explain.log_level`'s hidden alias `debug` (= `debug2`) is listed
  in `pg_settings.enumvals`.
- `log_parameter_max_length` exists on 14 and 15 too, but parameters are
  only printed on 16+ (`ExplainQueryParameters`).
- Whether a statement is logged is decided when it starts (upstream also
  re-checks `log_min_duration >= 0` and sampling when it ends).
- Sampling uses Go's `math/rand/v2` rather than `pg_prng`.
