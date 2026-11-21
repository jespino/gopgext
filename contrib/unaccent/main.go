// Command unaccent is PostgreSQL's contrib/unaccent in Go: a text search
// dictionary that removes accents (diacritic signs) from lexemes, and
// unaccent() functions that apply it to text. Its rules file
// (tsearch_data/unaccent.rules) maps characters, or sequences of
// characters, to replacements.
package main

import (
	"errors"
	"strings"

	"gopgext/pg"
	"gopgext/tsearch"
)

// rules maps source strings to replacements. Lookups find the longest
// source string a text starts with, as upstream's trie does (the trie has a
// node for every prefix of a source string).
type rules struct {
	repl   map[string]string
	maxLen int // of a source string
}

func isSpace(c byte) bool { return c == ' ' || c >= '\t' && c <= '\r' }

// add puts a rule (placeChar).
func (r *rules) add(src, trg string) {
	if _, dup := r.repl[src]; dup {
		pg.Warning("duplicate source strings, first one will be used")
		return
	}
	r.repl[src] = trg
	r.maxLen = max(r.maxLen, len(src))
}

// parseLine parses a rule: "src" or "src trg", src and trg sequences of
// non-space characters, trg optionally quoted (with "" for a quote, and
// spaces). It returns upstream's final state: > 0 a rule, -1 more than two
// strings, -2 an unfinished quoted string.
func parseLine(line string) (src, trg string, state int) {
	srcStart, srcLen, trgStart, trgLen := 0, 0, 0, 0
	quoted := false
	for p, n := 0, 0; p < len(line); p += n {
		n = tsearch.CharLen(line[p:])
		// Spaces end src or trg, and are ignored outside quotes.
		if isSpace(line[p]) {
			switch state {
			case 1:
				state = 2
			case 3:
				state = 5
			}
			if state != 4 {
				continue
			}
		}
		switch state {
		case 0: // start of src
			srcStart, srcLen, state = p, n, 1
		case 1:
			srcLen += n
		case 2: // start of trg
			if line[p] == '"' {
				quoted, state = true, 4
			} else {
				state = 3
			}
			trgStart, trgLen = p, n
		case 3:
			trgLen += n
		case 4: // quoted trg: a quote ends it, unless doubled
			trgLen += n
			if line[p] == '"' {
				if p+1 < len(line) && line[p+1] == '"' {
					p++
					trgLen++
				} else {
					state = 5
				}
			}
		default:
			state = -1
		}
	}
	src = line[srcStart : srcStart+srcLen]
	t := line[trgStart : trgStart+trgLen]
	switch state {
	case 1, 2: // no trg: ""
		t = ""
	case 4:
		state = -2
	}
	if quoted && state > 0 {
		// Strip the quotes and unescape doubled quotes.
		var b strings.Builder
		for i := 1; i < len(t)-1; i++ {
			b.WriteByte(t[i])
			if t[i] == '"' && t[i+1] == '"' {
				i++
			}
		}
		t = b.String()
	}
	return src, t, state
}

func load(name string) (*rules, error) {
	r := &rules{repl: map[string]string{}}
	path := tsearch.ConfigFile(name, "rules")
	for line, err := range tsearch.ReadConfigFile(path) {
		var oe *tsearch.OpenError
		var pe *pg.Error
		switch {
		case errors.As(err, &oe):
			return nil, pg.Errorf("F0000", "could not open unaccent file \"%s\": %s", oe.Path, oe.Reason) // config_file_error
		case errors.As(err, &pe) && pe.Code == "22P05":
			continue // untranslatable_character: no such character in the server encoding, skip the line
		case err != nil:
			return nil, err
		}
		src, trg, state := parseLine(line)
		switch {
		case state > 0:
			r.add(src, trg)
		case state == -1:
			pg.Warning("invalid syntax: more than two strings in unaccent rule")
		case state == -2:
			pg.Warning("invalid syntax: unfinished quoted string in unaccent rule")
		}
	}
	return r, nil
}

func unaccentInit(opts []tsearch.Option) (tsearch.Dictionary, error) {
	var r *rules
	for _, o := range opts {
		if o.Name != "rules" {
			return nil, pg.Errorf(pg.CodeInvalidParameterValue, "unrecognized Unaccent parameter: \"%s\"", o.Name)
		}
		if r != nil {
			return nil, pg.Errorf(pg.CodeInvalidParameterValue, "multiple Rules parameters")
		}
		name, err := o.Text()
		if err != nil {
			return nil, err
		}
		if r, err = load(name); err != nil {
			return nil, err
		}
	}
	if r == nil {
		return nil, pg.Errorf(pg.CodeInvalidParameterValue, "missing Rules parameter")
	}
	return r, nil
}

// Lexize replaces the longest source strings found, left to right: a
// filtering lexeme if anything was replaced, else nil (the next dictionary
// gets the word unchanged).
func (r *rules) Lexize(word string) ([]tsearch.Lexeme, error) {
	var b []byte // allocated at the first replacement
	for p := 0; p < len(word); {
		n, trg := 0, ""
		for l := min(r.maxLen, len(word)-p); l > 0; l-- {
			if t, ok := r.repl[word[p:p+l]]; ok {
				n, trg = l, t
				break
			}
		}
		if n > 0 {
			if b == nil {
				b = append(make([]byte, 0, len(word)), word[:p]...)
			}
			b = append(b, trg...)
		} else {
			n = tsearch.CharLen(word[p:])
			if b != nil {
				b = append(b, word[p:p+n]...)
			}
		}
		p += n
	}
	if b == nil {
		return nil, nil
	}
	return []tsearch.Lexeme{{Text: string(b), Flags: tsearch.Filter}}, nil
}

func unaccentWith(dict pg.Oid, s string) string {
	if res := tsearch.Lexize(dict, s); len(res) > 0 {
		return res[0].Text
	}
	return s
}

// UnaccentDict is unaccent(regdictionary, text): s through dictionary dict.
//
//gopgext:function stable parallel=safe manual symbol=unaccent_dict
func UnaccentDict(dict pg.Oid, s string) string { return unaccentWith(dict, s) }

// Unaccent is unaccent(text): s through the unaccent dictionary of the
// function's schema.
//
//gopgext:function stable parallel=safe manual symbol=unaccent_dict_default
func Unaccent(s string) (string, error) {
	dict, err := tsearch.FindDictionary("unaccent")
	if err != nil {
		return "", err
	}
	return unaccentWith(dict, s), nil
}

func main() {
	tsearch.RegisterDictionary("unaccent_init", unaccentInit)
}
