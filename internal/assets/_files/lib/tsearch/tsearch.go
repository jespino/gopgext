// Package tsearch writes full text search dictionary templates and parsers
// in Go, and gives Go code PostgreSQL's text search services: data files in
// $SHAREDIR/tsearch_data, lower-casing, and calling dictionaries.
//
// A dictionary template is registered under the name of its init function
// (in main or a package initializer):
//
//	tsearch.RegisterDictionary("dintdict_init", func(opts []tsearch.Option) (tsearch.Dictionary, error) {
//		d := &intDict{maxLen: 6}
//		for _, o := range opts { ... } // CREATE/ALTER TEXT SEARCH DICTIONARY options
//		return d, nil
//	})
//
//	func (d *intDict) Lexize(word string) ([]tsearch.Lexeme, error) { ... }
//
// and the project's sql/ files declare the functions with this package's C
// symbols, and the template:
//
//	CREATE FUNCTION dintdict_init(internal) RETURNS internal
//		AS 'MODULE_PATHNAME', 'gopgext_tsearch_init' LANGUAGE C STRICT;
//	CREATE FUNCTION dintdict_lexize(internal, internal, internal, internal) RETURNS internal
//		AS 'MODULE_PATHNAME', 'gopgext_tsearch_lexize' LANGUAGE C STRICT;
//	CREATE TEXT SEARCH TEMPLATE intdict_template (LEXIZE = dintdict_lexize, INIT = dintdict_init);
//
// The Dictionary init returns is the dictionary's state: PostgreSQL caches
// it for the backend's life, and tsearch drops it when PostgreSQL does
// (ALTER TEXT SEARCH DICTIONARY, the end of the statement that checked the
// options of CREATE TEXT SEARCH DICTIONARY...).
//
// Parsers are registered under the names of their start and lextype
// functions (see RegisterParser).
package tsearch

import (
	"fmt"
	"iter"
	"strings"
	"unsafe"

	"gopgext/fmgr"
	"gopgext/internal/core"
	"gopgext/pg"
)

// Flags of a Lexeme (TSL_* in tsearch/ts_public.h).
const (
	AddPos = 0x01 // TSL_ADDPOS: the lexeme takes a position of its own
	Prefix = 0x02 // TSL_PREFIX: prefix match (in tsqueries: lexeme:*)
	Filter = 0x04 // TSL_FILTER: a filtering dictionary: the next one gets this lexeme
)

// Lexeme is a normalized form of a word. Lexemes with the same Variant are
// one alternative (e.g. the parts of a compound word); different variants
// are alternatives.
type Lexeme struct {
	Text    string
	Variant uint16 // nvariant
	Flags   uint16 // AddPos, Prefix, Filter
}

// Dictionary is a text search dictionary's state.
type Dictionary interface {
	// Lexize returns the lexemes of word: nil if the dictionary doesn't
	// know it (the next dictionary of the configuration gets it), an empty
	// non-nil slice (StopWord) for a word to drop.
	Lexize(word string) ([]Lexeme, error)
}

// StopWord is the result of Lexize for a stop word.
var StopWord = []Lexeme{}

// Option is an option of CREATE/ALTER TEXT SEARCH DICTIONARY (a DefElem).
type Option struct {
	Name     string // lower-cased, unless quoted
	Value    string // the value as text (defGetString), "" without one
	HasValue bool   // false for an option without "= value"

	isInt bool
	ival  int64
}

// Text returns the value, like defGetString: an error if there is none.
func (o Option) Text() (string, error) {
	if !o.HasValue {
		return "", pg.Errorf(pg.CodeSyntaxError, "%s requires a parameter", o.Name)
	}
	return o.Value, nil
}

// Bool returns the value as a boolean, like defGetBoolean: none, true, on,
// 1 are true; false, off, 0 are false.
func (o Option) Bool() (bool, error) {
	switch {
	case !o.HasValue:
		return true, nil
	case o.isInt:
		if o.ival == 0 || o.ival == 1 {
			return o.ival == 1, nil
		}
	case strings.EqualFold(o.Value, "true"), strings.EqualFold(o.Value, "on"):
		return true, nil
	case strings.EqualFold(o.Value, "false"), strings.EqualFold(o.Value, "off"):
		return false, nil
	}
	return false, pg.Errorf(pg.CodeSyntaxError, "%s requires a Boolean value", o.Name)
}

// TokenType is a token type of a parser (ts_token_type()).
type TokenType struct {
	ID          int // 1 to 255
	Alias       string
	Description string
}

// Parser is a text search parser.
type Parser interface {
	// TokenTypes returns the token types Next returns.
	TokenTypes() []TokenType
	// Parse starts parsing text.
	Parse(text string) Tokenizer
}

// Tokenizer returns the tokens of a text, in order.
type Tokenizer interface {
	// Next returns the next token and its type; type 0 at the end.
	Next() (typ int, token string)
}

var (
	dicts   = map[string]func([]Option) (Dictionary, error){}
	parsers = map[string]Parser{}
)

// RegisterDictionary registers the dictionary template whose init function
// (symbol gopgext_tsearch_init) is the SQL function name; its lexize
// function has symbol gopgext_tsearch_lexize.
func RegisterDictionary(name string, init func(options []Option) (Dictionary, error)) {
	dicts[name] = init
}

// RegisterParser registers parser p under the names of its SQL functions
// start (internal, int4) RETURNS internal, symbol gopgext_tsearch_start;
// and lextype (internal) RETURNS internal, symbol gopgext_tsearch_lextype.
// Its gettoken function (internal, internal, internal) RETURNS internal has
// symbol gopgext_tsearch_gettoken, its end function (internal) RETURNS void
// gopgext_tsearch_end. The headline function can be prsd_headline.
func RegisterParser(start, lextype string, p Parser) {
	parsers[start] = p
	parsers[lextype] = p
}

// ---- Go states ------------------------------------------------------------

var (
	states = map[uint64]any{} // Dictionary or Tokenizer, by handle id
	lastID uint64
)

//export gopgext_tsearch_handle
func newHandle(id uint64, out *uint64) uintptr

//export gopgext_tsearch_freed
func handlesFreed(buf *uint64, max int64) int64

// drain drops the states whose memory context PostgreSQL freed.
func drain() {
	var buf [64]uint64
	for {
		n := int(handlesFreed(&buf[0], int64(len(buf))))
		for _, id := range buf[:n] {
			delete(states, id)
		}
		if n < len(buf) {
			return
		}
	}
}

// register returns a handle for v, in the current memory context.
func register(v any) uint64 {
	lastID++
	var h uint64
	core.Check(newHandle(lastID, &h))
	states[lastID] = v
	return h
}

func state[T any](id uint64, what string) T {
	v, ok := states[id].(T)
	if !ok {
		panic(&core.Error{Code: "XX000", Message: what + " was already freed"})
	}
	return v
}

// Live returns the number of dictionaries and parses alive in this backend
// (to check that none leak).
func Live() int {
	drain()
	return len(states)
}

// ---- Entry points (frameworks/tsearch/tsearch.c) --------------------------

// coption is struct ts_option.
type coption struct {
	name, value *byte
	isInt       int64
	ival        int64
}

// cinput is struct ts_input.
type cinput struct {
	name     *byte
	id       uint64
	text     *byte
	len      int64
	nopts    int64
	opts     *coption
	token    *byte
	tokenLen int64
}

// clexeme is struct ts_lexeme.
type clexeme struct {
	text     *byte
	len      int64
	nvariant int64
	flags    int64
}

// ctokentype is struct ts_tokentype.
type ctokentype struct {
	id       int64
	alias    *byte
	aliasLen int64
	descr    *byte
	descrLen int64
}

//export gopgext_tsearch_lexemes
func cLexemes(n int64, lexemes *clexeme, out *uint64) uintptr

//export gopgext_tsearch_tokentypes
func cTokenTypes(n int64, types *ctokentype, out *uint64) uintptr

func input(c *fmgr.Call) *cinput {
	d, _ := c.Datum(0)
	return (*cinput)(unsafe.Pointer(uintptr(d)))
}

func text(p *byte, n int64) string {
	if n <= 0 {
		return ""
	}
	return string(unsafe.Slice(p, n))
}

//export gopgext_go_tsearch_init
func dictInit(c *fmgr.Call) {
	defer c.End(c.Begin(false))
	drain()
	in := input(c)
	name := core.GoString(in.name)
	init := dicts[name]
	if init == nil {
		panic(&core.Error{Code: "55000", Message: fmt.Sprintf("no text search dictionary template %q", name),
			Hint: fmt.Sprintf("Call tsearch.RegisterDictionary(%q, ...) in main.", name)})
	}
	var opts []Option
	if in.nopts > 0 {
		for _, o := range unsafe.Slice(in.opts, in.nopts) {
			opts = append(opts, Option{Name: core.GoString(o.name), Value: core.GoString(o.value),
				HasValue: o.value != nil, isInt: o.isInt != 0, ival: o.ival})
		}
	}
	d, err := init(opts)
	if err != nil {
		c.Fail(err)
		return
	}
	if d == nil {
		panic(&core.Error{Code: "XX000", Message: fmt.Sprintf("text search dictionary template %q returned no dictionary", name)})
	}
	c.SetDatum(register(d))
}

//export gopgext_go_tsearch_lexize
func dictLexize(c *fmgr.Call) {
	defer c.End(c.Begin(true))
	drain()
	in := input(c)
	d := state[Dictionary](in.id, "text search dictionary")
	res, err := d.Lexize(text(in.text, in.len))
	if err != nil {
		c.Fail(err)
		return
	}
	if res == nil {
		c.SetDatum(0)
		return
	}
	cl := make([]clexeme, len(res)+1)
	for i, l := range res {
		cl[i] = clexeme{core.StringData(l.Text), int64(len(l.Text)), int64(l.Variant), int64(l.Flags)}
	}
	var out uint64
	core.Check(cLexemes(int64(len(res)), &cl[0], &out))
	c.SetDatum(out)
}

func parser(name string) Parser {
	p := parsers[name]
	if p == nil {
		panic(&core.Error{Code: "55000", Message: fmt.Sprintf("no text search parser %q", name),
			Hint: fmt.Sprintf("Call tsearch.RegisterParser with %q in main.", name)})
	}
	return p
}

//export gopgext_go_tsearch_start
func parseStart(c *fmgr.Call) {
	defer c.End(c.Begin(true))
	drain()
	in := input(c)
	t := parser(core.GoString(in.name)).Parse(text(in.text, in.len))
	c.SetDatum(register(t))
}

//export gopgext_strdup
func strdup(p *byte, n int64) *byte

//export gopgext_go_tsearch_token
func parseToken(c *fmgr.Call) {
	defer c.End(c.Begin(true))
	in := input(c)
	typ, tok := state[Tokenizer](in.id, "text search parse").Next()
	if typ != 0 {
		in.token = strdup(core.StringData(tok), int64(len(tok)))
		in.tokenLen = int64(len(tok))
	}
	c.SetDatum(fmgr.ToDatum(int32(typ)))
}

//export gopgext_go_tsearch_end
func parseEnd(c *fmgr.Call) {
	defer c.End(c.Begin(true))
	delete(states, input(c).id)
	drain()
}

//export gopgext_go_tsearch_lextype
func lexTypes(c *fmgr.Call) {
	defer c.End(c.Begin(true))
	types := parser(core.GoString(input(c).name)).TokenTypes()
	ct := make([]ctokentype, len(types)+1)
	for i, t := range types {
		if t.ID < 1 || t.ID > 255 {
			panic(&core.Error{Code: "XX000", Message: fmt.Sprintf("token type %d out of range (1 to 255)", t.ID)})
		}
		ct[i] = ctokentype{int64(t.ID), core.StringData(t.Alias), int64(len(t.Alias)),
			core.StringData(t.Description), int64(len(t.Description))}
	}
	var out uint64
	core.Check(cTokenTypes(int64(len(types)), &ct[0], &out))
	c.SetDatum(out)
}

// ---- Services --------------------------------------------------------------

// tslexeme is TSLexeme.
type tslexeme struct {
	nvariant uint16
	flags    uint16
	_        uint32
	lexeme   *byte
}

//export gopgext_tsearch_call_lexize
func callLexize(dict uint32, word *byte, n int64, out **tslexeme, count *int64) uintptr

// Lexize runs text search dictionary dict on word, like ts_lexize(): nil
// if the dictionary doesn't know it, an empty slice for a stop word.
// Errors (an unknown dictionary...) are raised.
func Lexize(dict pg.Oid, word string) []Lexeme {
	var res *tslexeme
	var n int64
	core.Check(callLexize(uint32(dict), core.StringData(word), int64(len(word)), &res, &n))
	if res == nil {
		return nil
	}
	out := make([]Lexeme, 0, n)
	for _, l := range unsafe.Slice(res, n) {
		out = append(out, Lexeme{Text: core.GoString(l.lexeme), Variant: l.nvariant, Flags: l.flags})
	}
	return out
}

//export gopgext_tsearch_dictionary
func findDictionary(fcinfo uintptr, name *byte, out *uint32, schema **byte) uintptr

// FindDictionary returns the text search dictionary name in the schema of
// the SQL function being called, whatever the search_path (as
// unaccent(text) finds the unaccent dictionary of its extension).
func FindDictionary(name string) (pg.Oid, error) {
	var oid uint32
	var schema *byte
	core.Check(findDictionary(core.FCInfo, core.CBytes(name), &oid, &schema))
	if oid == 0 {
		return 0, &pg.Error{Code: "42704", // undefined_object
			Message: fmt.Sprintf("text search dictionary \"%s.%s\" does not exist", core.GoString(schema), name)}
	}
	return pg.Oid(oid), nil
}

//export gopgext_tsearch_config_path
func configPath(name, ext *byte, out **byte) uintptr

// ConfigFile returns the path of text search data file name.ext in
// $SHAREDIR/tsearch_data (get_tsearch_config_filename: name can't
// contain "/" or other unsafe characters). A name with a "." can't end
// in ext.
func ConfigFile(name, ext string) string {
	var p *byte
	core.Check(configPath(core.CBytes(name), core.CBytes(ext), &p))
	return core.GoString(p)
}

// OpenError is the error of a data file that can't be opened: modules
// report it with their own message, e.g. "could not open synonym file".
type OpenError struct {
	Path   string
	Reason string // strerror
}

func (e *OpenError) Error() string {
	return fmt.Sprintf("could not open text search data file \"%s\": %s", e.Path, e.Reason)
}

type readlineState struct{}

//export gopgext_tsearch_open
func openFile(path *byte, out **readlineState) int32

//export gopgext_tsearch_readline
func readLine(st *readlineState, line **byte, eof *bool) uintptr

//export gopgext_tsearch_close
func closeFile(st *readlineState)

//export gopgext_strerror
func strerror(err int32) *byte

// ReadConfigFile reads a text search data file (a path from ConfigFile)
// line by line, like tsearch_readline: lines are converted from UTF-8 to
// the server encoding, and keep their line terminator. If the file can't be
// opened, it yields an *OpenError and stops. A line that can't be
// converted yields its error (a *pg.Error, e.g. code 22P05
// untranslatable_character), and reading goes on with the next line if the
// loop continues.
//
// While the loop runs, messages PostgreSQL reports (pg.Warning, errors of
// C helpers) get the context "line N of configuration file ...", as in C
// modules; errors Go code returns are raised after the file is closed, so
// they don't.
func ReadConfigFile(path string) iter.Seq2[string, error] {
	return func(yield func(string, error) bool) {
		var st *readlineState
		if errno := openFile(core.CBytes(path), &st); errno != 0 {
			yield("", &OpenError{Path: path, Reason: core.GoString(strerror(errno))})
			return
		}
		defer closeFile(st)
		for {
			var line *byte
			var eof bool
			if ed := readLine(st, &line, &eof); ed != 0 {
				if !yield("", core.FromErrorData(ed)) {
					return
				}
				continue
			}
			if eof || !yield(core.GoString(line), nil) {
				return
			}
		}
	}
}

//export gopgext_tsearch_lower
func lower(s *byte, n int64, out **byte) uintptr

// Lower lower-cases s with the database's default collation, like text
// search does (str_tolower).
func Lower(s string) string {
	var out *byte
	core.Check(lower(core.StringData(s), int64(len(s)), &out))
	return core.GoString(out)
}

//export gopgext_tsearch_mblen
func mblen(s *byte, n int64) int64

//export gopgext_tsearch_encoding
func encoding() int64

const pgUTF8 = 6 // PG_UTF8

var serverEncoding int64 = -1

// CharLen returns the length in bytes of the first character of s in the
// server encoding (pg_mblen), at most len(s); 0 for "".
func CharLen(s string) int {
	if s == "" {
		return 0
	}
	if serverEncoding < 0 {
		serverEncoding = encoding()
	}
	if serverEncoding == pgUTF8 {
		// pg_utf_mblen: from the first byte only.
		n := 1
		switch c := s[0]; {
		case c < 0x80:
			return 1
		case c&0xe0 == 0xc0:
			n = 2
		case c&0xf0 == 0xe0:
			n = 3
		case c&0xf8 == 0xf0:
			n = 4
		}
		return min(n, len(s))
	}
	return int(mblen(unsafe.StringData(s), int64(len(s))))
}
