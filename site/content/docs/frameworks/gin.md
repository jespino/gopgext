---
title: "gopgext/gin"
linkTitle: "gin"
description: "GIN operator classes: inverted indexes over keys of values."
weight: 4
---

API reference: [gopgext/gin](/api/gin/).

GIN operator classes in Go: the inverted index of full text search,
pg_trgm, jsonb and arrays. The extension registers a `gin.OpClass` under
the name of its operator class, and declares the support functions (all
with this package's C symbols) and the operator class in `sql/`:

```go
func main() {
	gin.Register("trgm_gin_ops", gin.OpClass{
		ExtractValue:  func(value []byte) ([][]byte, error) { ... },                // value -> keys
		ExtractQuery:  func(query []byte, strategy int) (gin.Query, error) { ... }, // keys to look for, search mode
		Consistent:    func(check []bool, strategy int, query []byte) (match, recheck bool) { ... },
		TriConsistent: func(check []gin.Ternary, strategy int, query []byte) gin.Ternary { ... }, // optional
		// Compare, ComparePartial (prefix-style partial matches): optional
	})
}
```

```sql
CREATE FUNCTION trgm_gin_extract_value(text, internal, internal)
	RETURNS internal AS 'MODULE_PATHNAME', 'gopgext_gin_extract_value' LANGUAGE C STRICT;
-- ... extract_query, consistent, tri_consistent, compare, compare_partial: see gopgext/gin
CREATE OPERATOR CLASS trgm_gin_ops FOR TYPE text USING gin AS
	OPERATOR 1 % (text, text), OPERATOR 3 ~~ (text, text),
	FUNCTION 2 trgm_gin_extract_value (text, internal, internal), ...
	STORAGE bytea;
```

Values, queries and keys are variable-size values (Go sees their bytes);
keys are usually stored as `bytea` and ordered by byte (or by `Compare`).
Lossy keys (trigrams for LIKE) set `recheck`; queries without useful keys
use `gin.All`; partial-match keys (prefixes) use `ComparePartial`.
`examples/trgm` is a complete example (a small pg_trgm).
