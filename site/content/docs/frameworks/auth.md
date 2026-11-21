---
title: "gopgext/auth"
linkTitle: "auth"
description: "Hook into client authentication and password checks."
weight: 11
---

API reference: [gopgext/auth](/api/auth/).

Hooks into client authentication (`ClientAuthentication_hook`) and password
changes (`check_password_hook`):

```go
func main() {
	auth.OnAuthenticate(func(c *auth.Connection) error {
		if c.Status != auth.StatusOK {
			time.Sleep(time.Second) // slow down password guessing
		}
		return nil // an error rejects the connection (FATAL)
	})
	auth.OnPasswordCheck(func(p *auth.Password) error {
		if p.Type == auth.PasswordPlain && len(p.Password) < 12 {
			return pg.Errorf(pg.CodeInvalidParameterValue, "password is too short")
		}
		if p.Type != auth.PasswordPlain && p.Matches(p.Role) { // encrypted: only guesses
			return pg.Errorf(pg.CodeInvalidParameterValue, "password must not equal user name")
		}
		return nil // an error rejects the password
	})
}
```

A `Connection` has the user, database, application name, remote host and
port, the pg_hba.conf method and the outcome (`StatusOK`, `StatusError`,
`StatusEOF`: the client hung up when asked for a password). It runs before
the backend connects to its database: no SQL. It needs the library in
`shared_preload_libraries`. A `Password` has the role, the password as
given (plain, or encrypted: `PasswordMD5`, `PasswordSCRAM`), and `ValidUntil`
(nil if none or infinity); `Matches(plain)` compares it with a plain text
guess. Password checks run once the library is loaded in the session
(`LOAD`, `session_preload_libraries`...). `contrib/auth_delay` and
`contrib/passwordcheck` are built on it.
