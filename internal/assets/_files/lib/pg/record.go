package pg

import "gopgext/internal/core"

// UUID is the SQL type uuid: its 16 bytes. String formats it; ParseUUID
// parses one. (Queries take and return uuids as text: pass u.String() with
// $1::uuid, and scan into a string.)
type UUID = core.UUID

// ParseUUID parses a UUID in the forms PostgreSQL accepts
// ("a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a11", with or without hyphens and
// braces).
func ParseUUID(s string) (UUID, error) { return core.ParseUUID(s) }

// Record is a row of the SQL type record: a function returning Record
// (RETURNS record) or iter.Seq[Record] (RETURNS SETOF record) returns rows
// whose columns the caller defines, with a column definition list:
//
//	SELECT * FROM crosstab('...') AS ct(row_name text, cat1 text, cat2 int);
//
// or by declaring the function (manual, in sql/) to return a composite
// type. ResultColumns tells the function which columns are expected. A
// record has one value per column: nil (NULL), or a Go value converted to
// the column's type. bool, integers, floats, string, []byte (bytea only),
// pg.Numeric, pg.UUID, time.Time and pg.AnyElement of the column's type
// convert directly; any other combination goes through the value's text
// form and the column type's input function (so "42" fits an integer
// column and 42 a numeric one), as do fmt.Stringer values. A record with
// the wrong number of values raises PostgreSQL's error for a mismatched
// column definition list.
type Record = core.Record

// Column is a column of a row: its name, type, type modifier (-1 for
// none); for the columns of a row result (ResultColumns) the type's SQL
// name with the modifier (for messages, e.g. "character varying(10)"), for
// those of a composite type (Columns) whether it was dropped.
type Column = core.Column

// ResultColumns returns the columns the caller expects from the running
// function returning record (its column definition list, or its composite
// result type). It returns PostgreSQL's errors when there are none: the
// function was called where a record can't be used (SELECT f() instead of
// SELECT * FROM f() AS (...)).
func ResultColumns() ([]Column, error) { return core.ResultColumns() }

// FormatType returns the SQL name of a type with a type modifier (-1 for
// none), e.g. FormatType(1043, 14) is "character varying(10)".
func FormatType(typ Oid, typmod int32) string { return core.FormatType(typ, typmod) }
