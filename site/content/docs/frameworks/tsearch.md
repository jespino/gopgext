---
title: "gopgext/tsearch"
linkTitle: "tsearch"
description: "Full text search dictionaries and parsers."
weight: 14
---

API reference: [gopgext/tsearch](/api/tsearch/).

Full text search dictionary templates and parsers in Go, and text search
services for Go code. A dictionary template is registered under the name of
its init function; the `Dictionary` it returns from the options of
`CREATE TEXT SEARCH DICTIONARY` is the dictionary's state (PostgreSQL caches
it per backend; it's dropped when PostgreSQL drops it, e.g. after `ALTER`):

```go
type intDict struct{ maxLen int }

func (d *intDict) Lexize(word string) ([]tsearch.Lexeme, error) {
	if len(word) > d.maxLen {
		return tsearch.StopWord, nil // drop it; nil: unknown word, for the next dictionary
	}
	return []tsearch.Lexeme{{Text: word}}, nil
}

func main() {
	tsearch.RegisterDictionary("dintdict_init", func(opts []tsearch.Option) (tsearch.Dictionary, error) {
		d := &intDict{maxLen: 6}
		for _, o := range opts { ... } // o.Name, o.Value, o.Text(), o.Bool() (defGetString/defGetBoolean)
		return d, nil
	})
}
```

```sql
CREATE FUNCTION dintdict_init(internal) RETURNS internal
	AS 'MODULE_PATHNAME', 'gopgext_tsearch_init' LANGUAGE C STRICT;
CREATE FUNCTION dintdict_lexize(internal, internal, internal, internal) RETURNS internal
	AS 'MODULE_PATHNAME', 'gopgext_tsearch_lexize' LANGUAGE C STRICT;
CREATE TEXT SEARCH TEMPLATE intdict_template (LEXIZE = dintdict_lexize, INIT = dintdict_init);
```

| API                                      | Purpose                                                    |
|------------------------------------------|------------------------------------------------------------|
| `RegisterDictionary(init, f)`            | a dictionary template (symbols `gopgext_tsearch_init`, `gopgext_tsearch_lexize`) |
| `Lexeme{Text, Variant, Flags}`, `Prefix`, `Filter`, `AddPos`, `StopWord` | `Lexize` results: variants (`nvariant`), `TSL_*` flags; nil is an unknown word, an empty slice a stop word |
| `RegisterParser(start, lextype, p)`      | a parser: `TokenTypes()` and `Parse(text)` returning a `Tokenizer` (`Next() (type, token)`, 0 at the end); symbols `gopgext_tsearch_start`, `_gettoken`, `_end`, `_lextype` (headlines: `prsd_headline`) |
| `ConfigFile(name, ext)`, `ReadConfigFile(path)` | data files in `$SHAREDIR/tsearch_data` (`get_tsearch_config_filename`, `tsearch_readline`: lines in the server encoding, a per-line error for characters it lacks, the "line N of configuration file" context while reading) |
| `Lexize(dict, word)`, `FindDictionary(name)` | call any dictionary (like `ts_lexize`); the dictionary `name` in the schema of the SQL function being called |
| `Lower(s)`, `CharLen(s)`                 | lower-casing as text search does (`str_tolower`, default collation); character length in the server encoding |

A project's `tsearch_data/` files (rules, synonyms, stop words) are
installed into `$SHAREDIR/tsearch_data` by `gopgext install`. Dictionaries
don't get the thesaurus' multi-word state (`DictSubState`). `examples/textsearch`
has a parser and a compound-word dictionary; `contrib/dict_int`,
`contrib/dict_xsyn` and `contrib/unaccent` are ports of the contrib
dictionaries.
