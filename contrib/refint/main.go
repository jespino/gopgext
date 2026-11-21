// Package main is a port of refint from PostgreSQL's contrib/spi:
// referential integrity with triggers (obsolete: use foreign keys).
package main

import (
	"fmt"
	"strconv"
	"strings"

	"gopgext/pg"
	"gopgext/trigger"
)

// CheckPrimaryKey checks that the key of an inserted or updated row exists
// in the referenced table. Arguments: the key columns of the trigger's
// table, the referenced table, and its key columns:
// check_primary_key('fkey1', 'fkey2', 'pkeys', 'pkey1', 'pkey2').
//
//gopgext:function
func CheckPrimaryKey(t *trigger.Data) (*trigger.Row, error) {
	const fn = "check_primary_key"
	if !t.ForEachRow {
		return nil, pg.Errorf(pg.CodeInternalError, "%s: must be fired for row", fn)
	}
	if t.When != trigger.After {
		return nil, pg.Errorf(pg.CodeInternalError, "%s: must be fired by AFTER trigger", fn)
	}
	if t.Op == trigger.Delete {
		return nil, pg.Errorf(pg.CodeInternalError, "%s: cannot process DELETE events", fn)
	}
	row := t.New
	if len(t.Args)%2 != 1 {
		return nil, pg.Errorf(pg.CodeInternalError, "%s: odd number of arguments should be specified", fn)
	}
	nkeys := len(t.Args) / 2
	relname := t.Args[nkeys]
	kvals, err := keyValues(t, row, t.Args[:nkeys])
	if kvals == nil || err != nil {
		return row, err // a NULL key references nothing
	}
	// SELECT 1 FROM _referenced_relation_ WHERE Pkey1 = $1 [AND Pkey2 = $2 [...]]
	sql := "select 1 from " + relname + " where " + keyCond(t.Args[nkeys+1:])
	err = pg.QueryRow(sql, kvals...).Scan(new(int32))
	if err == pg.ErrNoRows {
		return nil, &pg.Error{Code: trigger.CodeActionException, Message: "tuple references non-existent key",
			Detail: fmt.Sprintf("Trigger %q found tuple referencing non-existent key in %q.", t.Name, relname)}
	}
	if err != nil {
		return nil, err
	}
	return row, nil
}

// CheckForeignKey checks that the key of a deleted or updated row isn't
// referenced by rows of other tables, or deletes, updates or nulls those.
// Arguments: the number of referencing tables, the action ('restrict',
// 'cascade' or 'setnull'), the key columns of the trigger's table, then
// each referencing table and its key columns:
// check_foreign_key(2, 'cascade', 'pkey1', 'pkey2', 'fkeys', 'fkey1',
// 'fkey2', 'fkeys2', 'fkey21', 'fkey22').
//
//gopgext:function
func CheckForeignKey(t *trigger.Data) (*trigger.Row, error) {
	const fn = "check_foreign_key"
	if !t.ForEachRow {
		return nil, pg.Errorf(pg.CodeInternalError, "%s: must be fired for row", fn)
	}
	if t.Op == trigger.Insert {
		return nil, pg.Errorf(pg.CodeInternalError, "%s: cannot process INSERT events", fn)
	}
	if t.When != trigger.After {
		return nil, pg.Errorf(pg.CodeInternalError, "%s: must be fired by AFTER trigger", fn)
	}
	ret := t.Old
	if t.Op == trigger.Update {
		ret = t.New
	}
	args := t.Args
	if len(args) < 5 {
		return nil, pg.Errorf(pg.CodeInternalError, "%s: too short %d (< 5) list of arguments", fn, len(args))
	}
	nrefs, err := strconv.ParseInt(strings.TrimSpace(args[0]), 10, 32)
	if err != nil {
		return nil, pg.Errorf(pg.CodeInvalidTextRepresentation, "invalid input syntax for type %s: %q", "integer", args[0])
	}
	if nrefs < 1 {
		return nil, pg.Errorf(pg.CodeInternalError, "%s: %d (< 1) number of references specified", fn, nrefs)
	}
	var action byte
	if args[1] != "" {
		action = args[1][0] | 0x20 // tolower for letters
	}
	if action != 'r' && action != 'c' && action != 's' {
		return nil, pg.Errorf(pg.CodeInternalError, "%s: invalid action %s", fn, args[1])
	}
	args = args[2:]
	n := int(nrefs)
	nkeys := (len(args) - n) / (n + 1)
	if nkeys <= 0 || len(args) != n+nkeys*(n+1) {
		return nil, pg.Errorf(pg.CodeInternalError, "%s: invalid number of arguments %d for %d references", fn, len(args)+2, n)
	}
	keys := args[:nkeys]
	kvals, err := keyValues(t, t.Old, keys)
	if kvals == nil || err != nil {
		return ret, err
	}
	// In an UPDATE that keeps the key (compared as text, like the
	// original), there's nothing to do.
	isEqual := true
	if t.Op == trigger.Update {
		for _, k := range keys {
			o, _ := t.Old.Text(k)
			nv, _ := t.New.Text(k)
			if nv == nil || *o != *nv {
				isEqual = false
			}
		}
	}

	refs := args[nkeys:]
	var queries []string
	for r := 0; r < n; r++ {
		ref := refs[r*(nkeys+1) : (r+1)*(nkeys+1)]
		relname, fkeys := ref[0], ref[1:]
		var sql string
		switch {
		case action == 'r':
			// SELECT 1 FROM _referencing_relation_ WHERE Fkey1 = $1 [AND Fkey2 = $2 [...]]
			sql = "select 1 from " + relname + " where "
		case action == 'c' && t.Op == trigger.Update:
			// UPDATE _referencing_relation_ SET Fkey1 = new value [, ...] WHERE ...
			sql = "update " + relname + " set "
			for k, fk := range fkeys {
				nv, _ := t.New.Text(keys[k])
				lit := "NULL"
				if nv != nil {
					lit = quoteLiteral(*nv)
				}
				sql += " " + fk + " = " + lit + " "
				if k < nkeys-1 {
					sql += ", "
				}
			}
			sql += " where "
		case action == 'c':
			sql = "delete from " + relname + " where "
		default:
			// UPDATE _referencing_relation_ SET Fkey1 = null [, ...] WHERE ...
			sql = "update " + relname + " set "
			for k, fk := range fkeys {
				sql += fk + " = null"
				if k < nkeys-1 {
					sql += ", "
				}
			}
			sql += " where "
		}
		queries = append(queries, sql+keyCond(fkeys))
	}
	if t.Op == trigger.Update && isEqual {
		return ret, nil
	}

	for r, sql := range queries {
		relname := refs[r*(nkeys+1)]
		if action == 'r' {
			err := pg.QueryRow(sql, kvals...).Scan(new(int32))
			if err == nil {
				return nil, pg.Errorf(trigger.CodeActionException, "%q: tuple is referenced in %q", t.Name, relname)
			}
			if err != pg.ErrNoRows {
				return nil, err
			}
			continue
		}
		count, err := pg.Exec(sql, kvals...)
		if err != nil {
			return nil, err
		}
		op := "set to null"
		if action == 'c' {
			op = "deleted"
			if t.Op == trigger.Update {
				op = "updated"
			}
		}
		// The original's REFINT_VERBOSE, on in its build.
		pg.Noticef("%s: %d tuple(s) of %s are %s", t.Name, count, relname, op)
	}
	return ret, nil
}

// keyValues returns the values of the key columns of row, as parameters
// of their columns' types (nil if one is NULL).
func keyValues(t *trigger.Data, row *trigger.Row, keys []string) ([]any, error) {
	var vals []any
	for _, k := range keys {
		if _, err := t.Column(k); err != nil {
			return nil, pg.Errorf("42703", "there is no attribute %q in relation %q", k, t.Table)
		}
		v, err := row.Any(k)
		if v == nil || err != nil {
			return nil, err
		}
		vals = append(vals, *v)
	}
	return vals, nil
}

// keyCond returns "k1 = $1 and k2 = $2 ", as the original writes it.
func keyCond(keys []string) string {
	var b strings.Builder
	for i, k := range keys {
		fmt.Fprintf(&b, "%s = $%d ", k, i+1)
		if i < len(keys)-1 {
			b.WriteString("and ")
		}
	}
	return b.String()
}

// quoteLiteral is quote_literal_cstr: quotes doubled, and an E'...' string
// with doubled backslashes if there are any.
func quoteLiteral(s string) string {
	var b strings.Builder
	if strings.Contains(s, `\`) {
		b.WriteByte('E')
	}
	b.WriteByte('\'')
	for i := 0; i < len(s); i++ {
		if s[i] == '\'' || s[i] == '\\' {
			b.WriteByte(s[i])
		}
		b.WriteByte(s[i])
	}
	b.WriteByte('\'')
	return b.String()
}

func main() {}
