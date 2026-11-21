---
title: "Documentation"
description: "Everything about building PostgreSQL extensions in Go with gopgext."
weight: 1
cards: false
---

`gopgext` builds PostgreSQL extensions from plain Go code, using TinyGo. A
project is just Go files: functions marked with `//gopgext:function` become
SQL functions, and gopgext supplies everything else: the C glue, the
`CREATE FUNCTION` script and the control file. Go's standard library works
(`fmt`, `strings`, `regexp`, `encoding/json`, `crypto/*`, maps, generics,
`defer`/`recover`, iterators...), and Go code can run queries through SPI.

> [!WARNING]
> gopgext is **experimental**. Don't use it in production: a bug in the
> generated code or the Go runtime runs inside the server, where it can
> crash it or corrupt data, and the API and generated SQL may change
> without notice.

## Start here

- [Installation](/docs/install/): requirements and installing the `gopgext` command.
- [Tutorial](/docs/tutorial/): a "hello world" extension in ten minutes.

## Guides

- [Command line](/docs/commands/): `gopgext init`, `build` and `install`, and the layout of a project.
- [SQL functions](/docs/functions/): how Go functions become SQL functions.
- [Data types](/docs/types/): new SQL types backed by Go types.
- [The pg package](/docs/pg/): queries, errors, logging and settings.
- [Frameworks](/docs/frameworks/): triggers, aggregates, indexes, foreign data wrappers, hooks and more.
- [Examples and contrib ports](/docs/examples/): complete extensions to learn from.

## Reference

- [API reference](/api/): every exported identifier of the Go packages.
- [How it works](/docs/internals/): the build pipeline and the runtime model.
- [Limitations](/docs/limitations/): what isn't supported.
- [Development](/docs/development/): building gopgext itself and running its tests.
