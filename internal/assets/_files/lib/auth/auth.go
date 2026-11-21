// Package auth hooks into client authentication and password changes:
// to delay or reject connections (like auth_delay), or enforce password
// rules (like passwordcheck).
//
// Callbacks are registered while the library loads (in main):
//
//	func main() {
//		auth.OnPasswordCheck(func(p *auth.Password) error {
//			if p.Type == auth.PasswordPlain && len(p.Password) < 12 {
//				return pg.Errorf(pg.CodeInvalidParameterValue, "password is too short")
//			}
//			return nil
//		})
//	}
//
// OnAuthenticate needs the library in shared_preload_libraries (it runs
// before session_preload_libraries are loaded); OnPasswordCheck works once
// the library is loaded in the session (LOAD, session_preload_libraries...).
package auth

import (
	"math"
	"time"
	"unsafe"

	"gopgext/fmgr"
	"gopgext/internal/core"
)

// Status is the outcome of a client's authentication.
type Status int

const (
	// StatusOK is a successful authentication.
	StatusOK Status = iota
	// StatusError is a failed one (wrong password, no matching pg_hba.conf
	// line...).
	StatusError
	// StatusEOF is a client that disconnected while asked for a password
	// (as psql does before prompting for one).
	StatusEOF
)

// Connection is a client connection being authenticated.
type Connection struct {
	// User and Database are the role and database the client asked for;
	// Application is its application_name ("" if not given).
	User, Database, Application string
	// RemoteHost is the client's address ("[local]" for a Unix socket) and
	// RemotePort its port ("" for a Unix socket).
	RemoteHost, RemotePort string
	// Method is the authentication method of the pg_hba.conf line used
	// ("trust", "scram-sha-256", "md5", "password"...).
	Method string
	Status Status
}

// OnAuthenticate registers f, called after each client's authentication
// (ClientAuthentication_hook), successful or not. An error f returns
// rejects the connection (as a FATAL error). It runs before the backend
// connects to its database: there is no transaction, so f can't run SQL.
// Call it while the library loads, in shared_preload_libraries.
func OnAuthenticate(f func(c *Connection) error) {
	authHooks = append(authHooks, f)
	install(1)
}

// PasswordType is the form a password is given in.
type PasswordType int

const (
	// PasswordPlain is a plain text password.
	PasswordPlain PasswordType = iota
	// PasswordMD5 is an MD5-encrypted password ("md5...").
	PasswordMD5
	// PasswordSCRAM is a SCRAM-SHA-256 verifier ("SCRAM-SHA-256$...").
	PasswordSCRAM
)

// Password is a password set by CREATE ROLE or ALTER ROLE.
type Password struct {
	Role string
	// Password is as given: plain text or encrypted (see Type). Clients
	// like psql's \password encrypt it before sending it.
	Password string
	Type     PasswordType
	// ValidUntil is the VALID UNTIL time; nil if not given or infinity.
	ValidUntil *time.Time
}

// Matches reports whether plain is the password, plain or encrypted
// (PostgreSQL's plain_crypt_verify): to check an encrypted password
// against guesses.
func (p *Password) Matches(plain string) bool {
	if p.Type == PasswordPlain {
		return p.Password == plain
	}
	var ok bool
	core.Check(verify(core.CBytes(p.Role), core.CBytes(p.Password), core.CBytes(plain), &ok))
	return ok
}

// OnPasswordCheck registers f, called when CREATE ROLE or ALTER ROLE sets
// a password (check_password_hook). An error f returns rejects it. Call it
// while the library loads.
func OnPasswordCheck(f func(p *Password) error) {
	passwordHooks = append(passwordHooks, f)
	install(2)
}

var (
	authHooks     []func(*Connection) error
	passwordHooks []func(*Password) error
)

// ---- C side (frameworks/auth/auth.c) -----------------------------------

// cconnection is struct auth_connection.
type cconnection struct {
	user, database, application, remoteHost, remotePort, method *byte
	status                                                      int64
}

// cpassword is struct auth_password.
type cpassword struct {
	role, password *byte
	typ            int64
	validUntil     int64 // timestamptz
	hasValidUntil  int64
}

//export gopgext_auth_install
func install(hooks int64)

//export gopgext_auth_verify
func verify(role, shadow, plain *byte, ok *bool) uintptr

//export gopgext_go_auth_event
func event(c *fmgr.Call) {
	defer c.End(c.Begin(false))
	kind, _ := c.Datum(0)
	d, _ := c.Datum(1)
	p := unsafe.Pointer(uintptr(d))
	var err error
	if kind == 1 {
		cc := (*cconnection)(p)
		conn := &Connection{User: core.GoString(cc.user), Database: core.GoString(cc.database),
			Application: core.GoString(cc.application), RemoteHost: core.GoString(cc.remoteHost),
			RemotePort: core.GoString(cc.remotePort), Method: core.GoString(cc.method),
			Status: Status(cc.status)}
		for _, f := range authHooks {
			if err = f(conn); err != nil {
				break
			}
		}
	} else {
		cp := (*cpassword)(p)
		pw := &Password{Role: core.GoString(cp.role), Password: core.GoString(cp.password),
			Type: PasswordType(cp.typ)}
		if cp.hasValidUntil != 0 && cp.validUntil != math.MaxInt64 {
			var t time.Time // -infinity: the zero time, long past
			if cp.validUntil != math.MinInt64 {
				t = core.TimeFromDatum(uint64(cp.validUntil))
			}
			pw.ValidUntil = &t
		}
		for _, f := range passwordHooks {
			if err = f(pw); err != nil {
				break
			}
		}
	}
	if err != nil {
		c.Fail(err)
	}
}
