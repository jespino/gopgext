// Package main is a port of moddatetime from PostgreSQL's contrib/spi: a
// BEFORE UPDATE trigger that stores the modification time in a column.
package main

import (
	"gopgext/pg"
	"gopgext/trigger"
)

// Moddatetime sets the timestamp or timestamptz column named by the
// trigger's argument to the current time ('now': the transaction's start).
//
//gopgext:function
func Moddatetime(t *trigger.Data) (*trigger.Row, error) {
	if !t.ForEachRow {
		return nil, pg.Errorf(pg.CodeInternalError, "moddatetime: must be fired for row")
	}
	if t.When != trigger.Before {
		return nil, pg.Errorf(pg.CodeInternalError, "moddatetime: must be fired before event")
	}
	switch t.Op {
	case trigger.Insert:
		return nil, pg.Errorf(pg.CodeInternalError, "moddatetime: cannot process INSERT events")
	case trigger.Update:
	default:
		return nil, pg.Errorf(pg.CodeInternalError, "moddatetime: cannot process DELETE events")
	}
	if len(t.Args) != 1 {
		return nil, pg.Errorf(pg.CodeInternalError, "moddatetime (%s): A single argument was expected", t.Table)
	}
	name := t.Args[0]
	col, err := t.Column(name)
	if err != nil {
		return nil, pg.Errorf(trigger.CodeActionException, "%q has no attribute %q", t.Table, name)
	}
	if col.Type != 1114 && col.Type != 1184 { // timestamp, timestamptz
		return nil, pg.Errorf(trigger.CodeActionException, "attribute %q of %q must be type TIMESTAMP or TIMESTAMPTZ", name, t.Table)
	}
	now := "now"
	if err := t.New.SetText(name, &now); err != nil {
		return nil, err
	}
	return t.New, nil
}

func main() {}
