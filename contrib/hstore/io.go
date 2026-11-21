package main

// Input and output, constructors and conversions (hstore_io.c).

import (
	"encoding/binary"
	"fmt"
	"strings"

	"gopgext/pg"
)

// parser is the state of hstore_in's parser (HSParser). Positions past
// the end read as NUL, as in the C string.
type parser struct {
	s    []byte
	ptr  int
	word []byte
}

func (p *parser) c() byte {
	if p.ptr < len(p.s) {
		return p.s[p.ptr]
	}
	return 0
}

func isSpace(c byte) bool { // scanner_isspace
	return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\v' || c == '\f'
}

// mbLen is the length of the (UTF-8) character starting with c.
func mbLen(c byte) int {
	switch {
	case c < 0x80:
		return 1
	case c&0xe0 == 0xc0:
		return 2
	case c&0xf0 == 0xe0:
		return 3
	case c&0xf8 == 0xf0:
		return 4
	}
	return 1
}

func (p *parser) syntaxError() error {
	end := p.ptr + mbLen(p.c())
	if end > len(p.s) {
		end = len(p.s)
	}
	return pg.Errorf(pg.CodeSyntaxError, "syntax error in hstore, near \"%s\" at position %d", p.s[p.ptr:end], p.ptr)
}

func errEOF() error {
	return pg.Errorf(pg.CodeSyntaxError, "syntax error in hstore: unexpected end of string")
}

const (
	gvWaitVal = iota
	gvInVal
	gvInEscVal
	gvWaitEscIn
	gvWaitEscEscIn
)

// getVal reads a key (ignoreEq false) or a value into p.word; false at the
// end of the input.
func (p *parser) getVal(ignoreEq bool) (ok, escaped bool, err error) {
	st := gvWaitVal
	p.word = []byte{}
	for {
		c := p.c()
		switch st {
		case gvWaitVal:
			switch {
			case c == '"':
				escaped = true
				st = gvInEscVal
			case c == 0:
				return false, escaped, nil
			case c == '=' && !ignoreEq:
				return false, escaped, p.syntaxError()
			case c == '\\':
				st = gvWaitEscIn
			case !isSpace(c):
				p.word = append(p.word, c)
				st = gvInVal
			}
		case gvInVal:
			switch {
			case c == '\\':
				st = gvWaitEscIn
			case c == '=' && !ignoreEq:
				p.ptr--
				return true, escaped, nil
			case c == ',' && ignoreEq:
				p.ptr--
				return true, escaped, nil
			case isSpace(c):
				return true, escaped, nil
			case c == 0:
				p.ptr--
				return true, escaped, nil
			default:
				p.word = append(p.word, c)
			}
		case gvInEscVal:
			switch c {
			case '\\':
				st = gvWaitEscEscIn
			case '"':
				return true, escaped, nil
			case 0:
				return false, escaped, errEOF()
			default:
				p.word = append(p.word, c)
			}
		case gvWaitEscIn:
			if c == 0 {
				return false, escaped, errEOF()
			}
			p.word = append(p.word, c)
			st = gvInVal
		case gvWaitEscEscIn:
			if c == 0 {
				return false, escaped, errEOF()
			}
			p.word = append(p.word, c)
			st = gvInEscVal
		}
		p.ptr++
	}
}

const (
	wKey = iota
	wVal
	wEq
	wGt
	wDel
)

// parse reads the pairs of an hstore's text form (parse_hstore).
func parse(s []byte) ([]pair, error) {
	p := &parser{s: s}
	var pairs []pair
	st := wKey
	for {
		switch st {
		case wKey:
			ok, _, err := p.getVal(false)
			if err != nil {
				return nil, err
			}
			if !ok {
				return pairs, nil
			}
			if len(p.word) > maxKeyLen {
				return nil, errKeyTooLong()
			}
			pairs = append(pairs, pair{key: p.word})
			st = wEq
		case wEq:
			c := p.c()
			if c == '=' {
				st = wGt
			} else if c == 0 {
				return nil, errEOF()
			} else if !isSpace(c) {
				return nil, p.syntaxError()
			}
		case wGt:
			c := p.c()
			if c == '>' {
				st = wVal
			} else if c == 0 {
				return nil, errEOF()
			} else {
				return nil, p.syntaxError()
			}
		case wVal:
			ok, escaped, err := p.getVal(true)
			if err != nil {
				return nil, err
			}
			if !ok {
				return nil, errEOF()
			}
			if len(p.word) > maxValueLen {
				return nil, errValueTooLong()
			}
			last := &pairs[len(pairs)-1]
			last.val = p.word
			last.null = len(p.word) == 4 && !escaped && strings.EqualFold(string(p.word), "null")
			st = wDel
		case wDel:
			c := p.c()
			if c == ',' {
				st = wKey
			} else if c == 0 {
				return pairs, nil
			} else if !isSpace(c) {
				return nil, p.syntaxError()
			}
		}
		p.ptr++
	}
}

//gopgext:function symbol=hstore_in manual
func HstoreIn(s pg.CString) ([]byte, error) {
	pairs, err := parse([]byte(s))
	if err != nil {
		return nil, err
	}
	return fromPairs(uniquePairs(pairs)), nil
}

func appendQuoted(out, s []byte) []byte { // cpw, with the quotes
	out = append(out, '"')
	for _, c := range s {
		if c == '"' || c == '\\' {
			out = append(out, '\\')
		}
		out = append(out, c)
	}
	return append(out, '"')
}

//gopgext:function symbol=hstore_out manual
func HstoreOut(v pg.Varlena) pg.CString {
	h := load(v)
	var out []byte
	for i := 0; i < h.n; i++ {
		out = appendQuoted(out, h.key(i))
		out = append(out, "=>"...)
		if h.isNull(i) {
			out = append(out, "NULL"...)
		} else {
			out = appendQuoted(out, h.val(i))
		}
		if i+1 != h.n {
			out = append(out, ", "...)
		}
	}
	return pg.CString(out)
}

// maxPairs is MaxAllocSize / sizeof(Pairs).
const maxPairs = 0x3fffffff / 40

func errTooManyPairs(n int) error {
	return pg.Errorf("54000", "number of pairs (%d) exceeds the maximum allowed (%d)", n, maxPairs)
}

func errNullKey() error {
	return pg.Errorf(pg.CodeNullValueNotAllowed, "null value not allowed for hstore key")
}

//gopgext:function symbol=hstore_recv manual
func HstoreRecv(buf pg.StringInfo) ([]byte, error) {
	n := int(buf.ReadInt32())
	if n == 0 {
		return fromPairs(nil), nil
	}
	if n < 0 || n > maxPairs {
		return nil, errTooManyPairs(n)
	}
	pairs := make([]pair, n)
	for i := range pairs {
		l := int(buf.ReadInt32())
		if l < 0 {
			return nil, errNullKey()
		}
		pairs[i].key = append([]byte{}, buf.Read(l)...)
		l = int(buf.ReadInt32())
		if l < 0 {
			pairs[i].null = true
		} else {
			pairs[i].val = append([]byte{}, buf.Read(l)...)
		}
		if err := checkPair(pairs[i].key, pairs[i].val); err != nil {
			return nil, err
		}
	}
	return fromPairs(uniquePairs(pairs)), nil
}

//gopgext:function symbol=hstore_send manual
func HstoreSend(v pg.Varlena) []byte {
	h := load(v)
	out := binary.BigEndian.AppendUint32(nil, uint32(h.n))
	for i := 0; i < h.n; i++ {
		k := h.key(i)
		out = binary.BigEndian.AppendUint32(out, uint32(len(k)))
		out = append(out, k...)
		if h.isNull(i) {
			out = binary.BigEndian.AppendUint32(out, 0xFFFFFFFF)
		} else {
			val := h.val(i)
			out = binary.BigEndian.AppendUint32(out, uint32(len(val)))
			out = append(out, val...)
		}
	}
	return out
}

// hstore(text, text), tconvert: not STRICT ((key, NULL) is a pair).
//
//gopgext:function symbol=hstore_from_text manual
func HstoreFromText(key, val *pg.Varlena) (*[]byte, error) {
	if key == nil {
		return nil, nil
	}
	p := pair{key: *key, null: val == nil}
	if val != nil {
		p.val = *val
	}
	if err := checkPair(p.key, p.val); err != nil {
		return nil, err
	}
	b := fromPairs([]pair{p})
	return &b, nil
}

func errSubscripts() error {
	return pg.Errorf("2202E", "wrong number of array subscripts")
}

// texts returns the elements of a text[] (nil for NULL).
func texts(a pg.AnyArray) [][]byte {
	es := a.Elements()
	out := make([][]byte, len(es))
	for i, e := range es {
		if e != nil {
			out[i] = nonNil(e.Bytes())
		}
	}
	return out
}

func nonNil(b []byte) []byte {
	if b == nil {
		return []byte{}
	}
	return b
}

// hstore(text[], text[]): not STRICT (values can be NULL).
//
//gopgext:function symbol=hstore_from_arrays manual
func HstoreFromArrays(keys, vals *pg.AnyArray) (*[]byte, error) {
	if keys == nil {
		return nil, nil
	}
	kd := keys.Dims()
	if len(kd) > 1 {
		return nil, errSubscripts()
	}
	ks := texts(*keys)
	if len(ks) > maxPairs {
		return nil, errTooManyPairs(len(ks))
	}
	var vs [][]byte
	if vals != nil {
		vd := vals.Dims()
		if len(vd) > 1 {
			return nil, errSubscripts()
		}
		if (len(kd) > 0 || len(vd) > 0) &&
			(len(kd) != len(vd) || kd[0] != vd[0] || keys.LowerBounds()[0] != vals.LowerBounds()[0]) {
			return nil, pg.Errorf("2202E", "arrays must have same bounds")
		}
		vs = texts(*vals)
	}
	pairs := make([]pair, len(ks))
	for i, k := range ks {
		if k == nil {
			return nil, errNullKey()
		}
		pairs[i] = pair{key: k, null: true}
		if vs != nil && vs[i] != nil {
			pairs[i] = pair{key: k, val: vs[i]}
		}
		if err := checkPair(pairs[i].key, pairs[i].val); err != nil {
			return nil, err
		}
	}
	b := fromPairs(uniquePairs(pairs))
	return &b, nil
}

// hstore(text[]): {k, v, k, v...} or {{k, v}, {k, v}...}.
//
//gopgext:function symbol=hstore_from_array manual
func HstoreFromArray(a pg.AnyArray) ([]byte, error) {
	dims := a.Dims()
	switch len(dims) {
	case 0:
		return fromPairs(nil), nil
	case 1:
		if dims[0]%2 != 0 {
			return nil, pg.Errorf("2202E", "array must have even number of elements")
		}
	case 2:
		if dims[1] != 2 {
			return nil, pg.Errorf("2202E", "array must have two columns")
		}
	default:
		return nil, errSubscripts()
	}
	es := texts(a)
	count := len(es) / 2
	if count > maxPairs {
		return nil, errTooManyPairs(count)
	}
	pairs := make([]pair, count)
	for i := range pairs {
		k, v := es[2*i], es[2*i+1]
		if k == nil {
			return nil, errNullKey()
		}
		pairs[i] = pair{key: k, val: v, null: v == nil}
		if err := checkPair(k, v); err != nil {
			return nil, err
		}
	}
	return fromPairs(uniquePairs(pairs)), nil
}

const recordOID = 2249

// hstore(record): the columns as pairs. Not STRICT: a NULL record makes
// NULL values.
//
//gopgext:function symbol=hstore_from_record manual
func HstoreFromRecord(rec *pg.AnyElement) ([]byte, error) {
	typ, typmod := pg.ArgType(0), int32(-1)
	var fields []*pg.AnyElement
	if rec != nil {
		typ, typmod = rec.RowType()
		fields = rec.Fields()
	}
	cols, err := pg.Columns(typ, typmod)
	if err != nil {
		return nil, err
	}
	var pairs []pair
	for i, c := range cols {
		if c.Dropped {
			continue
		}
		p := pair{key: []byte(c.Name), null: true}
		if fields != nil && fields[i] != nil {
			p = pair{key: p.key, val: []byte(fields[i].String())}
		}
		if err := checkPair(p.key, p.val); err != nil {
			return nil, err
		}
		pairs = append(pairs, p)
	}
	return fromPairs(uniquePairs(pairs)), nil
}

// populate_record(anyelement, hstore), #=: the record with the columns
// named by keys set from the values. Not STRICT: a NULL record gives a
// record with only those columns.
//
//gopgext:function symbol=hstore_populate_record manual
func HstorePopulateRecord(rec *pg.AnyElement, hv *pg.Varlena) (*pg.AnyElement, error) {
	argtype := pg.ArgType(0)
	if !pg.IsComposite(argtype) {
		return nil, pg.Errorf("42804", "first argument must be a rowtype")
	}
	if hv == nil {
		return rec, nil
	}
	h := load(*hv)
	if h.n == 0 && rec != nil {
		return rec, nil
	}
	typ, typmod := argtype, int32(-1)
	var values []*pg.AnyElement
	if rec != nil {
		var rtyp pg.Oid
		rtyp, typmod = rec.RowType()
		if argtype == recordOID {
			typ = rtyp
		}
		values = rec.Fields()
	}
	cols, err := pg.Columns(typ, typmod)
	if err != nil {
		return nil, err
	}
	if values == nil {
		values = make([]*pg.AnyElement, len(cols))
	}
	for i, c := range cols {
		if c.Dropped {
			values[i] = nil
			continue
		}
		idx := h.find(nil, []byte(c.Name))
		if idx < 0 && rec != nil {
			continue
		}
		var s *string
		if idx >= 0 {
			s = h.value(idx)
		}
		v, err := pg.Input(c.Type, c.Typmod, s)
		if err != nil {
			return nil, err
		}
		values[i] = v
	}
	r, err := pg.NewRecord(typ, typmod, values)
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// ---- JSON ---------------------------------------------------------------

// appendJSONString is escape_json.
func appendJSONString(out, s []byte) []byte {
	out = append(out, '"')
	for _, c := range s {
		switch c {
		case '\b':
			out = append(out, `\b`...)
		case '\f':
			out = append(out, `\f`...)
		case '\n':
			out = append(out, `\n`...)
		case '\r':
			out = append(out, `\r`...)
		case '\t':
			out = append(out, `\t`...)
		case '"':
			out = append(out, `\"`...)
		case '\\':
			out = append(out, `\\`...)
		default:
			if c < ' ' {
				out = append(out, fmt.Sprintf(`\u%04x`, c)...)
			} else {
				out = append(out, c)
			}
		}
	}
	return append(out, '"')
}

// isJSONNumber is IsValidJsonNumber: -?(0|[1-9][0-9]*)(.[0-9]+)?([eE][+-]?[0-9]+)?
func isJSONNumber(s []byte) bool {
	i := 0
	if len(s) > 0 && s[0] == '-' {
		i++
	}
	digits := func() int {
		n := 0
		for i < len(s) && s[i] >= '0' && s[i] <= '9' {
			i++
			n++
		}
		return n
	}
	switch {
	case i < len(s) && s[i] == '0':
		i++
	case i < len(s) && s[i] >= '1' && s[i] <= '9':
		digits()
	default:
		return false
	}
	if i < len(s) && s[i] == '.' {
		i++
		if digits() == 0 {
			return false
		}
	}
	if i < len(s) && (s[i] == 'e' || s[i] == 'E') {
		i++
		if i < len(s) && (s[i] == '+' || s[i] == '-') {
			i++
		}
		if digits() == 0 {
			return false
		}
	}
	return i == len(s)
}

// toJSON returns the JSON object; loose guesses booleans and numbers.
func toJSON(v pg.Varlena, loose bool) string {
	h := load(v)
	if h.n == 0 {
		return "{}"
	}
	out := []byte{'{'}
	for i := 0; i < h.n; i++ {
		out = appendJSONString(out, h.key(i))
		out = append(out, ": "...)
		val := h.val(i)
		switch {
		case h.isNull(i):
			out = append(out, "null"...)
		case loose && len(val) == 1 && val[0] == 't':
			out = append(out, "true"...)
		case loose && len(val) == 1 && val[0] == 'f':
			out = append(out, "false"...)
		case loose && isJSONNumber(val):
			out = append(out, val...)
		default:
			out = appendJSONString(out, val)
		}
		if i+1 != h.n {
			out = append(out, ", "...)
		}
	}
	return string(append(out, '}'))
}

//gopgext:function symbol=hstore_to_json manual
func HstoreToJSON(v pg.Varlena) string { return toJSON(v, false) }

//gopgext:function symbol=hstore_to_json_loose manual
func HstoreToJSONLoose(v pg.Varlena) string { return toJSON(v, true) }

const jsonbOID = 3802

// The jsonb conversions go through jsonb's input function, which parses
// numbers with numeric_in like the original.

//gopgext:function symbol=hstore_to_jsonb manual
func HstoreToJSONB(v pg.Varlena) (pg.AnyElement, error) {
	return pg.ParseAny(jsonbOID, toJSON(v, false))
}

//gopgext:function symbol=hstore_to_jsonb_loose manual
func HstoreToJSONBLoose(v pg.Varlena) (pg.AnyElement, error) {
	return pg.ParseAny(jsonbOID, toJSON(v, true))
}
