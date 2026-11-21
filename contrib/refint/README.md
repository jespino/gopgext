# refint, in Go

A port of `refint` from PostgreSQL's [`contrib/spi`](https://www.postgresql.org/docs/current/contrib-spi.html)
(version 1.0, PostgreSQL 18) with [gopgext/trigger](../../README.md#package-gopgexttrigger):
referential integrity with AFTER row triggers (obsolete: use foreign keys).

- `check_primary_key('fkey1', ..., 'ptable', 'pkey1', ...)` checks that an
  inserted or updated row's key exists in the referenced table;
- `check_foreign_key(nrefs, 'restrict' | 'cascade' | 'setnull', 'pkey1',
  ..., 'ftable1', 'fkey11', ..., ...)` checks that a deleted or updated
  row's key isn't referenced, or deletes/updates the referencing rows, or
  sets their keys to NULL.

| Go source | Ported from            |
|-----------|------------------------|
| `main.go` | `contrib/spi/refint.c` |

```sh
gopgext install contrib/refint
```

## Verification

**Upstream regression test**: `contrib/spi/sql/refint.sql` with
`expected/refint.out`, unmodified, passes with `pg_regress` on PostgreSQL
18 and 14 (the 14 branch has no test; the 18 one is used), including the
`NOTICE`s, error details and the `CONTEXT` of an error raised by a
cascaded statement:

```sh
$(pg_config --pkglibdir)/pgxs/src/test/regress/pg_regress --temp-instance=/tmp/spi \
    --bindir=$(pg_config --bindir) --inputdir=<postgresql source>/contrib/spi refint
# ok 1 - refint
```

`test.sql` (run by `make pgtest`) repeats those checks as assertions and
adds `setnull`, cascaded updates of keys with quotes and backslashes, NULL
keys and the argument errors.

## Differences from the original

- Statements are built exactly like the original's (same text, so error
  contexts match) and run through SPI with the key values as parameters
  of their columns' types (`pg.AnyElement`). Like PostgreSQL 18's refint,
  plans aren't cached across calls (older releases kept them in a static
  array); each statement runs in its own subtransaction.
- The `REFINT_VERBOSE` notices ("N tuple(s) of T are deleted") are always
  on, as in upstream's build (its Makefile defines it).
- Calling a function outside a trigger fails with gopgext/trigger's
  "trigger functions can only be called as triggers" instead of
  "check_primary_key: not fired by trigger manager"; the other messages
  are the original's.
