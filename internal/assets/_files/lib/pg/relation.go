package pg

import (
	"unsafe"

	"gopgext/internal/core"
)

// ---- Relations ---------------------------------------------------------------

// RelKind is the kind of a relation (pg_class.relkind).
type RelKind byte

const (
	RelTable            RelKind = 'r'
	RelPartitionedTable RelKind = 'p'
	RelView             RelKind = 'v'
	RelMaterializedView RelKind = 'm'
	RelIndex            RelKind = 'i'
	RelSequence         RelKind = 'S'
	RelForeignTable     RelKind = 'f'
	RelComposite        RelKind = 'c'
	RelToast            RelKind = 't'
)

// Relation describes a table (or another relation) as the catalog has it
// in the current transaction.
type Relation struct {
	ID     Oid
	Name   string
	Schema string
	Owner  Oid
	Kind   RelKind
	// RowSecurity is ALTER TABLE ... ENABLE ROW LEVEL SECURITY;
	// ForceRowSecurity is FORCE ROW LEVEL SECURITY.
	RowSecurity, ForceRowSecurity bool
	// HasSubclass: the table has (or had, until the next VACUUM) inheritance
	// children or partitions. IsPartition: it's a partition.
	HasSubclass, IsPartition bool
	// Columns are the relation's columns by attribute number (Columns[n-1]
	// is column n), including dropped ones (Dropped; nothing but their name
	// is set).
	Columns []RelationColumn
	// PrimaryKey has the attribute numbers of the primary key's columns, in
	// the key's order (nil without a primary key).
	PrimaryKey []int16
	// Triggers are the relation's triggers, including internal ones (foreign
	// keys...) and disabled ones.
	Triggers []Trigger
}

// RelationColumn is a column of a relation.
type RelationColumn struct {
	Num       int16 // attribute number
	Name      string
	Type      Oid
	Typmod    int32  // -1: none
	TypeName  string // with the type modifier, e.g. "character varying(10)"
	Collation Oid
	NotNull   bool
	Dropped   bool
}

// Column returns the (not dropped) column named name, or nil.
func (r *Relation) Column(name string) *RelationColumn {
	for i := range r.Columns {
		if c := &r.Columns[i]; !c.Dropped && c.Name == name {
			return c
		}
	}
	return nil
}

// ColumnNum returns the column with attribute number num, or nil (dropped
// columns too).
func (r *Relation) ColumnNum(num int16) *RelationColumn {
	if num < 1 || int(num) > len(r.Columns) || r.Columns[num-1].Dropped {
		return nil
	}
	return &r.Columns[num-1]
}

// Trigger looks a trigger up by name, or returns nil.
func (r *Relation) Trigger(name string) *Trigger {
	for i := range r.Triggers {
		if r.Triggers[i].Name == name {
			return &r.Triggers[i]
		}
	}
	return nil
}

// Trigger is a trigger of a relation (pg_trigger).
type Trigger struct {
	Name string
	// Function is the trigger function's name (FunctionID its OID).
	Function   string
	FunctionID Oid
	// Args are the trigger's arguments, Columns those of UPDATE OF
	// (attribute numbers).
	Args    []string
	Columns []int16
	// Enabled is pg_trigger.tgenabled: 'O' (fires in origin and local
	// sessions, the default), 'D' (disabled), 'R' (replica), 'A' (always).
	// See Fires.
	Enabled byte
	// Internal triggers implement constraints (foreign keys...).
	Internal bool
	// HasWhen: the trigger has a WHEN condition.
	HasWhen bool
	// Row: FOR EACH ROW (else FOR EACH STATEMENT).
	Row bool
	// Before, After, InsteadOf: when it fires.
	Before, After, InsteadOf bool
	// The events it fires for.
	Insert, Update, Delete, Truncate bool
}

//export gopgext_trigger_fires
func triggerFires(enabled int64) bool

// Fires reports whether the trigger fires in this session: it isn't
// disabled, and session_replication_role ("origin", "replica", "local")
// matches its Enabled mode.
func (t *Trigger) Fires() bool { return triggerFires(int64(t.Enabled)) }

// tgtype bits (catalog/pg_trigger.h).
const (
	tgRow      = 1 << 0
	tgBefore   = 1 << 1
	tgInsert   = 1 << 2
	tgDelete   = 1 << 3
	tgUpdate   = 1 << 4
	tgTruncate = 1 << 5
	tgInstead  = 1 << 6
)

// crelcol is struct gopgext_relcol (csrc/relation.c).
type crelcol struct {
	name                   *byte
	typ, typmod, collation int64
	typname                *byte
	notnull, dropped       int64
}

// creltrig is struct gopgext_reltrig.
type creltrig struct {
	name                     *byte
	function                 int64
	funcname                 *byte
	typ, enabled             int64
	internal, hasWhen, nargs int64
	args                     **byte
	ncols                    int64
	cols                     *int16
}

// crelinfo is struct gopgext_relinfo.
type crelinfo struct {
	id                            int64
	name, schema                  *byte
	owner, kind                   int64
	rowSecurity, forceRowSecurity int64
	hasSubclass, isPartition      int64
	ncols                         int64
	cols                          *crelcol
	npk                           int64
	pk                            *int16
	ntrig                         int64
	trigs                         *creltrig
}

//export gopgext_relation_info
func relationInfo(rel uint32, found *bool, out *crelinfo) uintptr

// LookupRelation describes relation id, or returns nil if there's none.
// It reads the catalog caches (no SQL): it works anywhere a transaction is
// in progress, including planner and executor callbacks. The relation is
// locked (ACCESS SHARE, as by a query reading it) until the transaction
// ends, so it can't be dropped or altered meanwhile.
func LookupRelation(id Oid) (*Relation, error) {
	var info crelinfo
	var found bool
	if ed := relationInfo(uint32(id), &found, &info); ed != 0 {
		return nil, core.FromErrorData(ed)
	}
	if !found {
		return nil, nil
	}
	r := &Relation{
		ID: id, Name: core.GoString(info.name), Schema: core.GoString(info.schema),
		Owner: Oid(info.owner), Kind: RelKind(info.kind),
		RowSecurity: info.rowSecurity != 0, ForceRowSecurity: info.forceRowSecurity != 0,
		HasSubclass: info.hasSubclass != 0, IsPartition: info.isPartition != 0,
	}
	if info.ncols > 0 {
		cols := unsafe.Slice(info.cols, info.ncols)
		r.Columns = make([]RelationColumn, len(cols))
		for i := range cols {
			c := &cols[i]
			r.Columns[i] = RelationColumn{
				Num: int16(i + 1), Name: core.GoString(c.name), Dropped: c.dropped != 0,
				Type: Oid(c.typ), Typmod: int32(c.typmod), TypeName: core.GoString(c.typname),
				Collation: Oid(c.collation), NotNull: c.notnull != 0,
			}
		}
	}
	if info.npk > 0 {
		r.PrimaryKey = append([]int16(nil), unsafe.Slice(info.pk, info.npk)...)
	}
	if info.ntrig > 0 {
		trigs := unsafe.Slice(info.trigs, info.ntrig)
		r.Triggers = make([]Trigger, len(trigs))
		for i := range trigs {
			t := &trigs[i]
			o := &r.Triggers[i]
			o.Name = core.GoString(t.name)
			o.Function, o.FunctionID = core.GoString(t.funcname), Oid(t.function)
			for _, a := range unsafe.Slice(t.args, t.nargs) {
				o.Args = append(o.Args, core.GoString(a))
			}
			if t.ncols > 0 {
				o.Columns = append([]int16(nil), unsafe.Slice(t.cols, t.ncols)...)
			}
			o.Enabled, o.Internal, o.HasWhen = byte(t.enabled), t.internal != 0, t.hasWhen != 0
			o.Row = t.typ&tgRow != 0
			o.InsteadOf = t.typ&tgInstead != 0
			o.Before = !o.InsteadOf && t.typ&tgBefore != 0
			o.After = !o.InsteadOf && !o.Before
			o.Insert, o.Update = t.typ&tgInsert != 0, t.typ&tgUpdate != 0
			o.Delete, o.Truncate = t.typ&tgDelete != 0, t.typ&tgTruncate != 0
		}
	}
	return r, nil
}

//export gopgext_relation_invalidate
func relationInvalidate(rel uint32, found *bool) uintptr

// InvalidateRelation makes every backend forget what it cached about
// relation id when the current transaction commits (this backend: at once):
// plans using it are made again, and so are the caches extensions keep of
// it (e.g. gopgext/intercept's table policies). It returns false if the
// relation doesn't exist.
func InvalidateRelation(id Oid) bool {
	var found bool
	core.Check(relationInvalidate(uint32(id), &found))
	return found
}

// ---- Roles --------------------------------------------------------------------

// csaveduser is struct gopgext_saved_user.
type csaveduser struct{ userid, secContext, nestlevel int64 }

//export gopgext_run_as_begin
func runAsBegin(role uint32, saved *csaveduser) uintptr

//export gopgext_run_as_end
func runAsEnd(saved *csaveduser)

// RunAs runs f as role, the way maintenance commands (VACUUM, ANALYZE,
// REFRESH MATERIALIZED VIEW) run the code of a table's owner: in a
// security-restricted operation (no SET ROLE, no temporary objects...)
// with search_path set to pg_catalog, pg_temp. Settings f changes are
// reverted when it returns. Use it to write to users' tables from a
// background worker or another role, so that their triggers, defaults and
// index expressions don't run with more privileges than their owner has.
// It doesn't check privileges: the extension decides who may do what.
func RunAs(role Oid, f func() error) error {
	var saved csaveduser
	if ed := runAsBegin(uint32(role), &saved); ed != 0 {
		return core.FromErrorData(ed)
	}
	defer runAsEnd(&saved)
	return f()
}

// ---- Binary images of values ------------------------------------------------

//export gopgext_image_get
func imageGet(typ uint32, d uint64, ptr **byte, n *int64) uintptr

//export gopgext_image_datum
func imageDatum(typ uint32, ptr *byte, n int64, out *uint64) uintptr

// Image returns the binary image of a value: the bytes PostgreSQL keeps in
// memory for it (the 8-byte Datum of types passed by value, the varlena of
// variable-length types...), e.g. to keep it in shared memory and make it
// again with FromImage in another backend, without going through text (so
// settings like DateStyle don't matter). Images are only meaningful to the
// same server. The result refers to PostgreSQL's memory (valid during the
// call) for types not passed by value.
func Image(a AnyElement) ([]byte, error) {
	var p *byte
	var n int64
	d := a.Datum()
	if ed := imageGet(uint32(a.Type()), d, &p, &n); ed != 0 {
		return nil, core.FromErrorData(ed)
	}
	if p == nil {
		b := make([]byte, 8)
		*(*uint64)(unsafe.Pointer(&b[0])) = d
		return b, nil
	}
	return unsafe.Slice(p, n), nil
}

// FromImage makes a value of type typ from its binary image (see Image),
// copied into PostgreSQL memory (valid during the call). Its text form is
// FromImage(...).String(). An image of the wrong size is an error, but
// otherwise the bytes aren't checked: only use images Image returned.
func FromImage(typ Oid, image []byte) (AnyElement, error) {
	var d uint64
	if ed := imageDatum(uint32(typ), unsafe.SliceData(image), int64(len(image)), &d); ed != 0 {
		return AnyElement{}, core.FromErrorData(ed)
	}
	return core.MakeAnyElement(d, uint32(typ)), nil
}
