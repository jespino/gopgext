package main

// time.Time (timestamptz) and pg.Numeric (numeric).

import (
	"math/big"
	"time"

	"gopgext/pg"
)

// AddHours returns t plus h hours.
//
//gopgext:function immutable
func AddHours(t time.Time, h float64) time.Time {
	return t.Add(time.Duration(h * float64(time.Hour)))
}

// Latest returns the latest time (NULL for none).
//
//gopgext:function immutable
func Latest(ts []time.Time) *time.Time {
	if len(ts) == 0 {
		return nil
	}
	l := ts[0]
	for _, t := range ts[1:] {
		if t.After(l) {
			l = t
		}
	}
	return &l
}

// NumericSum adds numerics exactly (NULLs are skipped).
//
//gopgext:function immutable
func NumericSum(xs []*pg.Numeric) (pg.Numeric, error) {
	sum := new(big.Rat)
	for _, x := range xs {
		if x == nil {
			continue
		}
		r, ok := new(big.Rat).SetString(string(*x))
		if !ok {
			return "", pg.Errorf(pg.CodeFeatureNotSupported, "can't add %s", *x)
		}
		sum.Add(sum, r)
	}
	return pg.Numeric(sum.FloatString(20)), nil
}

// BadNumeric returns an invalid numeric (an error).
//
//gopgext:function immutable
func BadNumeric() pg.Numeric { return "12abc" }
