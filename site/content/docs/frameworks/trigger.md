---
title: "gopgext/trigger"
linkTitle: "trigger"
description: "Trigger and event trigger functions."
weight: 13
---

API reference: [gopgext/trigger](/api/trigger/).

Trigger and event trigger functions. A `//gopgext:function` whose only
parameter is a `*trigger.Data` is a trigger function (`RETURNS trigger`,
no SQL arguments); its result is the row the operation goes on with:

```go
//gopgext:function
func Stamp(t *trigger.Data) (*trigger.Row, error) {
	if t.When != trigger.Before || t.New == nil || len(t.Args) != 1 {
		return nil, pg.Errorf(trigger.CodeProtocolViolated, "stamp: BEFORE INSERT OR UPDATE FOR EACH ROW, with a column")
	}
	if err := trigger.Set(t.New, t.Args[0], time.Now()); err != nil { // a timestamptz column
		return nil, err
	}
	return t.New, nil // nil: skip the row
}
```

```sql
CREATE TRIGGER stamp BEFORE INSERT OR UPDATE ON items
	FOR EACH ROW EXECUTE FUNCTION stamp('changed_at');
```

| API                                       | Purpose                                                    |
|-------------------------------------------|------------------------------------------------------------|
| `Data.When`, `Op`, `ForEachRow`           | `Before`/`After`/`InsteadOf`, `Insert`/`Update`/`Delete`/`Truncate`, row or statement level |
| `Data.Name`, `Args`                       | the trigger's name and arguments (`EXECUTE FUNCTION f('a', 'b')`) |
| `Data.Table`, `Schema`, `TableOID`        | the table or view                                          |
| `Data.Old`, `Data.New`                    | the rows before and after the change (row-level; nil when absent) |
| `Data.Columns()`, `Column(name)`          | the columns: name, number, type (and a domain's base type), typmod, type name |
| `Data.UpdatedColumns()`, `Changed(name)`  | the columns an UPDATE assigns; whether a value changed      |
| `Row.Any`, `Text`, `IsNull`; `trigger.Get[T]` | a column as a `pg.AnyElement`, its text form, or a Go value of its type |
| `Row.SetText`, `SetAny`, `SetNull`; `trigger.Set` | change a column of the returned row (`SetText` applies typmods and domain checks) |
| `*trigger.Event`: `Event`, `Tag`          | an event trigger's event (`ddl_command_start`, ...) and command tag |

A function without a row result lets the operation go on unchanged (New,
or Old for DELETE); AFTER and statement-level triggers' results are
ignored, and an INSTEAD OF trigger returns nil when it did nothing. Rows
are only valid during the call: the tuples stay in C, Go gets their
values (`pg.AnyElement` values can be passed to `pg.Exec`/`Query` as is).
A function whose only parameter is a `*trigger.Event` is an event trigger
function, returning nothing or an error. `examples/triggers` shows them all;
`contrib/tcn`, `lo`, `autoinc`, `insert_username`, `moddatetime` and
`refint` are ports of PostgreSQL's trigger modules.
