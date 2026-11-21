// Package main is a port of insert_username from PostgreSQL's contrib/spi:
// a BEFORE ROW trigger that stores the current user's name in a column.
package main

import (
	"gopgext/pg"
	"gopgext/trigger"
)

// InsertUsername sets the text column named by the trigger's argument to
// the current user's name.
//
//gopgext:function
func InsertUsername(t *trigger.Data) (*trigger.Row, error) {
	if !t.ForEachRow {
		return nil, pg.Errorf(pg.CodeInternalError, "insert_username: must be fired for row")
	}
	if t.When != trigger.Before {
		return nil, pg.Errorf(pg.CodeInternalError, "insert_username: must be fired before event")
	}
	if t.Op != trigger.Insert && t.Op != trigger.Update {
		return nil, pg.Errorf(pg.CodeInternalError, "insert_username: cannot process DELETE events")
	}
	if len(t.Args) != 1 {
		return nil, pg.Errorf(pg.CodeInternalError, "insert_username (%s): one argument was expected", t.Table)
	}
	name := t.Args[0]
	col, err := t.Column(name)
	if err != nil {
		return nil, pg.Errorf(trigger.CodeActionException, "%q has no attribute %q", t.Table, name)
	}
	if col.Type != 25 { // text
		return nil, pg.Errorf(trigger.CodeActionException, "attribute %q of %q must be type TEXT", name, t.Table)
	}
	var user string
	if err := pg.QueryRow("SELECT current_user::text").Scan(&user); err != nil {
		return nil, err
	}
	if err := trigger.Set(t.New, name, user); err != nil {
		return nil, err
	}
	return t.New, nil
}

func main() {}
