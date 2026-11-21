# moddatetime, in Go

A port of `moddatetime` from PostgreSQL's [`contrib/spi`](https://www.postgresql.org/docs/current/contrib-spi.html)
(version 1.0, PostgreSQL 18) with [gopgext/trigger](../../README.md#package-gopgexttrigger):
`moddatetime(column)`, a BEFORE UPDATE row trigger that sets a `timestamp`
or `timestamptz` column to the current time (`'now'`, the transaction's
start).

| Go source | Ported from                 |
|-----------|-----------------------------|
| `main.go` | `contrib/spi/moddatetime.c` |

```sh
gopgext install contrib/moddatetime
```

## Verification

Upstream has no regression test for it (only `moddatetime.example`).
`test.sql` (run by `make pgtest`) runs the example as assertions for both
column types (the new values equal `localtimestamp`/`now()`), and the
errors (wrong column type, INSERT).

## Differences from the original

- The value is `'now'` parsed by the column's input function with the
  column's type modifier (`Row.SetText`): a `timestamp(0)` column gets
  whole seconds, where the original stores the unrounded value.
- Calling it outside a trigger fails with gopgext/trigger's "trigger
  functions can only be called as triggers" instead of "moddatetime: not
  fired by trigger manager"; the other messages are the original's.
