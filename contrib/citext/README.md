# citext, in Go

A port of PostgreSQL's [`contrib/citext`](https://www.postgresql.org/docs/current/citext.html)
(version 1.8, from PostgreSQL 18) to Go: a case-insensitive character
string type, as a drop-in replacement.

```sh
gopgext install -version 1.8 -trusted contrib/citext
```

| Go source | Ported from (PostgreSQL 18) |
|-----------|-----------------------------|
| `main.go` | `citext.c`: comparison, equality, hashing, `min`/`max` support, `citext_pattern_ops` |
| `sql/`    | `citext--1.4.sql` and the upgrade scripts to 1.8, in order, verbatim |

citext is mostly SQL: the type reuses `text`'s I/O functions, and the
regular expression, `LIKE`, `strpos`, `replace`, `split_part`, `translate`
wrappers are SQL functions. The 17 C functions are implemented in Go under
upstream's symbols. As upstream, values are lowercased with the database's
default collation (so equality and hashing don't depend on the collation),
and ordering compares the lowercased strings with the collation of the call.
That needed new gopgext APIs: `pg.Collation()` (the call's input collation,
`PG_GET_COLLATION()`), `pg.ToLower`/`pg.ToUpper` (`str_tolower` with a
collation), `pg.CompareStrings` (`varstr_cmp`) and `pg.HashBytes`
(`hash_any`), so hashes equal the C module's (hash indexes and hash
partitions are compatible).

## Verification

**Upstream regression tests**: `pg_regress` with upstream's `sql/` and
`expected/` unmodified passes against the port, in upstream's order
(`create_index_acl citext citext_utf8`): REL_18_STABLE's on PostgreSQL 18
with a UTF-8 `en_US.utf8` database (`citext.out`, `citext_utf8.out`) and a
C-locale one (the alternative outputs), and REL_14_STABLE's
(`create_index_acl citext`) on PostgreSQL 14.

```sh
cd <postgresql source>/contrib/citext
$(pg_config --pkglibdir)/pgxs/src/test/regress/pg_regress --temp-instance=/tmp/ci \
    --bindir=$(pg_config --bindir) --inputdir=. create_index_acl citext citext_utf8
```

`test.sql` (run by `make pgtest`) checks the operators, hashes against
`hashtext`, aggregates, the pattern operators and a unique index.

## Differences

None known.
