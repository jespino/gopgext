---
title: "Development"
description: "Building gopgext itself and running its unit and integration tests."
weight: 11
---

```sh
make                      # build ./gopgext
make test                 # go vet ./... && go test ./...
go test ./internal/build -run TestGenGolden -update   # rewrite the golden files after changing code generation
make pgtest PG_CONFIG=/path/to/pg_config              # integration tests against a real PostgreSQL
sh test/run.sh [pg_config] examples/hello             # integration tests of selected projects only
make site-serve           # preview this website at http://localhost:1313/gopgext/
```

The integration tests need TinyGo (tested with 0.43.0), the PostgreSQL 14+
server development files (`pg_config`, PGXS), `make`, a C compiler and
`objcopy`, on Linux x86-64. `test/run.sh` installs each project into the
given PostgreSQL (it may need write access to its directories), starts a
throwaway cluster in `_pgtest/<version>/` and runs every `test*.sql` file
in its own `psql` session with `ON_ERROR_STOP`: tests assert by raising
errors. `make pgtest` runs everything twice: normally, and with the
examples in `shared_preload_libraries`.

Conventions in each project directory:

| File | Meaning |
|---|---|
| `build-flags` | extra `gopgext build` flags |
| `expect-log` | lines that must appear in the server log |
| `*.fatal.sql` | must end the session (`psql` exit code 2) |
| `preload` | always load the extension through `shared_preload_libraries` |

A `TRAP:`, `PANIC:` or a backend killed by a signal in the server log fails
the run.

## This website

The site lives in `site/`: a [Hugo](https://gohugo.io/) site whose API
reference pages are generated from the packages' doc comments by
`site/apidoc`. `make site` builds it into `site/public/`, and a GitHub
Actions workflow publishes it to GitHub Pages on every push to `main`.
