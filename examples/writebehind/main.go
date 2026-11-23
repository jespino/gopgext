// writebehind shows gopgext/intercept and gopgext/xact: UPDATEs that only
// set the columns given to writebehind_enable(table, columns) aren't
// written. Their new values are kept in this backend (once the
// transaction commits, without the rolled-back savepoints) until
// writebehind_flush() writes them, as the table's owner. UPDATEs that
// write those columns otherwise make it forget the values they replace.
//
// It also has functions showing pg.LookupRelation, pg.RunAs and the binary
// images of values (pg.Image, pg.FromImage).
//
// (A real write-behind buffer, like autobatch, keeps the values in shared
// memory, so that every backend's go to the same place and a background
// worker writes them.)
package main

import (
	"fmt"
	"slices"
	"strings"

	"gopgext/intercept"
	"gopgext/pg"
	"gopgext/xact"
)

const triggerName = "writebehind"

// value is a new value of a column: NULL, text, or its binary image.
type value struct {
	typ        pg.Oid
	null, text bool
	b          string
}

func (v value) String() (*string, error) {
	switch {
	case v.null:
		return nil, nil
	case v.text:
		return &v.b, nil
	}
	a, err := pg.FromImage(v.typ, []byte(v.b))
	if err != nil {
		return nil, err
	}
	s := a.String()
	return &s, nil
}

// row is a row with new values.
type row struct {
	rel  pg.Oid
	pk   []string // text
	vals map[int16]value
}

func rowKey(rel pg.Oid, pk []string) string { return fmt.Sprint(rel, pk) }

// pending are the committed new values, by row.
var pending = map[string]*row{}

// change is a new value (or, with clear, a column to forget) of the
// current transaction.
type change struct {
	sp    xact.Savepoint
	rel   pg.Oid
	pk    []string // nil: every row of rel
	col   int16
	v     value
	clear bool
}

var (
	changes     []change
	changesXact uint64
)

var commit = xact.BeforeCommit(func() error {
	for _, c := range changes {
		if xact.RolledBack(c.sp) {
			continue
		}
		if c.pk == nil {
			for k, r := range pending {
				if r.rel == c.rel {
					delete(r.vals, c.col)
					if len(r.vals) == 0 {
						delete(pending, k)
					}
				}
			}
			continue
		}
		k := rowKey(c.rel, c.pk)
		r := pending[k]
		if c.clear {
			if r != nil {
				delete(r.vals, c.col)
				if len(r.vals) == 0 {
					delete(pending, k)
				}
			}
			continue
		}
		if r == nil {
			r = &row{rel: c.rel, pk: c.pk, vals: map[int16]value{}}
			pending[k] = r
		}
		r.vals[c.col] = c.v
	}
	changes = nil
	return nil
})

func add(c change) {
	if changesXact != xact.ID() {
		changes, changesXact = nil, xact.ID() // left over from a transaction that aborted
	}
	c.sp = xact.Current()
	changes = append(changes, c)
	commit.Schedule()
}

func main() {
	intercept.Register(intercept.Handler{
		Table:   policy,
		Rows:    rows,
		Written: written,
		Setting: "writebehind.enabled",
	})
}

var enabled = pg.NewBoolSetting("enabled", true, "Keep the UPDATEs of write-behind columns.")

// policy reads the table's writebehind trigger: its arguments are options
// ("keyed" or "") and the columns.
func policy(rel *pg.Relation) *intercept.Policy {
	t := rel.Trigger(triggerName)
	if t == nil || len(t.Args) < 2 {
		return nil
	}
	var cols []int16
	for _, name := range t.Args[1:] {
		c := rel.Column(name)
		if c == nil {
			return nil
		}
		cols = append(cols, c.Num)
	}
	return &intercept.Policy{
		Groups:  [][]int16{cols},
		Keyed:   t.Args[0] == "keyed",
		Watch:   cols,
		Trigger: triggerName,
		Tag:     rel.Name,
	}
}

func keyOf(b *intercept.Batch, r intercept.Row) []string {
	pk := make([]string, b.Keys())
	for i := range pk {
		pk[i] = string(r.Key(i))
	}
	return pk
}

func rows(b *intercept.Batch) error {
	for i := 0; i < b.Len(); i++ {
		r := b.Row(i)
		pk := keyOf(b, r)
		for j := 0; j < b.Values(); j++ {
			v := r.Value(j)
			add(change{rel: r.Rel(), pk: pk, col: v.Column,
				v: value{typ: v.Type, null: v.Null, text: v.Text, b: string(v.Bytes())}})
		}
	}
	return nil
}

func written(w *intercept.Write) error {
	if w.Rows == nil {
		for _, col := range w.Columns {
			add(change{rel: w.Rel, col: col, clear: true})
		}
		return nil
	}
	b := w.Rows
	for i := 0; i < b.Len(); i++ {
		r := b.Row(i)
		pk := keyOf(b, r)
		for j := 0; j < b.Values(); j++ {
			add(change{rel: r.Rel(), pk: pk, col: r.Value(j).Column, clear: true})
		}
	}
	return nil
}

// WritebehindPending returns the number of rows with values to write.
//
//gopgext:function
func WritebehindPending() int64 { return int64(len(pending)) }

// WritebehindValue returns the value to write to a column of the row of
// table rel whose primary key is pk (text, columns separated by commas).
//
//gopgext:function
func WritebehindValue(rel pg.Oid, pk string, column string) (*string, error) {
	r := pending[rowKey(rel, strings.Split(pk, ","))]
	if r == nil {
		return nil, nil
	}
	info, err := pg.LookupRelation(rel)
	if err != nil || info == nil {
		return nil, err
	}
	c := info.Column(column)
	if c == nil {
		return nil, pg.Errorf("42703", "column %q does not exist", column)
	}
	v, ok := r.vals[c.Num]
	if !ok {
		return nil, nil
	}
	return v.String()
}

func quote(s string) string { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }

// WritebehindFlush writes the values, each table's as its owner, and
// returns the number of rows written.
//
//gopgext:function
func WritebehindFlush() (int64, error) {
	var total int64
	err := intercept.Suspend(func() error {
		for k, r := range pending {
			info, err := pg.LookupRelation(r.rel)
			if err != nil {
				return err
			}
			if info == nil {
				delete(pending, k)
				continue
			}
			err = pg.RunAs(info.Owner, func() error {
				// Type names as the owner's search_path (pg_catalog) sees them.
				info, err := pg.LookupRelation(r.rel)
				if err != nil {
					return err
				}
				var set, where []string
				var args []any
				for i, num := range info.PrimaryKey {
					c := info.ColumnNum(num)
					args = append(args, r.pk[i])
					where = append(where, fmt.Sprintf("%s = $%d::%s", quote(c.Name), len(args), c.TypeName))
				}
				nums := make([]int16, 0, len(r.vals))
				for num := range r.vals {
					nums = append(nums, num)
				}
				slices.Sort(nums)
				for _, num := range nums {
					c := info.ColumnNum(num)
					if c == nil {
						continue // dropped
					}
					s, err := r.vals[num].String()
					if err != nil {
						return err
					}
					args = append(args, s)
					set = append(set, fmt.Sprintf("%s = $%d::%s", quote(c.Name), len(args), c.TypeName))
				}
				if len(set) == 0 {
					return nil
				}
				n, err := pg.Exec(fmt.Sprintf("UPDATE ONLY %s.%s SET %s WHERE %s", quote(info.Schema), quote(info.Name),
					strings.Join(set, ", "), strings.Join(where, " AND ")), args...)
				total += n
				return err
			})
			if err != nil {
				return err
			}
			delete(pending, k)
		}
		return nil
	})
	return total, err
}

// WritebehindDescribe describes a table as pg.LookupRelation sees it.
//
//gopgext:function
func WritebehindDescribe(rel pg.Oid) (*string, error) {
	info, err := pg.LookupRelation(rel)
	if err != nil || info == nil {
		return nil, err
	}
	var cols, pk, trigs []string
	for _, c := range info.Columns {
		if !c.Dropped {
			nn := ""
			if c.NotNull {
				nn = " not null"
			}
			cols = append(cols, fmt.Sprintf("%d:%s %s%s", c.Num, c.Name, c.TypeName, nn))
		}
	}
	for _, num := range info.PrimaryKey {
		pk = append(pk, info.ColumnNum(num).Name)
	}
	for _, t := range info.Triggers {
		if t.Internal {
			continue
		}
		level := "statement"
		if t.Row {
			level = "row"
		}
		when := "after"
		if t.Before {
			when = "before"
		} else if t.InsteadOf {
			when = "instead"
		}
		trigs = append(trigs, fmt.Sprintf("%s(%s %s update=%v when=%v fires=%v args=%s)", t.Name, when, level,
			t.Update, t.HasWhen, t.Fires(), strings.Join(t.Args, "|")))
	}
	s := fmt.Sprintf("%s.%s kind=%c rls=%v pk=%s cols=[%s] triggers=[%s]", info.Schema, info.Name, info.Kind,
		info.RowSecurity, strings.Join(pk, ","), strings.Join(cols, ", "), strings.Join(trigs, ", "))
	return &s, nil
}

// WritebehindRoundtrip formats a value after a trip through its binary
// image.
//
//gopgext:function
func WritebehindRoundtrip(v pg.AnyElement) (string, error) {
	img, err := pg.Image(v)
	if err != nil {
		return "", err
	}
	img = slices.Clone(img)
	a, err := pg.FromImage(v.Type(), img)
	if err != nil {
		return "", err
	}
	return a.String(), nil
}

// WritebehindRunAs returns current_user and search_path as role sees them
// through pg.RunAs.
//
//gopgext:function
func WritebehindRunAs(role pg.Oid) (string, error) {
	var s string
	err := pg.RunAs(role, func() error {
		return pg.QueryRow("SELECT current_user || ' ' || current_setting('search_path')").Scan(&s)
	})
	return s, err
}
