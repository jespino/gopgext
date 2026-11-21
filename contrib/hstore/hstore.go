// hstore is a Go port of PostgreSQL's contrib/hstore (version 1.8): sets
// of key/value pairs within a single value.
//
// Values have upstream's binary format, so data, dumps and the binary
// protocol are interchangeable with the C module. The SQL objects are
// upstream's install scripts in sql/, with the C symbols of the index and
// subscripting support functions pointed at gopgext's frameworks (see
// README.md).
//
// Ported from PostgreSQL 18's contrib/hstore (hstore.h, hstore_io.c,
// hstore_op.c, hstore_compat.c, hstore_gist.c, hstore_gin.c,
// hstore_subs.c).
package main

import (
	"bytes"
	"encoding/binary"

	"gopgext/pg"
)

// The format (hstore.h). After the varlena header: a uint32 with the
// count and flags, then two HEntry (uint32) per pair, key and value, then
// the strings. An HEntry holds the end position of its string in the
// string area; the first one is flagged, and a NULL value has an empty
// string and the ISNULL flag. Pairs are sorted by key length, then key
// bytes, and keys are unique.
const (
	flagNewVersion = 0x80000000
	countMask      = 0x0FFFFFFF

	entryIsFirst = 0x80000000
	entryIsNull  = 0x40000000
	entryPosMask = 0x3FFFFFFF

	maxKeyLen   = 0x3FFFFFFF
	maxValueLen = 0x3FFFFFFF

	hdrSize = 4 // the count, after the varlena header
)

var le = binary.LittleEndian

// hstore is a value: its bytes after the varlena header.
type hstore struct {
	b []byte
	n int
}

// load reads a value (DatumGetHStoreP: old formats are converted).
func load(v []byte) hstore {
	b := upgrade(v)
	return hstore{b, int(le.Uint32(b) & countMask)}
}

func (h hstore) entry(j int) uint32 { return le.Uint32(h.b[hdrSize+4*j:]) }

func endPos(e uint32) int { return int(e & entryPosMask) }

// off and length of string j (2i: key i, 2i+1: value i).
func (h hstore) off(j int) int {
	if h.entry(j)&entryIsFirst != 0 {
		return 0
	}
	return endPos(h.entry(j - 1))
}

func (h hstore) strs() []byte { return h.b[hdrSize+8*h.n:] }

func (h hstore) str(j int) []byte {
	o := h.off(j)
	return h.strs()[o:endPos(h.entry(j))]
}

func (h hstore) key(i int) []byte  { return h.str(2 * i) }
func (h hstore) val(i int) []byte  { return h.str(2*i + 1) }
func (h hstore) isNull(i int) bool { return h.entry(2*i+1)&entryIsNull != 0 }
func (h hstore) keyLen(i int) int  { return len(h.key(i)) }
func (h hstore) value(i int) *string { // the value as a NULL-able string
	if h.isNull(i) {
		return nil
	}
	s := string(h.val(i))
	return &s
}

// dataLen is the length of the string area.
func (h hstore) dataLen() int {
	if h.n == 0 {
		return 0
	}
	return endPos(h.entry(2*h.n - 1))
}

// cmpKey orders keys like hstore does: by length, then bytes.
func cmpKey(a, b []byte) int {
	if len(a) != len(b) {
		if len(a) > len(b) {
			return 1
		}
		return -1
	}
	return bytes.Compare(a, b)
}

// find returns the index of key, or -1 (hstoreFindKey). lowbound, if not
// nil, bounds the search from below and is updated, for keys searched in
// ascending order.
func (h hstore) find(lowbound *int, key []byte) int {
	lo, hi := 0, h.n
	if lowbound != nil {
		lo = *lowbound
	}
	for lo < hi {
		mid := lo + (hi-lo)/2
		d := cmpKey(h.key(mid), key)
		if d == 0 {
			if lowbound != nil {
				*lowbound = mid + 1
			}
			return mid
		} else if d < 0 {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	if lowbound != nil {
		*lowbound = lo
	}
	return -1
}

// pair is a key and value (Pairs).
type pair struct {
	key, val []byte
	null     bool
}

// builder builds a value from pairs added in order (HS_ADDITEM,
// HS_COPYITEM, HS_FINALIZE).
type builder struct {
	ents []uint32
	buf  []byte
}

func (b *builder) add(key, val []byte, null bool) {
	b.buf = append(b.buf, key...)
	b.ents = append(b.ents, uint32(len(b.buf))&entryPosMask)
	if null {
		b.ents = append(b.ents, uint32(len(b.buf))&entryPosMask|entryIsNull)
		return
	}
	b.buf = append(b.buf, val...)
	b.ents = append(b.ents, uint32(len(b.buf))&entryPosMask)
}

// copyFrom adds pair i of h.
func (b *builder) copyFrom(h hstore, i int) { b.add(h.key(i), h.val(i), h.isNull(i)) }

func (b *builder) bytes() []byte {
	n := len(b.ents) / 2
	out := make([]byte, hdrSize+4*len(b.ents), hdrSize+4*len(b.ents)+len(b.buf))
	le.PutUint32(out, uint32(n)|flagNewVersion)
	for j, e := range b.ents {
		if j == 0 {
			e |= entryIsFirst
		}
		le.PutUint32(out[hdrSize+4*j:], e)
	}
	return append(out, b.buf...)
}

// fromPairs builds a value from sorted, unique pairs (hstorePairs).
func fromPairs(ps []pair) []byte {
	var b builder
	for _, p := range ps {
		b.add(p.key, p.val, p.null)
	}
	return b.bytes()
}

// uniquePairs sorts pairs by key and drops later duplicates
// (hstoreUniquePairs).
func uniquePairs(ps []pair) []pair {
	if len(ps) < 2 {
		return ps
	}
	pgQsort(ps, func(x, y *pair) int { return cmpKey(x.key, y.key) })
	out := ps[:1]
	for _, p := range ps[1:] {
		if !bytes.Equal(p.key, out[len(out)-1].key) {
			out = append(out, p)
		}
	}
	return out
}

func errKeyTooLong() error {
	return pg.Errorf(pg.CodeStringDataRightTruncation, "string too long for hstore key")
}

func errValueTooLong() error {
	return pg.Errorf(pg.CodeStringDataRightTruncation, "string too long for hstore value")
}

func checkPair(key, val []byte) error {
	if len(key) > maxKeyLen {
		return errKeyTooLong()
	}
	if len(val) > maxValueLen {
		return errValueTooLong()
	}
	return nil
}

// ---- Old formats (hstore_compat.c) -------------------------------------

// validNew and validOld check a value against the current format and the
// one of contrib/hstore before PostgreSQL 9.0: 0 not valid, 1 valid with
// slop at the end, 2 exactly valid. vsize is the varlena size.
func validNew(b []byte) int {
	size := le.Uint32(b)
	if size&flagNewVersion != 0 {
		return 2
	}
	count := int(size & countMask)
	if count == 0 {
		return 2
	}
	vsize := len(b) + 4
	if hdrSize+8*count > len(b) {
		return 0
	}
	h := hstore{b, count}
	if h.entry(0)&entryIsFirst == 0 {
		return 0
	}
	if 4+hdrSize+8*count+h.dataLen() > vsize {
		return 0
	}
	for i := 1; i < 2*count; i++ {
		if h.entry(i)&entryIsFirst != 0 || endPos(h.entry(i)) < endPos(h.entry(i-1)) {
			return 0
		}
	}
	for i := 1; i < count; i++ {
		if h.keyLen(i) < h.keyLen(i-1) || h.entry(2*i)&entryIsNull != 0 {
			return 0
		}
	}
	if 4+hdrSize+8*count+h.dataLen() != vsize {
		return 1
	}
	return 2
}

// oldEntry is HOldEntry: uint16 keylen, uint16 vallen, then a uint32 with
// valisnull in bit 0 and pos in the other bits (x86-64 bit-field layout).
func oldEntry(b []byte, i int) (keylen, vallen, pos int, null bool) {
	p := b[hdrSize+8*i:]
	w := le.Uint32(p[4:])
	return int(le.Uint16(p)), int(le.Uint16(p[2:])), int(w >> 1), w&1 != 0
}

func validOld(b []byte) int {
	size := le.Uint32(b)
	if size&flagNewVersion != 0 {
		return 0
	}
	count := int(size)
	if count == 0 {
		return 2
	}
	if count > 0xFFFFFFF {
		return 0
	}
	vsize := len(b) + 4
	if 4+hdrSize+8*count > vsize {
		return 0
	}
	if _, _, pos, _ := oldEntry(b, 0); pos != 0 {
		return 0
	}
	for i := 1; i < count; i++ {
		k, _, _, _ := oldEntry(b, i)
		k0, _, _, _ := oldEntry(b, i-1)
		if k < k0 {
			return 0
		}
	}
	last := 0
	for i := 0; i < count; i++ {
		k, v, pos, null := oldEntry(b, i)
		if pos != last {
			return 0
		}
		last += k
		if !null {
			last += v
		}
	}
	if 4+hdrSize+8*count+last > vsize {
		return 0
	}
	if 4+hdrSize+8*count+last != vsize {
		return 1
	}
	return 2
}

// upgrade converts a value in an old format to the current one
// (hstoreUpgrade), or returns it as is.
func upgrade(v []byte) []byte {
	if len(v) < hdrSize {
		panic(&pg.Error{Code: pg.CodeInternalError, Message: "invalid hstore value found"})
	}
	if le.Uint32(v)&flagNewVersion != 0 {
		return v
	}
	b := bytes.Clone(v)
	size := le.Uint32(b)
	fix := func() []byte { // HS_SETCOUNT, HS_FIXSIZE
		n := int(size & countMask)
		le.PutUint32(b, uint32(n)|flagNewVersion)
		return b[:hdrSize+8*n+hstore{b, n}.dataLen()]
	}
	if size == 0 || (len(b)+4 < 32768 && le.Uint32(b[hdrSize:])&entryIsFirst != 0) {
		return fix()
	}
	vn, vo := validNew(b), validOld(b)
	if vo == 0 || size == 0 {
		if vn != 0 {
			return fix()
		}
		panic(&pg.Error{Code: pg.CodeInternalError, Message: "invalid hstore value found"})
	}
	if vn != 0 {
		pg.Warning("ambiguous hstore value resolved as hstore-old")
	}
	count := int(size)
	ents := make([]uint32, 2*count)
	for i := 0; i < count; i++ {
		k, v, pos, null := oldEntry(b, i)
		if null {
			v = 0
		}
		ents[2*i] = uint32(pos+k) & entryPosMask
		ents[2*i+1] = uint32(pos+k+v) & entryPosMask
		if null {
			ents[2*i+1] |= entryIsNull
		}
	}
	if count > 0 {
		ents[0] |= entryIsFirst
	}
	for j, e := range ents {
		le.PutUint32(b[hdrSize+4*j:], e)
	}
	return fix()
}

//gopgext:function symbol=hstore_version_diag manual
func HstoreVersionDiag(h pg.Varlena) int32 {
	return int32(validOld(h)*10 + validNew(h))
}

func main() {
	registerIndexes()
	registerSubscript()
}
