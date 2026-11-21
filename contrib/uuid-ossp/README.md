# uuid-ossp, in Go

A port of PostgreSQL's
[`uuid-ossp`](https://www.postgresql.org/docs/current/uuid-ossp.html)
(version 1.1): `uuid_generate_v1`, `uuid_generate_v1mc`,
`uuid_generate_v3`, `uuid_generate_v4`, `uuid_generate_v5`, `uuid_nil` and
`uuid_ns_dns/url/oid/x500`, with gopgext's `pg.UUID` (SQL `uuid`) and the
standard library (`crypto/md5`, `crypto/sha1`, `crypto/rand`). It needs no
UUID library (the original uses OSSP uuid, e2fsprogs' libuuid or the BSD
functions) and has no C code of its own.

| File      | Upstream                                                   |
|-----------|------------------------------------------------------------|
| `main.go` | `uuid-ossp.c`; the SQL is generated (same as `uuid-ossp--1.1.sql`, with the original's C symbols) |

## Install

```sh
gopgext install -name uuid-ossp -version 1.1 -trusted contrib/uuid-ossp
```

(the extension's name has a hyphen: `CREATE EXTENSION "uuid-ossp"`).

## Tests

`test.sql` (run by `make pgtest`): constants, v3/v5 known values,
version and variant bits, v1 node/clock sequence/timestamps (monotonic,
the current time), uniqueness. Upstream's regression test passes on
PostgreSQL 14 and 18:

```sh
cd <postgresql source>/contrib/uuid-ossp
$(pg_config --pkglibdir)/pgxs/src/test/regress/pg_regress --bindir=$(pg_config --bindir) \
    --inputdir=. --temp-instance=/tmp/uo uuid_ossp
# ok 1 - uuid_ossp
```

**Differential test**: `differential.sql` compares with the C original
(installed as `uuid-ossp_c.so`): the constants, v3 and v5 of 20,000
namespaces × names (empty, ASCII, non-ASCII, up to 300 characters) are
identical; v1, v1mc and v4 have the same version, variant and (v1) time.
PostgreSQL 18: 0 mismatches.

## Differences from the original

- Version 1 UUIDs use a random node identifier with the multicast bit set
  (RFC 4122 section 4.5), chosen once per backend, instead of the
  machine's MAC address (which gopgext can't read). This is what libuuid
  and OSSP uuid do when no MAC address is available; upstream's test
  accepts it. The clock sequence is random per backend, and timestamps
  never go backwards within a backend.
- `uuid_generate_v1mc` sets the multicast and locally administered bits
  of a random node, like the e2fs and BSD builds.
