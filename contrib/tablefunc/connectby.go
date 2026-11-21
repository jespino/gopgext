package main

import (
	"fmt"
	"iter"
	"strconv"
	"strings"

	"gopgext/pg"
)

// connectby produces the tree under startWith of a table of (key, parent
// key) rows: one row per node (key, parent key, level[, branch][, serial]),
// depth first, with siblings ordered by orderBy if given.
type connectby struct {
	relname, key, parentKey, orderBy, delim string
	maxDepth                                int
	showBranch, showSerial                  bool
	cols                                    []pg.Column
	serial                                  int
	out                                     []pg.Record
}

func connectbyError(detail string, args ...any) error {
	return &pg.Error{Code: "42804", Message: "invalid connectby return type", Detail: fmt.Sprintf(detail, args...)}
}

// validate checks the columns of the result (validateConnectbyTupleDesc).
func (c *connectby) validate() error {
	expected := 3
	if c.showBranch {
		expected++
	}
	if c.showSerial {
		expected++
	}
	if len(c.cols) != expected {
		return connectbyError("Return row must have %d columns, not %d.", expected, len(c.cols))
	}
	if c.cols[2].Type != oidInt4 {
		return connectbyError("Third return column (depth) must be type %s.", pg.FormatType(oidInt4, -1))
	}
	if c.showBranch && c.cols[3].Type != oidText {
		return connectbyError("Fourth return column (branch) must be type %s.", pg.FormatType(oidText, -1))
	}
	if c.showBranch && c.showSerial && c.cols[4].Type != oidInt4 {
		return connectbyError("Fifth return column (serial) must be type %s.", pg.FormatType(oidInt4, -1))
	}
	if !c.showBranch && c.showSerial && c.cols[3].Type != oidInt4 {
		return connectbyError("Fourth return column (serial) must be type %s.", pg.FormatType(oidInt4, -1))
	}
	return nil
}

// compatible checks the query's columns against the result's
// (compatConnectbyTupleDescs).
func (c *connectby) compatible(t *table) error {
	if len(t.cols) < 2 {
		return &pg.Error{Code: pg.CodeInvalidParameterValue, Message: "invalid connectby source data query",
			Detail: "The query must return at least two columns."}
	}
	if !sameType(c.cols[0], t.types[0], t.typmods[0]) {
		return connectbyError("Source key type %s does not match return key type %s.", t.typeName(0), c.cols[0].TypeName)
	}
	if !sameType(c.cols[1], t.types[1], t.typmods[1]) {
		return connectbyError("Source parent key type %s does not match return parent key type %s.",
			t.typeName(1), c.cols[1].TypeName)
	}
	return nil
}

// quoteLiteral is quote_literal_cstr.
func quoteLiteral(s string) string {
	q := "'" + strings.ReplaceAll(strings.ReplaceAll(s, `\`, `\\`), "'", "''") + "'"
	if strings.Contains(s, `\`) {
		return "E" + q
	}
	return q
}

func (c *connectby) emit(key, parent *string, level int, branch string) {
	vals := []*string{key, parent, ptr(strconv.Itoa(level))}
	if c.showBranch {
		vals = append(vals, ptr(branch))
	}
	if c.showSerial {
		vals = append(vals, ptr(strconv.Itoa(c.serial)))
		c.serial++
	}
	c.out = append(c.out, record(vals))
}

func ptr(s string) *string { return &s }

// build adds the subtree of the children of start (build_tuplestore_recursively).
func (c *connectby) build(start, branch string, level int) error {
	if c.maxDepth > 0 && level > c.maxDepth {
		return nil
	}
	sql := "SELECT " + c.key + ", " + c.parentKey + " FROM " + c.relname + " WHERE " + c.parentKey + " = " +
		quoteLiteral(start) + " AND " + c.key + " IS NOT NULL AND " + c.key + " <> " + c.parentKey
	if c.showSerial {
		sql += " ORDER BY " + c.orderBy
	}
	if level == 0 {
		c.emit(&start, nil, 0, start)
		level++
	}
	t, err := query(sql)
	if err != nil {
		return err
	}
	if len(t.rows) == 0 {
		return nil
	}
	if err := c.compatible(t); err != nil {
		return err
	}
	ancestors := c.delim + branch + c.delim
	for _, row := range t.rows {
		key, parent := row[0], row[1]
		cur := branch
		if key != nil {
			if strings.Contains(ancestors, c.delim+*key+c.delim) {
				return &pg.Error{Code: "42P19", Message: "infinite recursion detected"}
			}
			cur = branch + c.delim + *key
		}
		c.emit(key, parent, level, cur)
		if key != nil {
			if err := pg.CheckInterrupts(); err != nil {
				return err
			}
			if err := c.build(*key, cur, level+1); err != nil {
				return err
			}
		}
	}
	return nil
}

func (c *connectby) run(startWith string) (iter.Seq[pg.Record], error) {
	cols, err := pg.ResultColumns()
	if err != nil {
		return nil, err
	}
	c.cols = cols
	c.serial = 1
	if err := c.validate(); err != nil {
		return nil, err
	}
	if err := c.build(startWith, startWith, 0); err != nil {
		return nil, err
	}
	return records(c.out), nil
}

// ConnectbyBranch is connectby(relname, key, parent_key, start_with,
// max_depth, branch_delim): with a branch column.
//
//gopgext:function manual stable symbol=connectby_text
func ConnectbyBranch(relname, key, parentKey, startWith string, maxDepth int32, delim string) (iter.Seq[pg.Record], error) {
	c := &connectby{relname: relname, key: key, parentKey: parentKey, delim: delim, maxDepth: int(maxDepth), showBranch: true}
	return c.run(startWith)
}

// Connectby is connectby(relname, key, parent_key, start_with, max_depth).
//
//gopgext:function manual stable symbol=connectby_text_nobranch
func Connectby(relname, key, parentKey, startWith string, maxDepth int32) (iter.Seq[pg.Record], error) {
	c := &connectby{relname: relname, key: key, parentKey: parentKey, delim: "~", maxDepth: int(maxDepth)}
	return c.run(startWith)
}

// ConnectbySerialBranch is connectby(relname, key, parent_key, orderby,
// start_with, max_depth, branch_delim): siblings in order, with branch and
// serial columns.
//
//gopgext:function manual stable symbol=connectby_text_serial
func ConnectbySerialBranch(relname, key, parentKey, orderBy, startWith string, maxDepth int32, delim string) (iter.Seq[pg.Record], error) {
	c := &connectby{relname: relname, key: key, parentKey: parentKey, orderBy: orderBy, delim: delim,
		maxDepth: int(maxDepth), showBranch: true, showSerial: true}
	return c.run(startWith)
}

// ConnectbySerial is connectby(relname, key, parent_key, orderby,
// start_with, max_depth): siblings in order, with a serial column.
//
//gopgext:function manual stable symbol=connectby_text_serial_nobranch
func ConnectbySerial(relname, key, parentKey, orderBy, startWith string, maxDepth int32) (iter.Seq[pg.Record], error) {
	c := &connectby{relname: relname, key: key, parentKey: parentKey, orderBy: orderBy, delim: "~",
		maxDepth: int(maxDepth), showSerial: true}
	return c.run(startWith)
}
