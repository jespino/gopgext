// Package main is a port of PostgreSQL's contrib/tsm_system_time (version
// 1.0): the SYSTEM_TIME TABLESAMPLE method, which reads whole blocks in a
// random order until the given number of milliseconds has passed.
package main

import (
	"math"
	"time"

	"gopgext/pg"
	"gopgext/tablesample"
)

// sampler is the state of a scan.
type sampler struct {
	seed       uint32
	millis     float64   // time limit
	start      time.Time // of the scan
	lt         uint16    // last tuple returned from the current block (0: none)
	doneblocks uint32    // blocks already scanned
	lb         uint32    // last block visited
	// not changed during a rescan:
	nblocks, firstblock, step uint32 // step 0: not set yet
}

func main() {
	tablesample.Register("system_time", tablesample.Method{
		Params:     []pg.Oid{tablesample.Float8},
		SampleSize: sampleSize,
		Begin:      begin,
		NextBlock:  nextBlock,
		NextTuple:  nextTuple,
	})
}

func sampleSize(rel tablesample.Relation, params []any) (pages, tuples float64) {
	millis := 1000.0 // if the time isn't a known, valid constant
	if m, ok := params[0].(float64); ok && m >= 0 && !math.IsNaN(m) {
		millis = m
	}
	// Pages that can be read, assuming the cost of a page is milliseconds
	// (bogus, like the original says, but there's no better answer).
	npages := millis
	if rel.RandomPageCost > 0 {
		npages = millis / rel.RandomPageCost
	}
	npages = tablesample.ClampRows(min(rel.Pages, npages))
	ntuples := npages
	if rel.Tuples > 0 && rel.Pages > 0 {
		ntuples = npages * (rel.Tuples / rel.Pages)
	}
	return npages, tablesample.ClampRows(min(rel.Tuples, ntuples))
}

func begin(s *tablesample.Scan, params []any, seed uint32) error {
	millis := params[0].(float64)
	if millis < 0 || math.IsNaN(millis) {
		return pg.Errorf(tablesample.CodeInvalidTablesampleArgument, "sample collection time must not be negative")
	}
	sp, _ := s.State.(*sampler)
	if sp == nil {
		sp = &sampler{}
		s.State = sp
	}
	sp.seed = seed
	sp.millis = millis
	sp.lt = 0
	sp.doneblocks = 0
	return nil
}

// nextBlock picks blocks by linear probing from a random start, with a
// random step relatively prime to the number of blocks, until the time is
// up.
func nextBlock(s *tablesample.Scan, nblocks uint32) (uint32, bool) {
	sp := s.State.(*sampler)
	if sp.doneblocks == 0 {
		if sp.step == 0 { // first scan within the query
			if nblocks == 0 {
				return 0, false
			}
			r := tablesample.NewRandom(sp.seed)
			sp.nblocks = nblocks
			sp.firstblock = uint32(r.Fract() * float64(sp.nblocks))
			sp.step = randomRelativePrime(sp.nblocks, r)
		}
		sp.lb = sp.firstblock
		sp.start = time.Now()
	}
	sp.doneblocks++
	if sp.doneblocks > sp.nblocks {
		return 0, false
	}
	if float64(time.Since(sp.start).Nanoseconds())/1e6 >= sp.millis {
		return 0, false
	}
	for {
		sp.lb = uint32((uint64(sp.lb) + uint64(sp.step)) % uint64(sp.nblocks))
		if sp.lb < nblocks {
			return sp.lb, true
		}
	}
}

// nextTuple returns every tuple of the block.
func nextTuple(s *tablesample.Scan, block uint32, maxOffset uint16) (uint16, bool) {
	sp := s.State.(*sampler)
	off := sp.lt + 1
	if off > maxOffset {
		off = 0
	}
	sp.lt = off
	return off, off != 0
}

func gcd(a, b uint32) uint32 {
	for a != 0 {
		a, b = b%a, a
	}
	return b
}

// randomRelativePrime picks a random value less than and relatively prime
// to n, if possible (else 1).
func randomRelativePrime(n uint32, r *tablesample.Random) uint32 {
	if n <= 1 {
		return 1
	}
	for {
		x := uint32(r.Fract() * float64(n))
		if x != 0 && gcd(x, n) == 1 {
			return x
		}
	}
}
