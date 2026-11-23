package pg

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"unsafe"

	"gopgext/internal/core"
)

// Queries run through SPI, each in its own subtransaction: when a statement
// fails, its effects are rolled back and Exec/Query return the error, and
// the function can go on (like an EXCEPTION block in PL/pgSQL). Statements
// see the snapshot of the calling query; in a STABLE or IMMUTABLE function
// they run read-only.
//
// Parameters are referenced as $1, $2, ... and passed as Go values: bool,
// int, int16, int32, int64, float32, float64, string (text), []byte (bytea),
// a pointer to one of those (nil is a typed NULL), a pg.AnyElement (a value
// of its type; a nil *pg.AnyElement is a NULL of unknown type), or nil (NULL
// of unknown type).

// value types exchanged with C (struct gopgext_value).
const (
	kindNull = iota
	kindBool
	kindInt
	kindFloat
	kindText
	kindBytes
	kindOther
	kindDatum // parameters: a pg.AnyElement
)

// Type OIDs (pg_type.h).
const (
	oidBool   = 16
	oidBytea  = 17
	oidInt8   = 20
	oidInt2   = 21
	oidInt4   = 23
	oidText   = 25
	oidFloat4 = 700
	oidFloat8 = 701
)

type cvalue struct {
	i    int64
	f    float64
	ptr  unsafe.Pointer
	len  int64
	kind int32
	typ  int32
}

type cresult struct {
	processed int64
	ncols     int64
	nrows     int64
	colnames  unsafe.Pointer // const char **
	coltypes  unsafe.Pointer // int64 *
	values    unsafe.Pointer // struct gopgext_value *
	typmods   unsafe.Pointer // int64 *
}

//export gopgext_spi_execute
func spiExecute(sql *byte, nargs int64, args *cvalue, readOnly bool, limit int64, res *cresult) uintptr

//export gopgext_spi_prepare
func spiPrepare(sql *byte, nargs int64, args *cvalue, plan *unsafe.Pointer) uintptr

//export gopgext_spi_execute_plan
func spiExecutePlan(plan unsafe.Pointer, nargs int64, args *cvalue, readOnly bool, limit int64, res *cresult) uintptr

//export gopgext_spi_free_plan
func spiFreePlan(plan unsafe.Pointer)

// ErrNoRows is returned by Row.Scan when the query returned no rows.
var ErrNoRows = errors.New("pg: no rows in result set")

func toParam(a any) (cvalue, error) {
	switch v := a.(type) {
	case nil:
		return cvalue{kind: kindNull}, nil
	case bool:
		var i int64
		if v {
			i = 1
		}
		return cvalue{kind: kindBool, i: i}, nil
	case int:
		return cvalue{kind: kindInt, i: int64(v), typ: oidInt8}, nil
	case int64:
		return cvalue{kind: kindInt, i: v, typ: oidInt8}, nil
	case int32:
		return cvalue{kind: kindInt, i: int64(v), typ: oidInt4}, nil
	case int16:
		return cvalue{kind: kindInt, i: int64(v), typ: oidInt2}, nil
	case float64:
		return cvalue{kind: kindFloat, f: v, typ: oidFloat8}, nil
	case float32:
		return cvalue{kind: kindFloat, f: float64(v), typ: oidFloat4}, nil
	case string:
		return cvalue{kind: kindText, ptr: unsafe.Pointer(core.StringData(v)), len: int64(len(v))}, nil
	case []byte:
		return cvalue{kind: kindBytes, ptr: unsafe.Pointer(unsafe.SliceData(v)), len: int64(len(v))}, nil
	case *bool:
		return ptrParam(v, oidBool)
	case *int:
		return ptrParam(v, oidInt8)
	case *int64:
		return ptrParam(v, oidInt8)
	case *int32:
		return ptrParam(v, oidInt4)
	case *int16:
		return ptrParam(v, oidInt2)
	case *float64:
		return ptrParam(v, oidFloat8)
	case *float32:
		return ptrParam(v, oidFloat4)
	case *string:
		return ptrParam(v, oidText)
	case *[]byte:
		return ptrParam(v, oidBytea)
	case AnyElement:
		return cvalue{kind: kindDatum, i: int64(v.Datum()), typ: int32(v.Type())}, nil
	case *AnyElement:
		if v == nil {
			return cvalue{kind: kindNull}, nil
		}
		return toParam(*v)
	}
	return cvalue{}, fmt.Errorf("pg: unsupported query parameter type %T", a)
}

func ptrParam[T any](p *T, oid int32) (cvalue, error) {
	if p == nil {
		return cvalue{kind: kindNull, typ: oid}, nil
	}
	return toParam(*p)
}

// execute runs sql and converts the result into Go values.
func execute(sql string, limit int64, args []any) (*Rows, error) {
	return run(args, func(n int64, params *cvalue, res *cresult) uintptr {
		return spiExecute(core.CBytes(sql), n, params, core.ReadOnly, limit, res)
	})
}

// run runs a statement with parameters args (exec does) and converts the
// result into Go values.
func run(args []any, exec func(n int64, params *cvalue, res *cresult) uintptr) (*Rows, error) {
	params := make([]cvalue, len(args)+1)
	for i, a := range args {
		v, err := toParam(a)
		if err != nil {
			return nil, err
		}
		params[i] = v
	}
	var res cresult
	if ed := exec(int64(len(args)), &params[0], &res); ed != 0 {
		return nil, core.FromErrorData(ed)
	}
	rows := &Rows{processed: res.processed, cur: -1}
	ncols, nrows := int(res.ncols), int(res.nrows)
	if ncols == 0 {
		return rows, nil
	}
	names := unsafe.Slice((**byte)(res.colnames), ncols)
	types := unsafe.Slice((*int64)(res.coltypes), ncols)
	typmods := unsafe.Slice((*int64)(res.typmods), ncols)
	rows.typmods = make([]int32, ncols)
	rows.cols = make([]string, ncols)
	rows.types = make([]uint32, ncols)
	for i := range names {
		rows.cols[i] = core.GoString(names[i])
		rows.types[i] = uint32(types[i])
		rows.typmods[i] = int32(typmods[i])
	}
	cvals := unsafe.Slice((*cvalue)(res.values), ncols*nrows)
	rows.vals = make([]value, len(cvals))
	for i, c := range cvals {
		v := value{kind: c.kind, i: c.i, f: c.f}
		switch c.kind {
		case kindText, kindOther:
			v.s = string(unsafe.Slice((*byte)(c.ptr), c.len))
		case kindBytes:
			v.b = append([]byte{}, unsafe.Slice((*byte)(c.ptr), c.len)...)
		}
		rows.vals[i] = v
	}
	return rows, nil
}

// Type OIDs of parameters of prepared statements (Prepare).
const (
	OidBool   Oid = oidBool
	OidBytea  Oid = oidBytea
	OidInt8   Oid = oidInt8
	OidInt2   Oid = oidInt2
	OidInt4   Oid = oidInt4
	OidText   Oid = oidText
	OidFloat4 Oid = oidFloat4
	OidFloat8 Oid = oidFloat8
)

// Stmt is a prepared statement: parsed and planned once by Prepare, then
// run any number of times (in any transaction of the session), like
// PL/pgSQL does with its statements. PostgreSQL replans it when the
// objects it uses change.
type Stmt struct {
	plan  unsafe.Pointer
	nargs int
}

// Prepare prepares a statement with parameters $1, $2... of the given
// types. Text arguments of a parameter of another type are converted with
// the type's input function (Exec("...", "2026-01-01") for a date); other
// arguments must have the parameter's type.
//
//	stmt, err := pg.Prepare("UPDATE items SET qty = $2 WHERE name = $1", pg.OidText, pg.OidInt4)
//	...
//	_, err = stmt.Exec("apple", int32(3))
func Prepare(sql string, types ...Oid) (*Stmt, error) {
	params := make([]cvalue, len(types)+1)
	for i, t := range types {
		params[i].typ = int32(t)
	}
	s := &Stmt{nargs: len(types)}
	if ed := spiPrepare(core.CBytes(sql), int64(len(types)), &params[0], &s.plan); ed != 0 {
		return nil, core.FromErrorData(ed)
	}
	return s, nil
}

func (s *Stmt) execute(limit int64, args []any) (*Rows, error) {
	if s.plan == nil {
		return nil, errors.New("pg: statement is closed")
	}
	if len(args) != s.nargs {
		return nil, fmt.Errorf("pg: statement has %d parameters, got %d arguments", s.nargs, len(args))
	}
	return run(args, func(n int64, params *cvalue, res *cresult) uintptr {
		return spiExecutePlan(s.plan, n, params, core.ReadOnly, limit, res)
	})
}

// Exec runs the statement and returns the number of rows it processed. As
// with pg.Exec, it runs in a subtransaction.
func (s *Stmt) Exec(args ...any) (int64, error) {
	rows, err := s.execute(0, args)
	if err != nil {
		return 0, err
	}
	return rows.processed, nil
}

// Query runs the statement and returns its rows.
func (s *Stmt) Query(args ...any) (*Rows, error) { return s.execute(0, args) }

// QueryRow runs the statement for at most one row.
func (s *Stmt) QueryRow(args ...any) *Row {
	rows, err := s.execute(1, args)
	return &Row{rows: rows, err: err}
}

// Close frees the statement.
func (s *Stmt) Close() {
	spiFreePlan(s.plan)
	s.plan = nil
}

// Exec runs a statement and returns the number of rows it processed
// (inserted, updated, deleted or returned).
func Exec(sql string, args ...any) (int64, error) {
	rows, err := execute(sql, 0, args)
	if err != nil {
		return 0, err
	}
	return rows.processed, nil
}

// Query runs a statement that returns rows. The whole result is read into
// memory.
func Query(sql string, args ...any) (*Rows, error) {
	return execute(sql, 0, args)
}

// QueryRow runs a statement that returns at most one row (more are
// ignored). Errors are reported by Row.Scan.
func QueryRow(sql string, args ...any) *Row {
	rows, err := execute(sql, 1, args)
	return &Row{rows: rows, err: err}
}

// Rows is the result of a query.
//
//	rows, err := pg.Query("SELECT id, name FROM users WHERE age > $1", 30)
//	...
//	for rows.Next() {
//		var id int64
//		var name *string // NULL-able: nil for NULL
//		if err := rows.Scan(&id, &name); err != nil { ... }
//	}
type Rows struct {
	cols      []string
	types     []uint32
	typmods   []int32
	vals      []value
	processed int64
	cur       int
}

type value struct {
	kind int32
	i    int64
	f    float64
	s    string
	b    []byte
}

// Columns returns the names of the result columns.
func (r *Rows) Columns() []string { return r.cols }

// ColumnTypes returns the type OIDs of the result columns.
func (r *Rows) ColumnTypes() []uint32 { return r.types }

// ColumnTypmods returns the type modifiers of the result columns (-1 for
// none, e.g. 14 for varchar(10)): see FormatType.
func (r *Rows) ColumnTypmods() []int32 { return r.typmods }

// Len returns the number of rows.
func (r *Rows) Len() int {
	if len(r.cols) == 0 {
		return 0
	}
	return len(r.vals) / len(r.cols)
}

// Next advances to the next row; it returns false after the last one.
func (r *Rows) Next() bool {
	if r.cur+1 >= r.Len() {
		r.cur = r.Len()
		return false
	}
	r.cur++
	return true
}

func (r *Rows) row() ([]value, error) {
	if r.cur < 0 || r.cur >= r.Len() {
		return nil, errors.New("pg: Scan called without a successful Next")
	}
	n := len(r.cols)
	return r.vals[r.cur*n : (r.cur+1)*n], nil
}

// Scan copies the columns of the current row into dest: pointers to bool,
// int, int16, int32, int64, float32, float64, string, []byte or any, or
// pointers to pointers of those (set to nil for NULL). Numbers convert
// between Go types when they fit; numeric and other types convert from
// their text form; any value scans into a string.
func (r *Rows) Scan(dest ...any) error {
	row, err := r.row()
	if err != nil {
		return err
	}
	if len(dest) != len(row) {
		return fmt.Errorf("pg: Scan: %d destinations for %d columns", len(dest), len(row))
	}
	for i, d := range dest {
		if err := scanValue(row[i], d); err != nil {
			return fmt.Errorf("pg: Scan column %d (%s): %w", i, r.cols[i], err)
		}
	}
	return nil
}

// Values returns the current row as Go values: nil (NULL), bool, int64,
// float64, string (text and other types in their text form) or []byte.
func (r *Rows) Values() []any {
	row, err := r.row()
	if err != nil {
		return nil
	}
	out := make([]any, len(row))
	for i, v := range row {
		out[i] = v.any()
	}
	return out
}

// Row is the result of QueryRow.
type Row struct {
	rows *Rows
	err  error
}

// Scan copies the row's columns into dest (see Rows.Scan), or returns
// ErrNoRows.
func (r *Row) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	if !r.rows.Next() {
		return ErrNoRows
	}
	return r.rows.Scan(dest...)
}

func (v value) any() any {
	switch v.kind {
	case kindBool:
		return v.i != 0
	case kindInt:
		return v.i
	case kindFloat:
		return v.f
	case kindText, kindOther:
		return v.s
	case kindBytes:
		return v.b
	}
	return nil
}

func (v value) text() string {
	switch v.kind {
	case kindBool:
		if v.i != 0 {
			return "t"
		}
		return "f"
	case kindInt:
		return strconv.FormatInt(v.i, 10)
	case kindFloat:
		return strconv.FormatFloat(v.f, 'g', -1, 64)
	case kindBytes:
		return string(v.b)
	}
	return v.s
}

func (v value) int(bits int) (int64, error) {
	var n int64
	switch v.kind {
	case kindInt:
		n = v.i
	case kindFloat:
		if v.f != math.Trunc(v.f) || math.Abs(v.f) >= 1<<63 {
			return 0, fmt.Errorf("%v is not an integer", v.f)
		}
		n = int64(v.f)
	case kindText, kindOther:
		var err error
		if n, err = strconv.ParseInt(v.s, 10, 64); err != nil {
			return 0, err
		}
	default:
		return 0, fmt.Errorf("cannot convert %T to an integer", v.any())
	}
	if bits < 64 && (n < -1<<(bits-1) || n >= 1<<(bits-1)) {
		return 0, fmt.Errorf("%d out of range for int%d", n, bits)
	}
	return n, nil
}

func (v value) float() (float64, error) {
	switch v.kind {
	case kindInt:
		return float64(v.i), nil
	case kindFloat:
		return v.f, nil
	case kindText, kindOther:
		return strconv.ParseFloat(v.s, 64)
	}
	return 0, fmt.Errorf("cannot convert %T to a float", v.any())
}

func scanValue(v value, dest any) error {
	if v.kind == kindNull {
		switch d := dest.(type) {
		case *any:
			*d = nil
		case **bool:
			*d = nil
		case **int:
			*d = nil
		case **int16:
			*d = nil
		case **int32:
			*d = nil
		case **int64:
			*d = nil
		case **float32:
			*d = nil
		case **float64:
			*d = nil
		case **string:
			*d = nil
		case *[]byte:
			*d = nil
		case **[]byte:
			*d = nil
		default:
			return errors.New("NULL into a non-pointer destination (scan into a pointer to a pointer)")
		}
		return nil
	}
	switch d := dest.(type) {
	case *any:
		*d = v.any()
	case *bool:
		if v.kind != kindBool {
			return fmt.Errorf("cannot convert %T to bool", v.any())
		}
		*d = v.i != 0
	case *int:
		n, err := v.int(64)
		*d = int(n)
		return err
	case *int16:
		n, err := v.int(16)
		*d = int16(n)
		return err
	case *int32:
		n, err := v.int(32)
		*d = int32(n)
		return err
	case *int64:
		n, err := v.int(64)
		*d = n
		return err
	case *float32:
		f, err := v.float()
		*d = float32(f)
		return err
	case *float64:
		f, err := v.float()
		*d = f
		return err
	case *string:
		*d = v.text()
	case *[]byte:
		if v.kind == kindBytes {
			*d = v.b
		} else {
			*d = []byte(v.text())
		}
	case **bool:
		return scanPtr(v, d)
	case **int:
		return scanPtr(v, d)
	case **int16:
		return scanPtr(v, d)
	case **int32:
		return scanPtr(v, d)
	case **int64:
		return scanPtr(v, d)
	case **float32:
		return scanPtr(v, d)
	case **float64:
		return scanPtr(v, d)
	case **string:
		return scanPtr(v, d)
	case **[]byte:
		return scanPtr(v, d)
	default:
		return fmt.Errorf("unsupported destination type %T", dest)
	}
	return nil
}

func scanPtr[T any](v value, d **T) error {
	p := new(T)
	if err := scanValue(v, p); err != nil {
		return err
	}
	*d = p
	return nil
}
