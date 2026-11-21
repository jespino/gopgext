package main

import (
	"encoding/binary"

	"gopgext/pg"
)

// The GiST operator class gist_cube_ops: keys are cubes (the indexed
// values themselves on leaves, bounding boxes above them). Its support
// functions are gopgext/gist's, under upstream's C symbols (compress and
// decompress for databases created with cube 1.3 or older):
//
//gopgext:alias g_cube_consistent=gopgext_gist_consistent g_cube_union=gopgext_gist_union
//gopgext:alias g_cube_penalty=gopgext_gist_penalty g_cube_picksplit=gopgext_gist_picksplit
//gopgext:alias g_cube_same=gopgext_gist_same g_cube_distance=gopgext_gist_distance
//gopgext:alias g_cube_compress=gopgext_gist_compress g_cube_decompress=gopgext_gist_decompress

// Strategy numbers of gist_cube_ops.
const (
	stOverlap        = 3  // &&
	stSame           = 6  // =
	stContains       = 7  // @>
	stContainedBy    = 8  // <@
	stOldContains    = 13 // @ (dropped in 1.5)
	stOldContainedBy = 14 // ~ (dropped in 1.5)
	stCoord          = 15 // ~>
	stTaxicab        = 16 // <#>
	stEuclid         = 17 // <->
	stChebyshev      = 18 // <=>
)

func gistConsistent(key, query []byte, strategy int, leaf bool) (bool, bool) {
	k, q := Cube(key), Cube(query)
	if leaf {
		switch strategy {
		case stOverlap:
			return overlap(k, q), false
		case stSame:
			return cmp(k, q) == 0, false
		case stContains, stOldContains:
			return contains(k, q), false
		case stContainedBy, stOldContainedBy:
			return contains(q, k), false
		}
		return false, false
	}
	switch strategy {
	case stOverlap, stContainedBy, stOldContainedBy:
		return overlap(k, q), false
	case stSame, stContains, stOldContains:
		return contains(k, q), false
	}
	return false, false
}

// gistUnion is g_cube_union: the union of the keys, from the first.
func gistUnion(keys [][]byte) []byte {
	u := Cube(keys[0])
	for _, k := range keys[1:] {
		u = union(u, Cube(k))
	}
	return u
}

// gistPenalty is g_cube_penalty: the growth of key's volume.
func gistPenalty(key, added []byte) float64 {
	return size(union(Cube(key), Cube(added))) - size(Cube(key))
}

func gistSame(a, b []byte) bool { return cmp(Cube(a), Cube(b)) == 0 }

// gistPickSplit is g_cube_picksplit: Guttman's quadratic split. The seeds
// are the two keys wasting the most volume together (upstream leaves the
// last key out of that search), then each key goes to the side that grows
// the least.
func gistPickSplit(keys [][]byte) (left, right []int) {
	last := len(keys) - 2 // maxoff - 1: the last key isn't a seed
	seed1, seed2 := 0, 1
	waste, first := 0.0, true
	for i := 0; i < last; i++ {
		a := Cube(keys[i])
		for j := i + 1; j <= last; j++ {
			b := Cube(keys[j])
			w := size(union(a, b)) - size(inter(a, b))
			if w > waste || first {
				waste, seed1, seed2, first = w, i, j, false
			}
		}
	}
	dl, dr := Cube(keys[seed1]), Cube(keys[seed2])
	sl, sr := size(dl), size(dr)
	for i := range keys {
		switch i {
		case seed1:
			left = append(left, i)
			continue
		case seed2:
			right = append(right, i)
			continue
		}
		k := Cube(keys[i])
		ul, ur := union(dl, k), union(dr, k)
		al, ar := size(ul), size(ur)
		if al-sl < ar-sr {
			dl, sl = ul, al
			left = append(left, i)
		} else {
			dr, sr = ur, ar
			right = append(right, i)
		}
	}
	return left, right
}

// gistDistance is g_cube_distance: ~> (an int4 query, a coordinate) and the
// three distances, exact for keys of leaves and lower bounds above them.
func gistDistance(key, query []byte, strategy int, leaf bool) (float64, bool) {
	k := Cube(key)
	switch strategy {
	case stCoord:
		coord := int32(binary.LittleEndian.Uint32(query))
		if coord == 0 {
			panic(arrayError("zero cube index is not defined"))
		}
		return coordLLUR(k, coord, leaf), false
	case stTaxicab:
		return taxicab(k, Cube(query)), false
	case stEuclid:
		return euclid(k, Cube(query)), false
	case stChebyshev:
		return chebyshev(k, Cube(query)), false
	}
	panic(&pg.Error{Code: "XX000", Message: "unrecognized cube strategy number: " + itoa(int32(strategy))})
}
