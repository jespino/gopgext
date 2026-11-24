package build

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSnakeCase(t *testing.T) {
	for in, want := range map[string]string{
		"Add":           "add",
		"ReverseString": "reverse_string",
		"HTTPStatus":    "http_status",
		"sha256Sum":     "sha256_sum",
		"getURL":        "get_url",
		"already_snake": "already_snake",
		"ID":            "id",
	} {
		if got := snakeCase(in); got != want {
			t.Errorf("snakeCase(%q) = %q, want %q", in, got, want)
		}
	}
}

func scan(t *testing.T, src string) ([]*Function, error) {
	t.Helper()
	pkg, err := scanFile("main.go", []byte("package main\n\nimport \"iter\"\n\n"+src))
	if err != nil {
		return nil, err
	}
	return pkg.Funcs, nil
}

// pkgOf wraps the functions of a single-file package without types.
func pkgOf(funcs []*Function) *Package { return &Package{Funcs: funcs} }

func TestScanSignatures(t *testing.T) {
	funcs, err := scan(t, `
// Add adds.
//
//gopgext:function immutable parallel=safe
func Add(a, b int32) int32 { return a + b }

//gopgext:function name=lookup_item stable
func Lookup(id *int64, tags []string) (*string, error) { return nil, nil }

//gopgext:function
func Codes(s string) *[]string { return nil }

//gopgext:function rows=10
func Gen(n int) iter.Seq[float64] { return nil }

//gopgext:function
func Nothing(_ bool) {}

func NotExported(x int) int { return x }
`)
	if err != nil {
		t.Fatal(err)
	}
	if len(funcs) != 5 {
		t.Fatalf("got %d functions, want 5", len(funcs))
	}
	got := []string{}
	for _, f := range funcs {
		got = append(got, f.Signature())
	}
	want := []string{
		"add(a integer, b integer)",
		"lookup_item(id bigint, tags text[])",
		"codes(s text)",
		"gen(n bigint)",
		"nothing(arg1 boolean)",
	}
	if strings.Join(got, ";") != strings.Join(want, ";") {
		t.Errorf("signatures:\n got %q\nwant %q", got, want)
	}
	add, lookup, codes, gen := funcs[0], funcs[1], funcs[2], funcs[3]
	if codes.Result.Kind != NullableArray || codes.Result.SQL() != "text[]" || !codes.Strict {
		t.Errorf("codes: %+v", codes)
	}
	if !add.Strict || add.Volatility != "immutable" || add.Parallel != "safe" || add.Doc != "Add adds." || !add.ReadOnly() {
		t.Errorf("add: %+v", add)
	}
	if lookup.Strict || !lookup.HasError || lookup.Result.Kind != Nullable || lookup.Params[1].Type.Kind != Array {
		t.Errorf("lookup: %+v", lookup)
	}
	if gen.Result.SQL() != "SETOF double precision" || gen.Rows != "10" || gen.ReadOnly() {
		t.Errorf("gen: %+v", gen)
	}
}

func TestScanErrors(t *testing.T) {
	for _, tc := range []struct{ src, want string }{
		{"//gopgext:function\nfunc F(m map[string]int) {}", "unsupported type map[string]int"},
		{"//gopgext:function\nfunc F(xs ...int32) {}", "variadic"},
		{"//gopgext:function\nfunc F() (int32, int32) { return 0, 0 }", "at most one value"},
		{"//gopgext:function\nfunc F() iter.Seq[uint8] { return nil }", "unsupported set element type"},
		{"//gopgext:function bogus\nfunc F() {}", `unknown option "bogus"`},
		{"//gopgext:function rows=5\nfunc F() int32 { return 0 }", "only valid for set-returning"},
		{"type T struct{}\n//gopgext:function\nfunc (T) F() {}", "plain function"},
	} {
		_, err := scan(t, tc.src)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%q: got error %v, want %q", tc.src, err, tc.want)
		}
	}
}

func TestGenSQL(t *testing.T) {
	funcs, err := scan(t, `
// It's quoted.
//
//gopgext:function name=from immutable strict cost=5 leakproof
func From(order *int32) int32 { return 0 }
`)
	if err != nil {
		t.Fatal(err)
	}
	sql, err := genSQL("ext", "example.com/ext", pkgOf(funcs), nil, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`CREATE FUNCTION "from"("order" integer)`,
		"RETURNS integer",
		"AS 'MODULE_PATHNAME', 'gopgext_fn_From'",
		"LANGUAGE C IMMUTABLE STRICT LEAKPROOF COST 5;",
		`COMMENT ON FUNCTION "from"(integer) IS 'It''s quoted.';`,
	} {
		if !strings.Contains(string(sql), want) {
			t.Errorf("SQL lacks %q:\n%s", want, sql)
		}
	}
	goBytes, err := genGo(pkgOf(funcs))
	if err != nil {
		t.Fatal(err)
	}
	goSrc := string(goBytes)
	if !strings.Contains(goSrc, `_gopgext_fmgr.ArgPtr[int32](_c, 0)`) || !strings.Contains(goSrc, "defer _c.End(_c.Begin(true))") {
		t.Errorf("Go wrapper:\n%s", goSrc)
	}
}

func TestNames(t *testing.T) {
	if NameFromPath("github.com/x/My-Ext") != "my_ext" {
		t.Error(NameFromPath("github.com/x/My-Ext"))
	}
	for _, bad := range []string{"", "1abc", "Abc", "spi", "-x", "a.b", strings.Repeat("a", 64)} {
		if ValidateName(bad) == nil {
			t.Errorf("ValidateName(%q) accepted", bad)
		}
	}
	if ValidateName("uuid-ossp") != nil {
		t.Error("ValidateName(uuid-ossp)")
	}
	for s, want := range map[string]uint64{"4M": 4 << 20, "1G": 1 << 30, "65536": 65536, "5000": 4096} {
		if got, err := heapBytes(s); err != nil || got != want {
			t.Errorf("heapBytes(%q) = %d, %v", s, got, err)
		}
	}
	if _, err := heapBytes("4MB"); err == nil {
		t.Error("heapBytes(4MB) accepted")
	}
}

func TestManualSymbolsAndCString(t *testing.T) {
	pkg, err := scanFile("main.go", []byte(`package main

import "gopgext/pg"

//gopgext:function symbol=thing_in manual
func ThingIn(s pg.CString) (int64, error) { return 0, nil }

//gopgext:function symbol=thing_out manual
func ThingOut(v int64) pg.CString { return "" }

//gopgext:function
func Echo(s *pg.CString) string { return "" }
`))
	if err != nil {
		t.Fatal(err)
	}
	funcs := pkg.Funcs
	if false {
	}
	in, out, echo := funcs[0], funcs[1], funcs[2]
	if in.Symbol != "thing_in" || !in.Manual || in.Params[0].Type.SQL() != "cstring" {
		t.Errorf("thing_in: %+v", in)
	}
	if out.Result.SQL() != "cstring" || echo.Params[0].Type.Kind != Nullable || echo.Symbol != "gopgext_fn_Echo" {
		t.Errorf("thing_out/echo: %+v %+v", out, echo)
	}
	sql, err := genSQL("ext", "ext", pkgOf(funcs), nil, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(sql), "thing_") || !strings.Contains(string(sql), `"echo"("s" cstring)`) {
		t.Errorf("manual functions must not be declared:\n%s", sql)
	}
	c := string(genC(funcs))
	if !strings.Contains(c, "PG_FUNCTION_INFO_V1(thing_in);") || !strings.Contains(c, "gopgext_invoke(fcinfo, gopgext_go_ThingIn)") {
		t.Errorf("C wrappers:\n%s", c)
	}
	exp := string(genExports(funcs, []string{"my_handler"}))
	if !strings.Contains(exp, "thing_in;") || !strings.Contains(exp, "pg_finfo_thing_out;") || !strings.Contains(exp, "pg_finfo_my_handler;") {
		t.Errorf("exports:\n%s", exp)
	}
	if goSrc, err := genGo(pkgOf(funcs)); err != nil || !strings.Contains(string(goSrc), "_gopgext_fmgr.Arg[_gopgext_fmgr.CString](_c, 0") {
		t.Errorf("Go wrapper (%v):\n%s", err, goSrc)
	}

	for _, tc := range []struct{ src, want string }{
		{"//gopgext:function symbol=gopgext_x\nfunc F() {}", "prefix is reserved"},
		{"//gopgext:function symbol=a\nfunc F() {}\n//gopgext:function symbol=a\nfunc G() {}", ""},
		{"//gopgext:function\nfunc F(s []pg.Internal) {}", "can't be array elements"},
		{"//gopgext:function\nfunc F(s *pg.StringInfo) {}", "can't be pointers"},
		{"//gopgext:function\nfunc F() pg.StringInfo { return pg.StringInfo{} }", "can't be a result"},
	} {
		dir := t.TempDir()
		src := "package main\n\nimport \"gopgext/pg\"\n\n" + tc.src
		if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := prepareSource(dir, t.TempDir())
		want := tc.want
		if want == "" {
			want = "C symbol a already used"
		}
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: got %v, want %q", tc.src, err, want)
		}
	}
}

func TestLDFlags(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.go"), []byte("package main\n\n//gopgext:ldflags -lcurl -L/opt/lib\n//gopgext:ldflagsx -bogus\n"), 0o644)
	flags, err := scanLDFlags(dir)
	if err != nil || strings.Join(flags, " ") != "-lcurl -L/opt/lib" {
		t.Errorf("got %q, %v", flags, err)
	}
	os.WriteFile(filepath.Join(dir, "b.go"), []byte("package main\n//gopgext:ldflags $(shell rm -rf /)\n"), 0o644)
	if _, err := scanLDFlags(dir); err == nil {
		t.Error("accepted a non-flag")
	}
	if m := v1Re.FindAllStringSubmatch("PG_FUNCTION_INFO_V1(my_fdw_handler);\n  PG_FUNCTION_INFO_V1( other );", -1); len(m) != 2 || m[1][1] != "other" {
		t.Errorf("v1Re: %q", m)
	}
}

func TestAliases(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.go"), []byte("package main\n\n//gopgext:alias g_consistent=gopgext_gist_consistent g_union=gopgext_gist_union\n"), 0o644)
	aliases, err := scanAliases(dir)
	if err != nil || len(aliases) != 2 || aliases[1] != (Alias{"g_union", "gopgext_gist_union"}) {
		t.Fatalf("got %v, %v", aliases, err)
	}
	c := string(genAliases(aliases))
	if !strings.Contains(c, "PG_FUNCTION_INFO_V1(g_consistent);") || !strings.Contains(c, "return gopgext_gist_consistent(fcinfo);") {
		t.Errorf("genAliases:\n%s", c)
	}
	for _, bad := range []string{"g_union", "x=", "gopgext_x=y", "a-b=c"} {
		os.WriteFile(filepath.Join(dir, "b.go"), []byte("package main\n//gopgext:alias "+bad+"\n"), 0o644)
		if _, err := scanAliases(dir); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}

func TestControlRequires(t *testing.T) {
	got := string(genControl("earthdistance", "1.2", "x", false, []string{"cube", "seg"}))
	if !strings.Contains(got, "requires = 'cube, seg'\n") {
		t.Errorf("control file:\n%s", got)
	}
}
