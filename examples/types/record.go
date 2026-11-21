package main

import (
	"fmt"
	"iter"
	"time"

	"gopgext/pg"
)

// UuidNext returns the uuids, the next of each (the last byte plus one), and
// whether n is NULL.
//
//gopgext:function immutable
func UuidNext(us []pg.UUID, n *pg.UUID) []*pg.UUID {
	out := make([]*pg.UUID, 0, len(us)+1)
	for _, u := range us {
		v := u
		v[15]++
		out = append(out, &v)
	}
	return append(out, n)
}

// UuidParse parses a uuid in Go.
//
//gopgext:function immutable
func UuidParse(s string) (pg.UUID, error) { return pg.ParseUUID(s) }

// UuidSeries returns n uuids with the counter in their last bytes.
//
//gopgext:function immutable
func UuidSeries(n int32) iter.Seq[pg.UUID] {
	return func(yield func(pg.UUID) bool) {
		for i := int32(0); i < n; i++ {
			var u pg.UUID
			u[14], u[15] = byte(i>>8), byte(i)
			if !yield(u) {
				return
			}
		}
	}
}

// ResultColumnsOf returns the columns the caller asked for, one per row:
// (name, type, typmod) of the column definition list, as text.
//
//gopgext:function stable
func ResultColumnsOf(n int32) (iter.Seq[pg.Record], error) {
	cols, err := pg.ResultColumns()
	if err != nil {
		return nil, err
	}
	return func(yield func(pg.Record) bool) {
		for i := int32(0); i < n; i++ {
			rec := make(pg.Record, len(cols))
			for j, c := range cols {
				rec[j] = fmt.Sprintf("%s:%s", c.Name, c.TypeName)
			}
			if !yield(rec) {
				return
			}
		}
	}, nil
}

// RecordValues returns one record of Go values of many types, converted to the
// columns the caller defines; extra adds a value too many.
//
//gopgext:function stable
func RecordValues(extra bool) pg.Record {
	r := pg.Record{nil, true, int16(7), int32(-8), int64(9), 42, 1.5, float32(0.25), "text", []byte{1, 2},
		pg.Numeric("12.50"), time.Unix(1700000000, 0), pg.UUID{15: 1}, "17"}
	if extra {
		r = append(r, 1)
	}
	return r
}

// MaybeRecord returns NULL, or a record of (a, b).
//
//gopgext:function immutable
func MaybeRecord(null bool) *pg.Record {
	if null {
		return nil
	}
	return &pg.Record{1, "b"}
}
