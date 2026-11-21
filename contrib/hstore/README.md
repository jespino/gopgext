# hstore, in Go

A port of PostgreSQL's [`contrib/hstore`](https://www.postgresql.org/docs/current/hstore.html)
(version 1.8, from PostgreSQL 18) to Go: sets of key/value pairs in a
single value, as a drop-in replacement. Values have upstream's binary
format (the varlena with its array of `HEntry`), so tables, dumps, binary
COPY and the binary protocol are interchangeable with the C module, and
pre-9.0 values are converted on read like upstream does.

```sh
gopgext install -version 1.8 -trusted contrib/hstore
```

| Go source    | Ported from (PostgreSQL 18)                                      |
|--------------|------------------------------------------------------------------|
| `hstore.go`  | `hstore.h` (format, pairs, key search), `hstore_compat.c` (old formats, `hstore_version_diag`) |
| `io.go`      | `hstore_io.c`: parser and quoting, `hstore_in/out/recv/send`, constructors from text, arrays and records, `populate_record`, JSON/jsonb |
| `op.go`      | `hstore_op.c`: `->`, `?`, `?|`, `?&`, `@>`, `<@`, `||`, `-`, `%%`, `%#`, `each`/`skeys`/`svals`/`akeys`/`avals`, slices, btree comparison, hashes |
| `index.go`   | `hstore_gist.c` (`gist_hstore_ops` with `siglen`), `hstore_gin.c` (`gin_hstore_ops`), `hstore_subs.c` (`h['key']`) |
| `sort.go`    | PostgreSQL's `qsort` (`lib/sort_template.h`): it decides which duplicate key input keeps |
| `sql/`       | `hstore--1.4.sql` and the upgrade scripts to 1.8, in order; see below |

The SQL scripts are upstream's, with one change: the C symbols of the
index and subscripting support functions point to gopgext's frameworks
(`ghstore_compress` → `gopgext_gist_compress`, ..., `ghstore_options` →
`gopgext_gist_options`, `gin_extract_hstore` → `gopgext_gin_extract_value`,
..., `hstore_subscript_handler` → `gopgext_subscript_handler`). Every other
function keeps upstream's symbol (`symbol=hstore_in manual` in Go).

What the port needed from gopgext, added as general features:

- `gopgext/subscript`, a framework for `SUBSCRIPT` handlers;
- GiST operator class parameters (`gist.OpClass.Options`, `gist.Option`)
  and an identity decompress function;
- composite values of any row type for `record`/`anyelement` arguments
  (`pg.ArgType`, `pg.IsComposite`, `pg.Columns`, `AnyElement.RowType`,
  `AnyElement.Fields`, `pg.NewRecord`, `pg.Input` with typmod and domain
  checks);
- multi-dimensional arrays with bounds (`AnyArray.LowerBounds`,
  `pg.NewAnyArrayDims`), `AnyElement.Bytes`/`pg.FromBytes`;
- `pg.HashBytes`/`pg.HashBytesExtended` (`hash_any`), and set-returning
  functions with NULL rows (`iter.Seq[*string]`).

## Verification

**Upstream regression tests**: `pg_regress` with upstream's `sql/` and
`expected/` (and `data/hstore.data`) unmodified passes against the port:
`hstore` and `hstore_utf8` of REL_18_STABLE on PostgreSQL 18 (UTF-8
`en_US.utf8`, and C locale with SQL_ASCII, where `hstore_utf8` takes its
alternative output) and of REL_14_STABLE on PostgreSQL 14. They cover the
GiST (with `siglen`), GIN, btree and hash indexes, records, JSON and
subscripting.

```sh
cd <postgresql source>/contrib/hstore
$(pg_config --pkglibdir)/pgxs/src/test/regress/pg_regress --temp-instance=/tmp/hs \
    --bindir=$(pg_config --bindir) --inputdir=. hstore hstore_utf8
```

**Differential test**: `differential.sql` declares the functions of both
this port and the C library (copied to `hstore_c.so`) on `bytea`, so the
binary values are compared. On 100,000 generated inputs (token soup, and
valid input with quoting, escapes, NULLs, duplicate keys, non-ASCII), on
PostgreSQL 18: 0 mismatches in `hstore_in` (the same bytes or the same
error), and 0 in `hstore_out`, `hstore_send`, the four JSON conversions,
both hashes, `akeys`/`avals`/`hstore_to_matrix`, `->`, `||`, `-`, `@>`,
`hstore_cmp` and the array constructors on the 42,000 valid values. On
PostgreSQL 14 values match too; only syntax error messages differ, since
the port has PostgreSQL 16+'s (`syntax error in hstore, near "=" at
position 0`, 42601, vs `Syntax error near "=" at position 0`, XX000).

`test.sql` (run by `make pgtest`) checks the operators, the indexes against
sequential scans, binary COPY (`hstore_recv`), records with domains and
subscripting.

## Differences

- `hstore_send`/`hstore_recv` don't convert between the server and client
  encodings (`pq_sendtext`): binary I/O is byte-for-byte, which only
  differs from upstream when client and server encodings differ.
- Syntax errors show the offending character decoded as UTF-8.
- The subscript handler declares fetching as not leakproof (upstream: yes),
  which only matters for the planner with security barrier views and RLS.
- GiST keys are the same bytes, but pages may split differently: interior
  keys whose bits are all set aren't compacted to the "all true" form by
  `compress` (unions are).
- Old C function names of hstore 1.0 dumps (`tconvert`, `exists`,
  `hs_concat`, ... as C symbols) aren't exported: the scripts don't use
  them.
