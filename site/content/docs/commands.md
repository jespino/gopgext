---
title: "Command line"
description: "gopgext init, build and install, their flags, and what a project can contain."
weight: 3
---

```
gopgext init [-module path] [dir]
    Creates go.mod and main.go in dir (default "."). The extension name is the
    last element of the module path. Also writes a go.work so editors resolve
    gopgext's packages (machine-specific: don't commit it).

gopgext build [flags] [dir]
gopgext install [flags] [dir]
    -pg-config path    PostgreSQL to build for (default: $PG_CONFIG, else pg_config in PATH)
    -o dir             where to write the .so, .control and .sql (default: the project dir)
    -name name         extension name (letters, digits, _ and -, as in uuid-ossp)
    -version v         extension version (default 1.0)
    -comment text      control file comment
    -trusted           mark the extension trusted (installable by non-superusers)
    -requires ext,...  extensions this one depends on (control file requires;
                       CREATE EXTENSION ... CASCADE installs them)
    -heap-size size    initial size of each backend's Go heap (default 1M)
    -heap-max size     limit the Go heap can grow to (default 1G of reserved address space)
    -v                 show the commands run and their output
```

`install` builds, then copies the library to `pg_config --pkglibdir` and the
control and SQL files to `pg_config --sharedir`/extension (use `sudo` for a
system PostgreSQL). Everything but the outputs goes into `~/.cache/gopgext`
(or `$GOPGEXTCACHE`). A library is built for one PostgreSQL major version,
like any C extension.

A project can also have `sql/*.sql` files, appended to the install script
(tables, views, aggregates built on the Go functions...), and `csrc/*.c`
files compiled into the library, and `tsearch_data/` files that `install`
copies into `pg_config --sharedir`/tsearch_data (text search dictionaries'
data files, see gopgext/tsearch). `csrc/` files' `PG_FUNCTION_INFO_V1` functions are
exported (so `sql/` files can declare them, e.g. a foreign data wrapper's
handler), and Go can call their other functions through body-less
`//export` declarations. `//gopgext:ldflags -lcurl` in a Go file adds
linker flags (system libraries the C code uses).
`//gopgext:alias name=target` exports the library's V1 function `target`
(e.g. a framework's `gopgext_gist_consistent`) under another C symbol too,
so install scripts written for a C extension (`AS 'MODULE_PATHNAME'`, with
the function's name as symbol), and databases created with them, find it. `../victoria_fdw` is a
complete example: a foreign data wrapper whose planner glue is C and whose
requests and parsing are Go.
