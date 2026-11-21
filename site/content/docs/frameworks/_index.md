---
title: "Frameworks"
description: "Packages that let extensions plug into more of PostgreSQL than SQL functions."
weight: 7
---

Beyond SQL functions, gopgext has packages for the other kinds of code an
extension can add to PostgreSQL. They're frameworks, like gokm's:
importing one compiles its C glue into the library (and adds its linker
flags and install SQL), so extensions using them are pure Go.
