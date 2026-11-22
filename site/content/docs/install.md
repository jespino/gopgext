---
title: "Installation"
description: "What you need to build extensions with gopgext, and how to install it."
weight: 1
---

## Requirements

- Linux x86-64
- Go
- [TinyGo](https://tinygo.org/getting-started/install/), tested with 0.43.0.
  Older releases fail early.
- PostgreSQL 14 or later, with its server development files: `pg_config`,
  the headers and PGXS (`postgresql-server-dev-NN` on Debian/Ubuntu,
  `postgresql-server-devel` on Fedora). Tested with 14, 17, 18 and 19.
- `make`, a C compiler and binutils (`objcopy`)

## Install gopgext

```sh
go install github.com/jespino/gopgext@latest
```

`go install` puts `gopgext` in `$(go env GOPATH)/bin`; make sure it's in
your `PATH`. Then follow the [tutorial](/docs/tutorial/).

To build from source instead:

```sh
git clone https://github.com/jespino/gopgext
cd gopgext
go install .          # or: make  (builds ./gopgext)
```

A library is built for one PostgreSQL major version, like any C extension:
build it again for each version you install it into, with `-pg-config`.
