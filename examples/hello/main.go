// hello is a PostgreSQL extension written in Go, built with gopgext.
//
// Functions marked with //gopgext:function become SQL functions. main runs
// once per backend, when PostgreSQL loads the extension's library.
//
//	gopgext install && psql -c 'CREATE EXTENSION hello' -c "SELECT hello('world')"
package main

import (
	"strings"

	"gopgext/pg"
)

// Hello returns a greeting for name.
//
//gopgext:function immutable parallel=safe
func Hello(name string) string {
	return "Hello, " + name + "!"
}

// Shout upper-cases s. An empty string raises an error.
//
//gopgext:function immutable parallel=safe
func Shout(s string) (string, error) {
	if s == "" {
		return "", pg.Errorf(pg.CodeInvalidParameterValue, "nothing to shout")
	}
	return strings.ToUpper(s) + "!", nil
}

func main() {}
