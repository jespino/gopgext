package main

import "sort"

// The GiST operator class gist_seg_ops: keys are segs (the indexed values
// on leaves, their union above them). Its support functions are
// gopgext/gist's, under upstream's C symbols (compress and decompress for
// databases created with seg 1.2 or older):
//
//gopgext:alias gseg_consistent=gopgext_gist_consistent gseg_union=gopgext_gist_union
//gopgext:alias gseg_penalty=gopgext_gist_penalty gseg_picksplit=gopgext_gist_picksplit
//gopgext:alias gseg_same=gopgext_gist_same
//gopgext:alias gseg_compress=gopgext_gist_compress gseg_decompress=gopgext_gist_decompress

// Strategy numbers of gist_seg_ops.
const (
	stLeft           = 1  // <<
	stOverLeft       = 2  // &<
	stOverlap        = 3  // &&
	stOverRight      = 4  // &>
	stRight          = 5  // >>
	stSame           = 6  // =
	stContains       = 7  // @>
	stContainedBy    = 8  // <@
	stOldContains    = 13 // @ (dropped in 1.4)
	stOldContainedBy = 14 // ~ (dropped in 1.4)
)

func gistConsistent(key, query []byte, strategy int, leaf bool) (bool, bool) {
	k, q := decode(key), decode(query)
	if leaf {
		switch strategy {
		case stLeft:
			return left(k, q), false
		case stOverLeft:
			return overLeft(k, q), false
		case stOverlap:
			return overlap(k, q), false
		case stOverRight:
			return overRight(k, q), false
		case stRight:
			return right(k, q), false
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
	case stLeft:
		return !overRight(k, q), false
	case stOverLeft:
		return !right(k, q), false
	case stOverlap, stContainedBy, stOldContainedBy:
		return overlap(k, q), false
	case stOverRight:
		return !left(k, q), false
	case stRight:
		return !overLeft(k, q), false
	case stSame, stContains, stOldContains:
		return contains(k, q), false
	}
	return false, false
}

// gistUnion is gseg_union: the union of the keys, from the first.
func gistUnion(keys [][]byte) []byte {
	u := decode(keys[0])
	for _, k := range keys[1:] {
		u = union(u, decode(k))
	}
	b := u.encode()
	return b[:]
}

// gistPenalty is gseg_penalty: the growth of key's length (float4).
func gistPenalty(key, added []byte) float64 {
	k := decode(key)
	return float64(rtSize(union(k, decode(added))) - rtSize(k))
}

// gistPickSplit is gseg_picksplit: the keys sorted by center, the lower
// half to the left.
func gistPickSplit(keys [][]byte) (l, r []int) {
	type item struct {
		center float32
		index  int
	}
	items := make([]item, len(keys))
	for i, k := range keys {
		s := decode(k)
		items[i] = item{s.lower*0.5 + s.upper*0.5, i}
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].center < items[j].center })
	firstRight := len(items) / 2
	for i, it := range items {
		if i < firstRight || i == 0 {
			l = append(l, it.index)
		} else {
			r = append(r, it.index)
		}
	}
	return l, r
}
