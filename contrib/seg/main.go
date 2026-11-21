// seg is a Go port of PostgreSQL's contrib/seg (version 1.4): the seg data
// type for line segments or floating-point intervals ("5.0 .. 7.5",
// "6(+-)0.5", "<3 .. >5", "~4.0"), which keeps the number of significant
// digits of its bounds, with its operators and btree and GiST operator
// classes.
//
// Values have upstream's layout (struct SEG, 12 bytes passed by
// reference), so data written by the C module reads back unchanged.
//
// The SQL objects other than the type and its I/O functions are upstream's
// install scripts merged into sql/seg.sql; they reference the functions
// below (and gopgext/gist's) by upstream's C symbol names.
//
// Ported from PostgreSQL 18's contrib/seg (seg.c, segparse.y, segscan.l).
package main

import (
	"encoding/binary"
	"math"

	"gopgext/gist"
	"gopgext/pg"
)

func main() {
	gist.Register("gist_seg_ops", gist.OpClass{
		Consistent: gistConsistent,
		Union:      gistUnion,
		Penalty:    gistPenalty,
		PickSplit:  gistPickSplit,
		Same:       func(a, b []byte) bool { return cmp(decode(a), decode(b)) == 0 },
	})
}

// Seg is a floating point interval (struct SEG): float4 lower and upper
// bounds, then the significant digits of each (char) and their kinds
// (char: 0, '<', '>', '~', or '-' for an open end).
//
//gopgext:type input=SegIn output=SegOut
type Seg [12]byte

// seg is a Seg decoded.
type seg struct {
	lower, upper float32
	lSigd, uSigd int8
	lExt, uExt   byte
}

func decode(b []byte) seg {
	return seg{
		lower: math.Float32frombits(binary.LittleEndian.Uint32(b)),
		upper: math.Float32frombits(binary.LittleEndian.Uint32(b[4:])),
		lSigd: int8(b[8]), uSigd: int8(b[9]), lExt: b[10], uExt: b[11],
	}
}

func (s Seg) seg() seg { return decode(s[:]) }

func (s seg) encode() Seg {
	var b Seg
	binary.LittleEndian.PutUint32(b[:], math.Float32bits(s.lower))
	binary.LittleEndian.PutUint32(b[4:], math.Float32bits(s.upper))
	b[8], b[9], b[10], b[11] = byte(s.lSigd), byte(s.uSigd), s.lExt, s.uExt
	return b
}

// fltDig is FLT_DIG.
const fltDig = 6

// SegIn parses a seg.
//
//gopgext:function immutable parallel=safe symbol=seg_in
func SegIn(s pg.CString) (Seg, error) {
	v, err := parseSeg(string(s))
	return v.encode(), err
}

// SegOut formats a seg.
//
//gopgext:function immutable parallel=safe symbol=seg_out
func SegOut(v Seg) pg.CString {
	s := v.seg()
	var b []byte
	if s.lExt == '>' || s.lExt == '<' || s.lExt == '~' {
		b = append(b, s.lExt)
	}
	if s.lower == s.upper && s.lExt == s.uExt {
		// Built by seg_in off a single point.
		b = append(b, restore(s.lower, int(s.lSigd))...)
	} else {
		if s.lExt != '-' {
			b = append(b, restore(s.lower, int(s.lSigd))...)
			b = append(b, ' ')
		}
		b = append(b, ".."...)
		if s.uExt != '-' {
			b = append(b, ' ')
			if s.uExt == '>' || s.uExt == '<' || s.uExt == '~' {
				b = append(b, s.uExt)
			}
			b = append(b, restore(s.upper, int(s.uSigd))...)
		}
	}
	return pg.CString(b)
}

// SegCenter is seg_center(seg).
//
//gopgext:function symbol=seg_center manual
func SegCenter(v Seg) float32 {
	s := v.seg()
	return float32(float64(s.lower+s.upper) / 2.0)
}

// SegLower is seg_lower(seg).
//
//gopgext:function symbol=seg_lower manual
func SegLower(v Seg) float32 { return v.seg().lower }

// SegUpper is seg_upper(seg).
//
//gopgext:function symbol=seg_upper manual
func SegUpper(v Seg) float32 { return v.seg().upper }

// SegSize is seg_size(seg).
//
//gopgext:function symbol=seg_size manual
func SegSize(v Seg) float32 {
	s := v.seg()
	return float32(math.Abs(float64(s.upper - s.lower)))
}

// rtSize is rt_seg_size.
func rtSize(s seg) float32 {
	if s.upper <= s.lower {
		return 0
	}
	return float32(math.Abs(float64(s.upper - s.lower)))
}

func contains(a, b seg) bool { return a.lower <= b.lower && a.upper >= b.upper }

func overlap(a, b seg) bool {
	return (a.upper >= b.upper && a.lower <= b.upper) || (b.upper >= a.upper && b.lower <= a.upper)
}

func overLeft(a, b seg) bool  { return a.upper <= b.upper }
func left(a, b seg) bool      { return a.upper < b.lower }
func right(a, b seg) bool     { return a.lower > b.upper }
func overRight(a, b seg) bool { return a.lower >= b.lower }

// union is seg_union: the lower of the lower bounds and the upper of the
// upper bounds, with their digits and kinds (the second's on ties).
func union(a, b seg) seg {
	n := b
	if a.upper > b.upper {
		n.upper, n.uSigd, n.uExt = a.upper, a.uSigd, a.uExt
	}
	if a.lower < b.lower {
		n.lower, n.lSigd, n.lExt = a.lower, a.lSigd, a.lExt
	}
	return n
}

// inter is seg_inter.
func inter(a, b seg) seg {
	n := b
	if a.upper < b.upper {
		n.upper, n.uSigd, n.uExt = a.upper, a.uSigd, a.uExt
	}
	if a.lower > b.lower {
		n.lower, n.lSigd, n.lExt = a.lower, a.lSigd, a.lExt
	}
	return n
}

// SegContains is seg_contains(seg, seg) (@>).
//
//gopgext:function symbol=seg_contains manual
func SegContains(a, b Seg) bool { return contains(a.seg(), b.seg()) }

// SegContained is seg_contained(seg, seg) (<@).
//
//gopgext:function symbol=seg_contained manual
func SegContained(a, b Seg) bool { return contains(b.seg(), a.seg()) }

// SegOverlap is seg_overlap(seg, seg) (&&).
//
//gopgext:function symbol=seg_overlap manual
func SegOverlap(a, b Seg) bool { return overlap(a.seg(), b.seg()) }

// SegOverLeft is seg_over_left(seg, seg) (&<).
//
//gopgext:function symbol=seg_over_left manual
func SegOverLeft(a, b Seg) bool { return overLeft(a.seg(), b.seg()) }

// SegLeft is seg_left(seg, seg) (<<).
//
//gopgext:function symbol=seg_left manual
func SegLeft(a, b Seg) bool { return left(a.seg(), b.seg()) }

// SegRight is seg_right(seg, seg) (>>).
//
//gopgext:function symbol=seg_right manual
func SegRight(a, b Seg) bool { return right(a.seg(), b.seg()) }

// SegOverRight is seg_over_right(seg, seg) (&>).
//
//gopgext:function symbol=seg_over_right manual
func SegOverRight(a, b Seg) bool { return overRight(a.seg(), b.seg()) }

// SegUnion is seg_union(seg, seg).
//
//gopgext:function symbol=seg_union manual
func SegUnion(a, b Seg) Seg { return union(a.seg(), b.seg()).encode() }

// SegInter is seg_inter(seg, seg).
//
//gopgext:function symbol=seg_inter manual
func SegInter(a, b Seg) Seg { return inter(a.seg(), b.seg()).encode() }

// cmp is seg_cmp: by lower bound (then its kind and digits), then by
// upper bound.
func cmp(a, b seg) int32 {
	if a.lower < b.lower {
		return -1
	}
	if a.lower > b.lower {
		return 1
	}
	// A '-' lower bound is below any other kind, then '<'; '>' above all.
	if a.lExt != b.lExt {
		switch {
		case a.lExt == '-':
			return -1
		case b.lExt == '-':
			return 1
		case a.lExt == '<':
			return -1
		case b.lExt == '<':
			return 1
		case a.lExt == '>':
			return 1
		case b.lExt == '>':
			return -1
		}
	}
	// Fewer significant digits is blurrier, so lower.
	if a.lSigd < b.lSigd {
		return -1
	}
	if a.lSigd > b.lSigd {
		return 1
	}
	// For the same digits, an approximate bound is blurrier than an exact one.
	if a.lExt != b.lExt {
		if a.lExt == '~' {
			return -1
		}
		if b.lExt == '~' {
			return 1
		}
		panic(&pg.Error{Code: "XX000", Message: "bogus lower boundary types " + itoa(int(int8(a.lExt))) + " " + itoa(int(int8(b.lExt)))})
	}

	if a.upper < b.upper {
		return -1
	}
	if a.upper > b.upper {
		return 1
	}
	// A '-' upper bound is above any other kind, then '>'; '<' below all.
	if a.uExt != b.uExt {
		switch {
		case a.uExt == '-':
			return 1
		case b.uExt == '-':
			return -1
		case a.uExt == '<':
			return -1
		case b.uExt == '<':
			return 1
		case a.uExt == '>':
			return 1
		case b.uExt == '>':
			return -1
		}
	}
	// The converse of the lower bound's rules.
	if a.uSigd < b.uSigd {
		return 1
	}
	if a.uSigd > b.uSigd {
		return -1
	}
	if a.uExt != b.uExt {
		if a.uExt == '~' {
			return 1
		}
		if b.uExt == '~' {
			return -1
		}
		panic(&pg.Error{Code: "XX000", Message: "bogus upper boundary types " + itoa(int(int8(a.uExt))) + " " + itoa(int(int8(b.uExt)))})
	}
	return 0
}

// SegCmp is seg_cmp(seg, seg), the btree comparison function.
//
//gopgext:function symbol=seg_cmp manual
func SegCmp(a, b Seg) int32 { return cmp(a.seg(), b.seg()) }

// SegLt is seg_lt(seg, seg) (<).
//
//gopgext:function symbol=seg_lt manual
func SegLt(a, b Seg) bool { return cmp(a.seg(), b.seg()) < 0 }

// SegLe is seg_le(seg, seg) (<=).
//
//gopgext:function symbol=seg_le manual
func SegLe(a, b Seg) bool { return cmp(a.seg(), b.seg()) <= 0 }

// SegGt is seg_gt(seg, seg) (>).
//
//gopgext:function symbol=seg_gt manual
func SegGt(a, b Seg) bool { return cmp(a.seg(), b.seg()) > 0 }

// SegGe is seg_ge(seg, seg) (>=).
//
//gopgext:function symbol=seg_ge manual
func SegGe(a, b Seg) bool { return cmp(a.seg(), b.seg()) >= 0 }

// SegSame is seg_same(seg, seg) (=).
//
//gopgext:function symbol=seg_same manual
func SegSame(a, b Seg) bool { return cmp(a.seg(), b.seg()) == 0 }

// SegDifferent is seg_different(seg, seg) (<>).
//
//gopgext:function symbol=seg_different manual
func SegDifferent(a, b Seg) bool { return cmp(a.seg(), b.seg()) != 0 }

func itoa(n int) string {
	if n < 0 {
		return "-" + itoa(-n)
	}
	if n < 10 {
		return string(rune('0' + n))
	}
	return itoa(n/10) + string(rune('0'+n%10))
}
