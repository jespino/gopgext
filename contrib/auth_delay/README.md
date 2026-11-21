# auth_delay, in Go

A port of PostgreSQL's
[`auth_delay`](https://www.postgresql.org/docs/current/auth-delay.html)
(PostgreSQL 18) on `gopgext/auth` (`OnAuthenticate`). It pauses before
reporting an authentication failure, to slow down brute-force attacks. It
has no C code of its own.

| File      | Upstream (`auth_delay.c`)                     |
|-----------|-----------------------------------------------|
| `main.go` | `_PG_init` (setting), `auth_delay_checks`     |

## Install

```sh
gopgext install contrib/auth_delay
```

then in `postgresql.conf` (authentication runs before
`session_preload_libraries` are loaded):

```
shared_preload_libraries = 'auth_delay'
auth_delay.milliseconds = '500ms'
```

## Tests

Upstream has none. `test.sql` (run by `make pgtest`; the `preload` file
preloads the library) checks the setting (`PGC_SIGHUP`, unit ms), then
adds a `password` line for a test role to `pg_hba.conf`, sets
`auth_delay.milliseconds = 1500ms` with `ALTER SYSTEM`, and times psql
connections run with `\!`: one with a wrong password must take at least
1.5 s, ones with the right password or `trust` less. `pg_hba.conf` and the
setting are restored at the end.

## Differences from the original

None known. Like upstream, the delay also applies when a client hangs up
when asked for a password (as psql does before prompting).
