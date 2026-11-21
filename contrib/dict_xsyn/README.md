# dict_xsyn, in Go

A port of PostgreSQL's
[`contrib/dict_xsyn`](https://www.postgresql.org/docs/current/dict-xsyn.html)
(version 1.0) to Go, as a drop-in replacement: the `xsyn_template` text
search template and the `xsyn` dictionary, with the same options
(`rules`, `matchorig`, `keeporig`, `matchsynonyms`, `keepsynonyms`),
results and errors. It's built on `gopgext/tsearch`, which reads the rules
file like the original (`tsearch_readline`: UTF-8 converted to the server
encoding) and lower-cases with the database's default collation.

| File                              | Ported from (PostgreSQL 18)                          |
|-----------------------------------|------------------------------------------------------|
| `main.go`                         | `dict_xsyn.c`                                        |
| `sql/dict_xsyn.sql`               | `dict_xsyn--1.0.sql`, with gopgext/tsearch's C symbols |
| `tsearch_data/xsyn_sample.rules`  | `xsyn_sample.rules` (installed into `$SHAREDIR/tsearch_data`) |

```sh
gopgext install -version 1.0 contrib/dict_xsyn
```

## Verification

**Upstream regression test**: PostgreSQL's `pg_regress` with the `sql/` and
`expected/` files of `contrib/dict_xsyn`, unmodified, passes against this
port on PostgreSQL 18 and 14 (each with its own branch's files).

**Differential test**: `differential.sql` compares `ts_lexize` with the C
original (installed as `dict_xsyn_c.so`) with all 16 combinations of the
boolean options, on the sample's words in several cases and others, and the
errors of invalid options (missing and invalid file names, non-boolean
values, unknown options): 0 mismatches, on PostgreSQL 18 and 14.

## Differences

- Words are separated by ASCII white space; the original uses `isspace()`
  of the server's locale, which in a single-byte locale can also take
  e.g. a no-break space (0xA0) for a separator.
- If a rules file has the same word twice, the original's `qsort`/`bsearch`
  return either line; this port the first one.
- Errors that the Go code raises about a rules file (none in practice: the
  original raises none either) don't get the "line N of configuration file"
  context.
