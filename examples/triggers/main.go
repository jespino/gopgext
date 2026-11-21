// Package main shows trigger and event trigger functions with
// gopgext/trigger.
package main

import (
	"fmt"
	"strings"
	"time"

	"gopgext/pg"
	"gopgext/trigger"
)

// NormalizeRow trims and lower-cases the text columns named by the trigger's
// arguments, counts updates in "version" (bigint) if there is one, and
// skips rows whose "name" is "skip".
//
//gopgext:function
func NormalizeRow(t *trigger.Data) (*trigger.Row, error) {
	if t.When != trigger.Before || t.New == nil {
		return nil, pg.Errorf(trigger.CodeProtocolViolated, "normalize_row: must be fired BEFORE INSERT or UPDATE FOR EACH ROW")
	}
	for _, col := range t.Args {
		s, err := t.New.Text(col)
		if err != nil {
			return nil, err
		}
		if s != nil {
			v := strings.ToLower(strings.TrimSpace(*s))
			if err := t.New.SetText(col, &v); err != nil { // typmods and domains apply
				return nil, err
			}
		}
	}
	if name, err := trigger.Get[string](t.New, "name"); err == nil && name != nil && *name == "skip" {
		return nil, nil // the row isn't inserted or updated
	}
	if _, err := t.Column("version"); err == nil {
		var v int64
		if t.Old != nil {
			old, err := trigger.Get[int64](t.Old, "version")
			if err != nil {
				return nil, err
			}
			if old != nil {
				v = *old
			}
		}
		if err := trigger.Set(t.New, "version", v+1); err != nil {
			return nil, err
		}
	}
	return t.New, nil
}

// Audit records every event in the audit table: row-level ones with the
// changed columns, statement-level ones (and TRUNCATE) with a NULL row.
//
//gopgext:function
func Audit(t *trigger.Data) error {
	var detail []string
	switch {
	case t.Op == trigger.Update && t.ForEachRow:
		for _, c := range t.Columns() {
			changed, err := t.Changed(c.Name)
			if err != nil {
				return err
			}
			if changed {
				detail = append(detail, c.Name)
			}
		}
		detail = append(detail, "set:"+strings.Join(t.UpdatedColumns(), "+"))
	case t.ForEachRow:
		row := t.New
		if row == nil {
			row = t.Old
		}
		for _, c := range t.Columns() {
			s, err := row.Text(c.Name)
			if err != nil {
				return err
			}
			v := "NULL"
			if s != nil {
				v = *s
			}
			detail = append(detail, fmt.Sprintf("%s:%s=%s", c.Name, c.TypeName, v))
		}
	}
	level := "statement"
	if t.ForEachRow {
		level = "row"
	}
	_, err := pg.Exec("INSERT INTO audit (event, tbl, args, detail) VALUES ($1, $2, $3, $4)",
		fmt.Sprintf("%s %s %s %s", t.Name, t.When, t.Op, level), t.Schema+"."+t.Table,
		strings.Join(t.Args, ","), strings.Join(detail, " "))
	return err
}

// Revert keeps the old row of an UPDATE when the trigger's argument
// column is "locked" in it.
//
//gopgext:function
func Revert(t *trigger.Data) (*trigger.Row, error) {
	if t.Op != trigger.Update {
		return t.New, nil
	}
	s, err := t.Old.Text(t.Args[0])
	if err != nil {
		return nil, err
	}
	if s != nil && *s == "locked" {
		return t.Old, nil
	}
	return t.New, nil
}

// ViewWrite implements INSERT, UPDATE and DELETE on a view over items
// (INSTEAD OF): any value is copied with SetAny/Any.
//
//gopgext:function
func ViewWrite(t *trigger.Data) (*trigger.Row, error) {
	switch t.Op {
	case trigger.Insert:
		id, _ := t.New.Any("id")
		name, _ := t.New.Any("name")
		if _, err := pg.Exec("INSERT INTO items (id, name) VALUES ($1, $2)", id, name); err != nil {
			return nil, err
		}
		return t.New, nil
	case trigger.Update:
		id, _ := t.Old.Any("id")
		name, _ := t.New.Any("name")
		n, err := pg.Exec("UPDATE items SET name = $2 WHERE id = $1", id, name)
		if err != nil || n == 0 {
			return nil, err // nothing done
		}
		return t.New, nil
	default:
		id, _ := t.Old.Any("id")
		n, err := pg.Exec("DELETE FROM items WHERE id = $1", id)
		if err != nil || n == 0 {
			return nil, err
		}
		return t.Old, nil
	}
}

// Misuse checks the errors of the row API: the trigger's argument names
// the check.
//
//gopgext:function
func Misuse(t *trigger.Data) (*trigger.Row, error) {
	switch t.Args[0] {
	case "column":
		_, err := t.New.Text("no_such_column")
		return nil, err
	case "type":
		_, err := trigger.Get[int32](t.New, "name")
		return nil, err
	case "settype":
		return nil, trigger.Set(t.New, "id", "x")
	case "anytype":
		v, _ := t.New.Any("name")
		return nil, t.New.SetAny("id", v)
	case "input":
		s := "not a number"
		return nil, t.New.SetText("id", &s)
	case "time":
		if err := trigger.Set(t.New, "at", time.Unix(0, 0)); err != nil {
			return nil, err
		}
		return t.New, t.New.SetNull("name")
	case "panic":
		var r *trigger.Row
		_, err := r.Text("x")
		return nil, err
	}
	return nil, pg.Errorf(pg.CodeInvalidParameterValue, "unknown check %q", t.Args[0])
}

// LogDDL records DDL commands (an event trigger).
//
//gopgext:function
func LogDDL(e *trigger.Event) error {
	if e.Tag == "CREATE SEQUENCE" {
		return pg.Errorf(pg.CodeInsufficientPrivilege, "%s is not allowed (%s)", e.Tag, e.Event)
	}
	_, err := pg.Exec("INSERT INTO audit (event, tbl, args, detail) VALUES ($1, '', '', $2)", e.Event, e.Tag)
	return err
}

func main() {}
