package main

// GiST (hstore_gist.c) and GIN (hstore_gin.c) operator classes, and
// subscripting (hstore_subs.c).

import (
	"hash/crc32"

	"gopgext/gin"
	"gopgext/gist"
	"gopgext/pg"
	"gopgext/subscript"
)

// Strategy numbers of the operator classes.
const (
	strategyContains    = 7
	strategyExists      = 9
	strategyExistsAny   = 10
	strategyExistsAll   = 11
	strategyOldContains = 13
)

// textArray decodes the contents of a text[] value (an index query):
// ArrayType after its varlena header, elements flattened, nil for NULL.
func textArray(b []byte) [][]byte {
	ndim := int(le.Uint32(b))
	dataOffset := int(le.Uint32(b[4:]))
	n := 0
	if ndim > 0 {
		n = 1
		for i := 0; i < ndim; i++ {
			n *= int(le.Uint32(b[12+4*i:]))
		}
	}
	var bitmap []byte
	pos := (16+8*ndim+7)&^7 - 4 // ARR_OVERHEAD_NONULLS, from the end of the header
	if dataOffset != 0 {
		bitmap = b[12+8*ndim:]
		pos = dataOffset - 4
	}
	out := make([][]byte, n)
	for i := 0; i < n; i++ {
		if bitmap != nil && bitmap[i/8]&(1<<(i%8)) == 0 {
			continue
		}
		// att_align_pointer: a nonzero byte starts an element (padding is
		// zero); else align to 4 relative to the ArrayType's start.
		if b[pos] == 0 {
			pos = (pos+4+3)&^3 - 4
		}
		var l, hdr int
		if b[pos]&1 == 1 { // 1-byte header
			l, hdr = int(b[pos]>>1), 1
		} else {
			l, hdr = int(le.Uint32(b[pos:])>>2), 4
		}
		out[i] = b[pos+hdr : pos+l]
		pos += l
	}
	return out
}

// ---- GiST: signatures ---------------------------------------------------

const (
	allIsTrue      = 0x04
	siglenDefault  = 16
	siglenMax      = 2024 // GISTMaxIndexKeySize
	gistHeaderSize = 4    // flag, after the varlena header
)

func isAllTrue(k []byte) bool { return le.Uint32(k)&allIsTrue != 0 }

func sign(k []byte) []byte { return k[gistHeaderSize:] }

func newKey(allTrue bool, siglen int) []byte {
	if allTrue {
		k := make([]byte, gistHeaderSize)
		le.PutUint32(k, allIsTrue)
		return k
	}
	return make([]byte, gistHeaderSize+siglen)
}

func hashBit(b []byte, siglen int) int { return int(crc32.ChecksumIEEE(b) % uint32(siglen*8)) }

func getBit(s []byte, i int) bool { return s[i/8]>>(i%8)&1 != 0 }

func setBit(s []byte, i int) { s[i/8] |= 1 << (i % 8) }

func sizeBitvec(s []byte) int {
	n := 0
	for _, c := range s {
		for ; c != 0; c &= c - 1 {
			n++
		}
	}
	return n
}

func hemdist(a, b []byte, siglen int) int {
	if isAllTrue(a) {
		if isAllTrue(b) {
			return 0
		}
		return siglen*8 - sizeBitvec(sign(b))
	} else if isAllTrue(b) {
		return siglen*8 - sizeBitvec(sign(a))
	}
	d := 0
	sa, sb := sign(a), sign(b)
	for i := 0; i < siglen; i++ {
		for x := sa[i] ^ sb[i]; x != 0; x &= x - 1 {
			d++
		}
	}
	return d
}

func gistCompress(value []byte) ([]byte, error) {
	siglen := gist.Option("siglen")
	key := newKey(false, siglen)
	s := sign(key)
	h := load(value)
	for i := 0; i < h.n; i++ {
		setBit(s, hashBit(h.key(i), siglen))
		if !h.isNull(i) {
			setBit(s, hashBit(h.val(i), siglen))
		}
	}
	return key, nil
}

func gistConsistent(key, query []byte, strategy int, leaf bool) (bool, bool) {
	if isAllTrue(key) {
		return true, true
	}
	siglen := gist.Option("siglen")
	s := sign(key)
	has := func(b []byte) bool { return getBit(s, hashBit(b, siglen)) }
	switch strategy {
	case strategyContains, strategyOldContains:
		q := load(query)
		for i := 0; i < q.n; i++ {
			if !has(q.key(i)) || (!q.isNull(i) && !has(q.val(i))) {
				return false, true
			}
		}
		return true, true
	case strategyExists:
		return has(query), true
	case strategyExistsAll:
		for _, k := range textArray(query) {
			if k != nil && !has(k) {
				return false, true
			}
		}
		return true, true
	case strategyExistsAny:
		for _, k := range textArray(query) {
			if k != nil && has(k) {
				return true, true
			}
		}
		return false, true
	}
	panic(pg.Errorf(pg.CodeInternalError, "Unsupported strategy number: %d", strategy))
}

func gistUnion(keys [][]byte) []byte {
	siglen := gist.Option("siglen")
	out := newKey(false, siglen)
	s := sign(out)
	for _, k := range keys {
		if isAllTrue(k) {
			return newKey(true, siglen)
		}
		for i, c := range sign(k)[:siglen] {
			s[i] |= c
		}
	}
	return out
}

func gistPenalty(key, added []byte) float64 {
	return float64(hemdist(key, added, gist.Option("siglen")))
}

func gistSame(a, b []byte) bool {
	if isAllTrue(a) || isAllTrue(b) {
		return isAllTrue(a) && isAllTrue(b)
	}
	siglen := gist.Option("siglen")
	return string(sign(a)[:siglen]) == string(sign(b)[:siglen])
}

// gistPickSplit is ghstore_picksplit: the farthest pair of keys seeds the
// two sides, then keys go, in order of preference, to the side whose union
// they're closest to.
func gistPickSplit(keys [][]byte) (left, right []int) {
	siglen := gist.Option("siglen")
	n := len(keys)
	waste, seed1, seed2 := -1, -1, -1
	// As in the original, the last key isn't considered as a seed.
	for k := 0; k < n-2; k++ {
		for j := k + 1; j <= n-2; j++ {
			if w := hemdist(keys[k], keys[j], siglen); w > waste {
				waste, seed1, seed2 = w, k, j
			}
		}
	}
	if seed1 < 0 {
		seed1, seed2 = 0, 1
	}
	cp := func(k []byte) []byte {
		c := newKey(isAllTrue(k), siglen)
		copy(c, k)
		return c
	}
	dl, dr := cp(keys[seed1]), cp(keys[seed2])
	type cost struct{ pos, cost int }
	costs := make([]cost, n)
	for j := range keys {
		a, b := hemdist(dl, keys[j], siglen), hemdist(dr, keys[j], siglen)
		c := a - b
		if c < 0 {
			c = -c
		}
		costs[j] = cost{j, c}
	}
	pgQsort(costs, func(x, y *cost) int { return x.cost - y.cost })
	merge := func(d, k []byte) {
		if isAllTrue(d) {
			return
		}
		s := sign(d)
		if isAllTrue(k) {
			for i := range s {
				s[i] = 0xff
			}
			return
		}
		for i, c := range sign(k)[:siglen] {
			s[i] |= c
		}
	}
	for _, c := range costs {
		j := c.pos
		if j == seed1 {
			left = append(left, j)
			continue
		} else if j == seed2 {
			right = append(right, j)
			continue
		}
		a, b := hemdist(dl, keys[j], siglen), hemdist(dr, keys[j], siglen)
		nl, nr := float64(len(left)), float64(len(right))
		if float64(a) < float64(b)+(-(nl-nr)*(nl-nr)*(nl-nr)*0.0001) {
			merge(dl, keys[j])
			left = append(left, j)
		} else {
			merge(dr, keys[j])
			right = append(right, j)
		}
	}
	return left, right
}

//gopgext:function symbol=ghstore_in manual
func GhstoreIn(s pg.CString) ([]byte, error) {
	return nil, pg.Errorf(pg.CodeFeatureNotSupported, "cannot accept a value of type %s", "ghstore")
}

//gopgext:function symbol=ghstore_out manual
func GhstoreOut(v pg.Varlena) (pg.CString, error) {
	return "", pg.Errorf(pg.CodeFeatureNotSupported, "cannot display a value of type %s", "ghstore")
}

// ---- GIN: keys are K<key>, V<value> or N (NULL value) ----------------------

func ginItem(flag byte, s []byte) []byte { return append([]byte{flag}, s...) }

func ginExtractValue(value []byte) ([][]byte, error) {
	h := load(value)
	keys := make([][]byte, 0, 2*h.n)
	for i := 0; i < h.n; i++ {
		keys = append(keys, ginItem('K', h.key(i)))
		if h.isNull(i) {
			keys = append(keys, ginItem('N', nil))
		} else {
			keys = append(keys, ginItem('V', h.val(i)))
		}
	}
	return keys, nil
}

func ginExtractQuery(query []byte, strategy int) (gin.Query, error) {
	switch strategy {
	case strategyContains:
		keys, _ := ginExtractValue(query)
		if len(keys) == 0 {
			return gin.Query{Mode: gin.All}, nil // contains {}: every row
		}
		return gin.Query{Keys: keys}, nil
	case strategyExists:
		return gin.Query{Keys: [][]byte{ginItem('K', query)}}, nil
	case strategyExistsAny, strategyExistsAll:
		var keys [][]byte
		for _, k := range textArray(query) {
			if k != nil { // NULLs are ignored, cf. hstoreArrayToPairs
				keys = append(keys, ginItem('K', k))
			}
		}
		if len(keys) == 0 && strategy == strategyExistsAll {
			return gin.Query{Mode: gin.All}, nil
		}
		return gin.Query{Keys: keys}, nil
	}
	return gin.Query{}, pg.Errorf(pg.CodeInternalError, "unrecognized strategy number: %d", strategy)
}

func ginConsistent(check []bool, strategy int, query []byte) (bool, bool) {
	all := func() bool {
		for _, c := range check {
			if !c {
				return false
			}
		}
		return true
	}
	switch strategy {
	case strategyContains:
		// The index doesn't pair keys with values: recheck.
		return all(), true
	case strategyExists, strategyExistsAny:
		return true, false
	case strategyExistsAll:
		return all(), false
	}
	panic(pg.Errorf(pg.CodeInternalError, "unrecognized strategy number: %d", strategy))
}

func registerIndexes() {
	gist.Register("gist_hstore_ops", gist.OpClass{
		Compress:   gistCompress,
		Consistent: gistConsistent,
		Union:      gistUnion,
		Penalty:    gistPenalty,
		PickSplit:  gistPickSplit,
		Same:       gistSame,
		Options: []gist.IntOption{{Name: "siglen", Desc: "signature length in bytes",
			Default: siglenDefault, Min: 1, Max: siglenMax}},
	})
	gin.Register("gin_hstore_ops", gin.OpClass{
		ExtractValue: ginExtractValue,
		ExtractQuery: ginExtractQuery,
		Consistent:   ginConsistent,
	})
}

// ---- Subscripting: h['key'] ---------------------------------------------------

func subscriptFetch(container []byte, subs [][]byte) ([]byte, bool, error) {
	h := load(container)
	i := h.find(nil, subs[0])
	if i < 0 || h.isNull(i) {
		return nil, false, nil
	}
	return h.val(i), true, nil
}

func subscriptAssign(container []byte, subs [][]byte, value []byte) ([]byte, error) {
	if subs[0] == nil {
		return nil, pg.Errorf(pg.CodeNullValueNotAllowed, "hstore subscript in assignment must not be null")
	}
	if err := checkPair(subs[0], value); err != nil {
		return nil, err
	}
	if container == nil {
		return fromPairs([]pair{{key: subs[0], val: value, null: value == nil}}), nil
	}
	h := load(container)
	var b builder
	done := false
	for i := 0; i < h.n || !done; {
		d := 1
		if i < h.n && !done {
			d = cmpKey(h.key(i), subs[0])
		} else if i < h.n {
			d = -1
		}
		if d >= 0 && !done {
			b.add(subs[0], value, value == nil)
			done = true
			if d == 0 {
				i++
			}
		} else {
			b.copyFrom(h, i)
			i++
		}
	}
	return b.bytes(), nil
}

func registerSubscript() {
	subscript.Register("hstore", subscript.Type{
		Subscript: "text",
		Result:    "text",
		Fetch:     subscriptFetch,
		Assign:    subscriptAssign,
	})
}
