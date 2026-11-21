---
title: "gopgext/subscript"
linkTitle: "subscript"
description: "Subscripting (x[...]) for your data types."
weight: 15
---

API reference: [gopgext/subscript](/api/subscript/).

Subscripting for data types: `value['key']` and `SET col['key'] = x`, like
hstore and jsonb. The extension registers a `subscript.Type` under the
name of its SQL type, and gives the type this package's handler in `sql/`:

```go
func main() {
	subscript.Register("kv", subscript.Type{
		Subscript: "text", // subscripts are coerced to text
		Result:    "text", // kv['a'] is text
		Fetch: func(container []byte, subs [][]byte) ([]byte, bool, error) { ... }, // false: NULL
		Assign: func(container []byte, subs [][]byte, elem []byte) ([]byte, error) { ... }, // nil: NULL
	})
}
```

```sql
CREATE FUNCTION kv_subscript_handler(internal) RETURNS internal
	AS 'MODULE_PATHNAME', 'gopgext_subscript_handler' LANGUAGE C STRICT IMMUTABLE;
ALTER TYPE kv SET (SUBSCRIPT = kv_subscript_handler);
```

Containers, subscripts and elements are variable-size values (Go sees
their bytes). A NULL container or subscript reads as NULL without calling
Go. `Subscripts` sets how many subscripts are allowed (one by default);
slices (`a[1:2]`) aren't supported. `contrib/hstore` uses it.
