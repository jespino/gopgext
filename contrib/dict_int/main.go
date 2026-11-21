// Command dict_int is PostgreSQL's contrib/dict_int in Go: a text search
// dictionary template for integers, which keeps long numbers from bloating
// a text search index (an integer is cut to maxlen digits, or dropped).
package main

import (
	"math"

	"gopgext/pg"
	"gopgext/tsearch"
)

// intDict is a dictionary of the template (DictInt).
type intDict struct {
	maxLen     int
	rejectLong bool
	absVal     bool
}

func intDictInit(opts []tsearch.Option) (tsearch.Dictionary, error) {
	d := &intDict{maxLen: 6}
	for _, o := range opts {
		var err error
		switch o.Name {
		case "maxlen":
			var s string
			if s, err = o.Text(); err == nil {
				d.maxLen = atoi(s)
				if d.maxLen < 1 {
					return nil, pg.Errorf(pg.CodeInvalidParameterValue, "maxlen value has to be >= 1")
				}
			}
		case "rejectlong":
			d.rejectLong, err = o.Bool()
		case "absval":
			d.absVal, err = o.Bool()
		default:
			return nil, pg.Errorf(pg.CodeInvalidParameterValue, "unrecognized intdict parameter: \"%s\"", o.Name)
		}
		if err != nil {
			return nil, err
		}
	}
	return d, nil
}

// Lexize returns the integer, without its sign with absval, cut to maxlen
// digits (or nothing: a stop word, with rejectlong).
func (d *intDict) Lexize(word string) ([]tsearch.Lexeme, error) {
	if word == "" {
		return nil, nil
	}
	if d.absVal && (word[0] == '+' || word[0] == '-') {
		word = word[1:]
	}
	if len(word) > d.maxLen {
		if d.rejectLong {
			return tsearch.StopWord, nil
		}
		word = word[:d.maxLen]
	}
	return []tsearch.Lexeme{{Text: word}}, nil
}

// atoi is C's atoi (strtol, truncated to int): leading spaces, a sign and
// digits; anything after them is ignored.
func atoi(s string) int {
	i := 0
	for i < len(s) && (s[i] == ' ' || s[i] >= '\t' && s[i] <= '\r') {
		i++
	}
	neg := false
	if i < len(s) && (s[i] == '+' || s[i] == '-') {
		neg = s[i] == '-'
		i++
	}
	var v uint64 // saturates
	for ; i < len(s) && s[i] >= '0' && s[i] <= '9'; i++ {
		if v <= (math.MaxUint64-9)/10 {
			v = v*10 + uint64(s[i]-'0')
		} else {
			v = math.MaxUint64
		}
	}
	var l int64 // strtol clamps to the range of long
	switch {
	case neg && v >= 1<<63:
		l = math.MinInt64
	case neg:
		l = -int64(v)
	case v >= 1<<63:
		l = math.MaxInt64
	default:
		l = int64(v)
	}
	return int(int32(l))
}

func main() {
	tsearch.RegisterDictionary("dintdict_init", intDictInit)
}
