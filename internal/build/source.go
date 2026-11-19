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
	Symbol          string // C symbol PostgreSQL calls (default gopgext_fn_<GoName>)
	Manual          bool   // declared by the project's sql/ files, not generated
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
)

// Type is the type of a parameter or result.
type Type struct {
	Kind TypeKind
	Go   string // the scalar Go type: int32, string, []byte, ...
}

// scalars maps the supported Go types to SQL types.
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

	cstringType: "cstring",
}

// SQL returns the SQL type.
func (t Type) SQL() string {
	s := scalars[t.Go]
	switch t.Kind {
	case Array, NullableArray, NullableElems:
		return s + "[]"
	case SetOf:
		return "SETOF " + s
	}
	return s
}

// prepareSource copies the project's module into dst and returns the SQL
// functions declared in its main package.
func prepareSource(proj, dst string) ([]*Function, error) {
	if err := os.RemoveAll(dst); err != nil {
		return nil, err
	}
	var funcs []*Function
	err := filepath.WalkDir(proj, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(proj, p)
		if d.IsDir() {
			if rel != "." && (strings.HasPrefix(d.Name(), ".") || strings.HasPrefix(d.Name(), "_") ||
				d.Name() == CSrcDir || d.Name() == SQLDir) {
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
			fs, err := scanFile(p, data)
			if err != nil {
				return err
			}
			funcs = append(funcs, fs...)
		}
		return os.WriteFile(filepath.Join(dst, rel), data, 0o644)
	})
	if err != nil {
		return nil, err
	}
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
	return funcs, nil
}

var (
	identRe   = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	sqlNameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_$]*$`)
	numberRe  = regexp.MustCompile(`^[0-9]+(\.[0-9]+)?$`)
)

func scanFile(path string, src []byte) ([]*Function, error) {
	if !strings.Contains(string(src), Pragma) {
		return nil, nil
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, src, parser.ParseComments)
	if err != nil {
		return nil, err
	}
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
		}
	}
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

// imports are the local names of the packages types can come from.
type imports struct {
	iter string // "iter"
	pg   string // "gopgext/pg"
}

func checkFunc(fd *ast.FuncDecl, pkg string, names imports, pos token.Position) (*Function, error) {
	errf := func(format string, args ...any) error {
		return fmt.Errorf("%s: %s %s: %s", pos, Pragma, fd.Name.Name, fmt.Sprintf(format, args...))
	}
	if pkg != "main" {
		return nil, errf("SQL functions must be in package main")
	}
	if fd.Recv != nil || fd.Type.TypeParams != nil {
		return nil, errf("must be a plain function (no receiver or type parameters)")
	}
	if !identRe.MatchString(fd.Name.Name) {
		return nil, errf("function names must be ASCII")
	}
	fn := &Function{GoName: fd.Name.Name, SQLName: snakeCase(fd.Name.Name), Volatility: "volatile", Pos: pos,
		Symbol: "gopgext_fn_" + fd.Name.Name}
	nullable := false
	for _, field := range fd.Type.Params.List {
		if _, ok := field.Type.(*ast.Ellipsis); ok {
			return nil, errf("variadic parameters are not supported")
		}
		t, err := paramType(field.Type, names)
		if err != nil {
			return nil, errf("%v", err)
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
		fn.Result = &t
	default:
		return nil, errf("returns at most one value (and an error)")
	}
	return fn, nil
}

func typeString(e ast.Expr) string { return types.ExprString(e) }

// cstringType is the Go type of SQL cstring in generated code.
const cstringType = "_gopgext_fmgr.CString"

func paramType(e ast.Expr, names imports) (Type, error) {
	s := typeString(e)
	if names.pg != "" {
		// pg.CString, *pg.CString
		s = strings.Replace(s, names.pg+".CString", cstringType, 1)
		if s == "[]"+cstringType || s == "*[]"+cstringType || s == "[]*"+cstringType {
			return Type{}, fmt.Errorf("arrays of pg.CString are not supported")
		}
	}
	switch {
	case scalars[s] != "":
		return Type{Kind: Scalar, Go: s}, nil
	case strings.HasPrefix(s, "*") && scalars[s[1:]] != "":
		return Type{Kind: Nullable, Go: s[1:]}, nil
	case strings.HasPrefix(s, "[]") && scalars[s[2:]] != "":
		return Type{Kind: Array, Go: s[2:]}, nil
	case strings.HasPrefix(s, "*[]") && scalars[s[3:]] != "":
		return Type{Kind: NullableArray, Go: s[3:]}, nil
	case strings.HasPrefix(s, "[]*") && scalars[s[3:]] != "":
		return Type{Kind: NullableElems, Go: s[3:]}, nil
	}
	return Type{}, fmt.Errorf("unsupported type %s (supported: %s, pointers to them for NULL, slices of them for arrays, pointers to slices for NULL-able arrays)",
		s, supportedTypes())
}

func resultType(e ast.Expr, names imports) (Type, error) {
	iterName := names.iter
	if ix, ok := e.(*ast.IndexExpr); ok && iterName != "" {
		if sel, ok := ix.X.(*ast.SelectorExpr); ok {
			if x, ok := sel.X.(*ast.Ident); ok && x.Name == iterName && sel.Sel.Name == "Seq" {
				s := typeString(ix.Index)
				if scalars[s] == "" || strings.HasSuffix(s, ".CString") {
					return Type{}, fmt.Errorf("unsupported set element type %s", s)
				}
				return Type{Kind: SetOf, Go: s}, nil
			}
		}
	}
	t, err := paramType(e, names)
	if err != nil {
		return Type{}, fmt.Errorf("%v, or iter.Seq of one for SETOF", err)
	}
	return t, nil
}

func supportedTypes() string {
	var names []string
	for k := range scalars {
		names = append(names, strings.Replace(k, cstringType, "pg.CString", 1))
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
			for _, reserved := range []string{"gopgext_", "tinygo_", "pg_finfo_", "_PG_", "Pg_magic"} {
				if strings.HasPrefix(val, reserved) {
					return fmt.Errorf("symbol %q: the %s prefix is reserved", val, reserved)
				}
			}
			f.Symbol = val
		case !hasVal && key == "manual":
			f.Manual = true
		default:
			return fmt.Errorf("unknown option %q (options: name=..., immutable, stable, volatile, strict, "+
				"parallel=safe|restricted|unsafe, cost=N, rows=N, leakproof, security_definer, symbol=c_name, manual)", o)
		}
	}
	if f.Rows != "" && (f.Result == nil || f.Result.Kind != SetOf) {
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
