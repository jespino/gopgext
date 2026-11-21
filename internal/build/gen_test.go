package build

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"testing"
)

var update = flag.Bool("update", false, "rewrite the golden files in testdata")

// TestGenGolden checks the generated Go and SQL against testdata/*.golden;
// go test -run TestGenGolden -update rewrites them.
func TestGenGolden(t *testing.T) {
	pkg, err := scanFile("main.go", []byte(`package main

import (
	"iter"

	"gopgext/pg"
)

// Add adds.
//
//gopgext:function immutable parallel=safe
func Add(a, b int32) int32 { return a + b }

// Lookup's doc is quoted.
//
//gopgext:function name=lookup_item stable cost=5 leakproof security_definer
func Lookup(id *int64, tags []string) (*string, error) { return nil, nil }

//gopgext:function
func Codes(s string, xs *[]int16, ys []*float64) *[]string { return nil }

//gopgext:function
func Maybe(b []byte) []*bool { return nil }

//gopgext:function
func Ints(xs []int) []int { return nil }

//gopgext:function rows=10 parallel=restricted
func Gen(n int) iter.Seq[float64] { return nil }

//gopgext:function strict
func Nothing(_ bool, order *float32) {}

//gopgext:function
func Check() error { return nil }

//gopgext:function symbol=thing_in manual immutable
func ThingIn(s pg.CString) (int64, error) { return 0, nil }

//gopgext:function symbol=my_echo
func Echo(s *pg.CString) pg.CString { return "" }
`))
	if err != nil {
		t.Fatal(err)
	}
	sql, err := genSQL("ext", "example.com/ext", pkg, [][]byte{[]byte("-- framework")}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	goSrc, err := genGo(pkg)
	if err != nil {
		t.Fatal(err)
	}
	for name, got := range map[string][]byte{
		"gen.go.golden":      goSrc,
		"gen.sql.golden":     sql,
		"gen.c.golden":       genC(pkg.Funcs),
		"exports.map.golden": genExports(pkg.Funcs, []string{"my_handler"}),
		"control.golden":     genControl("ext", "1.0", "It's an extension", true, nil),
	} {
		path := filepath.Join("testdata", name)
		if *update {
			if err := os.MkdirAll("testdata", 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, got, 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		want, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%s differs:\n got:\n%s\nwant:\n%s", name, got, want)
		}
	}
}
