// Package main is a port of PostgreSQL's contrib/tsm_system_rows (version
// 1.0): the SYSTEM_ROWS TABLESAMPLE method, which reads whole blocks in a
// random order until it has returned the requested number of rows.
package main

import (
	"gopgext/pg"
	"gopgext/tablesample"
)

// sampler is the state of a scan.
type sampler struct {
	seed       uint32
	ntuples    int64  // number of tuples to return
	lt         uint16 // last tuple returned from the current block (0: none)
	doneblocks uint32 // blocks already scanned
	lb         uint32 // last block visited
	// not changed during a rescan:
	nblocks, firstblock, step uint32 // step 0: not set yet
}

func main() {
	tablesample.Register("system_rows", tablesample.Method{
		Params:                []pg.Oid{tablesample.Int8},
		RepeatableAcrossScans: true,
		SampleSize:            sampleSize,
		Begin:                 begin,
		NextBlock:             nextBlock,
		NextTuple:             nextTuple,
	})
}

func sampleSize(rel tablesample.Relation, params []any) (pages, tuples float64) {
	ntuples := int64(1000) // if the limit isn't a known, valid constant
	if n, ok := params[0].(int64); ok && n >= 0 {
		ntuples = n
	}
	// Clamp to the estimated relation size.
	nt := float64(ntuples)
	if nt > rel.Tuples {
		nt = float64(int64(rel.Tuples))
	}
	nt = tablesample.ClampRows(nt)
	var npages float64
	if rel.Tuples > 0 && rel.Pages > 0 {
		npages = nt / (rel.Tuples / rel.Pages) // from the tuple density
	} else {
		npages = nt // one tuple per page
	}
	return tablesample.ClampRows(min(rel.Pages, npages)), nt
}

func begin(s *tablesample.Scan, params []any, seed uint32) error {
	ntuples := params[0].(int64)
	if ntuples < 0 {
		return pg.Errorf(tablesample.CodeInvalidTablesampleArgument, "sample size must not be negative")
	}
	sp, _ := s.State.(*sampler)
	if sp == nil {
		sp = &sampler{}
		s.State = sp
	}
	sp.seed = seed
	sp.ntuples = ntuples
	sp.lt = 0
	sp.doneblocks = 0
	return nil
}

// nextBlock picks blocks by linear probing from a random start, with a
// random step relatively prime to the number of blocks.
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
	}
	sp.doneblocks++
	if sp.doneblocks > sp.nblocks || s.DoneTuples >= sp.ntuples {
		return 0, false
	}
	for {
		sp.lb = uint32((uint64(sp.lb) + uint64(sp.step)) % uint64(sp.nblocks))
		if sp.lb < nblocks {
			return sp.lb, true
		}
	}
}

// nextTuple returns every tuple of the block, until enough were returned.
func nextTuple(s *tablesample.Scan, block uint32, maxOffset uint16) (uint16, bool) {
	sp := s.State.(*sampler)
	if s.DoneTuples >= sp.ntuples {
		return 0, false
	}
	off := sp.lt + 1
	if sp.lt == 0 {
		off = 1
	}
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
