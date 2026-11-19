// Package core is state and C bindings shared by gopgext/pg and
// gopgext/fmgr.
package core

import "unsafe"

// Seq identifies the innermost call from PostgreSQL into Go that is running
// (each call gets a new number). Values tied to one call, like the
// PostgreSQL error behind an Error, are only valid while it runs.
var Seq uint64

// ReadOnly is set while running a STABLE or IMMUTABLE function: SPI then
// runs statements read-only, as PostgreSQL requires.
var ReadOnly bool

var lastSeq uint64

// NextSeq returns a new call number.
func NextSeq() uint64 {
	lastSeq++
	return lastSeq
}

// CString is a NUL-terminated C string (pg.CString, SQL type cstring).
type CString string

// Error is a PostgreSQL error (pg.Error).
type Error struct {
	Code    string // SQLSTATE, e.g. "22012" (division_by_zero)
	Message string
	Detail  string
	Hint    string
	Context string // where it happened (a PostgreSQL error's CONTEXT)

	edata uintptr // the original ErrorData, valid while call seq runs
	seq   uint64
}

func (e *Error) Error() string { return e.Message }

type cerrinfo struct {
	sqlstate                       int64
	message, detail, hint, context *byte
}

//export gopgext_error_info
func errorInfo(edata uintptr, out *cerrinfo)

// FromErrorData converts an ErrorData caught by a C helper during the
// current call.
func FromErrorData(edata uintptr) *Error {
	var info cerrinfo
	errorInfo(edata, &info)
	return &Error{
		Code:    UnpackSQLState(int32(info.sqlstate)),
		Message: GoString(info.message),
		Detail:  GoString(info.detail),
		Hint:    GoString(info.hint),
		Context: GoString(info.context),
		edata:   edata,
		seq:     Seq,
	}
}

// ErrorData returns the original ErrorData of e if it can be re-thrown as is
// (e came from PostgreSQL during the current call), or 0.
func ErrorData(e *Error) uintptr {
	if e.edata != 0 && e.seq == Seq {
		return e.edata
	}
	return 0
}

// PackSQLState converts a 5-character SQLSTATE to PostgreSQL's integer
// form (MAKE_SQLSTATE). It returns 0 if code isn't valid.
func PackSQLState(code string) int32 {
	if len(code) != 5 {
		return 0
	}
	var v int32
	for i := 0; i < 5; i++ {
		c := code[i]
		if !(c >= '0' && c <= '9' || c >= 'A' && c <= 'Z') {
			return 0
		}
		v |= int32((c-'0')&0x3F) << (6 * i)
	}
	return v
}

// UnpackSQLState is the inverse of PackSQLState.
func UnpackSQLState(v int32) string {
	var b [5]byte
	for i := range b {
		b[i] = byte((v>>(6*i))&0x3F) + '0'
	}
	return string(b[:])
}

//export strlen
func cStrlen(p *byte) uintptr

// GoString copies a NUL-terminated C string ("" for NULL).
func GoString(p *byte) string {
	if p == nil {
		return ""
	}
	return string(unsafe.Slice(p, cStrlen(p)))
}

// StringView returns a NUL-terminated C string as a Go string without
// copying it: it's only valid as long as the C memory is.
func StringView(p *byte) string {
	if p == nil {
		return ""
	}
	return unsafe.String(p, cStrlen(p))
}

// CBytes returns s as a NUL-terminated byte array for C.
func CBytes(s string) *byte {
	b := make([]byte, len(s)+1)
	copy(b, s)
	return &b[0]
}

// StringData returns a pointer to the bytes of s (nil if empty).
func StringData(s string) *byte {
	if len(s) == 0 {
		return nil
	}
	return unsafe.StringData(s)
}

// PendingError is the panic value used to unwind Go code when a C helper
// caught a PostgreSQL error that can't be handled in Go (it's re-thrown as is
// once the call returns to PostgreSQL).
type PendingError struct{ Err *Error }

func (p PendingError) Error() string { return p.Err.Message }

// Check panics with PendingError if a C helper returned an error.
func Check(edata uintptr) {
	if edata != 0 {
		panic(PendingError{FromErrorData(edata)})
	}
}

//export gopgext_name
func extName() *byte

// ExtensionName is the name of the extension (and of its library).
func ExtensionName() string { return GoString(extName()) }
