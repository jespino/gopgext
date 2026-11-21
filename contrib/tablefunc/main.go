// Package main is a port of PostgreSQL's contrib/tablefunc (version 1.0):
// normal_rand, crosstab (by position, and with a categories query) and
// connectby. The functions returning SETOF record use pg.Record: their
// columns come from the caller's column definition list (or the composite
// type of crosstab2/3/4).
package main

import (
	"encoding/hex"
	"fmt"
	"iter"
	"math"
	"math/rand"
	"slices"
	"strconv"
	"strings"

	"gopgext/pg"
)

func main() {}

// Type OIDs (pg_type.h).
const (
	oidBool   = 16
	oidBytea  = 17
	oidInt4   = 23
	oidText   = 25
	oidFloat4 = 700
)

// NormalRand returns n random values with a normal distribution.
//
//gopgext:function manual symbol=normal_rand
func NormalRand(n int32, mean, stddev float64) (iter.Seq[float64], error) {
	if n < 0 {
		return nil, pg.Errorf(pg.CodeInvalidParameterValue, "number of rows cannot be negative")
	}
	return func(yield func(float64) bool) {
		for i := int32(0); i < n; i += 2 {
			a, b := normalPair()
			if !yield(mean+stddev*a) || i+1 == n || !yield(mean+stddev*b) {
				return
			}
		}
	}, nil
}

// normalPair returns two normally distributed values (mean 0, standard
// deviation 1): Knuth's Algorithm P (the polar method), like the original.
func normalPair() (float64, float64) {
	var v1, v2, s float64
	for {
		v1 = 2*rand.Float64() - 1
		v2 = 2*rand.Float64() - 1
		s = v1*v1 + v2*v2
		if s < 1 {
			break
		}
	}
	if s == 0 {
		return 0, 0
	}
	s = math.Sqrt(-2 * math.Log(s) / s)
	return v1 * s, v2 * s
}

// ---- Query results as text -------------------------------------------------

// table is a query result with every value in its text form (NULL: nil),
// as SPI_getvalue returns it.
type table struct {
	cols    []string
	types   []uint32
	typmods []int32
	rows    [][]*string
}

func query(sql string) (*table, error) {
	rows, err := pg.Query(sql)
	if err != nil {
		return nil, err
	}
	t := &table{cols: rows.Columns(), types: rows.ColumnTypes(), typmods: rows.ColumnTypmods()}
	for rows.Next() {
		vals := rows.Values()
		row := make([]*string, len(vals))
		for i, v := range vals {
			if v != nil {
				s := textOf(v, t.types[i])
				row[i] = &s
			}
		}
		t.rows = append(t.rows, row)
	}
	return t, nil
}

func (t *table) typeName(i int) string { return pg.FormatType(pg.Oid(t.types[i]), t.typmods[i]) }

// textOf returns a value of a query result in PostgreSQL's text form
// (the output functions of bool, floats and bytea).
func textOf(v any, typ uint32) string {
	switch x := v.(type) {
	case string:
		return x
	case int64:
		return strconv.FormatInt(x, 10)
	case bool:
		if x {
			return "t"
		}
		return "f"
	case []byte:
		return `\x` + hex.EncodeToString(x)
	case float64:
		bits, fixed := 64, 15
		if typ == oidFloat4 {
			bits, fixed = 32, 6
		}
		switch {
		case math.IsNaN(x):
			return "NaN"
		case math.IsInf(x, 1):
			return "Infinity"
		case math.IsInf(x, -1):
			return "-Infinity"
		}
		// Shortest exact digits, in scientific notation outside
		// 1e-4 <= |x| < 1e15 (1e6 for float4), like float8out/float4out.
		s := strconv.FormatFloat(x, 'e', -1, bits)
		exp, _ := strconv.Atoi(s[strings.LastIndexByte(s, 'e')+1:])
		if x != 0 && (exp < -4 || exp >= fixed) {
			return s
		}
		return strconv.FormatFloat(x, 'f', -1, bits)
	}
	return ""
}

// streq compares two NULL-able strings (two NULLs are equal).
func streq(a, b *string) bool {
	return a == nil && b == nil || a != nil && b != nil && *a == *b
}

// record converts text values (nil: NULL) to a pg.Record: the column
// types' input functions read them.
func record(vals []*string) pg.Record {
	r := make(pg.Record, len(vals))
	for i, v := range vals {
		if v != nil {
			r[i] = *v
		}
	}
	return r
}

func records(rs []pg.Record) iter.Seq[pg.Record] { return slices.Values(rs) }

func returnTypeError(detail string, args ...any) error {
	return &pg.Error{Code: "42804", Message: "invalid crosstab return type", Detail: fmt.Sprintf(detail, args...)}
}

// sameType is the check of the original: the return column has the
// source column's type, and its type modifier if it has one.
func sameType(ret pg.Column, typ uint32, typmod int32) bool {
	return uint32(ret.Type) == typ && (ret.Typmod < 0 || ret.Typmod == typmod)
}

// ---- crosstab ------------------------------------------------------------

// Crosstab pivots the rows (row_name, category, value) of a query, ordered
// by row_name: each row_name becomes a row whose value columns get the
// values in order (missing ones are NULL, extra ones are dropped).
//
//gopgext:function manual stable symbol=crosstab
func Crosstab(sql string) (iter.Seq[pg.Record], error) {
	t, err := query(sql)
	if err != nil {
		return nil, err
	}
	if len(t.rows) == 0 {
		return records(nil), nil
	}
	if len(t.cols) != 3 {
		return nil, &pg.Error{Code: pg.CodeInvalidParameterValue, Message: "invalid crosstab source data query",
			Detail: "The query must return 3 columns: row_name, category, and value."}
	}
	cols, err := pg.ResultColumns()
	if err != nil {
		return nil, err
	}
	if len(cols) < 2 {
		return nil, returnTypeError("Return row must have at least two columns.")
	}
	if !sameType(cols[0], t.types[0], t.typmods[0]) {
		return nil, returnTypeError("Source row_name datatype %s does not match return row_name datatype %s.",
			t.typeName(0), cols[0].TypeName)
	}
	for i := 1; i < len(cols); i++ {
		if !sameType(cols[i], t.types[2], t.typmods[2]) {
			return nil, returnTypeError("Source value datatype %s does not match return value datatype %s in column %d.",
				t.typeName(2), cols[i].TypeName, i+1)
		}
	}

	ncats := len(cols) - 1
	var out []pg.Record
	var lastRowID *string
	first := true
	for n := 0; n < len(t.rows); n++ {
		values := make([]*string, ncats+1)
		skip := false
		for i := 0; i < ncats && n < len(t.rows); i++ {
			row := t.rows[n]
			rowID := row[0]
			if i == 0 {
				values[0] = rowID
				// Rows of the last row_name beyond the number of categories.
				if !first && streq(lastRowID, rowID) {
					skip = true
					break
				}
			}
			if !streq(rowID, values[0]) {
				n-- // the row starts the next output row
				break
			}
			values[1+i] = row[2]
			if i < ncats-1 {
				n++
			}
		}
		if !skip {
			out = append(out, record(values))
		}
		lastRowID = values[0]
		first = false
	}
	return records(out), nil
}

// maxCatName is the length of the categories crosstab_hash compares (the
// original's hash key: NAMEDATALEN bytes, NUL included, by snprintf with
// one byte less).
const maxCatName = 62

func catKey(s string) string {
	if len(s) > maxCatName {
		return s[:maxCatName]
	}
	return s
}

// CrosstabHash pivots the rows (row_name, extra columns..., category,
// value) of a query by the categories another query returns: each
// category is a column (missing values are NULL, values of other
// categories are dropped).
//
//gopgext:function manual stable symbol=crosstab_hash
func CrosstabHash(sql, catsSQL string) (iter.Seq[pg.Record], error) {
	cols, err := pg.ResultColumns()
	if err != nil {
		return nil, err
	}
	if len(cols) < 2 {
		return nil, returnTypeError("Return row must have at least two columns.")
	}
	cats, err := query(catsSQL)
	if err != nil {
		return nil, err
	}
	index := map[string]int{}
	if len(cats.rows) > 0 {
		if len(cats.cols) != 1 {
			return nil, &pg.Error{Code: pg.CodeInvalidParameterValue, Message: "invalid crosstab categories query",
				Detail: "The query must return one column."}
		}
		for i, row := range cats.rows {
			if row[0] == nil {
				return nil, pg.Errorf(pg.CodeNullValueNotAllowed, "crosstab category value must not be null")
			}
			k := catKey(*row[0])
			if _, dup := index[k]; dup {
				return nil, pg.Errorf("42710", "duplicate category name")
			}
			index[k] = i
		}
	}

	t, err := query(sql)
	if err != nil {
		return nil, err
	}
	var out []pg.Record
	if len(t.rows) == 0 {
		return records(nil), nil
	}
	if len(index) == 0 {
		return nil, pg.Errorf("21000", "crosstab categories query must return at least one row")
	}
	ncols := len(t.cols)
	if ncols < 3 {
		return nil, &pg.Error{Code: pg.CodeInvalidParameterValue, Message: "invalid crosstab source data query",
			Detail: "The query must return at least 3 columns: row_name, category, and value."}
	}
	resultCols := ncols - 2 + len(index)
	if len(cols) != resultCols {
		return nil, returnTypeError("Return row must have %d columns, not %d.", resultCols, len(cols))
	}
	var values []*string
	var lastRowID *string
	for i, row := range t.rows {
		rowID := row[0]
		if i == 0 || !streq(lastRowID, rowID) {
			if i > 0 {
				out = append(out, record(values))
			}
			values = make([]*string, resultCols)
			values[0] = rowID
			copy(values[1:ncols-2], row[1:ncols-2])
		}
		if cat := row[ncols-2]; cat != nil {
			if j, ok := index[catKey(*cat)]; ok {
				values[j+ncols-2] = row[ncols-1]
			}
		}
		lastRowID = rowID
	}
	out = append(out, record(values))
	return records(out), nil
}
