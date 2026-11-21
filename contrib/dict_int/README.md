# dict_int, in Go

A port of PostgreSQL's
[`contrib/dict_int`](https://www.postgresql.org/docs/current/dict-int.html)
(version 1.0) to Go, as a drop-in replacement: the `intdict_template` text
search template and the `intdict` dictionary, with the same options
(`maxlen`, `rejectlong`, `absval`), results and errors. It's built on
`gopgext/tsearch`.

| File               | Ported from (PostgreSQL 18)                                  |
|--------------------|--------------------------------------------------------------|
| `main.go`          | `dict_int.c` (`atoi` reproduces C's parsing of `maxlen`)     |
| `sql/dict_int.sql` | `dict_int--1.0.sql`, with gopgext/tsearch's C symbols        |

```sh
gopgext install -version 1.0 -trusted contrib/dict_int
```

## Verification

**Upstream regression test**: PostgreSQL's `pg_regress` with the `sql/` and
`expected/` files of `contrib/dict_int`, unmodified, passes against this
port on PostgreSQL 18 (REL_18_STABLE files) and 14 (REL_14_STABLE files):

```sh
$(pg_config --pkglibdir)/pgxs/src/test/regress/pg_regress --temp-instance=/tmp/di \
    --bindir=$(pg_config --bindir) --inputdir=<postgresql source>/contrib/dict_int dict_int
# ok 1         - dict_int
```

**Differential test**: `differential.sql` compares `ts_lexize` with the C
original (installed as `dict_int_c.so`) under 7 sets of options on
generated words (signs, digits, letters, lengths 0 to 20), and the errors
of invalid options: 700,035 lexizes, 0 mismatches, on PostgreSQL 18 and 14.

`test.sql` covers options and their catalog round trip, errors, and use in
a text search configuration.
