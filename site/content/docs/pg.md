---
title: "The pg package"
linkTitle: "Package pg"
description: "Queries through SPI, errors with SQLSTATE codes, logging, settings, interrupts and more."
weight: 6
---

API reference: [gopgext/pg](/api/pg/).

| API                                                  | Purpose                                         |
|------------------------------------------------------|-------------------------------------------------|
| `Exec(sql, args...) (int64, error)`                  | run a statement ($1, $2, ... parameters)        |
| `Query(sql, args...) (*Rows, error)`                 | rows: `Next`, `Scan`, `Values`, `Columns`       |
| `QueryRow(sql, args...).Scan(&a, &b)`                | one row, or `ErrNoRows`                         |
| `Prepare(sql, types...) (*Stmt, error)`             | a prepared statement: `Stmt.Exec`, `Query`, `QueryRow`, `Close` |
| `Error`, `Errorf(code, format, ...)`, `Code...`      | PostgreSQL errors with SQLSTATE codes           |
| `Debug`, `Log`, `Info`, `Notice`, `Warning` (+`f`)   | `ereport` at that level (`Notice` = `RAISE NOTICE`) |
| `Message{Level, Text, Detail, Hint, HideStatement, HideContext}.Report()` | `ereport` at any level from `LevelDebug5` to `LevelWarning`, with a detail and hint, optionally without the statement (`errhidestmt`) or context in the server log |
| `CheckInterrupts() error`                            | has the query been canceled? (call in long loops) |
| `SetConfig(name, value, local)`                      | change a setting, like `SET` or `set_config()`  |
| `CString`, `Oid`, `Varlena`, `StringInfo`, `Internal`, `UUID`, `Record` | SQL types of function signatures (see above)  |
| `ResultColumns() ([]Column, error)`                  | the columns a function returning `record` must return |
| `FormatType(typ, typmod)`, `Rows.ColumnTypmods()`    | a type's SQL name (`character varying(10)`); type modifiers of query results |
| `ParseUUID(s)`, `UUID.String()`                      | the text form of a `uuid` (queries take and return uuids as text) |
| `NewBoolSetting`, `NewIntSetting`, `NewFloatSetting`, `NewStringSetting`, `NewEnumSetting` | custom settings (GUCs) `<extension>.<name>` |
| `ExtensionName()`                                    | the extension's name                            |
| `Preloading()`                                       | is the library being loaded by `shared_preload_libraries`? (in `main`) |
| `UserID()`, `DatabaseID()`, `HasPrivilegesOf(role)`  | current user and database; role membership (`has_privs_of_role`) |
| `ReadFile`, `WriteFile`, `RemoveFile`                | server files (relative to the data directory; `WriteFile` is atomic and durable) |
| `Notify(channel, payload)`                           | `NOTIFY` (`pg_notify()`): delivered when the transaction commits |
| `Collation()`, `DefaultCollation`                    | the input collation of the call (`PG_GET_COLLATION()`, 0 if none) |
| `ToLower(s, coll)`, `ToUpper(s, coll)`, `CompareStrings(a, b, coll)` | `lower()`/`upper()` and text comparison with a collation (`str_tolower`, `varstr_cmp`) |
| `HashBytes(b)`, `HashBytesExtended(b, seed)`         | PostgreSQL's `hash_any`/`hash_any_extended` (hash operator classes) |
| `ArgType(i)`                                         | the actual type of argument i, even when it is NULL (`get_fn_expr_argtype`) |
| `AnyElement.Bytes()`, `FromBytes(typ, b)`            | contents of a variable-length value (text, json...), and a value from bytes |
| `Input(typ, typmod, s)`                              | the input function as for a column (typmod; `nil` checks a domain's constraints on NULL) |
| `IsComposite`, `Columns`, `AnyElement.RowType()`, `AnyElement.Fields()`, `NewRecord` | composite values of any row type: `record` and `anyelement` arguments (columns by name, values, building rows; domains over composites are checked) |
| `AnyArray.LowerBounds()`, `NewAnyArrayDims(elem, xs, dims, lbounds)` | multi-dimensional arrays with their bounds |
| `FormatFloat8(x)`                                    | `x` as `float8` outputs it (shortest exact form, or per `extra_float_digits`): for types that print floats |
| `LookupRelation(id) (*Relation, error)`              | a table's name, schema, owner, kind, columns, primary key, triggers, row-level security (catalog caches: works in planner callbacks) |
| `InvalidateRelation(id) bool`                        | make every backend replan statements on a table and reset caches of it (`CacheInvalidateRelcacheByRelid`) |
| `RunAs(role, f) error`                               | run `f` as another role, in a security-restricted operation with a safe `search_path` (like VACUUM runs a table owner's code) |
| `Image(v)`, `FromImage(typ, image)`                  | the binary image of a value (what PostgreSQL keeps in memory), and a value from one |

Queries go through SPI. Each statement runs in its own subtransaction, like
`plpy.execute` in PL/Python: if it fails, its effects are rolled back and the
error is returned, so Go code can handle it and go on (like an `EXCEPTION`
block). Returning that error unchanged re-raises the original PostgreSQL
error with all its fields (position, context...). Parameters can also be
`pg.AnyElement` values (e.g. a trigger's row values), passed with their own
type. Results are copied into Go
values: `Scan` converts between numeric types, `numeric`, `json`,
timestamps and other types come as their text form, and `**T` destinations
accept NULL.

```go
var lowStock = pg.NewIntSetting("low_stock", 5, 0, 1000000, "Quantity below which items are low on stock.")

//gopgext:function
func Restock(name string, qty int32) (string, error) {
	_, err := pg.Exec("INSERT INTO items (name, qty) VALUES ($1, $2)", name, qty)
	var pe *pg.Error
	if errors.As(err, &pe) && pe.Code == pg.CodeUniqueViolation {
		_, err = pg.Exec("UPDATE items SET qty = qty + $2 WHERE name = $1", name, qty)
		return "restocked " + name, err
	}
	return "new item " + name, err
}
```

Settings must be defined while the library loads (package-level variables or
`main`). Avoid SQL keywords as setting names (`SET ext.verbose` is a syntax
error). Settings are read with `Get()`, which sees the current value of
`SET`, `ALTER SYSTEM`, ... Options choose who can change them and when:
`pg.Superuser` (`PGC_SUSET`), `pg.Sighup` (configuration file, on reload)
and `pg.Postmaster` (server start: e.g. sizes of shared memory). Enum
settings take one of a list of names:
`pg.NewEnumSetting("track", "top", []string{"none", "top", "all"}, "...")`.
Integer and floating-point settings can have a unit (`pg.UnitBytes`,
`UnitKilobytes`, `UnitMilliseconds`, `UnitSeconds`, `UnitMinutes`): values
are then given and shown with one (`SET ext.timeout = '2s'`).

## Tables and roles

`LookupRelation` describes a relation as the catalog has it in the current
transaction. It reads the catalog caches, so it works where SQL can't run,
like planner and executor callbacks. The relation stays locked (`ACCESS
SHARE`) until the transaction ends.

```go
rel, err := pg.LookupRelation(oid) // nil, nil: no such relation
if rel != nil {
	for _, num := range rel.PrimaryKey {
		c := rel.ColumnNum(num) // Name, Type, TypeName ("character varying(10)"), NotNull...
		_ = c
	}
	if t := rel.Trigger("audit"); t != nil && t.Fires() { // Args, Row, Before, Update, Enabled...
		// ...
	}
}
```

`RunAs(role, f)` runs `f` as `role` the way maintenance commands run the
code of a table's owner: in a security-restricted operation (no `SET
ROLE`, no temporary objects) with `search_path` set to
`pg_catalog, pg_temp`. Settings `f` changes are reverted when it returns.
Use it to write to users' tables from a background worker, so that their
triggers, defaults and index expressions don't run with the worker's
privileges. It doesn't check privileges itself.

`InvalidateRelation(id)` makes every backend forget what it cached about a
table when the transaction commits (this backend: at once). Their plans
using it are made again, and so are extensions' caches of it, such as the
policies of [gopgext/intercept](/docs/frameworks/intercept/).

## Binary images of values

`Image(v)` returns the bytes PostgreSQL keeps in memory for a value: the
8-byte Datum of types passed by value (integers, timestamps...), the bytes
it points to for fixed-length types, and the detoasted varlena of
variable-length ones. `FromImage(typ, image)` makes the value again. Images
let a value be kept, for example in shared memory, and used in another
backend without going through text, so settings like `DateStyle` don't
matter. They're only meaningful to the same server build. `FromImage`
checks the image's size but trusts its contents, so only pass it images
`Image` returned.

```go
img, _ := pg.Image(v)                // copy it to keep it past the call
a, _ := pg.FromImage(v.Type(), img)  // in another call or backend
s := a.String()
```

## Prepared statements

`Prepare` parses and plans a statement once; `Stmt.Exec`, `Query` and
`QueryRow` then run it with new arguments, in any transaction of the
session, without parsing or planning again (PostgreSQL replans it when the
objects it uses change). Parameter types are given as OIDs (`pg.OidText`,
`pg.OidInt4`, ...). A text argument of a parameter of another type is
converted with the type's input function; other arguments must have the
parameter's type. Each run is a subtransaction, like `pg.Exec`.

```go
var setQty *pg.Stmt // prepared once per session

func SetQty(name string, qty int32) (int64, error) {
	if setQty == nil {
		s, err := pg.Prepare("UPDATE items SET qty = $2 WHERE name = $1", pg.OidText, pg.OidInt4)
		if err != nil {
			return 0, err
		}
		setQty = s
	}
	return setQty.Exec(name, qty)
}
```
