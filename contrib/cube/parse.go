package main

import (
	"math"
	"strconv"
	"strings"

	"gopgext/pg"
)

// The text format, as cubescan.l and cubeparse.y define it:
//
//	box:        '[' paren_list ',' paren_list ']' | paren_list ',' paren_list | paren_list | list
//	paren_list: '(' list ')' | '(' ')'
//	list:       FLOAT | list ',' FLOAT
//
// The parser reports syntax errors at the token Bison's LALR(1) parser
// stops at, and runs each form's semantic checks when Bison reduces it:
// before looking at what follows it (Bison's default reductions), so a
// value's errors come before a syntax error after it.

type tokKind int

const (
	tEOF tokKind = iota
	tFloat
	tOBracket
	tCBracket
	tOParen
	tCParen
	tComma
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
	j := i
	for j < len(sc.s) && isDigit(sc.s[j]) {
		j++
	}
	return j
}

// floatLen is the length of {float} at i (0: no match):
// [+-]?({n}|{n}\.{n}?|\.{n})([eE][+-]?{n})?
func (sc *scanner) floatLen(i int) int {
	s, j := sc.s, i
	if j < len(s) && (s[j] == '+' || s[j] == '-') {
		j++
	}
	if k := sc.digits(j); k > j {
		j = k
		if j < len(s) && s[j] == '.' {
			j = sc.digits(j + 1)
		}
	} else if j+1 < len(s) && s[j] == '.' && isDigit(s[j+1]) {
		j = sc.digits(j + 1)
	} else {
		return 0
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

// wordLen is the length of [+-]?word at i, case-insensitively (0: no
// match); sign allows the sign.
func (sc *scanner) wordLen(i int, word string, sign bool) int {
	j := i
	if sign && j < len(sc.s) && (sc.s[j] == '+' || sc.s[j] == '-') {
		j++
	}
	if len(sc.s)-j < len(word) || !strings.EqualFold(sc.s[j:j+len(word)], word) {
		return 0
	}
	return j + len(word) - i
}

func (sc *scanner) next() token {
	for sc.pos < len(sc.s) && strings.IndexByte(" \t\n\r\f\v", sc.s[sc.pos]) >= 0 {
		sc.pos++
	}
	if sc.pos >= len(sc.s) {
		return token{kind: tEOF}
	}
	i := sc.pos
	// The longest of {float}, {infinity}, {NaN} (the first on ties).
	n := sc.floatLen(i)
	if m := sc.wordLen(i, "inf", true); m > n {
		n = m
		if m2 := sc.wordLen(i, "infinity", true); m2 > n {
			n = m2
		}
	}
	if m := sc.wordLen(i, "nan", false); m > n {
		n = m
	}
	if n > 0 {
		sc.pos += n
		return token{tFloat, sc.s[i:sc.pos]}
	}
	sc.pos++
	k := map[byte]tokKind{'[': tOBracket, ']': tCBracket, '(': tOParen, ')': tCParen, ',': tComma}[sc.s[i]]
	if k == 0 {
		k = tGarbage
		if sc.s[i] >= 0x80 {
			// The scanner returns it as a negative char, which Bison
			// takes for the end of the input.
			k = tEOF
		}
	}
	return token{k, sc.s[i:sc.pos]}
}

func syntaxError(t token) error {
	detail := `syntax error at or near "` + t.text + `"`
	if t.text == "" {
		detail = "syntax error at end of input"
	}
	return &pg.Error{Code: "22P02", Message: "invalid input syntax for cube", Detail: detail}
}

type parser struct {
	sc scanner
}

// list parses list after its first FLOAT (first) up to the token that
// ends it, which it returns.
func (p *parser) list(first token) ([]string, token, error) {
	items := []string{first.text}
	for {
		t := p.sc.next()
		if t.kind != tComma {
			return items, t, nil
		}
		f := p.sc.next()
		if f.kind != tFloat {
			return nil, f, syntaxError(f)
		}
		items = append(items, f.text)
	}
}

// parenList parses paren_list after its '('.
func (p *parser) parenList() ([]string, error) {
	t := p.sc.next()
	switch t.kind {
	case tCParen:
		return []string{}, nil
	case tFloat:
		items, end, err := p.list(t)
		if err != nil {
			return nil, err
		}
		if end.kind != tCParen {
			return nil, syntaxError(end)
		}
		return items, nil
	}
	return nil, syntaxError(t)
}

// expect consumes a token of kind k.
func (p *parser) expect(k tokKind) error {
	if t := p.sc.next(); t.kind != k {
		return syntaxError(t)
	}
	return nil
}

func (p *parser) openParenList() ([]string, error) {
	if err := p.expect(tOParen); err != nil {
		return nil, err
	}
	return p.parenList()
}

func parseCube(s string) (Cube, error) {
	p := &parser{sc: scanner{s: s}}
	t := p.sc.next()
	switch t.kind {
	case tOBracket:
		a, err := p.openParenList()
		if err != nil {
			return nil, err
		}
		if err := p.expect(tComma); err != nil {
			return nil, err
		}
		b, err := p.openParenList()
		if err != nil {
			return nil, err
		}
		if err := p.expect(tCBracket); err != nil {
			return nil, err
		}
		c, err := writeBox(a, b)
		if err != nil {
			return nil, err
		}
		return c, p.expect(tEOF)
	case tOParen:
		a, err := p.parenList()
		if err != nil {
			return nil, err
		}
		switch t := p.sc.next(); t.kind {
		case tComma:
			b, err := p.openParenList()
			if err != nil {
				return nil, err
			}
			c, err := writeBox(a, b)
			if err != nil {
				return nil, err
			}
			return c, p.expect(tEOF)
		default:
			return pointThenEnd(a, t)
		}
	case tFloat:
		a, end, err := p.list(t)
		if err != nil {
			return nil, err
		}
		return pointThenEnd(a, end)
	}
	return nil, syntaxError(t)
}

// pointThenEnd reduces a point form (paren_list or list) followed by
// token t: Bison reduces it whatever t is (its default action in that
// state), so the point's errors come before a syntax error at t.
func pointThenEnd(a []string, t token) (Cube, error) {
	c, err := writePoint(a)
	if err != nil {
		return nil, err
	}
	if t.kind != tEOF {
		return nil, syntaxError(t)
	}
	return c, nil
}

func tooManyInputDims() error {
	return &pg.Error{Code: "22P02", Message: "invalid input syntax for cube",
		Detail: "A cube cannot have more than 100 dimensions."}
}

func writeBox(a, b []string) (Cube, error) {
	dim := len(a)
	if len(b) != dim {
		return nil, &pg.Error{Code: "22P02", Message: "invalid input syntax for cube",
			Detail: "Different point dimensions in (" + strings.Join(a, ",") + ") and (" + strings.Join(b, ",") + ")."}
	}
	if dim > maxDim {
		return nil, tooManyInputDims()
	}
	c := newCube(dim, false)
	point := true
	for i, s := range a {
		x, err := float8In(s)
		if err != nil {
			return nil, err
		}
		c.set(i, x)
	}
	for i, s := range b {
		x, err := float8In(s)
		if err != nil {
			return nil, err
		}
		c.set(dim+i, x)
		point = point && x == c.x(i)
	}
	if point {
		c = c[:4+8*dim]
		c[3] |= 0x80 // the point bit
	}
	return c, nil
}

func writePoint(a []string) (Cube, error) {
	if len(a) > maxDim {
		return nil, tooManyInputDims()
	}
	c := newCube(len(a), true)
	for i, s := range a {
		x, err := float8In(s)
		if err != nil {
			return nil, err
		}
		c.set(i, x)
	}
	return c, nil
}

// float8In is float8in_internal for a token of the scanner: strtod, with
// PostgreSQL's errors for values out of range (overflow, or underflow to
// zero; denormal results are accepted).
func float8In(s string) (float64, error) {
	if strings.EqualFold(s, "nan") {
		return math.Float64frombits(0x7ff8000000000000), nil // C's NAN, not Go's
	}
	x, err := strconv.ParseFloat(s, 64)
	if err != nil || (x == 0 && nonZeroMantissa(s)) {
		return 0, &pg.Error{Code: "22003", Message: `"` + s + `" is out of range for type double precision`}
	}
	return x, nil
}

// nonZeroMantissa reports whether a decimal number has a non-zero digit
// before its exponent.
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
