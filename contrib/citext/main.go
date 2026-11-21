// citext is a Go port of PostgreSQL's contrib/citext (version 1.8): a
// case-insensitive character string type.
//
// The SQL objects (the type, which reuses text's I/O functions, operators,
// operator classes, casts and the case-insensitive wrappers of string
// functions) are upstream's install scripts, in sql/. They reference 17 C
// functions, implemented here under upstream's C symbol names.
//
// Ported from PostgreSQL 18's contrib/citext/citext.c. As there, values are
// lowercased with the database's default collation (so that equality and
// hashing don't depend on the collation), and ordering compares the
// lowercased strings with the call's collation.
package main

import (
	"bytes"

	"gopgext/pg"
)

func main() {}

// lower lowercases s with the default collation (str_tolower).
func lower(s pg.Varlena) (string, error) { return pg.ToLower(string(s), pg.DefaultCollation) }

// citextcmp compares the lowercased strings with the call's collation.
func citextcmp(l, r pg.Varlena) (int, error) {
	ls, err := lower(l)
	if err != nil {
		return 0, err
	}
	rs, err := lower(r)
	if err != nil {
		return 0, err
	}
	return pg.CompareStrings(ls, rs, pg.Collation())
}

// patternCmp compares the lowercased strings byte by byte.
func patternCmp(l, r pg.Varlena) (int, error) {
	ls, err := lower(l)
	if err != nil {
		return 0, err
	}
	rs, err := lower(r)
	if err != nil {
		return 0, err
	}
	return bytes.Compare([]byte(ls), []byte(rs)), nil
}

func equal(l, r pg.Varlena) (bool, error) {
	ls, err := lower(l)
	if err != nil {
		return false, err
	}
	rs, err := lower(r)
	if err != nil {
		return false, err
	}
	return ls == rs, nil
}

//gopgext:function symbol=citext_cmp manual
func CitextCmp(l, r pg.Varlena) (int32, error) {
	c, err := citextcmp(l, r)
	return int32(c), err
}

//gopgext:function symbol=citext_pattern_cmp manual
func CitextPatternCmp(l, r pg.Varlena) (int32, error) {
	c, err := patternCmp(l, r)
	return int32(c), err
}

//gopgext:function symbol=citext_hash manual
func CitextHash(s pg.Varlena) (int32, error) {
	ls, err := lower(s)
	if err != nil {
		return 0, err
	}
	return int32(pg.HashBytes([]byte(ls))), nil
}

//gopgext:function symbol=citext_hash_extended manual
func CitextHashExtended(s pg.Varlena, seed int64) (int64, error) {
	ls, err := lower(s)
	if err != nil {
		return 0, err
	}
	return int64(pg.HashBytesExtended([]byte(ls), uint64(seed))), nil
}

//gopgext:function symbol=citext_eq manual
func CitextEq(l, r pg.Varlena) (bool, error) { return equal(l, r) }

//gopgext:function symbol=citext_ne manual
func CitextNe(l, r pg.Varlena) (bool, error) {
	eq, err := equal(l, r)
	return !eq, err
}

func cmpIs(l, r pg.Varlena, cmp func(l, r pg.Varlena) (int, error), ok func(int) bool) (bool, error) {
	c, err := cmp(l, r)
	return ok(c), err
}

func lt(c int) bool { return c < 0 }
func le(c int) bool { return c <= 0 }
func gt(c int) bool { return c > 0 }
func ge(c int) bool { return c >= 0 }

//gopgext:function symbol=citext_lt manual
func CitextLt(l, r pg.Varlena) (bool, error) { return cmpIs(l, r, citextcmp, lt) }

//gopgext:function symbol=citext_le manual
func CitextLe(l, r pg.Varlena) (bool, error) { return cmpIs(l, r, citextcmp, le) }

//gopgext:function symbol=citext_gt manual
func CitextGt(l, r pg.Varlena) (bool, error) { return cmpIs(l, r, citextcmp, gt) }

//gopgext:function symbol=citext_ge manual
func CitextGe(l, r pg.Varlena) (bool, error) { return cmpIs(l, r, citextcmp, ge) }

//gopgext:function symbol=citext_pattern_lt manual
func CitextPatternLt(l, r pg.Varlena) (bool, error) { return cmpIs(l, r, patternCmp, lt) }

//gopgext:function symbol=citext_pattern_le manual
func CitextPatternLe(l, r pg.Varlena) (bool, error) { return cmpIs(l, r, patternCmp, le) }

//gopgext:function symbol=citext_pattern_gt manual
func CitextPatternGt(l, r pg.Varlena) (bool, error) { return cmpIs(l, r, patternCmp, gt) }

//gopgext:function symbol=citext_pattern_ge manual
func CitextPatternGe(l, r pg.Varlena) (bool, error) { return cmpIs(l, r, patternCmp, ge) }

//gopgext:function symbol=citext_smaller manual
func CitextSmaller(l, r pg.Varlena) (pg.Varlena, error) {
	c, err := citextcmp(l, r)
	if c < 0 {
		return l, err
	}
	return r, err
}

//gopgext:function symbol=citext_larger manual
func CitextLarger(l, r pg.Varlena) (pg.Varlena, error) {
	c, err := citextcmp(l, r)
	if c > 0 {
		return l, err
	}
	return r, err
}
