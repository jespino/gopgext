// fuzzystrmatch is a Go port of PostgreSQL's contrib/fuzzystrmatch (version
// 1.2): functions to determine similarities and distance between strings.
// It's a drop-in replacement: same SQL functions, same results, and it
// passes the upstream regression tests.
//
// Ported from PostgreSQL 19's fuzzystrmatch.c, dmetaphone.c,
// daitch_mokotoff.c and src/backend/utils/adt/levenshtein.c. Like the
// original, it assumes a UTF-8 (or single-byte ASCII-compatible) database
// encoding.
package main

import "gopgext/pg"

func main() {}

// ---- Levenshtein ------------------------------------------------------

const maxLevenshteinStrlen = 255

//gopgext:function immutable parallel=safe
func Levenshtein(source, target string) (int32, error) {
	return levenshtein(source, target, 1, 1, 1, -1, false)
}

//gopgext:function name=levenshtein immutable parallel=safe
func LevenshteinWithCosts(source, target string, insCost, delCost, subCost int32) (int32, error) {
	return levenshtein(source, target, insCost, delCost, subCost, -1, false)
}

//gopgext:function immutable parallel=safe
func LevenshteinLessEqual(source, target string, maxD int32) (int32, error) {
	return levenshtein(source, target, 1, 1, 1, maxD, true)
}

//gopgext:function name=levenshtein_less_equal immutable parallel=safe
func LevenshteinLessEqualWithCosts(source, target string, insCost, delCost, subCost, maxD int32) (int32, error) {
	return levenshtein(source, target, insCost, delCost, subCost, maxD, true)
}

// utf8Len is pg_utf_mblen: the length of the UTF-8 character starting with b.
func utf8Len(b byte) int {
	switch {
	case b&0x80 == 0:
		return 1
	case b&0xe0 == 0xc0:
		return 2
	case b&0xf0 == 0xe0:
		return 3
	case b&0xf8 == 0xf0:
		return 4
	}
	return 1
}

// chars splits s into (possibly multibyte) characters, like
// pg_mblen_range() does.
func chars(s string) []string {
	var out []string
	for i := 0; i < len(s); {
		n := min(utf8Len(s[i]), len(s)-i)
		out = append(out, s[i:i+n])
		i += n
	}
	return out
}

func levenshteinResult(d int64) (int32, error) {
	if d < -1<<31 || d > 1<<31-1 {
		return 0, pg.Errorf(pg.CodeNumericValueOutOfRange, "levenshtein distance out of range")
	}
	return int32(d), nil
}

// levenshtein is varstr_levenshtein() (lessEqual false) and
// varstr_levenshtein_less_equal(): with maxD >= 0, the result is exact if
// it's <= maxD, else some value > maxD (the same value PostgreSQL returns).
func levenshtein(source, target string, insC, delC, subC, maxD int32, lessEqual bool) (int32, error) {
	s, t := chars(source), chars(target)
	m, n := len(s), len(t)
	ins, del, sub := int64(insC), int64(delC), int64(subC)

	if m == 0 {
		return levenshteinResult(int64(n) * ins)
	}
	if n == 0 {
		return levenshteinResult(int64(m) * del)
	}
	if m > maxLevenshteinStrlen || n > maxLevenshteinStrlen {
		return 0, pg.Errorf(pg.CodeInvalidParameterValue,
			"levenshtein argument exceeds maximum length of %d characters", maxLevenshteinStrlen)
	}

	startColumn, stopColumn := 0, m+1
	if !lessEqual {
		maxD = -1
	}
	if maxD >= 0 {
		netInserts := n - m
		var minTheoD int64
		if netInserts < 0 {
			minTheoD = int64(-netInserts) * del
		} else {
			minTheoD = int64(netInserts) * ins
		}
		if minTheoD > int64(maxD) {
			return levenshteinResult(int64(maxD) + 1)
		}
		if ins+del < sub {
			sub = ins + del
		}
		maxTheoD := minTheoD + sub*int64(min(m, n))
		if int64(maxD) >= maxTheoD {
			maxD = -1
		} else if ins+del > 0 {
			slackD := int64(maxD) - minTheoD
			bestColumn := 0
			if netInserts < 0 {
				bestColumn = -netInserts
			}
			stopColumn = int(min(int64(bestColumn)+slackD/(ins+del)+1, int64(m+1)))
		}
	}
	if !lessEqual {
		stopColumn = m + 1 // STOP_COLUMN is m (after the increment below)
	}

	// One more cell for the initialization column and row.
	m++
	n++
	prev := make([]int64, m)
	curr := make([]int64, m)
	for i := startColumn; i < stopColumn; i++ {
		prev[i] = int64(i) * del
	}
	for j := 1; j < n; j++ {
		y := t[j-1]
		var i int
		if lessEqual {
			if stopColumn < m {
				prev[stopColumn] = int64(maxD) + 1
				stopColumn++
			}
		}
		if startColumn == 0 {
			curr[0] = int64(j) * ins
			i = 1
		} else {
			i = startColumn
		}
		for ; i < stopColumn; i++ {
			insCost := prev[i] + ins
			delCost := curr[i-1] + del
			subCost := prev[i-1]
			if s[i-1] != y {
				subCost += sub
			}
			curr[i] = min(insCost, delCost, subCost)
		}
		prev, curr = curr, prev

		if maxD >= 0 {
			// The "zero point" is the column where the remaining portions
			// of the strings are of equal length.
			zp := j - (n - m)
			cost := func(col int) int64 {
				net := col - zp
				if net > 0 {
					return prev[col] + int64(net)*ins
				}
				return prev[col] + int64(-net)*del
			}
			for stopColumn > 0 {
				if cost(stopColumn-1) <= int64(maxD) {
					break
				}
				stopColumn--
			}
			for startColumn < stopColumn {
				if cost(startColumn) <= int64(maxD) {
					break
				}
				prev[startColumn] = int64(maxD) + 1
				curr[startColumn] = int64(maxD) + 1
				startColumn++
			}
			if startColumn >= stopColumn {
				return levenshteinResult(int64(maxD) + 1)
			}
		}
	}
	return levenshteinResult(prev[m-1])
}

// ---- Soundex -----------------------------------------------------------

const soundexLen = 4

const soundexTable = "01230120022455012623010202"

func asciiUpper(c byte) byte {
	if c >= 'a' && c <= 'z' {
		return c - 'a' + 'A'
	}
	return c
}

func asciiIsAlpha(c byte) bool { return c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' }

func soundexCode(c byte) byte {
	c = asciiUpper(c)
	if c >= 'A' && c <= 'Z' {
		return soundexTable[c-'A']
	}
	return c
}

// soundexOf returns the 4-character code; all zero bytes if s has no letters
// (an empty string as text, but compared as four NULs by difference).
func soundexOf(s string) [soundexLen]byte {
	var out [soundexLen]byte
	i := 0
	for i < len(s) && !asciiIsAlpha(s[i]) {
		i++
	}
	if i == len(s) {
		return out
	}
	out[0] = asciiUpper(s[i])
	i++
	count := 1
	for i < len(s) && count < soundexLen {
		if asciiIsAlpha(s[i]) && soundexCode(s[i]) != soundexCode(s[i-1]) {
			if c := soundexCode(s[i]); c != '0' {
				out[count] = c
				count++
			}
		}
		i++
	}
	for ; count < soundexLen; count++ {
		out[count] = '0'
	}
	return out
}

func soundexText(code [soundexLen]byte) string {
	if code[0] == 0 {
		return ""
	}
	return string(code[:])
}

//gopgext:function immutable parallel=safe
func Soundex(s string) string { return soundexText(soundexOf(s)) }

//gopgext:function immutable parallel=safe
func TextSoundex(s string) string { return soundexText(soundexOf(s)) }

//gopgext:function immutable parallel=safe
func Difference(a, b string) int32 {
	sa, sb := soundexOf(a), soundexOf(b)
	var n int32
	for i := range sa {
		if sa[i] == sb[i] {
			n++
		}
	}
	return n
}

// ---- Metaphone ---------------------------------------------------------

const maxMetaphoneStrlen = 255

//gopgext:function immutable parallel=safe
func Metaphone(s string, maxPhonemes int32) (string, error) {
	if len(s) == 0 {
		return "", nil
	}
	if len(s) > maxMetaphoneStrlen {
		return "", pg.Errorf(pg.CodeInvalidParameterValue,
			"argument exceeds the maximum length of %d bytes", maxMetaphoneStrlen)
	}
	if maxPhonemes > maxMetaphoneStrlen {
		return "", pg.Errorf(pg.CodeInvalidParameterValue,
			"output exceeds the maximum length of %d bytes", maxMetaphoneStrlen)
	}
	if maxPhonemes <= 0 {
		return "", pg.Errorf("2200F", "output cannot be empty string")
	}
	return metaphone(s, int(maxPhonemes)), nil
}

// Letter classes for metaphone.
var metaCodes = [26]byte{1, 16, 4, 16, 9, 2, 4, 16, 9, 2, 0, 2, 2, 2, 1, 4, 0, 2, 4, 4, 1, 0, 0, 0, 8, 0}

func getcode(c byte) byte {
	c = asciiUpper(c)
	if c >= 'A' && c <= 'Z' {
		return metaCodes[c-'A']
	}
	return 0
}

func isVowel(c byte) bool  { return getcode(c)&1 != 0 }  // AEIOU
func affectH(c byte) bool  { return getcode(c)&4 != 0 }  // CGPST
func makeSoft(c byte) bool { return getcode(c)&8 != 0 }  // EIY
func noGhToF(c byte) bool  { return getcode(c)&16 != 0 } // BDH

func metaphone(s string, maxPhonemes int) string {
	// NUL-terminated (and padded) like the C string the original works on.
	word := append([]byte(s), 0, 0, 0, 0)
	at := func(i int) byte {
		if i < 0 || i >= len(word) {
			return 0
		}
		return asciiUpper(word[i])
	}
	w := 0
	var out []byte
	phonize := func(c byte) { out = append(out, c) }
	curr := func() byte { return at(w) }
	next := func() byte { return at(w + 1) }
	prev := func() byte { return at(w - 1) }
	lookBack := func(n int) byte {
		if w >= n {
			return at(w - n)
		}
		return 0
	}
	afterNext := func() byte {
		if next() != 0 {
			return at(w + 2)
		}
		return 0
	}
	lookAhead := func(n int) byte {
		i := 0
		for word[w+i] != 0 && i < n {
			i++
		}
		return asciiUpper(word[w+i])
	}
	isBreak := func(c byte) bool { return !asciiIsAlpha(c) }

	// The first phoneme is processed specially.
	for ; !asciiIsAlpha(curr()); w++ {
		if curr() == 0 {
			return string(out)
		}
	}
	switch curr() {
	case 'A':
		if next() == 'E' {
			phonize('E')
			w += 2
		} else {
			phonize('A')
			w++
		}
	case 'G', 'K', 'P':
		if next() == 'N' {
			phonize('N')
			w += 2
		}
	case 'W':
		if next() == 'H' || next() == 'R' {
			phonize(next())
			w += 2
		} else if isVowel(next()) {
			phonize('W')
			w += 2
		}
	case 'X':
		phonize('S')
		w++
	case 'E', 'I', 'O', 'U':
		phonize(curr())
		w++
	}

	for ; curr() != 0 && len(out) < maxPhonemes; w++ {
		skip := 0
		c := curr()
		if !asciiIsAlpha(c) {
			continue
		}
		if c == prev() && c != 'C' {
			continue
		}
		switch c {
		case 'B':
			if prev() != 'M' {
				phonize('B')
			}
		case 'C':
			if makeSoft(next()) {
				if afterNext() == 'A' && next() == 'I' {
					phonize('X') // CIA
				} else if prev() == 'S' {
					// SC[IEY]: dropped
				} else {
					phonize('S')
				}
			} else if next() == 'H' {
				if afterNext() == 'R' || prev() == 'S' {
					phonize('K') // Christ, School
				} else {
					phonize('X')
				}
				skip++
			} else {
				phonize('K')
			}
		case 'D':
			if next() == 'G' && makeSoft(afterNext()) {
				phonize('J')
				skip++
			} else {
				phonize('T')
			}
		case 'G':
			if next() == 'H' {
				if !(noGhToF(lookBack(3)) || lookBack(4) == 'H') {
					phonize('F')
					skip++
				}
			} else if next() == 'N' {
				if isBreak(afterNext()) || (afterNext() == 'E' && lookAhead(3) == 'D') {
					// dropped
				} else {
					phonize('K')
				}
			} else if makeSoft(next()) && prev() != 'G' {
				phonize('J')
			} else {
				phonize('K')
			}
		case 'H':
			if isVowel(next()) && !affectH(prev()) {
				phonize('H')
			}
		case 'K':
			if prev() != 'C' {
				phonize('K')
			}
		case 'P':
			if next() == 'H' {
				phonize('F')
			} else {
				phonize('P')
			}
		case 'Q':
			phonize('K')
		case 'S':
			if next() == 'I' && (afterNext() == 'O' || afterNext() == 'A') {
				phonize('X')
			} else if next() == 'H' {
				phonize('X')
				skip++
			} else if next() == 'C' && lookAhead(2) == 'H' && lookAhead(3) == 'W' {
				phonize('X')
				skip += 2
			} else {
				phonize('S')
			}
		case 'T':
			if next() == 'I' && (afterNext() == 'O' || afterNext() == 'A') {
				phonize('X')
			} else if next() == 'H' {
				phonize('0')
				skip++
			} else {
				phonize('T')
			}
		case 'V':
			phonize('F')
		case 'W':
			if isVowel(next()) {
				phonize('W')
			}
		case 'X':
			phonize('K')
			if len(out) < maxPhonemes {
				phonize('S')
			}
		case 'Y':
			if isVowel(next()) {
				phonize('Y')
			}
		case 'Z':
			phonize('S')
		case 'F', 'J', 'L', 'M', 'N', 'R':
			phonize(c)
		}
		w += skip
	}
	return string(out)
}
