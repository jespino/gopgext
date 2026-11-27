---
title: "How it works"
description: "The build pipeline, the runtime model, memory, calls into Go and error handling."
weight: 9
---

```
project *.go + generated wrappers ──tinygo (pgext target, runtime overlay)──▶ go.o
go.o ──objcopy (strip debug, keep only the entry points global)──▶ ext/go.o
ext/{runtime.c, spi.c, functions.c, go.o} ──PGXS──▶ <name>.so (exports: Pg_magic_func, _PG_init, functions)
signatures ──▶ <name>.control, <name>--<version>.sql
```

- **Target and runtime.** A custom TinyGo target (hosted Linux x86-64, PIC)
  with a `pgext` runtime layer (`overlay/src/runtime/runtime_pgext.go`) in
  place of TinyGo's `runtime_unix.go`, which assumes the program owns the
  process: output goes to the server log, time comes from `clock_gettime`,
  sleeping is `pg_usleep`, signals are left to PostgreSQL, and `crypto/rand`
  reads PostgreSQL's `pg_strong_random` (OpenSSL's `RAND_bytes` or
  `/dev/urandom`). `syscall` and `os` are TinyGo's Linux ones, calling the
  backend's libc. A backend is a single-threaded process, so there is no
  scheduler: Go code runs on the backend's own stack (which keeps
  `check_stack_depth()` and Go → SPI → Go recursion working), and only while
  PostgreSQL is calling into the extension.
- **Memory.** TinyGo's conservative collector, over a heap reserved with
  `mmap` at load (`-heap-max` of address space; only touched pages use
  memory) that grows on demand. Each backend has its own heap; it scans the
  stack between the current position and the outermost entry from C.
  Results are copied into PostgreSQL memory (`palloc`) before returning.
- **Calls.** For every SQL function gopgext generates a V1 C function and a
  Go wrapper. `gopgext_invoke` (runtime.c) enters Go, and once Go has
  returned it raises the error Go reported, if any: a PostgreSQL error never
  `longjmp`s through Go frames. C helpers that may raise one catch it
  (`GOPGEXT_GUARD`); if it can't be handled in Go (say, a corrupt toast
  pointer), Go unwinds with a panic and the original error is re-thrown,
  and queries are refused meanwhile. `gopgext_invoke` also catches errors
  as a last resort, should a helper ever let one through: it resets the Go
  runtime's state, logs a WARNING ("a PostgreSQL error unwound Go frames")
  and re-throws the error; `make pgtest` fails if that WARNING appears.
- **Symbols.** All Go symbols are local to the library and it exports only
  what PostgreSQL looks up, so several Go extensions can be loaded in one
  backend.
- **Fatal errors.** If the Go runtime can't continue (heap exhausted beyond
  `-heap-max`), the backend exits with `FATAL` and the message goes to the
  server log. The session ends; the server and other sessions are unaffected.
