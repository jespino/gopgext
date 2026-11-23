---
title: "Limitations"
description: "What gopgext doesn't support (yet)."
weight: 10
---

- No goroutines: a backend is single-threaded and Go only runs during calls.
  (`sync.Mutex` and friends work; blocking on a channel deadlocks, which is
  reported as a panic.)
- Function signatures: no arrays of composite types, composite columns of
  composite types, `record` arguments (except as a `pg.AnyElement` in
  `manual` functions, read with `Fields`), `anycompatible`/`anyrange`-style
  polymorphic types or `VARIADIC`. Trigger functions (gopgext/trigger)
  don't see transition tables (`REFERENCING OLD TABLE ...`) and can't change
  system columns; `Set` doesn't apply typmods or domain checks (`SetText` does).
- Index operator classes (gopgext/gist, gin, spgist, brin): gin, spgist
  and brin take variable-size values, keys and queries (built-in fixed-size
  types like `point` aren't readable as bytes; gist reads them), strategies
  get no subtype, and
  operator class parameters are supported for GiST only (integers).
  `typanalyze` functions (custom statistics, like PostGIS's) and
  planner support functions (`SUPPORT`) aren't supported either.
- gopgext/am: writers to an index are serialized (readers aren't blocked),
  records fit in a page, freed pages aren't recycled (REINDEX compacts),
  builds write one WAL record per row; no parallel builds, bitmap scans,
  index-only scans or non-integer options. `examples/vector` has IVFFlat
  but not HNSW.
- gopgext/fdw: read-only scans (no INSERT/UPDATE/DELETE), no
  aggregate/sort/LIMIT pushdown; `IN (...)` lists and `LIKE` aren't offered
  as pushable conditions (they're checked locally).
- gopgext/shmem: fixed-size objects created at server start only (no
  dynamic shared memory segments or DSA areas); gopgext/hooks: no
  `emit_log_hook`; EXPLAIN output only of finished executions, with
  auto_explain's options (no custom EXPLAIN options or `MEMORY`/`SERIALIZE`).
- gopgext/bgworker: static workers registered at server start only (no
  dynamic workers started from SQL); gopgext/intercept: `UPDATE`s only,
  one handler per extension, and tables with row-level security, other
  `UPDATE` triggers or inheritance children aren't intercepted (see
  [its page](/docs/frameworks/intercept/)).
- Standard library packages that draw randomness from Go's internal
  generator instead of `crypto/rand.Reader` (`crypto/ecdh`, `crypto/ecdsa`,
  `crypto/rsa` key generation, `crypto/mlkem`, ...) don't build: it starts
  a timer goroutine. Use `crypto/rand` and `math/big`.
- Long-running Go code must call `pg.CheckInterrupts()` to be cancelable.
- Deep recursion in Go isn't checked (the backend's stack is the limit,
  usually 8 MB).
- Linux x86-64 only; `os` file APIs are not available (TinyGo's baremetal
  `os`): use `pg.ReadFile`/`pg.WriteFile`.
