// Package pg is the PostgreSQL API for extensions built with gopgext:
// logging, errors with SQLSTATE codes, queries (SPI) and settings (GUCs).
//
// SQL functions are ordinary Go functions marked with a //gopgext:function
// comment; gopgext generates the glue that converts their arguments and
// results (see the gopgext README for the supported types).
//
// Go code runs inside the backend process, on its stack, only while
// PostgreSQL is calling into the extension: there are no goroutines (a
// backend is single-threaded). main runs once, when the library is loaded.
package pg

import (
	"fmt"

	"gopgext/internal/core"
)

// Error is a PostgreSQL error: returned by Exec and Query when a statement
// fails, and returned by SQL functions to raise an error with a given
// SQLSTATE (see Errorf).
//
// A SQL function that returns an Error it got from Query/Exec unchanged
// re-raises the original PostgreSQL error with all its fields; any other
// error is raised with its Code (P0001 raise_exception if it isn't an Error),
// Message, Detail and Hint.
type Error = core.Error

// CString is the SQL type cstring, used by the input and output functions
// of data types: functions taking or returning one are declared with
// "manual" and a CREATE TYPE in sql/.
//
// A CString argument is not copied: it refers to PostgreSQL's memory and is
// only valid during the call. To keep it (in a global, a map...), copy it
// with strings.Clone. Results are copied.
type CString = core.CString

// Errorf returns an Error with the given SQLSTATE code (e.g. "22023",
// see the Code constants) and a formatted message.
func Errorf(code string, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

// Some SQLSTATE codes (https://www.postgresql.org/docs/current/errcodes-appendix.html).
const (
	CodeRaiseException            = "P0001"
	CodeNoDataFound               = "P0002"
	CodeDataException             = "22000"
	CodeStringDataRightTruncation = "22001"
	CodeNumericValueOutOfRange    = "22003"
	CodeNullValueNotAllowed       = "22004"
	CodeDivisionByZero            = "22012"
	CodeInvalidParameterValue     = "22023"
	CodeInvalidTextRepresentation = "22P02"
	CodeUniqueViolation           = "23505"
	CodeCheckViolation            = "23514"
	CodeFeatureNotSupported       = "0A000"
	CodeInsufficientPrivilege     = "42501"
	CodeUndefinedTable            = "42P01"
	CodeSyntaxError               = "42601"
	CodeQueryCanceled             = "57014"
	CodeInternalError             = "XX000"
)

// ExtensionName returns the extension's name (also the prefix of its
// settings).
func ExtensionName() string { return core.ExtensionName() }

//export gopgext_set_config
func setConfig(name, value *byte, local bool) uintptr

// SetConfig changes a setting, like SET (SET LOCAL if local) or
// set_config(). An invalid name or value raises the PostgreSQL error once the
// function returns (and SetConfig doesn't return).
func SetConfig(name, value string, local bool) {
	core.Check(setConfig(core.CBytes(name), core.CBytes(value), local))
}

//export gopgext_check_interrupts
func checkInterrupts() uintptr

// CheckInterrupts returns an error if the query was canceled (by the user,
// statement_timeout, ...): long-running functions should call it
// periodically and return the error, since Go code can't be interrupted
// otherwise. Once it has returned an error, queries fail and PostgreSQL
// raises the cancel error when the function returns, whatever it returns.
func CheckInterrupts() error {
	if ed := checkInterrupts(); ed != 0 {
		return core.FromErrorData(ed)
	}
	return nil
}

// Oid is the SQL type oid (an object identifier). Input and receive
// functions of data types get the type's OID as their second argument.
type Oid = core.Oid

// Internal is the SQL type internal: a pointer to some PostgreSQL structure
// (support functions of index operator classes, planner support, ...). Go
// code can't dereference it safely by itself: pass it to C code in csrc/,
// or use a framework package that understands it (like gopgext/gist).
type Internal = core.Internal

// Varlena is the contents of a variable-length value (bytea, or any type
// stored like one, e.g. a data type declared with //gopgext:type over
// pg.Varlena). Unlike []byte, a Varlena argument is not copied: it refers
// to PostgreSQL's memory and is only valid during the call (copy it with
// bytes.Clone to keep it), and it must not be modified. Results are copied.
type Varlena = core.Varlena

// StringInfo is the buffer the receive function of a data type reads its
// binary input from (the SQL type internal): the function must consume all
// of it, with Read* calls or ReadAll. The send function is the reverse: it
// returns a []byte (bytea); see encoding/binary's BigEndian.Append* for the
// network byte order receive functions read.
type StringInfo = core.StringInfo

// Numeric is the SQL type numeric, in its text form ("12.50", "NaN"): use
// math/big or a decimal package for arithmetic. A result that isn't a valid
// numeric raises numeric's input error. (time.Time is timestamptz; it
// can't represent infinite timestamps, which raise an error.)
type Numeric = core.Numeric

// Point is the SQL type point: (X, Y). Arrays (point[]) work too.
type Point = core.Point

// AnyElement is the SQL type anyelement: a value of the type the function
// is called with (Type tells which). String formats it, and ParseAny makes
// a value of a type from text. A polymorphic result must be of the type
// PostgreSQL resolved for the call (e.g. that of an anyelement argument).
// Values refer to PostgreSQL's memory: valid during the call.
type AnyElement = core.AnyElement

// AnyArray is the SQL type anyarray: an array of any element type.
type AnyArray = core.AnyArray

// ParseAny returns a value of type typ from its text form.
func ParseAny(typ Oid, s string) (AnyElement, error) { return core.ParseAny(typ, s) }

// NewAnyArray returns a one-dimensional array of type elem[] (nil elements
// are NULL).
func NewAnyArray(elem Oid, xs []*AnyElement) AnyArray { return core.NewAnyArray(elem, xs) }

//export gopgext_float8_out
func float8Out(x float64, out **byte) uintptr

// FormatFloat8 formats x as float8's output function does: the shortest
// text that reads back as x, or with extra_float_digits <= 0 that many
// fewer digits (%.15g by default), and NaN, Infinity, -Infinity. For data
// types whose output includes float8 values (like cube's coordinates).
func FormatFloat8(x float64) string {
	var p *byte
	core.Check(float8Out(x, &p))
	return core.GoString(p)
}
