// Command dict_xsyn is PostgreSQL's contrib/dict_xsyn in Go: an extended
// synonym dictionary template. A rules file (tsearch_data/*.rules) lists
// groups of synonyms, one per line ("word synonym1 synonym2 ..."); options
// choose which words of a group match and which are returned.
package main

import (
	"errors"
	"sort"

	"gopgext/pg"
	"gopgext/tsearch"
)

// syn is a word to match and its line (the word and its synonyms).
type syn struct {
	key, value string
}

// xsynDict is a dictionary of the template (DictSyn).
type xsynDict struct {
	syns []syn // sorted by key

	matchOrig, keepOrig, matchSynonyms, keepSynonyms bool
}

func isSpace(c byte) bool { return c == ' ' || c >= '\t' && c <= '\r' }

// findWord returns the first word of s and the rest of s after it; ok is
// false at the end of s or at a comment.
func findWord(s string) (word, rest string, ok bool) {
	i := 0
	for i < len(s) && isSpace(s[i]) {
		i++
	}
	if i == len(s) || s[i] == '#' {
		return "", "", false
	}
	j := i
	for j < len(s) && !isSpace(s[j]) {
		j++
	}
	return s[i:j], s[j:], true
}

func (d *xsynDict) read(name string) error {
	path := tsearch.ConfigFile(name, "rules")
	for line, err := range tsearch.ReadConfigFile(path) {
		var oe *tsearch.OpenError
		if errors.As(err, &oe) {
			return pg.Errorf("F0000", "could not open synonym file \"%s\": %s", oe.Path, oe.Reason) // config_file_error
		}
		if err != nil {
			return err
		}
		if line == "" {
			continue
		}
		value := tsearch.Lower(line)
		for pos, first := value, true; ; first = false {
			key, rest, ok := findWord(pos)
			if !ok {
				break
			}
			// Save the first word only if it's to be matched.
			if !first || d.matchOrig {
				d.syns = append(d.syns, syn{key, value})
			}
			pos = rest
			if !d.matchSynonyms {
				break
			}
		}
	}
	sort.SliceStable(d.syns, func(i, j int) bool { return d.syns[i].key < d.syns[j].key })
	return nil
}

func xsynInit(opts []tsearch.Option) (tsearch.Dictionary, error) {
	d := &xsynDict{matchOrig: true, keepOrig: true, keepSynonyms: true}
	rules, hasRules := "", false
	for _, o := range opts {
		var err error
		switch o.Name {
		case "matchorig":
			d.matchOrig, err = o.Bool()
		case "keeporig":
			d.keepOrig, err = o.Bool()
		case "matchsynonyms":
			d.matchSynonyms, err = o.Bool()
		case "keepsynonyms":
			d.keepSynonyms, err = o.Bool()
		case "rules":
			// The rules can only be read once all options are known.
			rules, err = o.Text()
			hasRules = true
		default:
			return nil, pg.Errorf(pg.CodeInvalidParameterValue, "unrecognized xsyn parameter: \"%s\"", o.Name)
		}
		if err != nil {
			return nil, err
		}
	}
	if hasRules {
		if err := d.read(rules); err != nil {
			return nil, err
		}
	}
	return d, nil
}

// Lexize returns the synonyms of the line word matches.
func (d *xsynDict) Lexize(word string) ([]tsearch.Lexeme, error) {
	if word == "" || len(d.syns) == 0 {
		return nil, nil
	}
	key := tsearch.Lower(word)
	i := sort.Search(len(d.syns), func(i int) bool { return d.syns[i].key >= key })
	if i == len(d.syns) || d.syns[i].key != key {
		return nil, nil
	}
	res := []tsearch.Lexeme{}
	for pos, first := d.syns[i].value, true; ; first = false {
		w, rest, ok := findWord(pos)
		if !ok {
			break
		}
		// The first word is returned only with keeporig.
		if !first || d.keepOrig {
			res = append(res, tsearch.Lexeme{Text: w})
		}
		pos = rest
		if !d.keepSynonyms {
			break
		}
	}
	return res, nil
}

func main() {
	tsearch.RegisterDictionary("dxsyn_init", xsynInit)
}
