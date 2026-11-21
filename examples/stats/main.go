// stats shows aggregates whose state is a Go value (gopgext/agg) and
// selectivity estimators written in Go (gopgext/selectivity):
//
//   - median(float8): the state is a slice of values; it runs in parallel
//     (states are combined, and serialized between processes);
//   - mode_of(text): the state is a map of counts;
//   - percentile(float8, fraction): a state with the values and the fraction;
//   - the operator bigint ~~= bigint ("within 10 of"), whose RESTRICT and
//     JOIN estimators use the column statistics (most common values,
//     histogram, distinct values).
package main

import (
	"encoding/binary"
	"math"
	"slices"
	"strconv"

	"gopgext/agg"
	"gopgext/pg"
	"gopgext/selectivity"
)

func main() {
	selectivity.RegisterRestriction("near_sel", nearSel)
	selectivity.RegisterJoin("near_joinsel", nearJoinSel)
}

// ---- median(float8) -------------------------------------------------------

type values struct{ xs []float64 }

// MedianAdd adds a value to the state of median.
//
//gopgext:function immutable parallel=safe
func MedianAdd(s *agg.State, x *float64) *agg.State {
	if s == nil {
		s = agg.New(&values{})
	}
	if x != nil {
		v := s.Value.(*values)
		v.xs = append(v.xs, *x)
	}
	return s
}

// MedianCombine merges two states (parallel aggregation).
//
//gopgext:function immutable parallel=safe
func MedianCombine(a, b *agg.State) *agg.State {
	switch {
	case a == nil:
		return b
	case b == nil:
		return a
	}
	va := a.Value.(*values)
	va.xs = append(va.xs, b.Value.(*values).xs...)
	return a
}

// MedianSerialize encodes a state (float64s, little-endian).
//
//gopgext:function immutable strict parallel=safe
func MedianSerialize(s *agg.State) []byte {
	xs := s.Value.(*values).xs
	b := make([]byte, 0, 8*len(xs))
	for _, x := range xs {
		b = binary.LittleEndian.AppendUint64(b, math.Float64bits(x))
	}
	return b
}

// MedianDeserialize decodes a state.
//
//gopgext:function immutable parallel=safe
func MedianDeserialize(b []byte, _ pg.Internal) *agg.State {
	v := &values{xs: make([]float64, len(b)/8)}
	for i := range v.xs {
		v.xs[i] = math.Float64frombits(binary.LittleEndian.Uint64(b[8*i:]))
	}
	return agg.New(v)
}

// percentileOf is percentile_cont: linear interpolation between the
// closest ranks.
func percentileOf(xs []float64, p float64) *float64 {
	if len(xs) == 0 {
		return nil
	}
	slices.Sort(xs)
	pos := p * float64(len(xs)-1)
	lo := int(math.Floor(pos))
	hi := min(lo+1, len(xs)-1)
	r := xs[lo] + (pos-float64(lo))*(xs[hi]-xs[lo])
	return &r
}

// MedianFinal returns the median (NULL for no values).
//
//gopgext:function immutable parallel=safe
func MedianFinal(s *agg.State) *float64 {
	if s == nil {
		return nil
	}
	return percentileOf(s.Value.(*values).xs, 0.5)
}

// ---- percentile(float8, fraction) -------------------------------------------

type percentile struct {
	values
	p float64
}

// PercentileAdd adds a value to the state of percentile.
//
//gopgext:function immutable parallel=safe
func PercentileAdd(s *agg.State, x *float64, p float64) (*agg.State, error) {
	if p < 0 || p > 1 {
		return nil, pg.Errorf(pg.CodeNumericValueOutOfRange, "percentile %g is not between 0 and 1", p)
	}
	if s == nil {
		s = agg.New(&percentile{p: p})
	}
	if x != nil {
		v := s.Value.(*percentile)
		v.xs = append(v.xs, *x)
	}
	return s, nil
}

// PercentileFinal returns the percentile (NULL for no values).
//
//gopgext:function immutable parallel=safe
func PercentileFinal(s *agg.State) *float64 {
	if s == nil {
		return nil
	}
	v := s.Value.(*percentile)
	return percentileOf(v.xs, v.p)
}

// ---- mode_of(text) ------------------------------------------------------------

// ModeAdd counts a value.
//
//gopgext:function immutable parallel=safe
func ModeAdd(s *agg.State, x *string) *agg.State {
	if s == nil {
		s = agg.New(map[string]int{})
	}
	if x != nil {
		s.Value.(map[string]int)[*x]++
	}
	return s
}

// ModeFinal returns the most frequent value (the smallest of ties).
//
//gopgext:function immutable parallel=safe
func ModeFinal(s *agg.State) *string {
	if s == nil {
		return nil
	}
	var best *string
	n := 0
	for k, c := range s.Value.(map[string]int) {
		if c > n || (c == n && k < *best) {
			k := k
			best, n = &k, c
		}
	}
	return best
}

// LiveStates returns the number of aggregate states alive in this backend
// (0 between queries: states are freed with their aggregate).
//
//gopgext:function volatile
func LiveStates() int64 { return int64(agg.Live()) }

// ---- bigint ~~= bigint, and its estimators -------------------------------------

const nearDistance = 10

// NearlyEqual is the ~~= operator: are a and b within 10 of each other?
//
//gopgext:function immutable parallel=safe
func NearlyEqual(a, b int64) bool { return a-b <= nearDistance && b-a <= nearDistance }

func intOf(v pg.AnyElement) float64 {
	f, _ := strconv.ParseFloat(v.String(), 64)
	return f
}

// nearSel estimates the fraction of rows within 10 of a constant: the
// frequencies of the matching most common values, plus the part of the
// histogram the interval [v-10, v+10] covers for the other rows.
func nearSel(r *selectivity.Restriction) float64 {
	c := r.Column
	if r.Value == nil || c == nil || !c.HasStats {
		return 0.005 // no constant or no statistics: a guess
	}
	v := intOf(*r.Value)
	lo, hi := v-nearDistance, v+nearDistance
	sel, mcvTotal := 0.0, 0.0
	for i, m := range c.MCV {
		mcvTotal += c.MCVFreqs[i]
		if x := intOf(m); x >= lo && x <= hi {
			sel += c.MCVFreqs[i]
		}
	}
	rest := max(0, 1-c.NullFrac-mcvTotal)
	if h := c.Histogram; len(h) >= 2 {
		covered := 0.0
		for i := 0; i+1 < len(h); i++ {
			a, b := intOf(h[i]), intOf(h[i+1])
			if b <= a {
				if a >= lo && a <= hi {
					covered++
				}
				continue
			}
			covered += max(0, min(b, hi)-max(a, lo)) / (b - a)
		}
		sel += rest * covered / float64(len(h)-1)
	} else if d := distinct(c); d > 0 {
		sel += rest * min(1, (2*nearDistance+1)/d)
	}
	return sel
}

// distinct returns the number of distinct values of a column (0: unknown).
func distinct(c *selectivity.Column) float64 {
	if c.Distinct < 0 {
		return -c.Distinct * c.Rows
	}
	return c.Distinct
}

// nearJoinSel estimates the fraction of pairs within 10 of each other: 21
// values match each value, out of the distinct values of the larger side.
func nearJoinSel(j *selectivity.Join) float64 {
	if j.Left == nil || j.Right == nil {
		return 0.005
	}
	d := max(distinct(j.Left), distinct(j.Right))
	if d <= 0 {
		return 0.005
	}
	return min(1, (2*nearDistance+1)/d) * (1 - j.Left.NullFrac) * (1 - j.Right.NullFrac)
}
