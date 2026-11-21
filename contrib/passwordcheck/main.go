// passwordcheck, in Go: a port of PostgreSQL's contrib/passwordcheck
// (PostgreSQL 18) on gopgext/auth. It checks the passwords CREATE ROLE and
// ALTER ROLE set, and rejects weak ones: shorter than
// passwordcheck.min_password_length, containing the role's name, or
// without both letters and non-letters. An encrypted password can only be
// checked against the role's name. Building with cracklib isn't supported.
package main

import (
	"math"
	"strconv"
	"strings"

	"gopgext/auth"
	"gopgext/pg"
)

var minPasswordLength = pg.NewIntSetting("min_password_length", 8, 0, math.MaxInt32,
	"Minimum allowed password length.", pg.Superuser, pg.UnitBytes)

func main() {
	auth.OnPasswordCheck(check)
}

func check(p *auth.Password) error {
	if p.Type != auth.PasswordPlain {
		// We can't check encrypted passwords, only guess: is it the role's name?
		if p.Matches(p.Role) {
			return pg.Errorf(pg.CodeInvalidParameterValue, "password must not equal user name")
		}
		return nil
	}
	if min := minPasswordLength.Get(); len(p.Password) < min {
		err := pg.Errorf(pg.CodeInvalidParameterValue, "password is too short")
		err.Detail = "password must be at least \"passwordcheck.min_password_length\" (" +
			strconv.Itoa(min) + ") bytes long"
		return err
	}
	if strings.Contains(p.Password, p.Role) {
		return pg.Errorf(pg.CodeInvalidParameterValue, "password must not contain user name")
	}
	// Non-ASCII bytes count as non-letters, as isalpha() does in C.
	var letter, nonletter bool
	for i := 0; i < len(p.Password); i++ {
		c := p.Password[i] | 0x20
		if c >= 'a' && c <= 'z' {
			letter = true
		} else {
			nonletter = true
		}
	}
	if !letter || !nonletter {
		return pg.Errorf(pg.CodeInvalidParameterValue, "password must contain both letters and nonletters")
	}
	return nil
}
