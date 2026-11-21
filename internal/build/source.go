package build

// SQL functions are declared in Go:
//
//	// Add returns the sum of a and b.
//	//
//	//gopgext:function immutable parallel=safe
//	func Add(a, b int32) int32 { return a + b }
//
// becomes the SQL function add(a integer, b integer) RETURNS integer, with
// the doc comment as its COMMENT. See the README for the type mapping.

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

// Pragma marks a Go function as a SQL function.
const Pragma = "//gopgext:function"

// Function is a //gopgext:function.
type Function struct {
	GoName          string
	SQLName         string
	Params          []Param
	Result          *Type // nil: RETURNS void
	HasError        bool  // last result is error
	Volatility      string
	Strict          bool
	Parallel        string
	Cost            string
	Rows            string
	Leakproof       bool
	SecurityDefiner bool
	Symbol          string   // C symbol PostgreSQL calls (default gopgext_fn_<GoName>)
	Manual          bool     // declared by the project's sql/ files, not generated
	IOType          *SQLType // a function of this //gopgext:type (created before it)
	Trigger         string   // "trigger" or "event_trigger": takes a *trigger.Data or *trigger.Event (no SQL parameters)
	TriggerRow      bool     // a trigger function returning a *trigger.Row
	Doc             string
	Pos             token.Position
}

// Param is a parameter of a Function.
type Param struct {
	GoName  string
	SQLName string
	Type    Type
}

// TypeKind is how a Go type maps to SQL.
type TypeKind int

const (
	Scalar        TypeKind = iota // T: NULL raises an error
	Nullable                      // *T: nil is NULL
	Array                         // []T: T[] (NULL raises an error)
	NullableArray                 // *[]T: T[], nil is NULL
	NullableElems                 // []*T: T[] whose elements can be NULL (nil)
	SetOf                         // iter.Seq[T] (results only): SETOF T
	NullableSetOf                 // iter.Seq[*T] (results only): SETOF T with NULL rows (nil)
)

// Type is the type of a parameter or result.
type Type struct {
	Kind TypeKind
	Go   string // the scalar Go type: int32, string, []byte, ... (of a custom type: its representation)

	Custom *SQLType // a //gopgext:type (Go is its representation)
	Struct *Struct  // a struct result: a row (Scalar, Nullable) or a table (SetOf)
}

// Package is what a project's main package declares for SQL.
type Package struct {
	Funcs   []*Function
	Types   []*SQLType // //gopgext:type, in source order
	Structs []*Struct  // structs used as results, in source order
}

// SQLType is a data type declared with //gopgext:type on a Go type whose
// underlying type is one of the scalars (its representation).
type SQLType struct {
	GoName    string
	SQLName   string
	Go        string            // representation: int64, _gopgext_fmgr.Varlena, []byte, ...
	Functions map[string]string // input, output, receive, ...: Go function names
	Storage   string
	Category  string
	Preferred bool
	Alignment string
	Doc       string
	Pos       token.Position
	Composite bool // a struct: CREATE TYPE ... AS (columns)

	funcs map[string]*Function // resolved Functions
}

// Struct is a Go struct returned by SQL functions as a row, or a composite
// type (//gopgext:type on a struct), which can also be an argument.
type Struct struct {
	GoName    string
	Fields    []Field
	Pos       token.Position
	Composite *SQLType // nil: an anonymous row (OUT parameters or TABLE columns)
}

// Field is a column of a Struct.
type Field struct {
	GoName  string
	SQLName string
	Type    Type
}

// scalars maps the supported Go types to SQL types (the gopgext types as
// spelled in generated code: pg.X becomes _gopgext_fmgr.X).
var scalars = map[string]string{
	"bool":    "boolean",
	"int16":   "smallint",
	"int32":   "integer",
	"int64":   "bigint",
	"int":     "bigint",
	"float32": "real",
	"float64": "double precision",
	"string":  "text",
	"[]byte":  "bytea",

	cstringType:            "cstring",
	fmgrType("Oid"):        "oid",
	fmgrType("Internal"):   "internal",
	fmgrType("Varlena"):    "bytea",
	fmgrType("StringInfo"): "internal",
	fmgrType("AnyElement"): "anyelement",
	fmgrType("AnyArray"):   "anyarray",
	fmgrType("Numeric"):    "numeric",
	fmgrType("Time"):       "timestamp with time zone",
	fmgrType("UUID"):       "uuid",
	fmgrType("Record"):     "record",
	fmgrType("Point"):      "point",
	aggState:               "internal",
}

// aggState is gopgext/agg's State in generated code (only as *agg.State).
const aggState = "_gopgext_agg.State"

// The gopgext/trigger types in generated code: a trigger function's only
// parameter (*trigger.Data, *trigger.Event) and its result (*trigger.Row).
const (
	triggerData  = "_gopgext_trigger.Data"
	triggerEvent = "_gopgext_trigger.Event"
	triggerRow   = "_gopgext_trigger.Row"
)

// pgTypes are the gopgext/pg types SQL functions can use.
var pgTypes = []string{"CString", "Oid", "Internal", "Varlena", "StringInfo", "AnyElement", "AnyArray", "Numeric", "UUID", "Record", "Point"}

// recordType is pg.Record in generated code: results of type record whose
// columns the caller defines.
var recordType = fmgrType("Record")

func fmgrType(name string) string { return "_gopgext_fmgr." + name }

// notInArrays are the scalars that can't be array elements or set members.
var notInArrays = map[string]bool{fmgrType("Internal"): true, fmgrType("StringInfo"): true,
	fmgrType("AnyElement"): true, fmgrType("AnyArray"): true, aggState: true, recordType: true}

// polymorphic are the polymorphic types (read with the argument's actual
// type).
var polymorphic = map[string]string{fmgrType("AnyElement"): "Any", fmgrType("AnyArray"): "AnyArray"}

// SQL returns the SQL type.
func (t Type) SQL() string {
	s := scalars[t.Go]
	if t.Custom != nil {
		s = quoteIdent(t.Custom.SQLName)
	}
	switch t.Kind {
	case Array, NullableArray, NullableElems:
		return s + "[]"
	case SetOf, NullableSetOf:
		return "SETOF " + s
	}
	return s
}

// prepareSource copies the project's module into dst and returns what its
// main package declares for SQL.
func prepareSource(proj, dst string) (*Package, error) {
	if err := os.RemoveAll(dst); err != nil {
		return nil, err
	}
	files := map[string][]byte{}
	err := filepath.WalkDir(proj, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(proj, p)
		if d.IsDir() {
			if rel != "." && (strings.HasPrefix(d.Name(), ".") || strings.HasPrefix(d.Name(), "_") ||
				d.Name() == CSrcDir || d.Name() == SQLDir || d.Name() == TSearchDataDir) {
				return filepath.SkipDir
			}
			if rel != "." {
				if _, err := os.Stat(filepath.Join(p, "go.mod")); err == nil {
					return filepath.SkipDir // a separate module
				}
			}
			return os.MkdirAll(filepath.Join(dst, rel), 0o755)
		}
		if !d.Type().IsRegular() || !(strings.HasSuffix(p, ".go") || rel == "go.mod" || rel == "go.sum") {
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if filepath.Dir(p) == filepath.Clean(proj) && strings.HasSuffix(p, ".go") && !strings.HasSuffix(p, "_test.go") {
			files[p] = data
		}
		return os.WriteFile(filepath.Join(dst, rel), data, 0o644)
	})
	if err != nil {
		return nil, err
	}
	return scanPackage(files)
}

// scanFile scans a package made of a single file.
func scanFile(path string, src []byte) (*Package, error) {
	return scanPackage(map[string][]byte{path: src})
}

// scanPackage scans the files of the main package.
func scanPackage(files map[string][]byte) (*Package, error) {
	var paths []string
	for p := range files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	fset := token.NewFileSet()
	pkg := &Package{}
	sc := &scope{fset: fset, types: map[string]*SQLType{}, structDecls: map[string]*ast.StructType{},
		structPos: map[string]token.Pos{}, structs: map[string]*Struct{}}
	var parsed []*ast.File
	for _, p := range paths {
		src := files[p]
		if !strings.Contains(string(src), "//gopgext:") {
			// Declarations of plain files can still be used as results.
			f, err := parser.ParseFile(fset, p, src, parser.SkipObjectResolution)
			if err != nil {
				return nil, err
			}
			sc.collectTypes(f, nil)
			continue
		}
		f, err := parser.ParseFile(fset, p, src, parser.ParseComments|parser.SkipObjectResolution)
		if err != nil {
			return nil, err
		}
		parsed = append(parsed, f)
	}
	// Types first: functions can use types declared in any file.
	for _, f := range parsed {
		if err := sc.collectTypes(f, pkg); err != nil {
			return nil, err
		}
	}
	for _, f := range parsed {
		fs, err := sc.scanFuncs(f)
		if err != nil {
			return nil, err
		}
		pkg.Funcs = append(pkg.Funcs, fs...)
	}
	funcs := pkg.Funcs
	sort.SliceStable(funcs, func(i, j int) bool {
		a, b := funcs[i].Pos, funcs[j].Pos
		if a.Filename != b.Filename {
			return a.Filename < b.Filename
		}
		return a.Line < b.Line
	})
	seen := map[string]*Function{}
	for _, f := range funcs {
		if f.Manual {
			continue
		}
		var types []string
		for _, p := range f.Params {
			types = append(types, p.Type.SQL())
		}
		key := f.SQLName + "(" + strings.Join(types, ",") + ")"
		if prev := seen[key]; prev != nil {
			return nil, fmt.Errorf("%s: SQL function %s already declared by %s at %s", f.Pos, key, prev.GoName, prev.Pos)
		}
		seen[key] = f
	}
	symbols := map[string]*Function{}
	for _, f := range funcs {
		if prev := symbols[f.Symbol]; prev != nil {
			return nil, fmt.Errorf("%s: C symbol %s already used by %s at %s", f.Pos, f.Symbol, prev.GoName, prev.Pos)
		}
		symbols[f.Symbol] = f
	}
	if err := sc.resolveTypes(pkg); err != nil {
		return nil, err
	}
	pkg.Structs = sc.used
	return pkg, nil
}

var (
	identRe   = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	sqlNameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_$]*$`)
	numberRe  = regexp.MustCompile(`^[0-9]+(\.[0-9]+)?$`)
)

// fileImports returns the local names of the packages types can come from.
func fileImports(f *ast.File) imports {
	var names imports
	for _, imp := range f.Imports {
		p, _ := strconv.Unquote(imp.Path.Value)
		name := p[strings.LastIndex(p, "/")+1:]
		if imp.Name != nil {
			name = imp.Name.Name
		}
		switch p {
		case "iter":
			names.iter = name
		case "gopgext/pg":
			names.pg = name
		case "gopgext/agg":
			names.agg = name
		case "gopgext/trigger":
			names.trigger = name
		case "time":
			names.time = name
		}
	}
	return names
}

func (sc *scope) scanFuncs(f *ast.File) ([]*Function, error) {
	fset := sc.fset
	names := fileImports(f)
	names.scope = sc
	var funcs []*Function
	for _, decl := range f.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok || fd.Doc == nil {
			continue
		}
		var pragma *ast.Comment
		var doc []string
		for _, c := range fd.Doc.List {
			if c.Text == Pragma || strings.HasPrefix(c.Text, Pragma+" ") {
				pragma = c
			}
		}
		if pragma == nil {
			continue
		}
		for _, line := range strings.Split(fd.Doc.Text(), "\n") {
			doc = append(doc, line)
		}
		pos := fset.Position(pragma.Pos())
		fn, err := checkFunc(fd, f.Name.Name, names, pos)
		if err != nil {
			return nil, err
		}
		if err := fn.parseOptions(strings.Fields(strings.TrimPrefix(pragma.Text, Pragma))); err != nil {
			return nil, fmt.Errorf("%s: %s: %v", pos, Pragma, err)
		}
		fn.Doc = strings.TrimSpace(strings.Join(doc, "\n"))
		funcs = append(funcs, fn)
	}
	return funcs, nil
}

// imports are the local names of the packages types can come from, and
// the types the package declares.
type imports struct {
	iter    string // "iter"
	pg      string // "gopgext/pg"
	agg     string // "gopgext/agg"
	trigger string // "gopgext/trigger"
	time    string // "time"
	scope   *scope
}

func checkFunc(fd *ast.FuncDecl, pkg string, names imports, pos token.Position) (*Function, error) {
	errf := func(format string, args ...any) error {
		return fmt.Errorf("%s: %s %s: %s", pos, Pragma, fd.Name.Name, fmt.Sprintf(format, args...))
	}
	if pkg != "main" {
		return nil, errf("SQL functions must be in package main")
	}
	if names.scope == nil {
		names.scope = &scope{types: map[string]*SQLType{}, structs: map[string]*Struct{}}
	}
	if fd.Recv != nil || fd.Type.TypeParams != nil {
		return nil, errf("must be a plain function (no receiver or type parameters)")
	}
	if !identRe.MatchString(fd.Name.Name) {
		return nil, errf("function names must be ASCII")
	}
	fn := &Function{GoName: fd.Name.Name, SQLName: snakeCase(fd.Name.Name), Volatility: "volatile", Pos: pos,
		Symbol: "gopgext_fn_" + fd.Name.Name}
	if isTrigger, err := fn.checkTrigger(fd, names); isTrigger || err != nil {
		if err != nil {
			return nil, errf("%v", err)
		}
		return fn, nil
	}
	nullable := false
	for _, field := range fd.Type.Params.List {
		if _, ok := field.Type.(*ast.Ellipsis); ok {
			return nil, errf("variadic parameters are not supported")
		}
		t, err := paramType(field.Type, names)
		if err != nil {
			return nil, errf("%v", err)
		}
		if t.Go == fmgrType("StringInfo") && t.Kind != Scalar {
			return nil, errf("pg.StringInfo parameters can't be pointers")
		}
		if t.Go == recordType {
			return nil, errf("pg.Record can only be a result")
		}
		nullable = nullable || t.Kind == Nullable || t.Kind == NullableArray
		names := field.Names
		if len(names) == 0 {
			names = []*ast.Ident{{Name: "_"}}
		}
		for _, n := range names {
			p := Param{GoName: n.Name, SQLName: snakeCase(n.Name), Type: t}
			if n.Name == "_" {
				p.SQLName = fmt.Sprintf("arg%d", len(fn.Params)+1)
			}
			fn.Params = append(fn.Params, p)
		}
	}
	// Without nullable parameters a NULL argument can only mean NULL out.
	fn.Strict = !nullable

	var results []ast.Expr
	if fd.Type.Results != nil {
		for _, field := range fd.Type.Results.List {
			n := len(field.Names)
			if n == 0 {
				n = 1
			}
			for i := 0; i < n; i++ {
				results = append(results, field.Type)
			}
		}
	}
	if len(results) > 0 {
		if id, ok := results[len(results)-1].(*ast.Ident); ok && id.Name == "error" {
			fn.HasError = true
			results = results[:len(results)-1]
		}
	}
	switch len(results) {
	case 0:
	case 1:
		t, err := resultType(results[0], names)
		if err != nil {
			return nil, errf("%v", err)
		}
		if t.Go == fmgrType("StringInfo") {
			return nil, errf("pg.StringInfo can't be a result")
		}
		fn.Result = &t
	default:
		return nil, errf("returns at most one value (and an error)")
	}
	return fn, nil
}

func typeString(e ast.Expr) string { return types.ExprString(e) }

// checkTrigger recognizes a trigger function: its only parameter is a
// *trigger.Data (results: an optional *trigger.Row and an optional error)
// or a *trigger.Event (event triggers: an optional error).
func (fn *Function) checkTrigger(fd *ast.FuncDecl, names imports) (bool, error) {
	var params, results []string
	for _, l := range []struct {
		fields *ast.FieldList
		out    *[]string
	}{{fd.Type.Params, &params}, {fd.Type.Results, &results}} {
		if l.fields == nil {
			continue
		}
		for _, f := range l.fields.List {
			n := max(len(f.Names), 1)
			for i := 0; i < n; i++ {
				*l.out = append(*l.out, normalize(f.Type, names))
			}
		}
	}
	isTriggerType := func(s string) bool {
		s = strings.TrimLeft(s, "[]*")
		return s == triggerData || s == triggerEvent || s == triggerRow
	}
	found := false
	for _, s := range append(append([]string{}, params...), results...) {
		found = found || isTriggerType(s)
	}
	if !found {
		return false, nil
	}
	if len(params) != 1 || (params[0] != "*"+triggerData && params[0] != "*"+triggerEvent) {
		return true, fmt.Errorf("a trigger function's only parameter is a *trigger.Data (or a *trigger.Event for event triggers); *trigger.Row is only a result")
	}
	if len(results) > 0 && results[len(results)-1] == "error" {
		fn.HasError = true
		results = results[:len(results)-1]
	}
	fn.Trigger = "trigger"
	if params[0] == "*"+triggerEvent {
		fn.Trigger = "event_trigger"
		if len(results) > 0 {
			return true, fmt.Errorf("an event trigger function returns nothing (or an error)")
		}
		return true, nil
	}
	switch {
	case len(results) == 1 && results[0] == "*"+triggerRow:
		fn.TriggerRow = true
	case len(results) > 0:
		return true, fmt.Errorf("a trigger function returns a *trigger.Row (or nothing: the row goes on unchanged) and an optional error")
	}
	return true, nil
}

// cstringType is the Go type of SQL cstring in generated code.
const cstringType = "_gopgext_fmgr.CString"

// normalize spells e as generated code does: pg.CString becomes
// _gopgext_fmgr.CString, and so on.
func normalize(e ast.Expr, names imports) string {
	s := typeString(e)
	if names.pg != "" {
		for _, t := range pgTypes {
			s = regexp.MustCompile(`\b`+regexp.QuoteMeta(names.pg+"."+t)+`\b`).ReplaceAllString(s, fmgrType(t))
		}
	}
	if names.agg != "" {
		s = regexp.MustCompile(`\b`+regexp.QuoteMeta(names.agg+".State")+`\b`).ReplaceAllString(s, aggState)
	}
	if names.trigger != "" {
		for _, t := range []string{"Data", "Event", "Row"} {
			s = regexp.MustCompile(`\b`+regexp.QuoteMeta(names.trigger+"."+t)+`\b`).ReplaceAllString(s, "_gopgext_trigger."+t)
		}
	}
	if names.time != "" {
		// time.Time is timestamptz; generated code spells it _gopgext_fmgr.Time.
		s = regexp.MustCompile(`\b`+regexp.QuoteMeta(names.time+".Time")+`\b`).ReplaceAllString(s, fmgrType("Time"))
	}
	return s
}

func paramType(e ast.Expr, names imports) (Type, error) {
	s := normalize(e, names)
	var compositeErr error
	elem := func(k TypeKind, rest string) (Type, bool) {
		if scalars[rest] != "" {
			if k != Scalar && k != Nullable && notInArrays[rest] {
				return Type{}, false
			}
			if rest == aggState && k != Nullable {
				return Type{}, false
			}
			return Type{Kind: k, Go: rest}, true
		}
		ct := names.scope.types[rest]
		if ct == nil {
			return Type{}, false
		}
		if ct.Composite {
			if k != Scalar && k != Nullable {
				return Type{}, false
			}
			st, err := names.scope.structType(&ast.Ident{Name: ct.GoName})
			if err != nil {
				compositeErr = err
				return Type{}, false
			}
			return Type{Kind: k, Go: ct.GoName, Custom: ct, Struct: st}, true
		}
		if fixedSize(ct.Go) != "" && k != Scalar && k != Nullable {
			compositeErr = fmt.Errorf("arrays of fixed-size types over [N]byte are not supported (%s)", typeString(e))
			return Type{}, false
		}
		return Type{Kind: k, Go: ct.Go, Custom: ct}, true
	}
	for _, c := range []struct {
		prefix string
		kind   TypeKind
	}{{"*[]", NullableArray}, {"[]*", NullableElems}, {"[]", Array}, {"*", Nullable}, {"", Scalar}} {
		if rest, ok := strings.CutPrefix(s, c.prefix); ok {
			if t, ok := elem(c.kind, rest); ok {
				return t, nil
			}
			if compositeErr != nil {
				return Type{}, compositeErr
			}
			if c.prefix == "" || c.prefix == "*" || scalars[rest] != "" || names.scope.types[rest] != nil {
				break // e.g. []byte is a scalar, not an array of byte
			}
		}
	}
	if ct := names.scope.types[strings.TrimLeft(s, "[]*")]; ct != nil && ct.Composite {
		return Type{}, fmt.Errorf("arrays of composite types are not supported (%s)", typeString(e))
	}
	if s == aggState || strings.TrimLeft(s, "[]*") == aggState {
		return Type{}, fmt.Errorf("unsupported type %s: aggregate states are *agg.State", typeString(e))
	}
	if notInArrays[strings.TrimLeft(s, "[]*")] {
		return Type{}, fmt.Errorf("unsupported type %s: pg.Internal, pg.StringInfo, pg.AnyElement and pg.AnyArray can't be array elements (use pg.AnyArray for anyarray)", typeString(e))
	}
	if _, ok := names.scope.structDecls[strings.TrimLeft(s, "[]*")]; ok {
		return Type{}, fmt.Errorf("unsupported type %s: declare the struct with //gopgext:type to use it as a composite argument (only results can be anonymous rows)", typeString(e))
	}
	return Type{}, fmt.Errorf("unsupported type %s (supported: %s, pointers to them for NULL, slices of them for arrays, pointers to slices for NULL-able arrays)",
		typeString(e), supportedTypes())
}

func resultType(e ast.Expr, names imports) (Type, error) {
	iterName := names.iter
	if ix, ok := e.(*ast.IndexExpr); ok && iterName != "" {
		if sel, ok := ix.X.(*ast.SelectorExpr); ok {
			if x, ok := sel.X.(*ast.Ident); ok && x.Name == iterName && sel.Sel.Name == "Seq" {
				s := normalize(ix.Index, names)
				if ct := names.scope.types[s]; ct != nil {
					if ct.Composite {
						st, err := names.scope.structType(ix.Index)
						if err != nil {
							return Type{}, err
						}
						return Type{Kind: SetOf, Go: ct.GoName, Custom: ct, Struct: st}, nil
					}
					if fixedSize(ct.Go) != "" {
						return Type{}, fmt.Errorf("sets of fixed-size types over [N]byte are not supported (%s)", typeString(ix.Index))
					}
					return Type{Kind: SetOf, Go: ct.Go, Custom: ct}, nil
				}
				if st, err := names.scope.anonymousRow(ix.Index); st != nil || err != nil {
					if err != nil {
						return Type{}, err
					}
					return Type{Kind: SetOf, Go: st.GoName, Struct: st}, nil
				}
				if s == fmgrType("AnyElement") || s == recordType {
					return Type{Kind: SetOf, Go: s}, nil
				}
				if rest, ok := strings.CutPrefix(s, "*"); ok && scalars[rest] != "" && rest != cstringType && !notInArrays[rest] {
					return Type{Kind: NullableSetOf, Go: rest}, nil
				}
				if scalars[s] == "" || s == cstringType || notInArrays[s] {
					return Type{}, fmt.Errorf("unsupported set element type %s", typeString(ix.Index))
				}
				return Type{Kind: SetOf, Go: s}, nil
			}
		}
	}
	// A struct, or a pointer to one: a row.
	inner, kind := e, Scalar
	if star, ok := e.(*ast.StarExpr); ok {
		inner, kind = star.X, Nullable
	}
	if id, ok := inner.(*ast.Ident); !ok || names.scope.types[id.Name] == nil {
		if st, err := names.scope.anonymousRow(inner); st != nil || err != nil {
			if err != nil {
				return Type{}, err
			}
			return Type{Kind: kind, Go: st.GoName, Struct: st}, nil
		}
	}
	t, err := paramType(e, names)
	if err != nil {
		return Type{}, fmt.Errorf("%v, a struct (a row), or iter.Seq of one of those for SETOF (a table)", err)
	}
	return t, nil
}

// reservedSymbol returns the reserved prefix of a C symbol, if any.
func reservedSymbol(sym string) string {
	for _, reserved := range []string{"gopgext_", "tinygo_", "pg_finfo_", "_PG_", "Pg_magic"} {
		if strings.HasPrefix(sym, reserved) {
			return reserved
		}
	}
	return ""
}

func supportedTypes() string {
	var names []string
	for k := range scalars {
		if k == fmgrType("Time") {
			k = "time.Time"
		}
		names = append(names, strings.Replace(k, "_gopgext_fmgr.", "pg.", 1))
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

func (f *Function) parseOptions(opts []string) error {
	for _, o := range opts {
		key, val, hasVal := strings.Cut(o, "=")
		switch {
		case key == "name" && hasVal:
			if !sqlNameRe.MatchString(val) {
				return fmt.Errorf("invalid SQL function name %q", val)
			}
			f.SQLName = val
		case !hasVal && (key == "immutable" || key == "stable" || key == "volatile"):
			f.Volatility = key
		case !hasVal && key == "strict":
			f.Strict = true
		case key == "parallel" && (val == "safe" || val == "restricted" || val == "unsafe"):
			f.Parallel = val
		case key == "cost" && numberRe.MatchString(val):
			f.Cost = val
		case key == "rows" && numberRe.MatchString(val):
			f.Rows = val
		case !hasVal && key == "leakproof":
			f.Leakproof = true
		case !hasVal && key == "security_definer":
			f.SecurityDefiner = true
		case key == "symbol" && identRe.MatchString(val):
			if reserved := reservedSymbol(val); reserved != "" {
				return fmt.Errorf("symbol %q: the %s prefix is reserved", val, reserved)
			}
			f.Symbol = val
		case !hasVal && key == "manual":
			f.Manual = true
		default:
			return fmt.Errorf("unknown option %q (options: name=..., immutable, stable, volatile, strict, "+
				"parallel=safe|restricted|unsafe, cost=N, rows=N, leakproof, security_definer, symbol=c_name, manual)", o)
		}
	}
	if f.Trigger != "" && f.Strict {
		return fmt.Errorf("strict: trigger functions have no arguments")
	}
	if f.Rows != "" && (f.Result == nil || (f.Result.Kind != SetOf && f.Result.Kind != NullableSetOf)) {
		return fmt.Errorf("rows= is only valid for set-returning functions")
	}
	return nil
}

// ReadOnly reports whether queries in the function must be read-only.
func (f *Function) ReadOnly() bool { return f.Volatility != "volatile" }

// snakeCase turns Go names into SQL ones: ReverseString -> reverse_string,
// HTTPStatus -> http_status, sha256Sum -> sha256_sum.
func snakeCase(s string) string {
	r := []rune(s)
	var sb strings.Builder
	for i, c := range r {
		if unicode.IsUpper(c) {
			prevLower := i > 0 && (unicode.IsLower(r[i-1]) || unicode.IsDigit(r[i-1]))
			nextLower := i > 0 && i+1 < len(r) && unicode.IsLower(r[i+1]) && unicode.IsUpper(r[i-1])
			if prevLower || nextLower {
				sb.WriteByte('_')
			}
			c = unicode.ToLower(c)
		}
		sb.WriteRune(c)
	}
	return sb.String()
}

// TypePragma declares a data type over a Go type:
//
//	// Vector is a vector of float4.
//	//
//	//gopgext:type input=VectorIn output=VectorOut storage=external
//	type Vector pg.Varlena
//
// The Go type's underlying type is the representation (a scalar), and the
// type is created with the given //gopgext:function functions.
const TypePragma = "//gopgext:type"

// scope is the main package's types, shared by its files.
type scope struct {
	fset        *token.FileSet
	types       map[string]*SQLType        // //gopgext:type, by Go name
	structDecls map[string]*ast.StructType // structs, by Go name
	structPos   map[string]token.Pos
	structFile  map[string]imports
	structs     map[string]*Struct // checked structs
	used        []*Struct          // structs used as results, in order of use
}

// collectTypes records the struct and //gopgext:type declarations of f.
// pkg is nil for files without pragmas (only their structs matter).
func (sc *scope) collectTypes(f *ast.File, pkg *Package) error {
	names := fileImports(f)
	names.scope = sc
	if sc.structFile == nil {
		sc.structFile = map[string]imports{}
	}
	for _, decl := range f.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.TYPE {
			continue
		}
		for _, spec := range gd.Specs {
			ts := spec.(*ast.TypeSpec)
			if st, ok := ts.Type.(*ast.StructType); ok && ts.TypeParams == nil {
				sc.structDecls[ts.Name.Name] = st
				sc.structPos[ts.Name.Name] = ts.Pos()
				sc.structFile[ts.Name.Name] = names
			}
			if pkg == nil {
				continue
			}
			doc := ts.Doc
			if doc == nil && len(gd.Specs) == 1 {
				doc = gd.Doc
			}
			if doc == nil {
				continue
			}
			for _, c := range doc.List {
				if c.Text != TypePragma && !strings.HasPrefix(c.Text, TypePragma+" ") {
					continue
				}
				t, err := sc.checkType(ts, doc, c, names)
				if err != nil {
					return err
				}
				if prev := sc.types[t.GoName]; prev != nil {
					return fmt.Errorf("%s: %s: %s already declared at %s", t.Pos, TypePragma, t.GoName, prev.Pos)
				}
				sc.types[t.GoName] = t
				pkg.Types = append(pkg.Types, t)
			}
		}
	}
	return nil
}

// typeFunctions are the functions of CREATE TYPE that //gopgext:type
// options name.
var typeFunctions = []string{"input", "output", "receive", "send", "typmod_in", "typmod_out", "analyze"}

var (
	storages   = map[string]bool{"plain": true, "external": true, "extended": true, "main": true}
	alignments = map[string]bool{"char": true, "int2": true, "int4": true, "double": true}
	categoryRe = regexp.MustCompile(`^[A-Z]$`)
)

func (sc *scope) checkType(ts *ast.TypeSpec, doc *ast.CommentGroup, pragma *ast.Comment, names imports) (*SQLType, error) {
	pos := sc.fset.Position(pragma.Pos())
	errf := func(format string, args ...any) error {
		return fmt.Errorf("%s: %s %s: %s", pos, TypePragma, ts.Name.Name, fmt.Sprintf(format, args...))
	}
	if ts.TypeParams != nil {
		return nil, errf("generic types can't be SQL types")
	}
	if _, ok := ts.Type.(*ast.StructType); ok {
		return sc.checkComposite(ts, doc, pragma, pos, errf)
	}
	rep := normalize(ts.Type, names)
	if _, ok := byValue[rep]; !ok && !varlenaReps[rep] && fixedSize(rep) == "" {
		return nil, errf("the representation must be one of bool, int16, int32, int64, int, float32, float64, pg.Oid (fixed size, by value), [N]byte (fixed size, by reference) or []byte, pg.Varlena, string (variable size), not %s", typeString(ts.Type))
	}
	t := &SQLType{GoName: ts.Name.Name, SQLName: snakeCase(ts.Name.Name), Go: rep, Functions: map[string]string{}, Pos: pos}
	if ts.Assign.IsValid() {
		return nil, errf("must be a defined type (type %s %s), not an alias", ts.Name.Name, typeString(ts.Type))
	}
	var doclines []string
	for _, line := range strings.Split(doc.Text(), "\n") {
		doclines = append(doclines, line)
	}
	t.Doc = strings.TrimSpace(strings.Join(doclines, "\n"))
	for _, o := range strings.Fields(strings.TrimPrefix(pragma.Text, TypePragma)) {
		key, val, hasVal := strings.Cut(o, "=")
		switch {
		case key == "name" && hasVal && sqlNameRe.MatchString(val):
			t.SQLName = val
		case hasVal && slicesContains(typeFunctions, key) && identRe.MatchString(val):
			t.Functions[key] = val
		case key == "storage" && storages[val]:
			if !varlenaReps[rep] && val != "plain" {
				return nil, errf("storage=%s needs a variable-size representation", val)
			}
			t.Storage = val
		case key == "alignment" && alignments[val]:
			t.Alignment = val
		case key == "category" && categoryRe.MatchString(val):
			t.Category = val
		case !hasVal && key == "preferred":
			t.Preferred = true
		default:
			return nil, errf("unknown option %q (options: name=..., %s=GoFunc, storage=plain|external|extended|main, alignment=char|int2|int4|double, category=X, preferred)",
				o, strings.Join(typeFunctions, "=GoFunc, "))
		}
	}
	if t.Functions["input"] == "" || t.Functions["output"] == "" {
		return nil, errf("input=GoFunc and output=GoFunc are required")
	}
	return t, nil
}

func slicesContains(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}

// byValue are the fixed-size representations: their length and alignment.
var byValue = map[string][2]string{
	"bool":          {"1", "char"},
	"int16":         {"2", "int2"},
	"int32":         {"4", "int4"},
	"float32":       {"4", "int4"},
	fmgrType("Oid"): {"4", "int4"},
	"int64":         {"8", "double"},
	"int":           {"8", "double"},
	"float64":       {"8", "double"},
}

// fixedRe matches the representation of fixed-size types passed by
// reference: [N]byte, N bytes (INTERNALLENGTH = N, like point or uuid).
var fixedRe = regexp.MustCompile(`^\[([1-9][0-9]{0,3})\]byte$`)

// fixedSize returns N for a [N]byte representation, "" for others.
func fixedSize(rep string) string {
	if m := fixedRe.FindStringSubmatch(rep); m != nil {
		return m[1]
	}
	return ""
}

// varlenaReps are the variable-size representations.
var varlenaReps = map[string]bool{"[]byte": true, "string": true, fmgrType("Varlena"): true}

// structType returns the struct e names (nil if it isn't a struct of the
// package).
func (sc *scope) structType(e ast.Expr) (*Struct, error) {
	id, ok := e.(*ast.Ident)
	if !ok || sc.structDecls == nil {
		return nil, nil
	}
	if st := sc.structs[id.Name]; st != nil {
		return st, nil
	}
	decl := sc.structDecls[id.Name]
	if decl == nil {
		return nil, nil
	}
	st := &Struct{GoName: id.Name, Pos: sc.fset.Position(sc.structPos[id.Name])}
	names := sc.structFile[id.Name]
	seen := map[string]bool{}
	for _, field := range decl.Fields.List {
		if len(field.Names) == 0 {
			return nil, fmt.Errorf("%s: struct %s: embedded fields can't be columns", st.Pos, st.GoName)
		}
		t, err := paramType(field.Type, names)
		if err != nil {
			return nil, fmt.Errorf("%s: struct %s: %v", st.Pos, st.GoName, err)
		}
		if t.Go == fmgrType("StringInfo") || t.Go == fmgrType("Internal") || polymorphic[t.Go] != "" || t.Struct != nil ||
			t.Go == aggState || t.Go == recordType ||
			fixedSize(t.Go) != "" {
			return nil, fmt.Errorf("%s: struct %s: %s can't be a column", st.Pos, st.GoName, typeString(field.Type))
		}
		col := ""
		if field.Tag != nil {
			tag, _ := strconv.Unquote(field.Tag.Value)
			col = reflectTag(tag, "pg")
			if col != "" && !sqlNameRe.MatchString(col) {
				return nil, fmt.Errorf("%s: struct %s: invalid column name %q", st.Pos, st.GoName, col)
			}
		}
		for _, n := range field.Names {
			if n.Name == "_" {
				return nil, fmt.Errorf("%s: struct %s: blank fields can't be columns", st.Pos, st.GoName)
			}
			name := col
			if name == "" {
				name = snakeCase(n.Name)
			}
			if seen[name] {
				return nil, fmt.Errorf("%s: struct %s: column %s declared twice", st.Pos, st.GoName, name)
			}
			seen[name] = true
			st.Fields = append(st.Fields, Field{GoName: n.Name, SQLName: name, Type: t})
		}
	}
	if len(st.Fields) == 0 {
		return nil, fmt.Errorf("%s: struct %s: a row needs at least 1 field", st.Pos, st.GoName)
	}
	if ct := sc.types[id.Name]; ct != nil && ct.Composite {
		st.Composite = ct
	}
	sc.structs[id.Name] = st
	sc.used = append(sc.used, st)
	return st, nil
}

// anonymousRow returns the struct e names, as an anonymous row result
// (OUT parameters or TABLE columns: at least 2).
func (sc *scope) anonymousRow(e ast.Expr) (*Struct, error) {
	st, err := sc.structType(e)
	if st != nil && st.Composite == nil && len(st.Fields) < 2 {
		return nil, fmt.Errorf("%s: struct %s: a row needs at least 2 fields (return the field's type for one value, or declare a composite type with //gopgext:type)", st.Pos, st.GoName)
	}
	return st, err
}

// checkComposite checks a //gopgext:type on a struct: a composite type.
func (sc *scope) checkComposite(ts *ast.TypeSpec, doc *ast.CommentGroup, pragma *ast.Comment, pos token.Position,
	errf func(string, ...any) error) (*SQLType, error) {
	t := &SQLType{GoName: ts.Name.Name, SQLName: snakeCase(ts.Name.Name), Go: ts.Name.Name, Composite: true,
		Functions: map[string]string{}, Pos: pos}
	if ts.Assign.IsValid() {
		return nil, errf("must be a defined type, not an alias")
	}
	t.Doc = strings.TrimSpace(doc.Text())
	for _, o := range strings.Fields(strings.TrimPrefix(pragma.Text, TypePragma)) {
		key, val, hasVal := strings.Cut(o, "=")
		if key != "name" || !hasVal || !sqlNameRe.MatchString(val) {
			return nil, errf("unknown option %q (a composite type takes only name=...)", o)
		}
		t.SQLName = val
	}
	return t, nil
}

// reflectTag is reflect.StructTag.Get.
func reflectTag(tag, key string) string {
	for tag != "" {
		i := 0
		for i < len(tag) && tag[i] == ' ' {
			i++
		}
		tag = tag[i:]
		i = 0
		for i < len(tag) && tag[i] > ' ' && tag[i] != ':' && tag[i] != '"' {
			i++
		}
		if i == 0 || i+1 >= len(tag) || tag[i] != ':' || tag[i+1] != '"' {
			return ""
		}
		name := tag[:i]
		tag = tag[i+1:]
		i = 1
		for i < len(tag) && tag[i] != '"' {
			if tag[i] == '\\' {
				i++
			}
			i++
		}
		if i >= len(tag) {
			return ""
		}
		qvalue := tag[:i+1]
		tag = tag[i+1:]
		if name == key {
			v, err := strconv.Unquote(qvalue)
			if err != nil {
				return ""
			}
			return v
		}
	}
	return ""
}

// resolveTypes links the functions of the //gopgext:type declarations.
func (sc *scope) resolveTypes(pkg *Package) error {
	byName := map[string]*Function{}
	for _, f := range pkg.Funcs {
		byName[f.GoName] = f
	}
	for _, t := range pkg.Types {
		t.funcs = map[string]*Function{}
		if t.Composite {
			if _, err := sc.structType(&ast.Ident{Name: t.GoName}); err != nil {
				return err
			}
			continue
		}
		for _, role := range typeFunctions {
			name := t.Functions[role]
			if name == "" {
				continue
			}
			f := byName[name]
			if f == nil {
				return fmt.Errorf("%s: %s %s: %s=%s: no //gopgext:function %s", t.Pos, TypePragma, t.GoName, role, name, name)
			}
			if f.Manual {
				return fmt.Errorf("%s: %s %s: %s=%s: the function can't be manual (gopgext creates the type)", t.Pos, TypePragma, t.GoName, role, name)
			}
			if f.IOType != nil && f.IOType != t {
				return fmt.Errorf("%s: %s %s: %s is already a function of type %s", t.Pos, TypePragma, t.GoName, name, f.IOType.GoName)
			}
			f.IOType = t
			t.funcs[role] = f
		}
	}
	return nil
}

// CreateType returns the CREATE TYPE options, e.g. "INPUT = vector_in,
// OUTPUT = vector_out, STORAGE = external".
func (t *SQLType) CreateType() string {
	var opts []string
	for _, role := range typeFunctions {
		if f := t.funcs[role]; f != nil {
			opts = append(opts, strings.ToUpper(role)+" = "+quoteIdent(f.SQLName))
		}
	}
	if bv, ok := byValue[t.Go]; ok {
		opts = append(opts, "INTERNALLENGTH = "+bv[0], "PASSEDBYVALUE")
		if t.Alignment == "" {
			opts = append(opts, "ALIGNMENT = "+bv[1])
		}
	} else if n := fixedSize(t.Go); n != "" {
		opts = append(opts, "INTERNALLENGTH = "+n) // ALIGNMENT: PostgreSQL's default (int4)
	} else {
		opts = append(opts, "INTERNALLENGTH = VARIABLE")
	}
	if t.Alignment != "" {
		opts = append(opts, "ALIGNMENT = "+t.Alignment)
	}
	if t.Storage != "" {
		opts = append(opts, "STORAGE = "+t.Storage)
	}
	if t.Category != "" {
		opts = append(opts, "CATEGORY = "+quoteLiteral(t.Category))
	}
	if t.Preferred {
		opts = append(opts, "PREFERRED = true")
	}
	return strings.Join(opts, ",\n\t")
}
