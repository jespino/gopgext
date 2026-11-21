package main

// Operators and functions (hstore_op.c).

import (
	"bytes"
	"iter"

	"gopgext/pg"
)

const textOID = 25

// keyPairs returns the non-NULL keys of a text[] as sorted, unique pairs
// (hstoreArrayToPairs).
func keyPairs(keys []*string) []pair {
	var ps []pair
	for _, k := range keys {
		if k != nil {
			ps = append(ps, pair{key: []byte(*k), null: true})
		}
	}
	return uniquePairs(ps)
}

//gopgext:function symbol=hstore_fetchval manual
func HstoreFetchval(v, key pg.Varlena) *pg.Varlena {
	h := load(v)
	i := h.find(nil, key)
	if i < 0 || h.isNull(i) {
		return nil
	}
	r := pg.Varlena(h.val(i))
	return &r
}

//gopgext:function symbol=hstore_exists manual
func HstoreExists(v, key pg.Varlena) bool { return load(v).find(nil, key) >= 0 }

//gopgext:function symbol=hstore_exists_any manual
func HstoreExistsAny(v pg.Varlena, keys []*string) bool {
	h := load(v)
	low := 0
	for _, p := range keyPairs(keys) {
		if h.find(&low, p.key) >= 0 {
			return true
		}
	}
	return false
}

//gopgext:function symbol=hstore_exists_all manual
func HstoreExistsAll(v pg.Varlena, keys []*string) bool {
	h := load(v)
	low := 0
	for _, p := range keyPairs(keys) {
		if h.find(&low, p.key) < 0 {
			return false
		}
	}
	return true
}

//gopgext:function symbol=hstore_defined manual
func HstoreDefined(v, key pg.Varlena) bool {
	h := load(v)
	i := h.find(nil, key)
	return i >= 0 && !h.isNull(i)
}

//gopgext:function symbol=hstore_delete manual
func HstoreDelete(v, key pg.Varlena) []byte {
	h := load(v)
	var b builder
	for i := 0; i < h.n; i++ {
		if !bytes.Equal(h.key(i), key) {
			b.copyFrom(h, i)
		}
	}
	return b.bytes()
}

//gopgext:function symbol=hstore_delete_array manual
func HstoreDeleteArray(v pg.Varlena, keys []*string) []byte {
	h := load(v)
	ks := keyPairs(keys)
	var b builder
	for i, j := 0, 0; i < h.n; {
		d := -1
		if j < len(ks) {
			d = cmpKey(h.key(i), ks[j].key)
		}
		switch {
		case d > 0:
			j++
		case d == 0:
			i++
			j++
		default:
			b.copyFrom(h, i)
			i++
		}
	}
	return b.bytes()
}

// sameValue reports whether pair i of a and pair j of b have equal values.
func sameValue(a hstore, i int, b hstore, j int) bool {
	if a.isNull(i) != b.isNull(j) {
		return false
	}
	return a.isNull(i) || bytes.Equal(a.val(i), b.val(j))
}

//gopgext:function symbol=hstore_delete_hstore manual
func HstoreDeleteHstore(v, v2 pg.Varlena) []byte {
	h, h2 := load(v), load(v2)
	var b builder
	for i, j := 0, 0; i < h.n; {
		d := -1
		if j < h2.n {
			d = cmpKey(h.key(i), h2.key(j))
		}
		switch {
		case d > 0:
			j++
		case d == 0:
			if !sameValue(h, i, h2, j) {
				b.copyFrom(h, i)
			}
			i++
			j++
		default:
			b.copyFrom(h, i)
			i++
		}
	}
	return b.bytes()
}

//gopgext:function symbol=hstore_concat manual
func HstoreConcat(v1, v2 pg.Varlena) []byte {
	s1, s2 := load(v1), load(v2)
	var b builder
	for i, j := 0, 0; i < s1.n || j < s2.n; {
		var d int
		switch {
		case i >= s1.n:
			d = 1
		case j >= s2.n:
			d = -1
		default:
			d = cmpKey(s1.key(i), s2.key(j))
		}
		if d >= 0 {
			b.copyFrom(s2, j)
			j++
			if d == 0 {
				i++
			}
		} else {
			b.copyFrom(s1, i)
			i++
		}
	}
	return b.bytes()
}

//gopgext:function symbol=hstore_slice_to_array manual
func HstoreSliceToArray(v pg.Varlena, keys pg.AnyArray) (pg.AnyArray, error) {
	h := load(v)
	ks := texts(keys)
	out := make([]*pg.AnyElement, len(ks))
	for n, k := range ks {
		i := -1
		if k != nil {
			i = h.find(nil, k)
		}
		if i >= 0 && !h.isNull(i) {
			e := pg.FromBytes(textOID, h.val(i))
			out[n] = &e
		}
	}
	if len(ks) == 0 {
		return pg.NewAnyArrayDims(textOID, nil, nil, nil)
	}
	return pg.NewAnyArrayDims(textOID, out, keys.Dims(), keys.LowerBounds())
}

//gopgext:function symbol=hstore_slice_to_hstore manual
func HstoreSliceToHstore(v pg.Varlena, keys []*string) []byte {
	h := load(v)
	var b builder
	low := 0
	for _, p := range keyPairs(keys) {
		if i := h.find(&low, p.key); i >= 0 {
			b.copyFrom(h, i)
		}
	}
	return b.bytes()
}

//gopgext:function symbol=hstore_akeys manual
func HstoreAkeys(v pg.Varlena) []string {
	h := load(v)
	out := make([]string, h.n)
	for i := range out {
		out[i] = string(h.key(i))
	}
	return out
}

//gopgext:function symbol=hstore_avals manual
func HstoreAvals(v pg.Varlena) []*string {
	h := load(v)
	out := make([]*string, h.n)
	for i := range out {
		out[i] = h.value(i)
	}
	return out
}

// flat returns keys and values alternating.
func flat(h hstore) []*string {
	out := make([]*string, 0, 2*h.n)
	for i := 0; i < h.n; i++ {
		k := string(h.key(i))
		out = append(out, &k, h.value(i))
	}
	return out
}

//gopgext:function symbol=hstore_to_array manual
func HstoreToArray(v pg.Varlena) []*string { return flat(load(v)) }

//gopgext:function symbol=hstore_to_matrix manual
func HstoreToMatrix(v pg.Varlena) (pg.AnyArray, error) {
	h := load(v)
	if h.n == 0 {
		return pg.NewAnyArrayDims(textOID, nil, nil, nil)
	}
	es := make([]*pg.AnyElement, 0, 2*h.n)
	for i := 0; i < h.n; i++ {
		k := pg.FromBytes(textOID, h.key(i))
		es = append(es, &k)
		if h.isNull(i) {
			es = append(es, nil)
		} else {
			val := pg.FromBytes(textOID, h.val(i))
			es = append(es, &val)
		}
	}
	return pg.NewAnyArrayDims(textOID, es, []int{h.n, 2}, nil)
}

//gopgext:function symbol=hstore_skeys manual
func HstoreSkeys(v pg.Varlena) iter.Seq[string] {
	h := load(v)
	return func(yield func(string) bool) {
		for i := 0; i < h.n; i++ {
			if !yield(string(h.key(i))) {
				return
			}
		}
	}
}

//gopgext:function symbol=hstore_svals manual
func HstoreSvals(v pg.Varlena) iter.Seq[*string] {
	h := load(v)
	return func(yield func(*string) bool) {
		for i := 0; i < h.n; i++ {
			if !yield(h.value(i)) {
				return
			}
		}
	}
}

// keyValue is a row of each().
type keyValue struct {
	Key   string
	Value *string
}

//gopgext:function symbol=hstore_each manual
func HstoreEach(v pg.Varlena) iter.Seq[keyValue] {
	h := load(v)
	return func(yield func(keyValue) bool) {
		for i := 0; i < h.n; i++ {
			if !yield(keyValue{string(h.key(i)), h.value(i)}) {
				return
			}
		}
	}
}

func contains(val, tmpl hstore) bool {
	low := 0
	for i := 0; i < tmpl.n; i++ {
		j := val.find(&low, tmpl.key(i))
		if j < 0 || !sameValue(tmpl, i, val, j) {
			return false
		}
	}
	return true
}

//gopgext:function symbol=hstore_contains manual
func HstoreContains(v, tmpl pg.Varlena) bool { return contains(load(v), load(tmpl)) }

//gopgext:function symbol=hstore_contained manual
func HstoreContained(tmpl, v pg.Varlena) bool { return contains(load(v), load(tmpl)) }

// compare orders values (hstore_cmp): the string areas, then the counts,
// then the entries. Only equality is meaningful.
func compare(a, b pg.Varlena) int {
	h1, h2 := load(a), load(b)
	if h1.n == 0 || h2.n == 0 {
		switch {
		case h1.n > 0:
			return 1
		case h2.n > 0:
			return -1
		}
		return 0
	}
	l1, l2 := h1.dataLen(), h2.dataLen()
	if r := bytes.Compare(h1.strs()[:min(l1, l2)], h2.strs()[:min(l1, l2)]); r != 0 {
		return r
	}
	switch {
	case l1 > l2:
		return 1
	case l1 < l2:
		return -1
	case h1.n > h2.n:
		return 1
	case h1.n < h2.n:
		return -1
	}
	for j := 0; j < 2*h1.n; j++ {
		e1, e2 := h1.entry(j), h2.entry(j)
		n1, n2 := e1&entryIsNull != 0, e2&entryIsNull != 0
		if endPos(e1) == endPos(e2) && n1 == n2 {
			continue
		}
		switch {
		case endPos(e1) < endPos(e2):
			return -1
		case endPos(e1) > endPos(e2):
			return 1
		case n1:
			return 1
		case n2:
			return -1
		}
		return 0
	}
	return 0
}

//gopgext:function symbol=hstore_cmp manual
func HstoreCmp(a, b pg.Varlena) int32 { return int32(compare(a, b)) }

//gopgext:function symbol=hstore_eq manual
func HstoreEq(a, b pg.Varlena) bool { return compare(a, b) == 0 }

//gopgext:function symbol=hstore_ne manual
func HstoreNe(a, b pg.Varlena) bool { return compare(a, b) != 0 }

//gopgext:function symbol=hstore_gt manual
func HstoreGt(a, b pg.Varlena) bool { return compare(a, b) > 0 }

//gopgext:function symbol=hstore_ge manual
func HstoreGe(a, b pg.Varlena) bool { return compare(a, b) >= 0 }

//gopgext:function symbol=hstore_lt manual
func HstoreLt(a, b pg.Varlena) bool { return compare(a, b) < 0 }

//gopgext:function symbol=hstore_le manual
func HstoreLe(a, b pg.Varlena) bool { return compare(a, b) <= 0 }

//gopgext:function symbol=hstore_hash manual
func HstoreHash(v pg.Varlena) int32 { return int32(pg.HashBytes(load(v).b)) }

//gopgext:function symbol=hstore_hash_extended manual
func HstoreHashExtended(v pg.Varlena, seed int64) int64 {
	return int64(pg.HashBytesExtended(load(v).b, uint64(seed)))
}
