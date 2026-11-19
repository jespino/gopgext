// Package fdw writes PostgreSQL foreign data wrappers in Go. The extension
// registers a Wrapper (in main or a package initializer):
//
//	fdw.Register(fdw.Wrapper{
//		Options: []fdw.Option{{Name: "url", Where: fdw.OnServer, Required: fdw.OnServer}},
//		Plan:    plan, // which conditions are evaluated remotely
//		Scan:    scan, // the rows, given the values of those conditions
//	})
//
// and the install script creates the wrapper, named like the extension:
//
//	CREATE EXTENSION myfdw;
//	CREATE SERVER s FOREIGN DATA WRAPPER myfdw OPTIONS (url '...');
//	CREATE FOREIGN TABLE t (...) SERVER s;
//
// gopgext provides the rest: planning (including parameterized paths, so
// join conditions can be pushed down in nested loops), evaluating the
// pushed-down values (constants, stable expressions like now() - interval
// '1 hour', parameters) when a scan starts or restarts, converting rows to
// the columns' types, EXPLAIN (pushed conditions; with ANALYZE, what Rows
// describes), IMPORT FOREIGN SCHEMA and option validation. PostgreSQL also
// checks all conditions itself, so pushing a condition down only reduces
// what is fetched: it must never drop rows that match.
package fdw

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Where says where an option can be set.
type Where int

const (
	OnServer Where = 1 << iota
	OnTable
	OnColumn
	OnUserMapping
	OnWrapper // the FOREIGN DATA WRAPPER itself
)

// Option is an option the wrapper accepts.
type Option struct {
	Name     string
	Where    Where // where it can be set
	Required Where // where it must be set (CREATE SERVER/FOREIGN TABLE ... fails without it)
	// Check validates a value (nil: anything).
	Check func(value string) error
}

// Wrapper is a foreign data wrapper.
type Wrapper struct {
	Options []Option

	// Plan is called while planning a scan of t with conditions that could
	// be evaluated remotely (the table's own conditions, plus join
	// conditions for scans in nested loops). It returns which of them the
	// scan will evaluate remotely, and an estimate of the number of rows (0:
	// 1000). An error means the scan isn't possible with these conditions
	// (e.g. a required one is missing): the planner then uses another plan
	// if there is one, e.g. a nested loop providing the condition, else the
	// error is raised when the scan starts. Nil: nothing is pushed down.
	Plan func(t *Table, conds []Condition) (Plan, error)

	// Scan starts a scan, given the values of the conditions Plan accepted.
	// Called again for each rescan (e.g. each outer row of a nested loop).
	Scan func(t *Table, conds []Value) (Rows, error)

	// Import returns the CREATE FOREIGN TABLE statements for IMPORT FOREIGN
	// SCHEMA remote FROM SERVER s INTO local (without a schema name: they're
	// created in local; LIMIT TO and EXCEPT are applied by PostgreSQL).
	Import func(s *ServerInfo, remote string) ([]string, error)
}

// Plan is the result of Wrapper.Plan.
type Plan struct {
	Pushed []bool // per condition: evaluated remotely
	Rows   float64
}

// Rows produces the rows of a scan.
type Rows interface {
	// Next returns the next row: one value per column of the table, in
	// order (dropped columns included), or nil at the end. Values can be
	// nil (NULL), string, []byte, bool, ints, floats, time.Time,
	// time.Duration, json.RawMessage, a fmt.Stringer, or anything
	// encoding/json can marshal (maps, slices, structs) for json/jsonb
	// columns. Strings are converted with the column type's input function.
	Next() ([]any, error)
	// Close releases the scan's resources. Called at the end, before a
	// rescan, and (for scans abandoned by an error) at the next scan start.
	Close() error
}

// Describer is implemented by Rows that can describe their remote request
// for EXPLAIN ANALYZE ("Remote Request").
type Describer interface {
	Describe() string
}

// Kind is the type of a condition's value.
type Kind int

const (
	Text  Kind = iota + 1 // text, varchar, char, name
	Int                   // smallint, integer, bigint
	Float                 // real, double precision, numeric
	Bool
	Time     // timestamptz, timestamp (UTC)
	Interval // interval (months count as 30 days)
)

func (k Kind) String() string {
	return [...]string{"?", "text", "int", "float", "bool", "time", "interval"}[k]
}

// Condition is a condition "column op value": op is one of = <> < <= > >=
// (with the column on the left).
type Condition struct {
	Column int // index in Table.Columns
	Op     string
	Kind   Kind
}

// Value is a pushed-down condition with its value.
type Value struct {
	Condition
	Null     bool // the value is NULL: the condition can't match
	Text     string
	Int      int64
	Float    float64
	Bool     bool
	Time     time.Time
	Inf      int // Time: -1 for -infinity, 1 for infinity
	Interval time.Duration
}

// ServerInfo is a foreign server.
type ServerInfo struct {
	Name    string
	Options map[string]string
	User    map[string]string // options of the user mapping (current user or PUBLIC)
}

// Table is a foreign table.
type Table struct {
	Name    string
	Server  *ServerInfo
	Options map[string]string
	Columns []ColumnInfo
}

// Option returns an option of the table, else of the user mapping, else of
// the server.
func (t *Table) Option(name string) (string, bool) {
	for _, m := range []map[string]string{t.Options, t.Server.User, t.Server.Options} {
		if v, ok := m[name]; ok {
			return v, true
		}
	}
	return "", false
}

// ColumnInfo is a column of a foreign table.
type ColumnInfo struct {
	Name    string // "" if dropped
	Type    string // e.g. "timestamp with time zone", "character varying(10)"
	Dropped bool
	Options map[string]string
}

var wrapper *Wrapper

//export gopgext_fdw_statement_start
func statementStart() int64

// Now returns the start time of the current statement (like
// statement_timestamp()): the same for all the scans of a statement.
func Now() time.Time { return time.UnixMicro(statementStart()).UTC() }

// Register registers the extension's foreign data wrapper.
func Register(w Wrapper) {
	if wrapper != nil {
		panic("fdw: Register called twice")
	}
	if w.Scan == nil {
		panic("fdw: Wrapper.Scan is required")
	}
	wrapper = &w
}

// QuoteIdent quotes an SQL identifier (for Import).
func QuoteIdent(s string) string { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }

// QuoteLiteral quotes an SQL string literal (for Import).
func QuoteLiteral(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

// text converts a row value to the text given to the column's input
// function (nil: NULL).
func text(v any) *string {
	s := func(x string) *string { return &x }
	switch x := v.(type) {
	case nil:
		return nil
	case *string:
		return x
	case string:
		return &x
	case []byte:
		const hex = "0123456789abcdef"
		b := make([]byte, 2, 2+2*len(x))
		copy(b, `\x`)
		for _, c := range x {
			b = append(b, hex[c>>4], hex[c&15])
		}
		return s(string(b))
	case json.RawMessage:
		return s(string(x))
	case bool:
		if x {
			return s("true")
		}
		return s("false")
	case int:
		return s(fmt.Sprint(x))
	case int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		return s(fmt.Sprint(x))
	case float32:
		return s(formatFloat(float64(x)))
	case float64:
		return s(formatFloat(x))
	case time.Time:
		return s(x.Format(time.RFC3339Nano))
	case time.Duration:
		return s(fmt.Sprintf("%d microseconds", x.Microseconds()))
	case fmt.Stringer:
		return s(x.String())
	}
	b, err := json.Marshal(v)
	if err != nil {
		return s(fmt.Sprint(v))
	}
	return s(string(b))
}

func formatFloat(f float64) string {
	switch {
	case f != f:
		return "NaN"
	case f > 1.7976931348623157e308:
		return "Infinity"
	case f < -1.7976931348623157e308:
		return "-Infinity"
	}
	return fmt.Sprint(f)
}
