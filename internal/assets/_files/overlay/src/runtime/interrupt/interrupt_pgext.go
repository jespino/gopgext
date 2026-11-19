//go:build pgext

package interrupt

// Go code in a gopgext extension never runs in a signal handler (a backend
// is single-threaded and Go only runs while PostgreSQL calls into it), so
// there is nothing to disable.

// State is unused.
type State uintptr

// Disable does nothing.
func Disable() (state State) { return 0 }

// Restore does nothing.
func Restore(state State) {}

// In reports whether we're in an interrupt handler: never.
func In() bool { return false }
