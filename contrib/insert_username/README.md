# insert_username, in Go

A port of `insert_username` from PostgreSQL's [`contrib/spi`](https://www.postgresql.org/docs/current/contrib-spi.html)
(version 1.0, PostgreSQL 18) with [gopgext/trigger](../../README.md#package-gopgexttrigger):
`insert_username(column)`, a BEFORE INSERT OR UPDATE row trigger that sets a
`text` column to the current user's name.

| Go source | Ported from                     |
|-----------|---------------------------------|
| `main.go` | `contrib/spi/insert_username.c` |

```sh
gopgext install contrib/insert_username
```

## Verification

Upstream has no regression test for it (only `insert_username.example`).
`test.sql` (run by `make pgtest`) runs the example as assertions, an
UPDATE as another role, and the errors (wrong column type, DELETE).

## Differences from the original

- The user name comes from `current_user` through SPI (the original calls
  `GetUserNameFromId(GetUserId())`: the same user).
- Calling it outside a trigger fails with gopgext/trigger's "trigger
  functions can only be called as triggers" instead of "insert_username:
  not fired by trigger manager"; the other messages are the original's.
