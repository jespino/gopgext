# earthdistance, in Go

A port of PostgreSQL's
[`contrib/earthdistance`](https://www.postgresql.org/docs/current/earthdistance.html)
(version 1.2) to Go: great-circle distances on the surface of the Earth,
on points of the cube extension (the `earth` domain, `ll_to_earth`,
`earth_distance`, `earth_box`, ...) or on the built-in `point` type
(`geo_distance` and the `<@>` operator), as a drop-in replacement.

It depends on cube (`requires = 'cube'` in the control file, from
gopgext's `-requires` flag): install this repository's `contrib/cube` or
the C one.

| Source     | Ported from (PostgreSQL 18)                                     |
|------------|-----------------------------------------------------------------|
| `main.go`  | `earthdistance.c`: `geo_distance(point, point)` on `pg.Point`, under upstream's symbol |
| `sql/earthdistance.sql` | `earthdistance--1.1.sql` with `earthdistance--1.1--1.2.sql` applied (see below) |

```sh
gopgext install -version 1.2 -requires cube \
    -comment 'calculate great-circle distances on the surface of the Earth' contrib/earthdistance
```

## gopgext features it brought

- `-requires ext,...`: the control file's `requires`.
- `pg.Point`: the built-in `point` type (16 bytes passed by reference) as a
  Go struct, in arguments, results, arrays and rows.

## Differences from upstream

- Upstream's 1.2 objects refer to cube's objects as `@extschema:cube@.x`,
  which PostgreSQL substitutes only from version 16 on. The port creates
  the objects that use cube (the `earth` domain, `ll_to_earth`, `latitude`,
  `longitude`, `earth_distance`, `earth_box`) in a `DO` block with cube's
  schema read from `pg_extension`, so the single install script works
  from PostgreSQL 14 on, with the same objects and the same protection
  against search path tricks.

## Verification

**Upstream regression test**: `pg_regress` with `contrib/earthdistance`'s
`sql/` and `expected/` unmodified passes against this port and
`contrib/cube` (`earthdistance`, which also exercises creating and dropping
cube under and beside earthdistance, in another schema), on PostgreSQL 18
and on PostgreSQL 14 (REL_14_STABLE's files).

**Tests**: `test.sql` checks `requires`, `<@>` (including across the
antimeridian), `earth_distance`, `earth_box`, `latitude` and `longitude`.

**Differential test**: `differential.sql` compares `<@>` with the original
C `geo_distance` (installed as `earthdistance_c.so`) on 120,000 point pairs
(random coordinates at several scales, NaN, infinities, ±0, antipodes,
identical points): 0 mismatches in the exact `float8` results, on
PostgreSQL 18 and 14.
