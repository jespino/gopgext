package build

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const typesSrc = `package main

import (
	"iter"

	"gopgext/pg"
)

// Vec is a vector.
//
//gopgext:type input=VecIn output=VecOut receive=VecRecv send=VecSend typmod_in=VecTypmodIn storage=external
type Vec pg.Varlena

// Code is a small code.
//
//gopgext:type name=code input=CodeIn output=CodeOut
type Code int32

// Pair is a row.
type Pair struct {
	Name  string
	Value *float64 ` + "`pg:\"val\"`" + `
	Vec   Vec
	Tags  []string
}

//gopgext:function immutable
func VecIn(s pg.CString, typ pg.Oid, typmod int32) (Vec, error) { return nil, nil }

//gopgext:function immutable
func VecOut(v Vec) pg.CString { return "" }

//gopgext:function immutable
func VecRecv(buf pg.StringInfo, typ pg.Oid, typmod int32) Vec { return nil }

//gopgext:function immutable
func VecSend(v Vec) []byte { return nil }

//gopgext:function immutable
func VecTypmodIn(mods []pg.CString) int32 { return 0 }

//gopgext:function immutable
func CodeIn(s pg.CString) Code { return 0 }

//gopgext:function immutable
func CodeOut(c Code) pg.CString { return "" }

//gopgext:function immutable
func VecDims(v Vec, w *Vec) *Code { return nil }

//gopgext:function
func Pairs(n int32) iter.Seq[Pair] { return nil }

//gopgext:function
func OnePair(n int32) *Pair { return nil }

//gopgext:function
func Vecs() iter.Seq[Vec] { return nil }

//gopgext:function
func Raw(p pg.Internal, oids []pg.Oid) pg.Internal { return 0 }

// Item is a composite type.
//
//gopgext:type name=item
type Item struct {
	ID    int64
	Label *string
	Codes []Code
	Vecs  *[]Vec
}

//gopgext:function
func Codes(cs []Code, more *[]Code, some []*Code) []*Code { return nil }

//gopgext:function
func ItemOf(it Item, other *Item) *Item { return nil }

//gopgext:function
func Items() iter.Seq[Item] { return nil }

//gopgext:function
func First(xs pg.AnyArray, def *pg.AnyElement) *pg.AnyElement { return nil }

//gopgext:function
func Unnest(xs pg.AnyArray) iter.Seq[pg.AnyElement] { return nil }

//gopgext:function
func Same(x pg.AnyElement) pg.AnyArray { return pg.AnyArray{} }
`

func TestTypesAndRowsGolden(t *testing.T) {
	pkg, err := scanFile("main.go", []byte(typesSrc))
	if err != nil {
		t.Fatal(err)
	}
	sql, err := genSQL("ext", "example.com/ext", pkg, nil, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	goSrc, err := genGo(pkg)
	if err != nil {
		t.Fatal(err)
	}
	checkGolden(t, "types.sql.golden", sql)
	checkGolden(t, "types.go.golden", goSrc)
}

func checkGolden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *update {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Errorf("%s differs:\n got:\n%s\nwant:\n%s", name, got, want)
	}
}

func TestTypeErrors(t *testing.T) {
	for _, tc := range []struct{ src, want string }{
		{"//gopgext:type input=In\ntype T int32", "input=GoFunc and output=GoFunc are required"},
		{"//gopgext:type input=In output=Out\ntype T map[string]int", "the representation must be"},
		{"//gopgext:type input=In output=Out\ntype T = int32", "not an alias"},
		{"//gopgext:type input=In output=Out storage=external\ntype T int32", "needs a variable-size"},
		{"//gopgext:type input=In output=Out bogus\ntype T int32", `unknown option "bogus"`},
		{"//gopgext:type input=In output=Out\ntype T int32", "input=In: no //gopgext:function In"},
		{"//gopgext:type input=In output=Out\ntype T int32\n//gopgext:function manual\nfunc In(s pg.CString) T { return 0 }\n//gopgext:function\nfunc Out(T) pg.CString { return \"\" }",
			"can't be manual"},
		{"//gopgext:type\ntype C struct{ A int32 }\n//gopgext:function\nfunc F(cs []C) {}", "arrays of composite types"},
		{"//gopgext:type bogus\ntype C struct{ A int32 }", "a composite type takes only name="},
		{"//gopgext:type\ntype C struct{ A pg.AnyElement }", "can't be a column"},
		{"//gopgext:type\ntype C struct{ A int32 }\n//gopgext:type\ntype D struct{ C C }", "can't be a column"},
		{"//gopgext:function\nfunc F(xs []pg.AnyElement) {}", "use pg.AnyArray"},
		{"//gopgext:function\nfunc F(s agg.State) {}", "aggregate states are *agg.State"},
		{"type R struct{ A int32 }\n//gopgext:function\nfunc F() R { return R{} }", "at least 2 fields"},
		{"type R struct{ A, B map[int]int }\n//gopgext:function\nfunc F() R { return R{} }", "struct R: unsupported type"},
		{"type R struct{ A int32 `pg:\"x\"`; B int32 `pg:\"x\"` }\n//gopgext:function\nfunc F() R { return R{} }", "column x declared twice"},
		{"type R struct{ A, B int32 }\n//gopgext:function\nfunc F(r R) {}", "declare the struct with //gopgext:type"},
	} {
		src := "package main\n\nimport (\n\t\"gopgext/agg\"\n\t\"gopgext/pg\"\n)\n\nvar _ pg.Oid\nvar _ agg.State\n\n" + tc.src
		_, err := scanFile("main.go", []byte(src))
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%q: got error %v, want %q", tc.src, err, tc.want)
		}
	}
}

func TestStructInOtherFile(t *testing.T) {
	pkg, err := scanPackage(map[string][]byte{
		"a.go": []byte("package main\n\nimport \"gopgext/pg\"\n\n//gopgext:function\nfunc F() P { return P{} }\n\nvar _ pg.Oid\n"),
		"b.go": []byte("package main\n\nimport q \"gopgext/pg\"\n\ntype P struct {\n\tX q.Oid\n\tY q.Varlena\n}\n"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := pkg.Funcs[0].SQLParams(); got != `OUT "x" oid, OUT "y" bytea` {
		t.Errorf("SQLParams = %s", got)
	}
}

func TestAggState(t *testing.T) {
	pkg, err := scanFile("main.go", []byte(`package main

import a "gopgext/agg"

//gopgext:function
func Add(s *a.State, x *float64) *a.State { return s }
`))
	if err != nil {
		t.Fatal(err)
	}
	f := pkg.Funcs[0]
	if f.Signature() != "add(s internal, x double precision)" || f.Strict || f.Returns() != "internal" {
		t.Errorf("%s strict=%v returns %s", f.Signature(), f.Strict, f.Returns())
	}
	src, err := genGo(pkg)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`_gopgext_agg "gopgext/agg"`, "_gopgext_agg.Arg(_c, 0)", "_gopgext_agg.Return(_c, _r)"} {
		if !strings.Contains(string(src), want) {
			t.Errorf("generated code lacks %s:\n%s", want, src)
		}
	}
}

func TestTimeAndNumeric(t *testing.T) {
	pkg, err := scanFile("main.go", []byte(`package main

import (
	"iter"
	tm "time"

	"gopgext/pg"
)

type Row struct {
	At    tm.Time
	Total pg.Numeric
	Prev  *tm.Time
}

//gopgext:function
func Shift(t tm.Time, ts []tm.Time, n *pg.Numeric) (tm.Time, error) { return t, nil }

//gopgext:function
func Rows() iter.Seq[Row] { return nil }
`))
	if err != nil {
		t.Fatal(err)
	}
	f := pkg.Funcs[0]
	if f.Signature() != "shift(t timestamp with time zone, ts timestamp with time zone[], n numeric)" || f.Strict ||
		f.Returns() != "timestamp with time zone" {
		t.Errorf("%s strict=%v returns %s", f.Signature(), f.Strict, f.Returns())
	}
	if got := pkg.Funcs[1].Returns(); got != `TABLE ("at" timestamp with time zone, "total" numeric, "prev" timestamp with time zone)` {
		t.Errorf("Returns = %s", got)
	}
	src, err := genGo(pkg)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"_gopgext_fmgr.Arg[_gopgext_fmgr.Time](_c, 0", "_gopgext_fmgr.ArgPtr[_gopgext_fmgr.Numeric](_c, 2)",
		"_gopgext_fmgr.Return(_c, _r)"} {
		if !strings.Contains(string(src), want) {
			t.Errorf("generated code lacks %s:\n%s", want, src)
		}
	}
	if strings.Contains(string(src), "tm.") {
		t.Errorf("generated code uses the file's import name:\n%s", src)
	}
}

func TestTrigger(t *testing.T) {
	pkg, err := scanFile("main.go", []byte(`package main

import tg "gopgext/trigger"

// Stamp stamps.
//
//gopgext:function
func Stamp(t *tg.Data) (*tg.Row, error) { return t.New, nil }

//gopgext:function name=audit
func Audit(t *tg.Data) {}

//gopgext:function
func NoDrops(e *tg.Event) error { return nil }
`))
	if err != nil {
		t.Fatal(err)
	}
	for i, want := range []string{"stamp() trigger", "audit() trigger", "no_drops() event_trigger"} {
		f := pkg.Funcs[i]
		if got := f.Signature() + " " + f.Returns(); got != want || f.Strict || f.SQLParams() != "" {
			t.Errorf("got %s strict=%v, want %s", got, f.Strict, want)
		}
	}
	src, err := genGo(pkg)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`_gopgext_trigger "gopgext/trigger"`, "_t := _gopgext_trigger.Arg(_c)",
		"_r, _err := Stamp(_t)", "_gopgext_trigger.Return(_c, _t, _r)", "Audit(_t)\n\t_gopgext_trigger.ReturnDefault(_c, _t)",
		"_t := _gopgext_trigger.EventArg(_c)", "_err := NoDrops(_t)"} {
		if !strings.Contains(string(src), want) {
			t.Errorf("generated code lacks %q:\n%s", want, src)
		}
	}
	sql, err := genSQL("ext", "example.com/ext", pkg, nil, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"CREATE FUNCTION \"stamp\"()\nRETURNS trigger\n", "COMMENT ON FUNCTION \"stamp\"() IS 'Stamp stamps.';",
		"RETURNS event_trigger\nAS 'MODULE_PATHNAME', 'gopgext_fn_NoDrops'\nLANGUAGE C VOLATILE;"} {
		if !strings.Contains(string(sql), want) {
			t.Errorf("SQL lacks %q:\n%s", want, sql)
		}
	}
}

func TestTriggerErrors(t *testing.T) {
	for _, tc := range []struct{ src, want string }{
		{"//gopgext:function\nfunc F(t *trigger.Data, x int32) {}", "only parameter is a *trigger.Data"},
		{"//gopgext:function\nfunc F(t trigger.Data) {}", "only parameter is a *trigger.Data"},
		{"//gopgext:function\nfunc F() *trigger.Row { return nil }", "only parameter is a *trigger.Data"},
		{"//gopgext:function\nfunc F(t *trigger.Data) int32 { return 0 }", "returns a *trigger.Row"},
		{"//gopgext:function\nfunc F(e *trigger.Event) *trigger.Row { return nil }", "event trigger function returns nothing"},
		{"//gopgext:function strict\nfunc F(t *trigger.Data) {}", "trigger functions have no arguments"},
	} {
		src := "package main\n\nimport \"gopgext/trigger\"\n\n" + tc.src
		_, err := scanFile("main.go", []byte(src))
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%q: got error %v, want %q", tc.src, err, tc.want)
		}
	}
}

func TestRecordAndUUID(t *testing.T) {
	pkg, err := scanFile("main.go", []byte(`package main

import (
	"iter"

	"gopgext/pg"
)

//gopgext:function stable
func Pivot(sql string) (iter.Seq[pg.Record], error) { return nil, nil }

//gopgext:function
func One(u pg.UUID, us []pg.UUID, n *pg.UUID) pg.Record { return nil }

//gopgext:function
func Maybe() (*pg.Record, error) { return nil, nil }

//gopgext:function
func NewID() pg.UUID { return pg.UUID{} }
`))
	if err != nil {
		t.Fatal(err)
	}
	for i, want := range []string{"SETOF record", "record", "record", "uuid"} {
		if got := pkg.Funcs[i].Returns(); got != want {
			t.Errorf("%s returns %s, want %s", pkg.Funcs[i].GoName, got, want)
		}
	}
	if got := pkg.Funcs[1].Signature(); got != "one(u uuid, us uuid[], n uuid)" {
		t.Errorf("signature %s", got)
	}
	src, err := genGo(pkg)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"_gopgext_fmgr.ReturnRecordSet(_c, _r)", "_gopgext_fmgr.ReturnRecord(_c, _r)",
		"_gopgext_fmgr.ReturnRecordPtr(_c, _r)", "_gopgext_fmgr.Arg[_gopgext_fmgr.UUID](_c, 0, \"u\")",
		"_gopgext_fmgr.ArgArray[_gopgext_fmgr.UUID](_c, 1, \"us\")", "_gopgext_fmgr.Return(_c, _r)"} {
		if !strings.Contains(string(src), want) {
			t.Errorf("generated code lacks %s:\n%s", want, src)
		}
	}
	for _, bad := range []string{
		"//gopgext:function\nfunc F(r pg.Record) {}",
		"//gopgext:function\nfunc F() []pg.Record { return nil }",
		"type R struct {\n\tA int32\n\tB pg.Record\n}\n\n//gopgext:function\nfunc F() R { return R{} }",
	} {
		if _, err := scanFile("main.go", []byte("package main\n\nimport \"gopgext/pg\"\n\n"+bad)); err == nil {
			t.Errorf("accepted:\n%s", bad)
		}
	}
}

func TestFixedSizeTypeAndPoint(t *testing.T) {
	pkg, err := scanFile("main.go", []byte(`package main

import "gopgext/pg"

//gopgext:type input=SegIn output=SegOut
type Seg [12]byte

//gopgext:function
func SegIn(s pg.CString) (Seg, error) { return Seg{}, nil }

//gopgext:function
func SegOut(s Seg) pg.CString { return "" }

//gopgext:function
func Widen(s *Seg, p pg.Point, ps []pg.Point) *Seg { return s }
`))
	if err != nil {
		t.Fatal(err)
	}
	if got := pkg.Types[0].CreateType(); !strings.Contains(got, "INTERNALLENGTH = 12") || strings.Contains(got, "PASSEDBYVALUE") {
		t.Errorf("CreateType = %s", got)
	}
	if f := pkg.Funcs[2]; f.Signature() != `widen(s "seg", p point, ps point[])` || f.Returns() != `"seg"` {
		t.Errorf("%s returns %s", f.Signature(), f.Returns())
	}
	src, err := genGo(pkg)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`Seg(_gopgext_fmgr.ArgFixed[[12]byte](_c, 0, "s"))`, "(*Seg)(_gopgext_fmgr.ArgFixedPtr[[12]byte](_c, 0))",
		"_gopgext_fmgr.Arg[_gopgext_fmgr.Point](_c, 1", "_gopgext_fmgr.ReturnFixedPtr[[12]byte](_c, (*[12]byte)(_r))"} {
		if !strings.Contains(string(src), want) {
			t.Errorf("generated code lacks %s:\n%s", want, src)
		}
	}
	for _, tc := range []struct{ src, want string }{
		{"//function\nfunc F(xs []Seg) {}", "arrays of fixed-size types"},
		{"//function\nfunc F() iter.Seq[Seg] { return nil }", "sets of fixed-size types"},
		{"type R struct{ A, B Seg }\n//function\nfunc F() R { return R{} }", "can't be a column"},
	} {
		src := "package main\n\nimport (\n\t\"iter\"\n\t\"gopgext/pg\"\n)\n\nvar _ iter.Seq[int]\n\n//gopgext:type input=In output=Out\ntype Seg [12]byte\n\n" +
			"//gopgext:function\nfunc In(s pg.CString) Seg { return Seg{} }\n\n//gopgext:function\nfunc Out(s Seg) pg.CString { return \"\" }\n\n" +
			strings.Replace(tc.src, "//function", "//gopgext:function", 1)
		if _, err := scanFile("main.go", []byte(src)); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%q: got error %v, want %q", tc.src, err, tc.want)
		}
	}
}
