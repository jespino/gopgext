// Package trigger writes trigger functions in Go. A //gopgext:function
// whose only parameter is a *trigger.Data is a trigger function (SQL
// RETURNS trigger, no arguments):
//
//	// Stamp sets the column named by the trigger's argument to the time.
//	//
//	//gopgext:function
//	func Stamp(t *trigger.Data) (*trigger.Row, error) {
//		if t.New == nil || len(t.Args) != 1 {
//			return nil, pg.Errorf(trigger.CodeProtocolViolated, "stamp: must be fired for INSERT or UPDATE of rows, with a column name")
//		}
//		if err := trigger.Set(t.New, t.Args[0], time.Now()); err != nil {
//			return nil, err
//		}
//		return t.New, nil
//	}
//
//	CREATE TRIGGER stamp BEFORE INSERT OR UPDATE ON items
//		FOR EACH ROW EXECUTE FUNCTION stamp('changed_by');
//
// Data describes the event (when, operation, row or statement level), the
// trigger (name, arguments) and the table, with the old and new rows of a
// row-level trigger. The function's result is the row the operation goes
// on with: in a BEFORE row trigger, Data.New (changed with Set, SetText,
// SetAny or SetNull) or Data.Old, or nil to skip the row; an INSTEAD OF
// trigger returns nil when it did nothing; the result of AFTER and
// statement-level triggers is ignored. Without a *trigger.Row result, the
// operation goes on unchanged (New for INSERT and UPDATE, Old for DELETE).
//
// Row values are pg.AnyElement values (Any), text (Text: the type's output
// function) or Go values of the column's type (Get). Data and its rows are
// only valid during the call.
//
// A function whose only parameter is a *trigger.Event is an event trigger
// function (RETURNS event_trigger), with no result but an error:
//
//	//gopgext:function
//	func NoDrops(e *trigger.Event) error {
//		return pg.Errorf(pg.CodeInsufficientPrivilege, "%s is not allowed", e.Tag)
//	}
//
//	CREATE EVENT TRIGGER no_drops ON ddl_command_start WHEN TAG IN ('DROP TABLE')
//		EXECUTE FUNCTION no_drops();
package trigger

import (
	"unsafe"

	"gopgext/fmgr"
	"gopgext/internal/core"
	"gopgext/pg"
)

// SQLSTATE codes of trigger errors.
const (
	CodeActionException  = "09000" // triggered_action_exception
	CodeProtocolViolated = "39P01" // trigger_protocol_violated
	codeUndefinedColumn  = "42703"
	codeDatatypeMismatch = "42804"
)

// When is when a trigger fires.
type When int

const (
	Before When = iota
	After
	InsteadOf
)

func (w When) String() string {
	return [...]string{"BEFORE", "AFTER", "INSTEAD OF"}[w]
}

// Op is the operation that fired a trigger.
type Op int

const (
	Insert Op = iota
	Update
	Delete
	Truncate
)

func (o Op) String() string {
	return [...]string{"INSERT", "UPDATE", "DELETE", "TRUNCATE"}[o]
}

// Data is the event a trigger function is called for.
type Data struct {
	Name       string   // the trigger's name
	When       When     // BEFORE, AFTER or INSTEAD OF
	Op         Op       // INSERT, UPDATE, DELETE or TRUNCATE
	ForEachRow bool     // a row-level trigger (else statement-level)
	Args       []string // the arguments of CREATE TRIGGER ... EXECUTE FUNCTION f(args)
	Table      string   // the table (or view) the trigger is on
	Schema     string   // its schema
	TableOID   pg.Oid

	// The row before an UPDATE or DELETE and the row after an INSERT or
	// UPDATE, in row-level triggers (else nil).
	Old, New *Row

	c    *fmgr.Call
	cols []Column // by attribute number - 1, dropped ones included
}

// Column is a column of the trigger's table.
type Column struct {
	Name     string
	Num      int    // attribute number (from 1)
	Type     pg.Oid // the column's type (maybe a domain)
	BaseType pg.Oid // Type, or the base type of a domain
	Typmod   int32  // type modifier, e.g. the 10 of varchar(10) (-1: none)
	TypeName string // e.g. "character varying(10)"

	typlen  int
	dropped bool
	updated bool
}

// Row is the old or new row of a row-level trigger.
type Row struct {
	d       *Data
	which   int64 // 0 old, 1 new
	values  []uint64
	nulls   []bool
	replace []bool // nil until a column is set
}

type cinfo struct {
	when, op, row, nargs, natts, relid, hasOld, hasNew int64
	name, relname, nspname                             *byte
	args                                               **byte
}

type ccolumn struct {
	name, typname                                   *byte
	typ, basetype, typmod, typlen, dropped, updated int64
}

//export gopgext_trigger_info
func triggerInfo(c *fmgr.Call, out *cinfo) uintptr

//export gopgext_trigger_columns
func triggerColumns(c *fmgr.Call, out *ccolumn) uintptr

//export gopgext_trigger_row
func triggerRow(c *fmgr.Call, which int64, values *uint64, nulls *bool) uintptr

//export gopgext_trigger_return
func triggerReturn(c *fmgr.Call, which int64, values *uint64, nulls *bool, replace *bool) uintptr

//export gopgext_trigger_input
func triggerInput(typ uint32, typmod int32, s *byte, out *uint64) uintptr

//export gopgext_trigger_equal
func triggerEqual(typ uint32, a, b uint64, out *bool) uintptr

//export gopgext_trigger_event
func triggerEvent(c *fmgr.Call, event, tag **byte) uintptr

// Arg returns the Data of a trigger function's call (generated code).
func Arg(c *fmgr.Call) *Data {
	var info cinfo
	core.Check(triggerInfo(c, &info))
	d := &Data{
		Name:       core.GoString(info.name),
		When:       When(info.when),
		Op:         Op(info.op),
		ForEachRow: info.row != 0,
		Table:      core.GoString(info.relname),
		Schema:     core.GoString(info.nspname),
		TableOID:   pg.Oid(info.relid),
		c:          c,
	}
	if info.nargs > 0 {
		for _, p := range unsafe.Slice(info.args, info.nargs) {
			d.Args = append(d.Args, core.GoString(p))
		}
	}
	n := int(info.natts)
	cols := make([]ccolumn, n+1)
	core.Check(triggerColumns(c, &cols[0]))
	d.cols = make([]Column, n)
	for i, cc := range cols[:n] {
		d.cols[i] = Column{
			Name:     core.GoString(cc.name),
			Num:      i + 1,
			Type:     pg.Oid(cc.typ),
			BaseType: pg.Oid(cc.basetype),
			Typmod:   int32(cc.typmod),
			TypeName: core.GoString(cc.typname),
			typlen:   int(cc.typlen),
			dropped:  cc.dropped != 0,
			updated:  cc.updated != 0,
		}
	}
	if info.hasOld != 0 {
		d.Old = d.row(0)
	}
	if info.hasNew != 0 {
		d.New = d.row(1)
	}
	return d
}

func (d *Data) row(which int64) *Row {
	n := len(d.cols)
	r := &Row{d: d, which: which, values: make([]uint64, n+1), nulls: make([]bool, n+1)}
	core.Check(triggerRow(d.c, which, &r.values[0], &r.nulls[0]))
	r.values, r.nulls = r.values[:n], r.nulls[:n]
	return r
}

// Return sets the result of a trigger function: r (Data.Old or Data.New,
// with the columns set) or NULL for nil (generated code).
func Return(c *fmgr.Call, d *Data, r *Row) {
	if r == nil {
		core.Check(triggerReturn(c, -1, nil, nil, nil))
		return
	}
	if r.d != d {
		panic(pg.Errorf(CodeProtocolViolated, "trigger %q returned a row of another trigger call", d.Name))
	}
	if r.replace == nil {
		core.Check(triggerReturn(c, r.which, nil, nil, nil))
		return
	}
	core.Check(triggerReturn(c, r.which, &r.values[0], &r.nulls[0], &r.replace[0]))
}

// ReturnDefault sets the result of a trigger function without a row
// result: the operation goes on unchanged (generated code).
func ReturnDefault(c *fmgr.Call, d *Data) {
	switch {
	case d.New != nil:
		Return(c, d, d.New)
	case d.Old != nil:
		Return(c, d, d.Old)
	default:
		Return(c, d, nil)
	}
}

// Columns returns the columns of the table (not the dropped ones).
func (d *Data) Columns() []Column {
	var cols []Column
	for _, c := range d.cols {
		if !c.dropped {
			cols = append(cols, c)
		}
	}
	return cols
}

// Column returns the column called name (as spelled in the catalog: the
// name of a column created as "Name" or Name is "Name" and name
// respectively), or an undefined_column error.
func (d *Data) Column(name string) (Column, error) {
	for _, c := range d.cols {
		if !c.dropped && c.Name == name {
			return c, nil
		}
	}
	return Column{}, pg.Errorf(codeUndefinedColumn, "column %q of relation %q does not exist", name, d.Table)
}

// UpdatedColumns returns the columns an UPDATE assigns (its SET list,
// tg_updatedcols), whether or not their values change.
func (d *Data) UpdatedColumns() []string {
	var names []string
	for _, c := range d.cols {
		if c.updated {
			names = append(names, c.Name)
		}
	}
	return names
}

// Changed reports whether column name has a different value (or NULL-ness)
// in the new row than in the old one, comparing the stored values: e.g.
// numeric 1.0 and 1.00 differ. It is false outside UPDATE row triggers.
func (d *Data) Changed(name string) (bool, error) {
	c, err := d.Column(name)
	if err != nil || d.Old == nil || d.New == nil {
		return false, err
	}
	i := c.Num - 1
	o, n := d.Old, d.New
	if o.nulls[i] || n.nulls[i] {
		return o.nulls[i] != n.nulls[i], nil
	}
	var eq bool
	core.Check(triggerEqual(uint32(c.Type), o.values[i], n.values[i], &eq))
	return !eq, nil
}

func (r *Row) column(name string) (Column, error) { return r.d.Column(name) }

// Any returns the value of column name as a value of the column's type
// (nil for NULL).
func (r *Row) Any(name string) (*pg.AnyElement, error) {
	c, err := r.column(name)
	if err != nil || r.nulls[c.Num-1] {
		return nil, err
	}
	v := core.MakeAnyElement(r.values[c.Num-1], uint32(c.Type))
	return &v, nil
}

// Text returns the text form of column name (nil for NULL), as its type's
// output function writes it.
func (r *Row) Text(name string) (*string, error) {
	v, err := r.Any(name)
	if v == nil {
		return nil, err
	}
	s := v.String()
	return &s, nil
}

// IsNull reports whether column name is NULL.
func (r *Row) IsNull(name string) (bool, error) {
	c, err := r.column(name)
	if err != nil {
		return false, err
	}
	return r.nulls[c.Num-1], nil
}

func (r *Row) set(c Column, d uint64, null bool) {
	if r.replace == nil {
		r.replace = make([]bool, len(r.values))
	}
	i := c.Num - 1
	r.values[i], r.nulls[i], r.replace[i] = d, null, true
}

// SetText sets column name from its text form (nil: NULL) with the
// column's input function: the type modifier (varchar(10)) and domain
// checks apply.
func (r *Row) SetText(name string, s *string) error {
	c, err := r.column(name)
	if err != nil {
		return err
	}
	var p *byte
	if s != nil {
		p = core.CBytes(*s)
	}
	var d uint64
	if ed := triggerInput(uint32(c.Type), c.Typmod, p, &d); ed != 0 {
		return core.FromErrorData(ed)
	}
	r.set(c, d, s == nil)
	return nil
}

// SetAny sets column name to v (nil: NULL), which must be of the column's
// type or of its base type.
func (r *Row) SetAny(name string, v *pg.AnyElement) error {
	c, err := r.column(name)
	if err != nil {
		return err
	}
	if v == nil {
		r.set(c, 0, true)
		return nil
	}
	if v.Type() != c.Type && v.Type() != c.BaseType {
		return pg.Errorf(codeDatatypeMismatch, "column %q is of type %s, not of the value's type (OID %d)", name, c.TypeName, v.Type())
	}
	r.set(c, v.Datum(), false)
	return nil
}

// SetNull sets column name to NULL.
func (r *Row) SetNull(name string) error { return r.SetAny(name, nil) }

// Get returns the value of column name as a T (nil for NULL). T must
// match the column's type, or the base type of its domain: bool (boolean),
// int16, int32, int64 or int (smallint, integer, bigint), float32,
// float64, string (text, varchar, char), []byte (bytea), pg.Varlena (any
// variable-length type), pg.Oid, pg.Numeric (numeric) or time.Time
// (timestamptz).
func Get[T fmgr.Scalar](r *Row, name string) (*T, error) {
	c, err := r.column(name)
	if err != nil {
		return nil, err
	}
	if err := checkType[T](c); err != nil {
		return nil, err
	}
	if r.nulls[c.Num-1] {
		return nil, nil
	}
	v := fmgr.FromDatum[T](r.values[c.Num-1])
	return &v, nil
}

// Set sets column name to v, of a Go type matching the column's (see Get).
// Unlike SetText, it doesn't apply the type modifier or the checks of a
// domain.
func Set[T fmgr.Scalar](r *Row, name string, v T) error {
	c, err := r.column(name)
	if err != nil {
		return err
	}
	if err := checkType[T](c); err != nil {
		return err
	}
	r.set(c, fmgr.ToDatum(v), false)
	return nil
}

// Type OIDs (pg_type.h).
const (
	oidBool        = 16
	oidBytea       = 17
	oidInt8        = 20
	oidInt2        = 21
	oidInt4        = 23
	oidText        = 25
	oidOid         = 26
	oidFloat4      = 700
	oidFloat8      = 701
	oidBpchar      = 1042
	oidVarchar     = 1043
	oidTimestamptz = 1184
	oidNumeric     = 1700
)

func checkType[T fmgr.Scalar](c Column) error {
	var v T
	var ok bool
	var want string
	t := c.BaseType
	switch any(v).(type) {
	case bool:
		ok, want = t == oidBool, "boolean"
	case int16:
		ok, want = t == oidInt2, "smallint"
	case int32:
		ok, want = t == oidInt4, "integer"
	case int64, int:
		ok, want = t == oidInt8, "bigint"
	case float32:
		ok, want = t == oidFloat4, "real"
	case float64:
		ok, want = t == oidFloat8, "double precision"
	case string:
		ok, want = t == oidText || t == oidVarchar || t == oidBpchar, "text, varchar or char"
	case []byte:
		ok, want = t == oidBytea, "bytea"
	case pg.Varlena:
		ok, want = c.typlen == -1, "a variable-length type"
	case pg.Oid:
		ok, want = t == oidOid, "oid"
	case pg.Numeric:
		ok, want = t == oidNumeric, "numeric"
	case fmgr.Time:
		ok, want = t == oidTimestamptz, "timestamp with time zone"
	default:
		want = "a column type"
	}
	if !ok {
		return pg.Errorf(codeDatatypeMismatch, "column %q is of type %s: a %T needs %s", c.Name, c.TypeName, v, want)
	}
	return nil
}

// Event is the event an event trigger function is called for.
type Event struct {
	Event string // ddl_command_start, ddl_command_end, sql_drop, table_rewrite or login
	Tag   string // the command tag, e.g. "CREATE TABLE"
}

// EventArg returns the Event of an event trigger function's call
// (generated code).
func EventArg(c *fmgr.Call) *Event {
	var event, tag *byte
	core.Check(triggerEvent(c, &event, &tag))
	return &Event{Event: core.GoString(event), Tag: core.GoString(tag)}
}
