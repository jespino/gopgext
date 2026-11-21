# pg_stat_statements, in Go

A port of PostgreSQL's
[`pg_stat_statements`](https://www.postgresql.org/docs/current/pgstatstatements.html)
(version 1.12, from PostgreSQL 18) on gopgext's packages: `gopgext/hooks`
for the query processing hooks, `gopgext/shmem` for the shared hash table,
lock and texts, and `gopgext/pg` for settings and the statistics file. It
has no C code of its own.

| File         | Upstream (`pg_stat_statements.c`)                                  |
|--------------|--------------------------------------------------------------------|
| `main.go`    | `_PG_init`, settings, the hooks, `pgss_store`, `entry_alloc`, `entry_dealloc` |
| `texts.go`   | `qtext_store`/`qtext_fetch`, `gc_qtexts`, `generate_normalized_query` |
| `views.go`   | `pg_stat_statements()`, `pg_stat_statements_info()`, `entry_reset` |
| `persist.go` | `pgss_shmem_startup` (loading), `pgss_shmem_shutdown` (saving)     |
| `sql/`       | the views, `pg_stat_statements_reset` with defaults, privileges   |

## Install

```sh
gopgext install -version 1.12 contrib/pg_stat_statements
```

then in `postgresql.conf`:

```
shared_preload_libraries = 'pg_stat_statements'
```

restart, and `CREATE EXTENSION pg_stat_statements`. The settings are the
original's: `pg_stat_statements.max`, `.track` (`none`, `top`, `all`),
`.track_utility`, `.track_planning` and `.save`.

## Tests

`test.sql` (run by `make pgtest`; the `preload` file makes the test runner
preload the library). Upstream's regression suite passes unmodified on
PostgreSQL 18, except `oldextversions` (only 1.12 is provided):

```sh
gopgext install -version 1.12 contrib/pg_stat_statements
cd <postgresql 18 source>/contrib/pg_stat_statements
$(pg_config --pkglibdir)/pgxs/src/test/regress/pg_regress --bindir=$(pg_config --bindir) \
    --inputdir=. --temp-instance=/tmp/pgss --temp-config=pg_stat_statements.conf \
    --encoding=UTF8 --no-locale select dml cursors utility level_tracking planning \
    user_activity wal entry_timestamp privileges extended parallel cleanup squashing
# ok 1 - select ... ok 14 - squashing
```

Eviction (`pg_stat_statements.max = 100` with 400 statements),
compaction of the texts and persistence across restarts were checked on
PostgreSQL 18 and 14.

## Differences from the original

- One version, 1.12, with PostgreSQL 18's behavior on every server version
  (14+): e.g. statements in a `DO` block are nested even when utility
  statements aren't tracked (1.9 counted them as top-level). Counters a
  server version doesn't have stay 0 (`wal_buffers_full`, parallel workers
  before 18; JIT deform before 17; local block times are included in the
  shared ones before 17).
- Query texts are kept in shared memory (2 kB per entry of
  `pg_stat_statements.max`, compacted when needed) instead of a file in
  `pg_stat_tmp/`. A statement whose normalized text doesn't fit isn't
  tracked.
- Statistics are saved to `pg_stat/gopgext_pg_stat_statements.stat`, in a
  format of its own.
- Texts aren't converted between database encodings.
