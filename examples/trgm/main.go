// trgm is a small pg_trgm: trigram similarity of strings (similarity,
// show_trgm, the % operator with a threshold setting) and a GIN index
// (gopgext/gin) that answers %, LIKE and ILIKE with trigrams (lossy: rows
// are rechecked). A second GIN operator class indexes words, and answers
// "has a word starting with" (^@@) with partial-match keys.
package main

import (
	"bytes"
	"sort"
	"strings"
	"unicode"

	"gopgext/gin"
	"gopgext/pg"
)

// trgm.similarity_threshold: the threshold of the % operator.
var threshold = pg.NewFloatSetting("similarity_threshold", 0.3, 0, 1,
	"Sets the threshold used by the % operator.")

func main() {
	gin.Register("trgm_gin_ops", gin.OpClass{
		ExtractValue:  extractValue,
		ExtractQuery:  extractQuery,
		Consistent:    consistent,
		TriConsistent: triConsistent,
	})
	gin.Register("word_prefix_gin_ops", gin.OpClass{
		ExtractValue:   extractWords,
		ExtractQuery:   extractPrefix,
		Consistent:     prefixConsistent,
		ComparePartial: comparePrefix,
	})
}

// ---- Trigrams --------------------------------------------------------

func isWordChar(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }

// words returns the lowercased words (runs of letters and digits) of s.
func words(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !isWordChar(r) })
}

// wordTrigrams appends the trigrams of a word, padded with two spaces
// before and one after where the word's ends are known.
func wordTrigrams(out []string, w []rune, padLeft, padRight bool) []string {
	if len(w) == 0 {
		return out
	}
	var r []rune
	if padLeft {
		r = append(r, ' ', ' ')
	}
	r = append(r, w...)
	if padRight {
		r = append(r, ' ')
	}
	for i := 0; i+3 <= len(r); i++ {
		out = append(out, string(r[i:i+3]))
	}
	return out
}

// trigrams returns the unique trigrams of s, sorted.
func trigrams(s string) []string {
	var t []string
	for _, w := range words(s) {
		t = wordTrigrams(t, []rune(w), true, true)
	}
	return unique(t)
}

func unique(t []string) []string {
	sort.Strings(t)
	out := t[:0]
	for i, x := range t {
		if i == 0 || x != t[i-1] {
			out = append(out, x)
		}
	}
	return out
}

// likeTrigrams returns the trigrams every string matching a LIKE pattern
// has (lowercased): those of the literal words between wildcards, padded
// only where the pattern fixes a word boundary.
func likeTrigrams(pattern string) []string {
	var word []rune
	var out []string
	leftBound := true // the start of the pattern is the start of the string
	rs := []rune(pattern)
	for i := 0; i < len(rs); i++ {
		c := rs[i]
		switch {
		case c == '%' || c == '_':
			out = wordTrigrams(out, word, leftBound, false)
			word, leftBound = nil, false
			continue
		case c == '\\' && i+1 < len(rs):
			i++
			c = rs[i]
		}
		if isWordChar(c) {
			word = append(word, unicode.ToLower(c))
		} else {
			out = wordTrigrams(out, word, leftBound, true)
			word, leftBound = nil, true
		}
	}
	out = wordTrigrams(out, word, leftBound, true)
	return unique(out)
}

func similarity(a, b []string) float32 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	common := 0
	for i, j := 0, 0; i < len(a) && j < len(b); {
		switch {
		case a[i] == b[j]:
			common++
			i++
			j++
		case a[i] < b[j]:
			i++
		default:
			j++
		}
	}
	return float32(common) / float32(len(a)+len(b)-common)
}

// ShowTrgm returns the trigrams of a string.
//
//gopgext:function immutable parallel=safe
func ShowTrgm(s string) []string { return trigrams(s) }

// Similarity returns how similar two strings are: the shared trigrams
// over all their trigrams (0 to 1).
//
//gopgext:function immutable parallel=safe
func Similarity(a, b string) float32 { return similarity(trigrams(a), trigrams(b)) }

// SimilarityOp is the % operator: is the similarity at least
// trgm.similarity_threshold?
//
//gopgext:function stable parallel=safe
func SimilarityOp(a, b string) bool {
	return float64(Similarity(a, b)) >= threshold.Get()
}

// LikeTrigrams returns the trigrams the index looks for for a LIKE pattern.
//
//gopgext:function immutable parallel=safe
func LikeTrigrams(pattern string) []string { return likeTrigrams(pattern) }

// ---- GIN: trigram keys -------------------------------------------------

const (
	strategySimilar = 1 // %
	strategyLike    = 3 // ~~
	strategyILike   = 4 // ~~*
)

func keys(t []string) [][]byte {
	k := make([][]byte, len(t))
	for i, s := range t {
		k[i] = []byte(s)
	}
	return k
}

func extractValue(value []byte) ([][]byte, error) { return keys(trigrams(string(value))), nil }

func extractQuery(query []byte, strategy int) (gin.Query, error) {
	switch strategy {
	case strategySimilar:
		return gin.Query{Keys: keys(trigrams(string(query)))}, nil
	case strategyLike, strategyILike:
		t := likeTrigrams(string(query))
		if len(t) == 0 {
			return gin.Query{Mode: gin.All}, nil // e.g. '%a%': check every row
		}
		return gin.Query{Keys: keys(t)}, nil
	}
	return gin.Query{}, pg.Errorf(pg.CodeInternalError, "unknown strategy %d", strategy)
}

func consistent(check []bool, strategy int, query []byte) (match, recheck bool) {
	n := 0
	for _, c := range check {
		if c {
			n++
		}
	}
	if strategy == strategySimilar {
		// The similarity is at most shared/len(query trigrams).
		return len(check) > 0 && float64(n)/float64(len(check)) >= threshold.Get(), true
	}
	return n == len(check), true
}

func triConsistent(check []gin.Ternary, strategy int, query []byte) gin.Ternary {
	n := 0
	for _, c := range check {
		if c != gin.False {
			n++
		}
	}
	if strategy == strategySimilar {
		if len(check) > 0 && float64(n)/float64(len(check)) >= threshold.Get() {
			return gin.Maybe
		}
		return gin.False
	}
	if n == len(check) {
		return gin.Maybe
	}
	return gin.False
}

// ---- GIN: word keys, prefix search ---------------------------------------

// HasWordPrefix is the ^@@ operator: does a word of t start with prefix
// (ignoring case)?
//
//gopgext:function immutable parallel=safe
func HasWordPrefix(t, prefix string) bool {
	p := strings.ToLower(prefix)
	for _, w := range words(t) {
		if strings.HasPrefix(w, p) {
			return true
		}
	}
	return false
}

func extractWords(value []byte) ([][]byte, error) { return keys(words(string(value))), nil }

func extractPrefix(query []byte, strategy int) (gin.Query, error) {
	p := strings.ToLower(string(query))
	if p == "" {
		return gin.Query{Mode: gin.All}, nil
	}
	return gin.Query{Keys: [][]byte{[]byte(p)}, Partial: []bool{true}}, nil
}

func prefixConsistent(check []bool, strategy int, query []byte) (match, recheck bool) {
	if len(check) == 0 {
		return true, true // empty prefix: every row, rechecked
	}
	return check[0], false // the matched key is a word with the prefix: exact
}

// comparePrefix: the words with the prefix follow it in byte order.
func comparePrefix(partial, key []byte, strategy int) int {
	if bytes.HasPrefix(key, partial) {
		return 0
	}
	if bytes.Compare(key, partial) > 0 {
		return 1 // past the words with the prefix
	}
	return -1
}
