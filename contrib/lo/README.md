# lo, in Go

A port of PostgreSQL's [`contrib/lo`](https://www.postgresql.org/docs/current/lo.html)
(version 1.2, PostgreSQL 18) with [gopgext/trigger](../../README.md#package-gopgexttrigger):
the `lo` domain (a large object reference, over `oid`), `lo_oid(lo)`, and
the `lo_manage` trigger, which unlinks the large object a column stops
referencing when an UPDATE replaces it or a DELETE removes the row.

| Source       | Ported from                                              |
|--------------|----------------------------------------------------------|
| `main.go`    | `lo.c` (`lo_manage`)                                     |
| `sql/lo.sql` | `lo--1.1.sql` and `lo--1.1--1.2.sql`: the domain and `lo_oid` (SQL-standard body) |

```sh
gopgext install -version 1.2 -trusted contrib/lo
```

## Verification

**Upstream regression test**: `sql/lo.sql` with `expected/lo.out`,
unmodified, passes with `pg_regress` on PostgreSQL 18 (its own test) and
14 (both the 14 and the 18 versions of the test), including the deferred
AFTER trigger cases:

```sh
$(pg_config --pkglibdir)/pgxs/src/test/regress/pg_regress --temp-instance=/tmp/lo \
    --bindir=$(pg_config --bindir) --inputdir=<postgresql source>/contrib/lo lo
# ok 1 - lo
```

`test.sql` (run by `make pgtest`) repeats those cases as assertions and
adds NULLs, unchanged values, a missing large object, a missing column and
a statement-level trigger.

## Differences from the original

- Large objects are unlinked with `lo_unlink()` through SPI instead of a
  direct call of its C function: same checks (ownership) and errors.
- Calling `lo_manage()` outside a trigger fails with gopgext/trigger's
  "trigger functions can only be called as triggers" instead of
  "lo_manage: not fired by trigger manager".
