# tcn, in Go

A port of PostgreSQL's [`contrib/tcn`](https://www.postgresql.org/docs/current/tcn.html)
(version 1.0, PostgreSQL 18) with [gopgext/trigger](../../README.md#package-gopgexttrigger):
`triggered_change_notification()`, an AFTER ROW trigger that sends a
`NOTIFY` with the primary key of each inserted, updated or deleted row
(`"table",I,"key"='1'`), on the channel given as its argument (default
`tcn`).

| Go source | Ported from        |
|-----------|--------------------|
| `main.go` | `tcn.c`            |

The SQL (`tcn--1.0.sql`: one `CREATE FUNCTION ... RETURNS trigger`) is
generated from the Go signature.

```sh
gopgext install -trusted contrib/tcn
```

## Verification

**Upstream test**: tcn's test is an isolation spec (`specs/tcn.spec`,
`expected/tcn.out`); it passes unmodified with `pg_isolation_regress` on
PostgreSQL 18 and 14 (the 14 branch has no test; the 18 spec is used):

```sh
$(pg_config --pkglibdir)/pgxs/src/test/isolation/pg_isolation_regress --temp-instance=/tmp/tcn \
    --bindir=$(pg_config --bindir) --inputdir=<postgresql source>/contrib/tcn --load-extension=tcn tcn
# ok 1 - tcn
```

**`test.sql`** (run by `make pgtest`) is the same scenario in plain psql,
plus the default channel, a composite key and quotes in names and values,
and a rolled-back insert: psql prints the notifications its session
receives after each command, so the script sends them to a temporary file
(`\o`), reads it back with `pg_read_file()` (the test runs as superuser on
the same machine) and compares the payloads.

## Differences from the original

- The primary key's columns come from a catalog query through SPI (the
  original reads the relcache), and the notification is sent with
  `pg.Notify` (`pg_notify()` through SPI) instead of `Async_Notify`: same
  payloads, same delivery at commit.
- Calling the function outside a trigger fails with gopgext/trigger's
  "trigger functions can only be called as triggers" (`0A000`) instead of
  "triggered_change_notification: must be called as trigger"; the other
  messages are the original's.
