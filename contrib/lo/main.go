// Package main is a port of PostgreSQL's contrib/lo: the lo domain (a large
// object reference), lo_oid, and the lo_manage trigger, which unlinks the
// large objects a table stops referencing.
package main

import (
	"slices"

	"gopgext/pg"
	"gopgext/trigger"
)

// LoManage unlinks the large object in the column named by the trigger's
// argument when an UPDATE replaces it or a DELETE removes the row.
//
//gopgext:function
func LoManage(t *trigger.Data) error {
	if !t.ForEachRow {
		return pg.Errorf(pg.CodeInternalError, "%s: must be fired for row", t.Name)
	}
	if len(t.Args) == 0 {
		return pg.Errorf(pg.CodeInternalError, "%s: no column name provided in the trigger definition", t.Name)
	}
	col := t.Args[0]
	if _, err := t.Column(col); err != nil {
		return pg.Errorf(pg.CodeInternalError, "%s: column %q does not exist", t.Name, col)
	}
	// tg_trigtuple: the new row of an INSERT, else the old one.
	orig := t.Old
	if t.Op == trigger.Insert {
		orig = t.New
	}
	if t.Op == trigger.Update && slices.Contains(t.UpdatedColumns(), col) {
		o, _ := t.Old.Text(col)
		n, _ := t.New.Text(col)
		if o != nil && (n == nil || *o != *n) {
			if err := unlink(*o); err != nil {
				return err
			}
		}
	}
	if t.Op == trigger.Delete {
		if o, _ := orig.Text(col); o != nil {
			return unlink(*o)
		}
	}
	return nil
}

func unlink(oid string) error {
	_, err := pg.Exec("SELECT pg_catalog.lo_unlink($1::pg_catalog.oid)", oid)
	return err
}

func main() {}
