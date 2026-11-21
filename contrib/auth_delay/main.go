// auth_delay, in Go: a port of PostgreSQL's contrib/auth_delay
// (PostgreSQL 18) on gopgext/auth. It pauses for auth_delay.milliseconds
// before reporting an authentication failure, to make brute-force attacks
// on passwords slower. It must be loaded by shared_preload_libraries.
package main

import (
	"math"
	"time"

	"gopgext/auth"
	"gopgext/pg"
)

var milliseconds = pg.NewIntSetting("milliseconds", 0, 0, math.MaxInt32/1000,
	"Milliseconds to delay before reporting authentication failure", pg.Sighup, pg.UnitMilliseconds)

func main() {
	auth.OnAuthenticate(func(c *auth.Connection) error {
		if c.Status != auth.StatusOK {
			time.Sleep(time.Duration(milliseconds.Get()) * time.Millisecond)
		}
		return nil
	})
}
