// Package agg writes aggregates whose state is a Go value (transition type
// internal): a slice of values for a median, a map for a mode, a sketch...
// The state is an *agg.State, and the aggregate's functions are ordinary
// //gopgext:function functions taking and returning one (SQL type
// internal):
//
//	// MedianAdd is the transition function of median(float8).
//	//
//	//gopgext:function parallel=safe
//	func MedianAdd(s *agg.State, x *float64) *agg.State {
//		if s == nil {
//			s = agg.New(&values{}) // first row of a group
//		}
//		if x != nil {
//			v := s.Value.(*values)
//			*v = append(*v, *x)
//		}
//		return s
//	}
//
//	//gopgext:function parallel=safe
//	func MedianFinal(s *agg.State) *float64 { ... } // nil s: no rows
//
// declared in sql/:
//
//	CREATE AGGREGATE median(float8) (
//		SFUNC = median_add, STYPE = internal, FINALFUNC = median_final,
//		-- parallel aggregation (optional): states are merged and travel
//		-- between processes as bytea
//		COMBINEFUNC = median_combine, SERIALFUNC = median_serialize,
//		DESERIALFUNC = median_deserialize, PARALLEL = SAFE);
//
// Functions with an *agg.State parameter aren't STRICT (the state starts as
// NULL, a nil *agg.State): use pointer parameters for the aggregated values
// to see NULLs as nil (non-pointer ones raise an error on NULL). The
// serialization function takes an *agg.State and returns []byte; the
// deserialization function takes ([]byte, pg.Internal) and returns a new
// state.
//
// A state lives as long as the aggregate's memory context (a group, or the
// query): PostgreSQL frees it, and agg drops the Go value, when that
// context is reset, also after errors. Don't keep states elsewhere.
package agg

import (
	"gopgext/fmgr"
	"gopgext/internal/core"
)

// State is the state of an aggregate: Value is any Go value.
type State struct {
	Value any

	id  uint64 // in states; 0 until returned to PostgreSQL
	ptr uint64 // the handle (struct agg_handle in agg.c)
}

// New returns a new state holding v.
func New(v any) *State { return &State{Value: v} }

var (
	states = map[uint64]*State{} // live states, by id
	lastID uint64
)

//export gopgext_agg_new
func aggNew(c *fmgr.Call, id uint64, out *uint64) uintptr

//export gopgext_agg_id
func aggID(d uint64, out *uint64) uintptr

//export gopgext_agg_freed
func aggFreed(buf *uint64, max int64) int64

// drain drops the states whose memory context PostgreSQL freed.
func drain() {
	var buf [64]uint64
	for {
		n := int(aggFreed(&buf[0], int64(len(buf))))
		for _, id := range buf[:n] {
			if s := states[id]; s != nil {
				s.id, s.ptr = 0, 0
				delete(states, id)
			}
		}
		if n < len(buf) {
			return
		}
	}
}

// Arg returns state argument i, nil for NULL (generated code).
func Arg(c *fmgr.Call, i int) *State {
	drain()
	d, null := c.Datum(i)
	if null || d == 0 {
		return nil
	}
	var id uint64
	core.Check(aggID(d, &id))
	s := states[id]
	if s == nil {
		panic(&core.Error{Code: "XX000", Message: "aggregate state was already freed"})
	}
	return s
}

// Return sets a state result, NULL for nil (generated code). A new state
// gets a handle in the calling aggregate's memory context.
func Return(c *fmgr.Call, s *State) {
	drain()
	if s == nil {
		c.SetNull()
		return
	}
	if s.ptr == 0 {
		lastID++
		core.Check(aggNew(c, lastID, &s.ptr))
		s.id = lastID
		states[s.id] = s
	}
	c.SetDatum(s.ptr)
}

// Live returns the number of states alive in this backend (to check that
// none leak).
func Live() int {
	drain()
	return len(states)
}
