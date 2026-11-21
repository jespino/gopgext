package fmgr

import (
	"fmt"
	"iter"
	"math"
	"strconv"
	"time"
	"unsafe"

	"gopgext/internal/core"
)

// ---- Results of type record (columns defined by the caller) -------------

// Record is a row whose columns the caller defines (pg.Record).
type Record = core.Record

// cell mirrors struct gopgext_cell (record.c): one column of a row.
type cell struct {
	d     uint64
	s     *byte
	n     int64
	kind  int32 // cellNull, cellDatum, cellText
	alloc int32 // the datum was palloc'd (freed once stored)
}

const (
	cellNull = iota
	cellDatum
	cellText
)

// Type OIDs of the values Go converts directly.
const (
	oidBool        = 16
	oidBytea       = 17
	oidInt8        = 20
	oidInt2        = 21
	oidInt4        = 23
	oidText        = 25
	oidFloat4      = 700
	oidFloat8      = 701
	oidTimestampTz = 1184
	oidNumeric     = 1700
	oidUUID        = 2950
)

//export gopgext_record_begin
func recordBegin(c *Call, state *uintptr, n *int64, cols *unsafe.Pointer) uintptr

//export gopgext_record_form
func recordForm(state uintptr, n int64, cells *cell, out *uint64) uintptr

//export gopgext_record_put
func recordPut(state uintptr, n int64, cells *cell) uintptr

// recordOut converts records to the row type of a call's result.
type recordOut struct {
	state uintptr
	cols  []core.Column
	cells []cell
	keep  [][]byte // text of the cells, alive until the row is built
}

func beginRecords(c *Call) *recordOut {
	r := &recordOut{}
	var n int64
	var p unsafe.Pointer
	core.Check(recordBegin(c, &r.state, &n, &p))
	r.cols = core.ResultColumnsOf(p, n)
	r.cells = make([]cell, n+1)
	return r
}

func mismatch(detail string, args ...any) {
	panic(&core.Error{Code: "42804", Message: "function return row and query-specified return row do not match",
		Detail: fmt.Sprintf(detail, args...)})
}

// fill converts rec into r.cells.
func (r *recordOut) fill(rec Record) {
	if len(rec) != len(r.cols) {
		mismatch("Returned row contains %d attributes, but query expects %d.", len(rec), len(r.cols))
	}
	r.keep = r.keep[:0]
	for i, v := range rec {
		r.cells[i] = r.convert(i, v)
	}
}

func (r *recordOut) text(s string) cell {
	b := []byte(s)
	r.keep = append(r.keep, b)
	return cell{s: unsafe.SliceData(b), n: int64(len(b)), kind: cellText}
}

func datumCell[T Scalar](v T) cell {
	d, alloc := toDatum(v)
	c := cell{d: d, kind: cellDatum}
	if alloc {
		c.alloc = 1
	}
	return c
}

// convert converts v for column i: directly when its Go type is the
// column's type (without a type modifier), else through the text form and
// the column type's input function.
func (r *recordOut) convert(i int, v any) cell {
	col := r.cols[i]
	typ := uint32(col.Type)
	if col.Typmod >= 0 {
		typ = 0 // the input function applies the modifier
	}
	switch x := v.(type) {
	case nil:
		return cell{kind: cellNull}
	case string:
		if typ == oidText {
			return datumCell(x)
		}
		return r.text(x)
	case CString:
		return r.text(string(x))
	case bool:
		if typ == oidBool {
			return datumCell(x)
		}
		return r.text(strconv.FormatBool(x))
	case int:
		return r.integer(typ, int64(x))
	case int16:
		return r.integer(typ, int64(x))
	case int32:
		return r.integer(typ, int64(x))
	case int64:
		return r.integer(typ, x)
	case float32:
		if typ == oidFloat4 {
			return datumCell(x)
		}
		return r.float(typ, float64(x), 32)
	case float64:
		return r.float(typ, x, 64)
	case []byte:
		if uint32(col.Type) != oidBytea {
			mismatch("Returned type bytea at ordinal position %d, but query expects %s.", i+1, col.TypeName)
		}
		return datumCell(x)
	case Varlena:
		if uint32(col.Type) != oidBytea {
			mismatch("Returned type bytea at ordinal position %d, but query expects %s.", i+1, col.TypeName)
		}
		return datumCell(x)
	case Numeric:
		if typ == oidNumeric {
			return datumCell(x)
		}
		return r.text(string(x))
	case time.Time:
		if typ == oidTimestampTz {
			return datumCell(x)
		}
		return r.text(x.Format("2006-01-02 15:04:05.999999Z07:00"))
	case UUID:
		if typ == oidUUID {
			return datumCell(x)
		}
		return r.text(x.String())
	case AnyElement:
		if uint32(x.Type()) == uint32(col.Type) {
			return cell{d: x.Datum(), kind: cellDatum}
		}
		return r.text(x.String())
	case fmt.Stringer:
		return r.text(x.String())
	}
	panic(&core.Error{Code: "42804", Message: fmt.Sprintf("unsupported Go type %T in column %d of a record", v, i+1),
		Hint: "Use nil, bool, integers, floats, string, []byte, pg.Numeric, pg.UUID, time.Time or pg.AnyElement."})
}

func (r *recordOut) integer(typ uint32, x int64) cell {
	switch {
	case typ == oidInt8:
		return datumCell(x)
	case typ == oidInt4 && x >= math.MinInt32 && x <= math.MaxInt32:
		return datumCell(int32(x))
	case typ == oidInt2 && x >= math.MinInt16 && x <= math.MaxInt16:
		return datumCell(int16(x))
	}
	return r.text(strconv.FormatInt(x, 10))
}

func (r *recordOut) float(typ uint32, x float64, bits int) cell {
	if typ == oidFloat8 {
		return datumCell(x)
	}
	switch {
	case math.IsNaN(x):
		return r.text("NaN")
	case math.IsInf(x, 1):
		return r.text("Infinity")
	case math.IsInf(x, -1):
		return r.text("-Infinity")
	}
	return r.text(strconv.FormatFloat(x, 'g', -1, bits))
}

func (r *recordOut) form(rec Record) uint64 {
	r.fill(rec)
	var d uint64
	core.Check(recordForm(r.state, int64(len(rec)), &r.cells[0], &d))
	return d
}

func (r *recordOut) put(rec Record) {
	r.fill(rec)
	core.Check(recordPut(r.state, int64(len(rec)), &r.cells[0]))
}

// ReturnRecord sets a result of type record: its columns are those the
// caller expects (a column definition list, or the function's composite
// result type).
func ReturnRecord(c *Call, rec Record) { c.result = beginRecords(c).form(rec) }

// ReturnRecordPtr sets a record result, NULL for nil.
func ReturnRecordPtr(c *Call, rec *Record) {
	if rec == nil {
		c.isnull = 1
		return
	}
	ReturnRecord(c, *rec)
}

// ReturnRecordSet returns the records of seq as the rows of a function
// returning SETOF record.
func ReturnRecordSet(c *Call, seq iter.Seq[Record]) {
	core.Check(srfBegin(c))
	r := beginRecords(c)
	for rec := range seq {
		r.put(rec)
	}
}
