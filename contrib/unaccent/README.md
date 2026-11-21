# unaccent, in Go

A port of PostgreSQL's
[`contrib/unaccent`](https://www.postgresql.org/docs/current/unaccent.html)
(version 1.1) to Go, as a drop-in replacement: the `unaccent` text search
template and dictionary (a filtering dictionary), the `unaccent(text)` and
`unaccent(regdictionary, text)` functions, and the `unaccent.rules` file. It's
built on `gopgext/tsearch`.

| File                          | Ported from (PostgreSQL 18)                                   |
|-------------------------------|---------------------------------------------------------------|
| `main.go`                     | `unaccent.c`: the rules parser (quoted targets), longest-match lookup, the dictionary and `unaccent_dict` |
| `sql/unaccent.sql`            | `unaccent--1.1.sql`, with gopgext/tsearch's C symbols (and `unaccent_dict_default` for `unaccent(text)`) |
| `tsearch_data/unaccent.rules` | `unaccent.rules` (installed into `$SHAREDIR/tsearch_data`)    |

The original keeps the rules in a byte trie and replaces, at each position,
the longest source string found; this port keeps them in a map and tries the
prefixes from the longest source string's length down, which finds the same
match (the trie has a node for every prefix). `unaccent(text)` finds the
`unaccent` dictionary of its own schema whatever the `search_path`, as the
original (`tsearch.FindDictionary`). Lines of the rules file with characters
the server encoding lacks are skipped, as in the original.

```sh
gopgext install -version 1.1 -trusted contrib/unaccent
```

## Verification

**Upstream regression test**: PostgreSQL's `pg_regress` with the `sql/` and
`expected/` files of `contrib/unaccent`, unmodified, passes against this
port on PostgreSQL 18 (REL_18_STABLE) and 14 (REL_14_STABLE), in a UTF-8
database:

```sh
$(pg_config --pkglibdir)/pgxs/src/test/regress/pg_regress --temp-instance=/tmp/ua \
    --bindir=$(pg_config --bindir) --inputdir=<postgresql source>/contrib/unaccent \
    --encoding=UTF8 --no-locale unaccent
# ok 1         - unaccent
```

**Differential test**: `differential.sql` compares `ts_lexize` and
`unaccent()` with the C original (installed as `unaccent_c.so`, reading the
same rules file) on random strings of 1 to 7 characters over Latin, Greek,
Cyrillic, combining marks, letterlike symbols and punctuation, plus every
character of that alphabet: on PostgreSQL 18, 101,638 strings, 0
mismatches. PostgreSQL 14's C unaccent doesn't know the quoted targets of
version 18's rules syntax (it warns about those lines), and with 14's
rules file the only differences are the rules mapping to a lone `"`, which
18's syntax (this port) reads as an unfinished quoted string, as
PostgreSQL 18's C unaccent does.

`test.sql` covers the functions, the dictionary's own-schema lookup, rules
errors, and use as a filtering dictionary in a configuration.

## Differences

- White space in the rules file is ASCII white space (the original:
  `isspace()` of the server's locale, which can differ for bytes above 127
  in single-byte locales).
- The rules' syntax warnings have SQLSTATE 01000 (the original's are
  `config_file_error`); their text and context are the same.
