# isn, in Go

A port of PostgreSQL's [`contrib/isn`](https://www.postgresql.org/docs/current/isn.html)
(version 1.3) to Go: data types for international product numbering
standards (EAN13, UPC, ISBN, ISMN, ISSN and their 13-digit forms), as a
drop-in replacement.

isn is mostly SQL: 8 types declared `LIKE int8`, with 168 operators, 16
btree/hash operator classes and 20 casts built on PostgreSQL's int8
functions. That SQL is upstream's own install scripts, unmodified, in
`sql/` (gopgext appends them to the install script). They reference 15 C
functions: the parsers, formatters and casts, implemented in `main.go`,
exported under upstream's C symbol names:

```go
//gopgext:function symbol=isbn_in manual
func IsbnIn(s pg.CString) (int64, error) { return in(s, typeISBN) }
```

| Go source  | Ported from (PostgreSQL 19)                                     |
|------------|-----------------------------------------------------------------|
| `main.go`  | `isn.c`: parsing, check digits, hyphenation, casts, `isn.weak` |
| `tables.go`| the hyphenation range tables of `EAN13.h`, `ISBN.h`, `ISMN.h`, `ISSN.h`, `UPC.h` |
| `sql/`     | `isn--1.1.sql`, `isn--1.1--1.2.sql`, `isn--1.2--1.3.sql`, verbatim |

```sh
gopgext install -version 1.3 -trusted contrib/isn
```

## Verification

**Upstream regression test**: PostgreSQL's `pg_regress` with
`contrib/isn/sql/isn.sql` and `expected/isn.out` unmodified passes against
this port, including the non-error-throwing input API
(`pg_input_is_valid`, `pg_input_error_info`) and weak mode:

```sh
$(pg_config --pkglibdir)/pgxs/src/test/regress/pg_regress --temp-instance=/tmp/isn \
    --bindir=$(pg_config --bindir) --inputdir=<postgresql source>/contrib/isn isn
# ok 1 - isn
```

**Differential test**: `differential.sql` runs the 15 functions of both
this port and the original C library (installed as `cisn.so`, see the
script) on generated input: numbers of every prefix family and length,
correct and wrong check digits, `M`/`X`/`?`/`!` markers, hyphens, garbage,
with `isn.weak` off and on, then formats and casts everything parsed plus
random raw values. On 100,000 inputs (103,087 distinct values): 0
mismatches in results and error messages.

Speed, 1,000,000 ISBNs (PostgreSQL 19): parsing 146 ms (C: 111 ms),
formatting 177 ms (C: 147 ms). Of the remaining difference, about 15 ns
per call is gopgext's call boundary and the rest TinyGo's code (bounds
checks); the parser uses stack buffers like the original, since heap
allocations and closures capturing its locals made it markedly slower.
