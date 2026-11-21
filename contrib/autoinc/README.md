# autoinc, in Go

A port of `autoinc` from PostgreSQL's [`contrib/spi`](https://www.postgresql.org/docs/current/contrib-spi.html)
(version 1.0, PostgreSQL 18) with [gopgext/trigger](../../README.md#package-gopgexttrigger):
`autoinc(column, sequence [, column, sequence ...])`, a BEFORE INSERT OR
UPDATE row trigger that sets each `int4` column that is NULL or 0 to the
sequence's next value (skipping 0). Each `contrib/spi` module is its own
extension upstream, and here: see also `insert_username`, `moddatetime` and
`refint`.

| Go source | Ported from          |
|-----------|----------------------|
| `main.go` | `contrib/spi/autoinc.c` |

```sh
gopgext install contrib/autoinc
```

## Verification

**Upstream regression test**: `contrib/spi/sql/autoinc.sql` with
`expected/autoinc.out`, unmodified, passes with `pg_regress` on PostgreSQL
18 and 14 (the 14 branch has no test; the 18 one is used):

```sh
$(pg_config --pkglibdir)/pgxs/src/test/regress/pg_regress --temp-instance=/tmp/spi \
    --bindir=$(pg_config --bindir) --inputdir=<postgresql source>/contrib/spi autoinc
# ok 1 - autoinc
```

`test.sql` (run by `make pgtest`) has the same checks as assertions, the
`autoinc.example` cases and the error messages.

## Differences from the original

- `nextval` runs through SPI (`nextval($1::text)`: the same name lookup).
- Calling it outside a trigger fails with gopgext/trigger's "trigger
  functions can only be called as triggers" instead of "not fired by
  trigger manager"; the other messages are the original's.
