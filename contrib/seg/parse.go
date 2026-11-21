package main

import (
	"math"
	"strconv"
	"strings"

	"gopgext/pg"
)

// The text format, as segscan.l and segparse.y define it:
//
//	range:     boundary PLUMIN deviation | boundary RANGE boundary
//	         | boundary RANGE | RANGE boundary | boundary
//	boundary:  SEGFLOAT | EXTENSION SEGFLOAT
//	deviation: SEGFLOAT
//
// RANGE is ".." or "...", PLUMIN "'+-'" or "(+-)", EXTENSION one of < > ~.
// Errors come in the order Bison's parser finds them: a bound's float is
// converted (and may be out of range) as soon as it's read; the swapped
// boundaries check runs before what follows the range is looked at.

type tokKind int

const (
	tEOF tokKind = iota
	tFloat
	tRange
	tPlumin
	tExt
	tGarbage
)

type token struct {
	kind tokKind
	text string
}

type scanner struct {
	s   string
	pos int
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func (sc *scanner) digits(i int) int {
	for i < len(sc.s) && isDigit(sc.s[i]) {
		i++
	}
	return i
}

// floatLen is the length of {float} at i: [+-]?[0-9]+(\.[0-9]+)?([eE][+-]?[0-9]+)?
func (sc *scanner) floatLen(i int) int {
	s, j := sc.s, i
	if j < len(s) && (s[j] == '+' || s[j] == '-') {
		j++
	}
	k := sc.digits(j)
	if k == j {
		return 0
	}
	j = k
	if j+1 < len(s) && s[j] == '.' && isDigit(s[j+1]) {
		j = sc.digits(j + 1)
	}
	if j < len(s) && (s[j] == 'e' || s[j] == 'E') {
		k := j + 1
		if k < len(s) && (s[k] == '+' || s[k] == '-') {
			k++
		}
		if e := sc.digits(k); e > k {
			j = e
		}
	}
	return j - i
}

func (sc *scanner) next() token {
	s := sc.s
	for sc.pos < len(s) && strings.IndexByte(" \t\n\r\f\v", s[sc.pos]) >= 0 {
		sc.pos++
	}
	i := sc.pos
	if i >= len(s) {
		return token{kind: tEOF}
	}
	tok := func(k tokKind, n int) token {
		sc.pos += n
		return token{k, s[i:sc.pos]}
	}
	switch rest := s[i:]; {
	case strings.HasPrefix(rest, "..."):
		return tok(tRange, 3)
	case strings.HasPrefix(rest, ".."):
		return tok(tRange, 2)
	case strings.HasPrefix(rest, "'+-'"), strings.HasPrefix(rest, "(+-)"):
		return tok(tPlumin, 4)
	}
	if n := sc.floatLen(i); n > 0 {
		return tok(tFloat, n)
	}
	if c := s[i]; c == '<' || c == '>' || c == '~' {
		return tok(tExt, 1)
	}
	if s[i] >= 0x80 {
		// The scanner returns it as a negative char, which Bison takes for
		// the end of the input.
		return tok(tEOF, 1)
	}
	return tok(tGarbage, 1)
}

func syntaxError(t token) error {
	detail := `syntax error at or near "` + t.text + `"`
	if t.text == "" {
		detail = "syntax error at end of input"
	}
	return &pg.Error{Code: "42601", Message: "bad seg representation", Detail: detail}
}

// bound is a boundary: its value, kind (0 or < > ~) and significant digits.
type bound struct {
	val  float32
	ext  byte
	sigd int8
}

type parser struct{ sc scanner }

// boundary parses a boundary starting at t (SEGFLOAT or EXTENSION).
func (p *parser) boundary(t token) (bound, error) {
	var b bound
	if t.kind == tExt {
		b.ext = t.text[0]
		if t = p.sc.next(); t.kind != tFloat {
			return b, syntaxError(t)
		}
	}
	err := b.read(t.text)
	return b, err
}

func (b *bound) read(text string) error {
	v, err := float4In(text)
	b.val, b.sigd = v, int8(sigDigits(text))
	return err
}

func (p *parser) end() error {
	if t := p.sc.next(); t.kind != tEOF {
		return syntaxError(t)
	}
	return nil
}

func parseSeg(str string) (seg, error) {
	p := &parser{sc: scanner{s: str}}
	var r seg
	t := p.sc.next()
	switch t.kind {
	case tRange: // RANGE boundary
		t = p.sc.next()
		if t.kind != tFloat && t.kind != tExt {
			return r, syntaxError(t)
		}
		b, err := p.boundary(t)
		if err != nil {
			return r, err
		}
		r = seg{lower: float32(math.Inf(-1)), upper: b.val, uSigd: b.sigd, lExt: '-', uExt: b.ext}
		return r, p.end()
	case tFloat, tExt:
	default:
		return r, syntaxError(t)
	}
	b1, err := p.boundary(t)
	if err != nil {
		return r, err
	}
	switch t := p.sc.next(); t.kind {
	case tEOF: // boundary
		return seg{lower: b1.val, upper: b1.val, lSigd: b1.sigd, uSigd: b1.sigd, lExt: b1.ext, uExt: b1.ext}, nil
	case tPlumin: // boundary PLUMIN deviation
		t = p.sc.next()
		if t.kind != tFloat {
			return r, syntaxError(t)
		}
		var d bound
		if err := d.read(t.text); err != nil {
			return r, err
		}
		r.lower = b1.val - d.val
		r.upper = b1.val + d.val
		sigd := max(b1.sigd, d.sigd)
		r.lSigd = max(int8(sigDigits(formatG(r.lower))), sigd)
		r.uSigd = max(int8(sigDigits(formatG(r.upper))), sigd)
		return r, p.end()
	case tRange:
		switch t := p.sc.next(); t.kind {
		case tEOF: // boundary RANGE
			return seg{lower: b1.val, upper: float32(math.Inf(1)), lSigd: b1.sigd, lExt: b1.ext, uExt: '-'}, nil
		case tFloat, tExt: // boundary RANGE boundary
			b2, err := p.boundary(t)
			if err != nil {
				return r, err
			}
			if b1.val > b2.val {
				return r, &pg.Error{Code: "22023",
					Message: "swapped boundaries: " + formatG(b1.val) + " is greater than " + formatG(b2.val)}
			}
			r = seg{lower: b1.val, upper: b2.val, lSigd: b1.sigd, uSigd: b2.sigd, lExt: b1.ext, uExt: b2.ext}
			return r, p.end()
		default:
			return r, syntaxError(t)
		}
	default:
		return r, syntaxError(t)
	}
}

// float4In is float4in_internal for a token of the scanner: strtof, with
// PostgreSQL's errors for values out of range (overflow, or underflow to
// zero; denormal results are accepted).
func float4In(s string) (float32, error) {
	x, err := strconv.ParseFloat(s, 32)
	if err != nil || (x == 0 && nonZeroMantissa(s)) {
		return 0, &pg.Error{Code: "22003", Message: `"` + s + `" is out of range for type real`}
	}
	return float32(x), nil
}

func nonZeroMantissa(s string) bool {
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c == 'e' || c == 'E':
			return false
		case c >= '1' && c <= '9':
			return true
		}
	}
	return false
}

// sigDigits is sig_digits: significant_digits clamped to FLT_DIG.
func sigDigits(s string) int { return min(significantDigits(s), fltDig) }

// significantDigits counts the significant digits of a number's text
// (the zeros of 0.00 count).
func significantDigits(s string) int {
	i := 0
	for i < len(s) && (s[i] == '0' || s[i] == '+' || s[i] == '-') {
		i++
	}
	zeroes := 1
	for i < len(s) && (s[i] == '0' || s[i] == '.') {
		if s[i] != '.' {
			zeroes++
		}
		i++
	}
	n := 0
	for ; i < len(s) && (isDigit(s[i]) || s[i] == '.'); i++ {
		if s[i] != '.' {
			n++
		}
	}
	if n == 0 {
		return zeroes
	}
	return n
}

// cSpecial is C's printf text for infinities and NaNs ("" for others).
func cSpecial(x float64) string {
	switch {
	case math.IsInf(x, 1):
		return "inf"
	case math.IsInf(x, -1):
		return "-inf"
	case math.IsNaN(x) && math.Signbit(x):
		return "-nan"
	case math.IsNaN(x):
		return "nan"
	}
	return ""
}

// formatG is C's "%g" of a float (promoted to double).
func formatG(f float32) string {
	x := float64(f)
	if s := cSpecial(x); s != "" {
		return s
	}
	return strconv.FormatFloat(x, 'g', 6, 64)
}

// restore prints val with exactly n significant digits (FLT_DIG at most):
// like %.ng, but 8.00 where %.ng prints 8. A transcription of seg.c's
// restore(), buffer manipulations included.
func restore(val float32, n int) string {
	if n <= 0 {
		n = fltDig
	} else {
		n = min(n, fltDig)
	}
	sign := 0
	if val < 0 {
		sign = 1
	}
	x := float64(val)
	if s := cSpecial(x); s != "" {
		return s
	}
	result := strconv.FormatFloat(x, 'e', n-1, 64) // %.*e
	e := strings.IndexByte(result, 'e')
	exp, _ := strconv.Atoi(result[e+1:])
	if exp == 0 {
		return result[:e]
	}
	if exp < -4 || exp > 4 {
		return result
	}
	var buf [25]byte
	for i := range 24 {
		buf[i] = '0'
	}
	cstr := func(from int) string { // the NUL-terminated string at buf[from:]
		to := from
		for to < len(buf) && buf[to] != 0 {
			to++
		}
		return string(buf[from:to])
	}
	// The mantissa's digits, without the decimal point, from buf[10].
	i, dp := 10, 0
	for p := sign; result[p] != 'e'; p, i = p+1, i+1 {
		buf[i] = result[p]
		if result[p] == '.' {
			dp = i
			i--
		}
	}
	if dp == 0 {
		dp = i
		i--
	}
	if exp > 0 {
		if dp-10+exp >= n {
			// The decimal point is beyond the last significant digit: put it
			// after the first digit, the rest goes to the exponent.
			exp = dp - 10 + exp - n
			buf[10+n] = 0
			if n > 1 {
				dp = 11
				for i = 23; i > dp; i-- {
					buf[i] = buf[i-1]
				}
				buf[dp] = '.'
			}
			at := 11
			if n > 1 {
				at = 11 + n
			}
			copy(buf[at:], "e"+strconv.Itoa(exp+n-1)+"\x00")
		} else {
			dp += exp
			for i = 23; i > dp; i-- {
				buf[i] = buf[i-1]
			}
			buf[11+n] = 0
			buf[dp] = '.'
		}
		if sign == 1 {
			buf[9] = '-'
			return cstr(9)
		}
		return cstr(10)
	}
	dp += exp - 1
	buf[10+n] = 0
	buf[dp] = '.'
	if sign == 1 {
		buf[dp-2] = '-'
		return cstr(dp - 2)
	}
	return cstr(dp - 1)
}
