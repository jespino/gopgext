package build

import (
	"strings"
	"testing"
)

func TestNullableSetOf(t *testing.T) {
	pkg, err := scanFile("main.go", []byte(`package main

import "iter"

//gopgext:function immutable rows=5
func Vals(s string) iter.Seq[*string] { return nil }
`))
	if err != nil {
		t.Fatal(err)
	}
	f := pkg.Funcs[0]
	if f.Result.Kind != NullableSetOf || f.Returns() != "SETOF text" || f.Rows != "5" {
		t.Errorf("kind %v returns %s rows %s", f.Result.Kind, f.Returns(), f.Rows)
	}
	src, err := genGo(pkg)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(src), "_gopgext_fmgr.ReturnSetNullable(_c, _r)") {
		t.Errorf("generated code:\n%s", src)
	}
	if _, err := scanFile("main.go", []byte(`package main

import "iter"

//gopgext:function
func Bad() iter.Seq[*[]string] { return nil }
`)); err == nil {
		t.Error("iter.Seq[*[]string] accepted")
	}
}
