// textutil brings parts of Go's standard library to SQL: regular
// expressions, JSON, hashing, Unicode-aware text processing.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"iter"
	"regexp"
	"strings"
	"unicode"

	"gopgext/pg"
)

// Compiled regular expressions are cached per backend.
var regexps = map[string]*regexp.Regexp{}

func compile(pattern string) (*regexp.Regexp, error) {
	if re, ok := regexps[pattern]; ok {
		return re, nil
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, pg.Errorf(pg.CodeInvalidParameterValue, "invalid regular expression: %v", err)
	}
	regexps[pattern] = re
	return re, nil
}

// GoMatches returns every match of a Go (RE2) regular expression in s.
//
//gopgext:function immutable parallel=safe
func GoMatches(s, pattern string) (iter.Seq[string], error) {
	re, err := compile(pattern)
	if err != nil {
		return nil, err
	}
	return func(yield func(string) bool) {
		for _, m := range re.FindAllString(s, -1) {
			if !yield(m) {
				return
			}
		}
	}, nil
}

// Slugify turns a title into a URL slug: "Héllo, World!" -> "hello-world".
//
//gopgext:function immutable parallel=safe
func Slugify(s string) string {
	var sb strings.Builder
	dash := false
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z' || r >= '0' && r <= '9':
			sb.WriteRune(r)
			dash = false
		case unicode.IsLetter(r):
			if a := asciiFold(r); a != 0 {
				sb.WriteRune(a)
				dash = false
			}
		case sb.Len() > 0 && !dash:
			sb.WriteByte('-')
			dash = true
		}
	}
	return strings.TrimSuffix(sb.String(), "-")
}

func asciiFold(r rune) rune {
	for _, f := range []struct {
		from string
		to   rune
	}{{"áàäâã", 'a'}, {"éèëê", 'e'}, {"íìïî", 'i'}, {"óòöôõ", 'o'}, {"úùüû", 'u'}, {"ñ", 'n'}, {"ç", 'c'}} {
		if strings.ContainsRune(f.from, r) {
			return f.to
		}
	}
	return 0
}

// Levenshtein is the edit distance between a and b (in runes).
//
//gopgext:function immutable parallel=safe
func Levenshtein(a, b string) int32 {
	ra, rb := []rune(a), []rune(b)
	prev := make([]int32, len(rb)+1)
	cur := make([]int32, len(rb)+1)
	for j := range prev {
		prev[j] = int32(j)
	}
	for i := 1; i <= len(ra); i++ {
		cur[0] = int32(i)
		for j := 1; j <= len(rb); j++ {
			cost := int32(1)
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(rb)]
}

// Sha256Hex is the hex-encoded SHA-256 of data.
//
//gopgext:function immutable parallel=safe
func Sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// JSONKeys returns the sorted keys of a JSON object, with Go's encoding/json.
//
//gopgext:function immutable parallel=safe
func JSONKeys(doc string) ([]string, error) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal([]byte(doc), &obj); err != nil {
		return nil, pg.Errorf(pg.CodeInvalidTextRepresentation, "invalid JSON object: %v", err)
	}
	keys := make([]string, 0, len(obj))
	for k := range obj {
		keys = append(keys, k)
	}
	// sorted, without importing sort: insertion sort is fine for small objects
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j] < keys[j-1]; j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
	return keys, nil
}

type person struct {
	Name string   `json:"name"`
	Age  int      `json:"age"`
	Tags []string `json:"tags,omitempty"`
}

// PersonJSON builds a JSON document from a Go struct.
//
//gopgext:function immutable parallel=safe
func PersonJSON(name string, age int32, tags []string) (string, error) {
	b, err := json.Marshal(person{Name: name, Age: int(age), Tags: tags})
	return string(b), err
}

func main() {}
