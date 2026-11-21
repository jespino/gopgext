package main

// Composite types, arrays of extension-defined types and polymorphic
// functions.

import (
	"iter"
	"strconv"
	"strings"

	"gopgext/pg"
)

// Celsius is a temperature (a fixed-size type over float64).
//
//gopgext:type input=CelsiusIn output=CelsiusOut
type Celsius float64

//gopgext:function immutable
func CelsiusIn(s pg.CString) (Celsius, error) {
	f, err := strconv.ParseFloat(strings.TrimSuffix(strings.TrimSpace(string(s)), "C"), 64)
	if err != nil {
		return 0, pg.Errorf(pg.CodeInvalidTextRepresentation, "invalid temperature: %q", string(s))
	}
	return Celsius(f), nil
}

//gopgext:function immutable
func CelsiusOut(c Celsius) pg.CString {
	return pg.CString(strconv.FormatFloat(float64(c), 'g', -1, 64) + "C")
}

// Warmest returns the highest temperature (NULL for none).
//
//gopgext:function immutable
func Warmest(temps []Celsius) *Celsius {
	if len(temps) == 0 {
		return nil
	}
	w := temps[0]
	for _, t := range temps[1:] {
		w = max(w, t)
	}
	return &w
}

// Warmer adds by to each temperature, keeping NULLs.
//
//gopgext:function immutable
func Warmer(temps []*Celsius, by float64) []*Celsius {
	out := make([]*Celsius, len(temps))
	for i, t := range temps {
		if t != nil {
			v := *t + Celsius(by)
			out[i] = &v
		}
	}
	return out
}

// MaybeTemps returns n temperatures (NULL for n < 0).
//
//gopgext:function immutable
func MaybeTemps(n int32) *[]Celsius {
	if n < 0 {
		return nil
	}
	ts := make([]Celsius, n)
	for i := range ts {
		ts[i] = Celsius(i) / 2
	}
	return &ts
}

// Reading is a composite type: a sensor's temperatures.
//
//gopgext:type
type Reading struct {
	Sensor string
	Temps  []Celsius
	Note   *string
	Tags   *[]string `pg:"labels"`
}

// DescribeReading summarizes a reading.
//
//gopgext:function immutable
func DescribeReading(r Reading) string {
	s := r.Sensor + ": " + strconv.Itoa(len(r.Temps)) + " temps"
	if w := Warmest(r.Temps); w != nil {
		s += ", max " + string(CelsiusOut(*w))
	}
	if r.Note != nil {
		s += ", " + *r.Note
	}
	if r.Tags != nil {
		s += " [" + strings.Join(*r.Tags, ",") + "]"
	}
	return s
}

// Calibrate returns the reading with offset added to its temperatures.
//
//gopgext:function immutable
func Calibrate(r Reading, offset float64) Reading {
	for i := range r.Temps {
		r.Temps[i] += Celsius(offset)
	}
	note := "calibrated"
	r.Note = &note
	return r
}

// NoteOf returns the note of a reading (NULL for a NULL reading).
//
//gopgext:function immutable
func NoteOf(r *Reading) *string {
	if r == nil {
		return nil
	}
	return r.Note
}

// Readings returns n readings.
//
//gopgext:function immutable
func Readings(n int32) iter.Seq[Reading] {
	return func(yield func(Reading) bool) {
		for i := range n {
			if !yield(Reading{Sensor: "s" + strconv.Itoa(int(i)), Temps: []Celsius{Celsius(i), Celsius(i) + 1}}) {
				return
			}
		}
	}
}

// ---- Polymorphic functions ------------------------------------------------

// FirstNonNull returns the first non-NULL element of an array.
//
//gopgext:function immutable
func FirstNonNull(xs pg.AnyArray) *pg.AnyElement {
	for _, x := range xs.Elements() {
		if x != nil {
			return x
		}
	}
	return nil
}

// TypeOID returns the type of a value.
//
//gopgext:function immutable
func TypeOID(x pg.AnyElement) pg.Oid { return x.Type() }

// AsText formats any value.
//
//gopgext:function immutable
func AsText(x pg.AnyElement) string { return x.String() }

// ArrayText formats any array.
//
//gopgext:function immutable
func ArrayText(xs pg.AnyArray) string { return xs.String() }

// Twice returns an array of the value, twice.
//
//gopgext:function immutable
func Twice(x pg.AnyElement) pg.AnyArray {
	return pg.NewAnyArray(x.Type(), []*pg.AnyElement{&x, nil, &x})
}

// ParseAs parses s as a value of x's type.
//
//gopgext:function immutable
func ParseAs(x pg.AnyElement, s string) (pg.AnyElement, error) { return pg.ParseAny(x.Type(), s) }

// Elements returns the non-NULL elements of an array.
//
//gopgext:function immutable
func Elements(xs pg.AnyArray) iter.Seq[pg.AnyElement] {
	return func(yield func(pg.AnyElement) bool) {
		for _, x := range xs.Elements() {
			if x != nil && !yield(*x) {
				return
			}
		}
	}
}

// Pick returns a if pickA, else b (NULLs allowed).
//
//gopgext:function immutable
func Pick(pickA bool, a, b *pg.AnyElement) *pg.AnyElement {
	if pickA {
		return a
	}
	return b
}
