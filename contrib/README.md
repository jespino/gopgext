# contrib: PostgreSQL's contrib modules in Go

Ports of modules from PostgreSQL's
[`contrib/`](https://www.postgresql.org/docs/current/contrib.html), built
with gopgext. A port only uses gopgext's public packages (`gopgext/pg`,
the framework packages) and the Go standard library: no C of its own.
When a module needs something gopgext lacks, gopgext gets it as a general
feature (`pg_stat_statements` brought `gopgext/hooks`, `gopgext/shmem`,
enum settings, `time.Time` and `pg.Numeric`; `pgcrypto` a working
`crypto/rand` and `AnyArray.Dims`; `auto_explain` instrumentation and
EXPLAIN output in gopgext/hooks, `pg.Message` and setting units;
`passwordcheck` and `auth_delay` gopgext/auth; the trigger modules
`gopgext/trigger`, `pg.Notify` and `pg.AnyElement` query parameters;
the text search dictionaries `gopgext/tsearch` and `tsearch_data/` files;
`hstore` `gopgext/subscript`, GiST
operator class parameters, record and multi-dimensional array APIs;
`citext` collation-aware string functions; `tablefunc` `pg.Record` results;
`uuid-ossp` `pg.UUID`; `tsm_system_rows`/`tsm_system_time`
`gopgext/tablesample`; `cube`, `seg` and `earthdistance` fixed-size types,
`pg.Point`, `pg.FormatFloat8`, `-requires`, `//gopgext:alias` and
fixed-size GiST keys and queries).

Ports keep the original's name, SQL interface and behavior, and are checked
against upstream's regression tests and, where possible, against the C
original on generated inputs (`differential.sql`).

| Port                 | Upstream version | Status |
|----------------------|------------------|--------|
| `pg_stat_statements` | 1.12 (PostgreSQL 18) | passes upstream's PostgreSQL 18 regression suite; needs `shared_preload_libraries` |
| `pgcrypto`           | 1.4              | passes upstream's regression suite on 14 and 18; matches the C original (0 mismatches on PostgreSQL 18) |
| `isn`                | 1.3              | passes upstream's regression test; matches the C original on 100,000 inputs |
| `fuzzystrmatch`      | 1.2              | passes upstream's regression tests; matches the C original on 100,000 strings |
| `auto_explain`       | PostgreSQL 18    | passes upstream's tests (TAP test ported to SQL on 14 and 18, `alter_reset` on 18); logs the same plans as the C original |
| `passwordcheck`      | PostgreSQL 18    | passes upstream's regression test on 18 (14's expected output lacks 15's DETAIL) |
| `auth_delay`         | PostgreSQL 18    | delays failed logins (tested with a password login); needs `shared_preload_libraries` |
| `tcn`                | 1.0              | passes upstream's isolation spec on 14 and 18 (gopgext/trigger) |
| `lo`                 | 1.2              | passes upstream's regression test on 14 and 18 |
| `autoinc`, `insert_username`, `moddatetime`, `refint` (`contrib/spi`) | 1.0 | autoinc and refint pass upstream's regression tests on 14 and 18 |
| `dict_int`           | 1.0              | passes upstream's regression test on 14 and 18; matches the C original (700,000 lexizes over 7 option sets) |
| `dict_xsyn`          | 1.0              | passes upstream's regression test on 14 and 18; matches the C original with all 16 option combinations |
| `unaccent`           | 1.1              | passes upstream's regression test on 14 and 18; matches the C original on 100,000 strings (PostgreSQL 18) |
| `hstore`             | 1.8              | passes upstream's regression tests on 14 and 18; binary compatible: matches the C original on 100,000 inputs (values, errors, every operator) |
| `citext`             | 1.8              | passes upstream's regression tests on 14 and 18 (UTF-8 and C locales) |
| `tablefunc`          | 1.0              | passes upstream's PostgreSQL 18 regression test on 14 and 18; matches the C original (940 generated queries) |
| `uuid-ossp`          | 1.1              | passes upstream's regression test on 14 and 18; v3/v5 match the C original on 20,000 names; v1 uses a random node |
| `tsm_system_rows`    | 1.0              | passes upstream's regression test on 14 and 18 |
| `tsm_system_time`    | 1.0              | passes upstream's regression test on 14 and 18 |
| `cube`               | 1.5              | passes upstream's regression tests on 14 and 18; matches the C original (0 mismatches on 14 and 18); same binary layout and C symbols |
| `earthdistance`      | 1.2              | passes upstream's regression test on 14 and 18; `<@>` matches the C original on 120,000 point pairs; needs cube |
| `seg`                | 1.4              | passes upstream's regression tests on 14 and 18; matches the C original (0 mismatches); same 12-byte layout and C symbols |

Each directory has a README with the details and differences. Install one
with `gopgext install $(cat contrib/<name>/build-flags) contrib/<name>`; it
replaces the C module of the same name in that PostgreSQL installation.
