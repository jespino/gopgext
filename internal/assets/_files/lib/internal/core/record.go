package core

import (
	"errors"
	"unsafe"
)

// Call is the struct gopgext_call of the innermost call from PostgreSQL
// into Go that is running (0 outside calls): fmgr.Call.Begin sets it.
var Call uintptr

// UUID is a value of SQL type uuid (pg.UUID): its 16 bytes.
type UUID [16]byte

const hexDigits = "0123456789abcdef"

// String returns the standard form, e.g.
// "a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a11".
func (u UUID) String() string {
	b := make([]byte, 0, 36)
	for i, x := range u {
		if i == 4 || i == 6 || i == 8 || i == 10 {
			b = append(b, '-')
		}
		b = append(b, hexDigits[x>>4], hexDigits[x&15])
	}
	return string(b)
}

// ParseUUID parses a UUID in the forms uuid's input function accepts:
// hexadecimal digits, optionally in braces, with a hyphen allowed after any
// group of four digits ("a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a11",
// "{a0eebc999c0b4ef8bb6d6bb9bd380a11}").
func ParseUUID(s string) (UUID, error) {
	var u UUID
	errInvalid := errors.New(`invalid input syntax for type uuid: "` + s + `"`)
	braces := len(s) > 0 && s[0] == '{'
	if braces {
		s = s[1:]
	}
	for i := 0; i < 16; i++ {
		if len(s) < 2 {
			return u, errInvalid
		}
		hi, lo := unhex(s[0]), unhex(s[1])
		if hi < 0 || lo < 0 {
			return u, errInvalid
		}
		u[i] = byte(hi<<4 | lo)
		s = s[2:]
		if i%2 == 1 && i < 15 && len(s) > 0 && s[0] == '-' {
			s = s[1:]
		}
	}
	if braces {
		if len(s) == 0 || s[0] != '}' {
			return u, errInvalid
		}
		s = s[1:]
	}
	if s != "" {
		return u, errInvalid
	}
	return u, nil
}

func unhex(c byte) int {
	switch {
	case c >= '0' && c <= '9':
		return int(c - '0')
	case c >= 'a' && c <= 'f':
		return int(c-'a') + 10
	case c >= 'A' && c <= 'F':
		return int(c-'A') + 10
	}
	return -1
}

// Record is a row whose columns the caller defines (pg.Record).
type Record []any

// cresultColumn mirrors struct gopgext_column (record.c).
type cresultColumn struct {
	name, typname *byte
	typ, typmod   int64
}

// ResultColumnsOf converts n struct gopgext_column.
func ResultColumnsOf(p unsafe.Pointer, n int64) []Column {
	cs := unsafe.Slice((*cresultColumn)(p), n)
	out := make([]Column, n)
	for i, c := range cs {
		out[i] = Column{Name: GoString(c.name), Type: Oid(c.typ), Typmod: int32(c.typmod), TypeName: GoString(c.typname)}
	}
	return out
}

//export gopgext_result_columns
func resultColumns(call uintptr, n *int64, cols *unsafe.Pointer) uintptr

// ResultColumns returns the columns of the row result of the running call
// (a function returning record, or a composite type).
func ResultColumns() ([]Column, error) {
	var n int64
	var p unsafe.Pointer
	if ed := resultColumns(Call, &n, &p); ed != 0 {
		return nil, FromErrorData(ed)
	}
	return ResultColumnsOf(p, n), nil
}

//export gopgext_format_type
func formatType(typ uint32, typmod int32, out **byte) uintptr

// FormatType returns the SQL name of a type with a type modifier
// (format_type_with_typemod).
func FormatType(typ Oid, typmod int32) string {
	var p *byte
	Check(formatType(uint32(typ), typmod, &p))
	return GoString(p)
}
