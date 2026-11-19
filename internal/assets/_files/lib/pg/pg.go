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
