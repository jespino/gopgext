# passwordcheck, in Go

A port of PostgreSQL's
[`passwordcheck`](https://www.postgresql.org/docs/current/passwordcheck.html)
(PostgreSQL 18) on `gopgext/auth` (`OnPasswordCheck`,
`Password.Matches`). It has no C code of its own.

| File      | Upstream (`passwordcheck.c`)                 |
|-----------|----------------------------------------------|
| `main.go` | `_PG_init` (setting), `check_password`       |

## Install

```sh
gopgext install contrib/passwordcheck
```

then add it to `shared_preload_libraries` (or `LOAD 'passwordcheck'` in a
session). Setting: `passwordcheck.min_password_length` (bytes, default 8).

## Tests

`test.sql` (run by `make pgtest`): upstream's regression test with
assertions on the messages and details, plus non-ASCII passwords, SCRAM
verifiers (one equal to the role's name is rejected), `password_encryption
= scram-sha-256`, `VALID UNTIL` and `CREATE USER`.

Upstream's regression test passes on PostgreSQL 18:

```sh
$(pg_config --pkglibdir)/pgxs/src/test/regress/pg_regress --bindir=$(pg_config --bindir) \
    --inputdir=<postgresql 18 source>/contrib/passwordcheck --temp-instance=/tmp/pc passwordcheck
# ok 1 - passwordcheck
```

With PostgreSQL 14's test it differs only by the "password is too short"
DETAIL, which 14's module didn't have (the port behaves as 18's).

## Differences from the original

- No cracklib support (upstream's is a compile-time option, off by
  default).
- Non-ASCII bytes count as non-letters, as upstream's `isalpha()` does in
  the C locale; upstream's result for them depends on the server's
  `LC_CTYPE` for single-byte encodings.
