// Command textsearch shows gopgext/tsearch: a text search parser and a
// dictionary template written in Go, a data file in tsearch_data/, and
// calling dictionaries from Go.
package main

import (
	"iter"
	"strings"
	"unicode"

	"gopgext/pg"
	"gopgext/tsearch"
)

// Token types of the parser.
const (
	tokWord   = 1
	tokNumber = 2
	tokTag    = 3
	tokBlank  = 4
)

// tagParser splits text into words, numbers, #tags and the blanks between
// them.
type tagParser struct{}

func (tagParser) TokenTypes() []tsearch.TokenType {
	return []tsearch.TokenType{
		{ID: tokWord, Alias: "word", Description: "Word"},
		{ID: tokNumber, Alias: "number", Description: "Number"},
		{ID: tokTag, Alias: "tag", Description: "Hash tag"},
		{ID: tokBlank, Alias: "blank", Description: "Space symbols"},
	}
}

func (tagParser) Parse(text string) tsearch.Tokenizer { return &tokenizer{text: text} }

type tokenizer struct{ text string }

func class(r rune) int {
	switch {
	case unicode.IsLetter(r):
		return tokWord
	case unicode.IsDigit(r):
		return tokNumber
	}
	return tokBlank
}

func (t *tokenizer) Next() (int, string) {
	if t.text == "" {
		return 0, ""
	}
	s := t.text
	typ, i := 0, 0
	if s[0] == '#' && len(s) > 1 && class([]rune(s[1:2])[0]) != tokBlank {
		typ, i = tokTag, 1
	}
	for j, r := range s[i:] {
		c := class(r)
		if typ == 0 {
			typ = c
		} else if c != typ && !(typ == tokTag && c != tokBlank) || typ == tokBlank && r == '#' {
			i += j
			t.text = s[i:]
			return typ, s[:i]
		}
	}
	t.text = ""
	if typ == 0 {
		typ = tokBlank
	}
	return typ, s
}

// compoundDict splits the compound words of a data file into their parts,
// as a second variant; with "prefix", lexemes match as prefixes in queries.
type compoundDict struct {
	parts  map[string][]string
	prefix bool
}

func compoundInit(opts []tsearch.Option) (tsearch.Dictionary, error) {
	d := &compoundDict{parts: map[string][]string{}}
	file := ""
	for _, o := range opts {
		var err error
		switch o.Name {
		case "file":
			file, err = o.Text()
		case "prefix":
			d.prefix, err = o.Bool()
		default:
			err = pg.Errorf(pg.CodeInvalidParameterValue, "unrecognized compound parameter: %q", o.Name)
		}
		if err != nil {
			return nil, err
		}
	}
	if file == "" {
		return nil, pg.Errorf(pg.CodeInvalidParameterValue, "missing File parameter")
	}
	for line, err := range tsearch.ReadConfigFile(tsearch.ConfigFile(file, "txt")) {
		if err != nil {
			return nil, err
		}
		f := strings.Fields(tsearch.Lower(line))
		if len(f) == 0 || strings.HasPrefix(f[0], "#") {
			continue
		}
		if len(f) == 1 {
			// Has the context "line N of configuration file ...".
			pg.Warningf("compound word %q without parts", f[0])
			continue
		}
		d.parts[f[0]] = f[1:]
	}
	return d, nil
}

func (d *compoundDict) Lexize(word string) ([]tsearch.Lexeme, error) {
	w := tsearch.Lower(word)
	var flags uint16
	if d.prefix {
		flags = tsearch.Prefix
	}
	parts, ok := d.parts[w]
	if !ok {
		if strings.HasPrefix(w, "stop") {
			return tsearch.StopWord, nil
		}
		return nil, nil // unknown: the next dictionary
	}
	res := []tsearch.Lexeme{{Text: w, Variant: 1, Flags: flags}}
	for _, p := range parts {
		res = append(res, tsearch.Lexeme{Text: p, Variant: 2, Flags: flags})
	}
	return res, nil
}

// Lexeme is a lexeme returned by a dictionary.
type Lexeme struct {
	Lexeme  string
	Variant int32
	Flags   int32
}

// Lexemes runs dictionary dict on word from Go (tsearch.Lexize).
//
//gopgext:function stable
func Lexemes(dict pg.Oid, word string) iter.Seq[Lexeme] {
	res := tsearch.Lexize(dict, word)
	return func(yield func(Lexeme) bool) {
		for _, l := range res {
			if !yield(Lexeme{l.Text, int32(l.Variant), int32(l.Flags)}) {
				return
			}
		}
	}
}

// CharLengths returns the byte lengths of the characters of s in the
// server encoding (tsearch.CharLen).
//
//gopgext:function immutable
func CharLengths(s string) []int32 {
	var out []int32
	for s != "" {
		n := tsearch.CharLen(s)
		out = append(out, int32(n))
		s = s[n:]
	}
	return out
}

// LiveStates is the number of Go dictionaries and parses alive in this
// backend.
//
//gopgext:function volatile
func LiveStates() int32 { return int32(tsearch.Live()) }

func main() {
	tsearch.RegisterParser("tagprs_start", "tagprs_lextype", tagParser{})
	tsearch.RegisterDictionary("compound_init", compoundInit)
}
