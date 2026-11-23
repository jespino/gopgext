// Package intercept hands UPDATE statements to Go instead of executing
// them: for write-behind buffers (coalescing frequent updates of
// timestamps or counters and writing them later), queues, or routing
// writes elsewhere. An intercepted UPDATE doesn't lock or write rows: it
// runs as a query of the rows it would update (their primary key and new
// values), which Go receives, and it reports them as updated ("UPDATE n",
// FOUND, ROW_COUNT).
//
// Which UPDATEs are intercepted is decided per table, by the policy Go
// gives for it (Handler.Table, asked when a statement using the table is
// planned, then cached until the table's relcache entry is invalidated:
// pg.InvalidateRelation makes every backend ask again). A policy has
// column groups: an UPDATE whose SET list only has columns of one group
// is intercepted, if
//
//   - the table is a plain or partitioned table with a primary key, no
//     row-level security, no enabled UPDATE trigger but the policy's
//     Trigger (intercepted UPDATEs don't fire triggers), and no
//     inheritance children;
//   - the UPDATE has no RETURNING, WHERE CURRENT OF, multiple-column
//     assignments or data-modifying CTEs, and its new values don't read
//     columns of the groups (SET n = n + 1: the table may not have the
//     latest value);
//   - Handler.Setting (if set) is on, and Policy.Trigger (if set) fires in
//     the session (ALTER TABLE ... DISABLE TRIGGER, session_replication_role).
//
// UPDATE permission is checked as usual. FROM, joins, subqueries and
// parameters work: the rows are those the UPDATE would update. With a
// Keyed policy, an UPDATE whose WHERE is exactly pk = value (constants,
// parameters) doesn't even read the table: the row is taken as existing.
//
//	func main() {
//		intercept.Register(intercept.Handler{
//			Table: func(rel *pg.Relation) *intercept.Policy {
//				if rel.Trigger("track_seen") == nil {
//					return nil
//				}
//				return &intercept.Policy{Groups: [][]int16{{rel.Column("seen_at").Num}}}
//			},
//			Rows: func(b *intercept.Batch) error {
//				for i := 0; i < b.Len(); i++ {
//					r := b.Row(i)
//					remember(r.Rel(), string(r.Key(0)), r.Value(0))
//				}
//				return nil
//			},
//		})
//	}
//
// Rows of a statement come in batches (of up to 1000), all of them before
// the statement ends: an error fails the statement (and nothing Go did is
// undone). Use gopgext/xact to apply them only if the transaction commits.
// Writes by Go itself (the UPDATEs that eventually write intercepted rows)
// must run in Suspend, or they would be intercepted too.
//
// Policies can also watch columns (Policy.Watch): Handler.Written is then
// told about writes of them that weren't intercepted, e.g. to drop values
// Go holds that are now stale. Plain UPDATEs report their rows (the
// primary keys); MERGE, INSERT ... ON CONFLICT DO UPDATE, UPDATEs with
// RETURNING and data-modifying CTEs report the table only.
//
// Callbacks run inside the planner and executor: they can't run SQL in
// Table, and should be fast in Rows (which runs for every intercepted
// statement).
package intercept

import (
	"unsafe"

	"gopgext/fmgr"
	"gopgext/internal/core"
	"gopgext/pg"
)

// Handler is an extension's interception: one per extension.
type Handler struct {
	// Table returns the policy of a table (with a primary key), or nil to
	// leave it alone. It's called when a statement updating the table is
	// planned in a backend, and again after the table's relcache entry is
	// invalidated (pg.InvalidateRelation, ALTER TABLE, CREATE TRIGGER...).
	Table func(rel *pg.Relation) *Policy
	// Rows receives the rows of intercepted UPDATEs.
	Rows func(b *Batch) error
	// Written receives writes of watched columns (optional).
	Written func(w *Write) error
	// Setting is a boolean setting (defined with pg.NewBoolSetting):
	// UPDATEs are only intercepted while it's on, when planned ("": always).
	Setting string
	// MaxImage is the longest binary image (see Value) a value is passed
	// as; longer values are passed as text (0: no limit).
	MaxImage int
}

// Policy is how a table's UPDATEs are intercepted.
type Policy struct {
	// Groups are sets of columns (attribute numbers, see
	// pg.Relation.Column): UPDATEs setting columns of one group only are
	// intercepted. A policy without groups only watches.
	Groups [][]int16
	// Keyed: an UPDATE whose WHERE gives the primary key (pk = value) is
	// intercepted without reading the table; the row counts as updated
	// whether it exists or not.
	Keyed bool
	// Watch are columns whose writes by statements that aren't intercepted
	// go to Handler.Written.
	Watch []int16
	// Trigger is the name of a trigger of the table: UPDATEs are only
	// intercepted if it exists and fires in the session (it doesn't fire
	// for intercepted UPDATEs). It's a way to keep the table's
	// configuration in the catalog (dumped and restored with it), e.g. as
	// the arguments of a statement trigger with WHEN (false).
	Trigger string
	// Tag is passed back with the table's rows (Batch.Tag, Write.Tag),
	// fixed when the statement is planned, e.g. settings from the trigger.
	Tag string
}

var handler *Handler

// Register installs the handler. Call it while the library loads (in
// main); to intercept every backend's UPDATEs, the library must be in
// shared_preload_libraries (or session_preload_libraries).
func Register(h Handler) {
	if handler != nil {
		panic("intercept: Register called twice")
	}
	if h.Table == nil || h.Rows == nil {
		panic("intercept: a Handler needs Table and Rows")
	}
	handler = &h
	var setting *byte
	if h.Setting != "" {
		setting = core.CBytes(h.Setting)
	}
	install(setting, int64(h.MaxImage))
}

// Suspend runs f with interception off in this backend: UPDATEs are
// planned and executed normally, and writes aren't reported to Written.
// Go's own writes of intercepted tables go through it.
func Suspend(f func() error) error {
	was := suspend(true)
	defer suspend(was)
	return f()
}

// ---- Rows ---------------------------------------------------------------------

// Batch is rows of an intercepted UPDATE (or, for Written, of an UPDATE
// that wrote watched columns). It's only valid during the call.
type Batch struct {
	c     *cbatch
	cells []ccell
	idx   []int32 // distinct rows, when rows can repeat
}

// Root is the table the statement updates (its rows can be in
// partitions: Row.Rel).
func (b *Batch) Root() pg.Oid { return pg.Oid(b.c.root) }

// Tag is the table's Policy.Tag when the statement was planned.
func (b *Batch) Tag() string { return core.GoString(b.c.tag) }

// First and Last tell whether this is the first or last batch of the
// statement (a statement's rows come in one batch or more; Last can come
// with no rows).
func (b *Batch) First() bool { return b.c.first != 0 }
func (b *Batch) Last() bool  { return b.c.last != 0 }

// Len is the number of rows.
func (b *Batch) Len() int {
	if b.idx != nil {
		return len(b.idx)
	}
	return int(b.c.nrows)
}

// Keys is the number of primary key columns (Row.Key); Values that of
// values per row (Row.Value).
func (b *Batch) Keys() int   { return int(b.c.nkeys) }
func (b *Batch) Values() int { return int(b.c.ncols - 1 - b.c.nkeys) }

// Row returns row i.
func (b *Batch) Row(i int) Row {
	if b.idx != nil {
		i = int(b.idx[i])
	}
	n := int(b.c.ncols)
	return Row{b: b, cells: b.cells[i*n : (i+1)*n]}
}

// Row is a row of a batch.
type Row struct {
	b     *Batch
	cells []ccell
}

// Rel is the row's table: the statement's (Batch.Root), or a partition.
func (r Row) Rel() pg.Oid { return pg.Oid(uint32(r.cells[0].datum)) }

// Key returns the text of primary key column i (in the key's order).
func (r Row) Key(i int) []byte { return r.cells[1+i].bytes() }

// Value returns value i: the new value of a SET column (in the SET
// list's order), or for Written a written column (Null).
func (r Row) Value(i int) Value {
	c := &r.cells[1+int(r.b.c.nkeys)+i]
	return Value{Column: int16(c.attnum), Type: pg.Oid(c.typ), Null: c.len < 0, Text: c.text != 0, c: c}
}

// Value is a value of a row: NULL, its binary image (see pg.Image: the
// 8-byte Datum for types passed by value, the bytes it points to
// otherwise), or its text if Text (longer than Handler.MaxImage).
type Value struct {
	// Column is the value's column (attribute number) in the row's table,
	// a partition's numbers can differ from its root's.
	Column int16
	Type   pg.Oid
	Null   bool
	Text   bool
	c      *ccell
}

// Bytes returns the image or the text (nil if Null). It refers to
// PostgreSQL's memory: valid during the call.
func (v Value) Bytes() []byte {
	if v.Null {
		return nil
	}
	return v.c.bytes()
}

// String returns the text form of the value (the type's output function).
func (v Value) String() (string, error) {
	if v.Text {
		return string(v.Bytes()), nil
	}
	a, err := pg.FromImage(v.Type, v.Bytes())
	if err != nil {
		return "", err
	}
	return a.String(), nil
}

// ---- Writes -------------------------------------------------------------------

// Write is a write of watched columns by a statement that wasn't
// intercepted: either rows of a table (Rows, from a plain UPDATE: their
// keys, and the columns written as Null values), or the whole table
// (Rows nil: Rel and Columns).
type Write struct {
	Rel     pg.Oid  // the table (a partition, for whole tables)
	Root    pg.Oid  // the table of the statement
	Columns []int16 // whole tables: the columns written, in Rel
	Tag     string
	Rows    *Batch
}

// ---- C side (frameworks/intercept/intercept.c) ---------------------------------

// ccell is struct cell: NULL (len < 0), the image of a value of type typ
// (at ptr, or datum if ptr is nil), or text.
type ccell struct {
	ptr    *byte
	len    int64
	typ    int64
	datum  uint64
	attnum int64
	text   int64
}

func (c *ccell) bytes() []byte {
	if c.len < 0 {
		return nil
	}
	if c.ptr == nil {
		return unsafe.Slice((*byte)(unsafe.Pointer(&c.datum)), 8)
	}
	return unsafe.Slice(c.ptr, c.len)
}

// cbatch is struct batch.
type cbatch struct {
	nrows, ncols, nkeys, root, dedupe int64
	tag                               *byte
	cells                             *ccell
	first, last, write                int64
	count                             int64
}

// cpolicy is struct intercept_policy.
type cpolicy struct {
	ok      int64
	ncols   int64
	cols    *int16
	nwatch  int64
	watch   *int16
	keyed   int64
	trigger *byte
	tag     *byte
	tagLen  int64
}

// ctablewrite is struct table_write.
type ctablewrite struct {
	rel, root, ncols int64
	cols             *int16
	tag              *byte
}

//export gopgext_intercept_install
func install(setting *byte, maxImage int64)

//export gopgext_intercept_suspend
func suspend(on bool) bool

// Kept until the next call: C copies the policy as soon as Table returns.
var lastPolicy struct {
	cols, watch []int16
	trigger     *byte
	tag         []byte
}

//export gopgext_go_intercept_table
func table(c *fmgr.Call) {
	defer c.End(c.Begin(false))
	d0, _ := c.Datum(0)
	d1, _ := c.Datum(1)
	out := (*cpolicy)(unsafe.Pointer(uintptr(d1)))
	rel, err := pg.LookupRelation(pg.Oid(d0))
	if err != nil {
		c.Fail(err)
		return
	}
	if rel == nil {
		return
	}
	p := handler.Table(rel)
	if p == nil {
		return
	}
	lp := &lastPolicy
	lp.cols = lp.cols[:0]
	for _, g := range p.Groups {
		lp.cols = append(lp.cols, g...)
		lp.cols = append(lp.cols, 0)
	}
	lp.watch = append(lp.watch[:0], p.Watch...)
	lp.trigger = nil
	if p.Trigger != "" {
		lp.trigger = core.CBytes(p.Trigger)
	}
	lp.tag = append(lp.tag[:0], p.Tag...)
	out.ok = 1
	out.ncols, out.nwatch = int64(len(lp.cols)), int64(len(lp.watch))
	if len(lp.cols) > 0 {
		out.cols = &lp.cols[0]
	}
	if len(lp.watch) > 0 {
		out.watch = &lp.watch[0]
	}
	if p.Keyed {
		out.keyed = 1
	}
	out.trigger = lp.trigger
	out.tagLen = int64(len(lp.tag))
	if len(lp.tag) > 0 {
		out.tag = &lp.tag[0]
	}
}

// Rows of the running statement (counting distinct rows).
var (
	seen  map[string]struct{}
	count int64
	batch Batch
	key   []byte
)

//export gopgext_go_intercept_rows
func rows(c *fmgr.Call) {
	defer c.End(c.Begin(false))
	d, _ := c.Datum(0)
	cb := (*cbatch)(unsafe.Pointer(uintptr(d)))
	if cb.first != 0 {
		count = 0
		seen = nil
		if cb.dedupe != 0 {
			seen = map[string]struct{}{}
		}
	}
	b := &batch
	b.c = cb
	b.cells = nil
	b.idx = b.idx[:0]
	if cb.nrows > 0 {
		b.cells = unsafe.Slice(cb.cells, cb.nrows*cb.ncols)
	}
	if seen != nil {
		// Rows matched several times (FROM other tables): once.
		n := int(cb.ncols)
		for i := 0; i < int(cb.nrows); i++ {
			row := b.cells[i*n : (i+1)*n]
			key = key[:0]
			rel := uint32(row[0].datum)
			key = append(key, byte(rel), byte(rel>>8), byte(rel>>16), byte(rel>>24))
			for k := 1; k <= int(cb.nkeys); k++ {
				v := row[k].bytes()
				key = append(key, byte(len(v)), byte(len(v)>>8), byte(len(v)>>16), byte(len(v)>>24))
				key = append(key, v...)
			}
			if _, dup := seen[string(key)]; dup {
				continue
			}
			seen[string(key)] = struct{}{}
			b.idx = append(b.idx, int32(i))
		}
	} else {
		b.idx = nil
	}
	count += int64(b.Len())
	var err error
	if cb.write != 0 {
		if handler.Written != nil {
			err = handler.Written(&Write{Rel: pg.Oid(cb.root), Root: pg.Oid(cb.root), Tag: b.Tag(), Rows: b})
		}
	} else {
		err = handler.Rows(b)
	}
	if cb.last != 0 {
		cb.count = count
		seen = nil
	}
	b.c, b.cells = nil, nil
	if err != nil {
		c.Fail(err)
	}
}

//export gopgext_go_intercept_table_written
func tableWritten(c *fmgr.Call) {
	defer c.End(c.Begin(false))
	if handler.Written == nil {
		return
	}
	d, _ := c.Datum(0)
	tw := (*ctablewrite)(unsafe.Pointer(uintptr(d)))
	w := &Write{Rel: pg.Oid(tw.rel), Root: pg.Oid(tw.root), Tag: core.GoString(tw.tag)}
	if tw.ncols > 0 {
		w.Columns = append([]int16(nil), unsafe.Slice(tw.cols, tw.ncols)...)
	}
	if err := handler.Written(w); err != nil {
		c.Fail(err)
	}
}
