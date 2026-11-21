// eventlog shows BRIN operator classes written in Go (gopgext/brin), for an
// append-only event log where values follow the physical order of rows:
//
//   - epoch, a timestamp in milliseconds (a fixed-size type over int64),
//     with a min/max summary per block range: range queries skip the
//     ranges outside [min, max].
//   - code, an event source name (a variable-size type over string), with
//     a Bloom filter summary per block range: equality queries skip the
//     ranges whose filter rules the code out (false positives are
//     rechecked by PostgreSQL).
package main

import (
	"encoding/binary"
	"hash/fnv"
	"strings"
	"time"

	"gopgext/brin"
	"gopgext/pg"
)

func main() {
	brin.Register("epoch_minmax_ops", brin.OpClass{
		Add:        minmaxAdd,
		Consistent: minmaxConsistent,
		Union:      minmaxUnion,
	})
	brin.Register("code_bloom_ops", brin.OpClass{
		Add:        bloomAdd,
		Consistent: bloomConsistent,
		Union:      bloomUnion,
	})
}

// ---- epoch ---------------------------------------------------------------

// Epoch is a point in time, in milliseconds since 1970-01-01 UTC.
//
//gopgext:type input=EpochIn output=EpochOut
type Epoch int64

// EpochIn parses RFC 3339 ('2024-05-01T10:00:00Z', with optional
// fractional seconds and offset).
//
//gopgext:function immutable parallel=safe
func EpochIn(s pg.CString) (Epoch, error) {
	t, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(string(s)))
	if err != nil {
		return 0, pg.Errorf(pg.CodeInvalidTextRepresentation, "invalid input syntax for type epoch: %q", string(s))
	}
	return Epoch(t.UnixMilli()), nil
}

// EpochOut formats an epoch as RFC 3339 in UTC, with milliseconds.
//
//gopgext:function immutable parallel=safe
func EpochOut(e Epoch) pg.CString {
	return pg.CString(time.UnixMilli(int64(e)).UTC().Format("2006-01-02T15:04:05.000Z"))
}

// EpochFromMillis converts milliseconds since 1970 to an epoch.
//
//gopgext:function name=epoch immutable parallel=safe
func EpochFromMillis(ms int64) Epoch { return Epoch(ms) }

// EpochMillis returns the milliseconds since 1970 of an epoch.
//
//gopgext:function immutable parallel=safe
func EpochMillis(e Epoch) int64 { return int64(e) }

//gopgext:function immutable parallel=safe
func EpochLt(a, b Epoch) bool { return a < b }

//gopgext:function immutable parallel=safe
func EpochLe(a, b Epoch) bool { return a <= b }

//gopgext:function immutable parallel=safe
func EpochEq(a, b Epoch) bool { return a == b }

//gopgext:function immutable parallel=safe
func EpochGe(a, b Epoch) bool { return a >= b }

//gopgext:function immutable parallel=safe
func EpochGt(a, b Epoch) bool { return a > b }

//gopgext:function immutable parallel=safe
func EpochCmp(a, b Epoch) int32 {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// The min/max summary: min and max, int64 little-endian. Values are the 8
// bytes of an epoch (little-endian: by-value types in native order).

func epochOf(b []byte) int64 { return int64(binary.LittleEndian.Uint64(b)) }

func minmaxSummary(lo, hi int64) []byte {
	s := make([]byte, 16)
	binary.LittleEndian.PutUint64(s, uint64(lo))
	binary.LittleEndian.PutUint64(s[8:], uint64(hi))
	return s
}

func minmaxAdd(summary, value []byte) ([]byte, bool) {
	v := epochOf(value)
	if summary == nil {
		return minmaxSummary(v, v), true
	}
	lo, hi := epochOf(summary), epochOf(summary[8:])
	if v >= lo && v <= hi {
		return summary, false
	}
	return minmaxSummary(min(lo, v), max(hi, v)), true
}

func minmaxConsistent(summary []byte, strategy int, query []byte) bool {
	lo, hi, q := epochOf(summary), epochOf(summary[8:]), epochOf(query)
	switch strategy {
	case 1: // <
		return lo < q
	case 2: // <=
		return lo <= q
	case 3: // =
		return lo <= q && q <= hi
	case 4: // >=
		return hi >= q
	case 5: // >
		return hi > q
	}
	return true // unknown strategy: can't rule the range out
}

func minmaxUnion(a, b []byte) []byte {
	return minmaxSummary(min(epochOf(a), epochOf(b)), max(epochOf(a[8:]), epochOf(b[8:])))
}

// ---- code ----------------------------------------------------------------

// Code is the name of an event source: letters, digits, '-', '_' and '.'.
//
//gopgext:type input=CodeIn output=CodeOut
type Code string

// CodeIn checks a code.
//
//gopgext:function immutable parallel=safe
func CodeIn(s pg.CString) (Code, error) {
	if len(s) == 0 || len(s) > 64 {
		return "", pg.Errorf(pg.CodeInvalidTextRepresentation, "invalid input syntax for type code: %q", string(s))
	}
	for _, c := range []byte(s) {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.') {
			return "", pg.Errorf(pg.CodeInvalidTextRepresentation, "invalid input syntax for type code: %q", string(s))
		}
	}
	return Code(strings.Clone(string(s))), nil
}

//gopgext:function immutable parallel=safe
func CodeOut(c Code) pg.CString { return pg.CString(c) }

//gopgext:function immutable parallel=safe
func CodeEq(a, b Code) bool { return a == b }

// The Bloom summary: a 512-bit filter, 3 bits per code.

const bloomBits = 512

func bloomPositions(value []byte) [3]uint32 {
	h := fnv.New64a()
	h.Write(value)
	sum := h.Sum64()
	h1, h2 := uint32(sum), uint32(sum>>32)|1
	return [3]uint32{h1 % bloomBits, (h1 + h2) % bloomBits, (h1 + 2*h2) % bloomBits}
}

func bloomAdd(summary, value []byte) ([]byte, bool) {
	s := make([]byte, bloomBits/8)
	copy(s, summary)
	changed := summary == nil
	for _, p := range bloomPositions(value) {
		if s[p/8]&(1<<(p%8)) == 0 {
			s[p/8] |= 1 << (p % 8)
			changed = true
		}
	}
	return s, changed
}

func bloomConsistent(summary []byte, strategy int, query []byte) bool {
	if strategy != 1 { // =
		return true
	}
	for _, p := range bloomPositions(query) {
		if summary[p/8]&(1<<(p%8)) == 0 {
			return false
		}
	}
	return true
}

func bloomUnion(a, b []byte) []byte {
	s := make([]byte, bloomBits/8)
	for i := range s {
		s[i] = a[i] | b[i]
	}
	return s
}
