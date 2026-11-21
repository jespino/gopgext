// Package main is a port of autoinc from PostgreSQL's contrib/spi: a BEFORE
// ROW trigger that fills integer columns from sequences.
package main

import (
	"gopgext/pg"
	"gopgext/trigger"
)

// Autoinc takes pairs of arguments (column, sequence): a column that is
// NULL or 0 gets the sequence's next value (skipping 0).
//
//gopgext:function
func Autoinc(t *trigger.Data) (*trigger.Row, error) {
	if !t.ForEachRow {
		return nil, pg.Errorf(pg.CodeInternalError, "must be fired for row")
	}
	if t.When != trigger.Before {
		return nil, pg.Errorf(pg.CodeInternalError, "must be fired before event")
	}
	if t.Op != trigger.Insert && t.Op != trigger.Update {
		return nil, pg.Errorf(pg.CodeInternalError, "cannot process DELETE events")
	}
	row := t.New
	if len(t.Args) == 0 || len(t.Args)%2 != 0 {
		return nil, pg.Errorf(pg.CodeInternalError, "autoinc (%s): even number gt 0 of arguments was expected", t.Table)
	}
	for i := 0; i < len(t.Args); i += 2 {
		name, seq := t.Args[i], t.Args[i+1]
		col, err := t.Column(name)
		if err != nil {
			return nil, pg.Errorf(trigger.CodeActionException, "%q has no attribute %q", t.Table, name)
		}
		if col.Type != 23 { // int4
			return nil, pg.Errorf(trigger.CodeActionException, "attribute %q of %q must be type INT4", name, t.Table)
		}
		v, err := trigger.Get[int32](row, name)
		if err != nil {
			return nil, err
		}
		if v != nil && *v != 0 {
			continue
		}
		next, err := nextval(seq)
		if err == nil && next == 0 {
			next, err = nextval(seq)
		}
		if err != nil {
			return nil, err
		}
		if err := trigger.Set(row, name, next); err != nil {
			return nil, err
		}
	}
	return row, nil
}

// nextval returns the next value of sequence seq (nextval(text): a name,
// maybe qualified), coerced to int4 like the original.
func nextval(seq string) (int32, error) {
	var n int64
	err := pg.QueryRow("SELECT pg_catalog.nextval($1::text)", seq).Scan(&n)
	return int32(n), err
}

func main() {}
