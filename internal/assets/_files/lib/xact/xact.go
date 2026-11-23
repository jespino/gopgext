// Package xact ties Go state to PostgreSQL transactions: work to do just
// before the current transaction commits (where errors still abort it),
// an identifier that changes with each transaction, and the fate of
// savepoints (subtransactions), so that state kept for a transaction
// follows ROLLBACK TO SAVEPOINT and exception blocks.
//
// A typical use queues work while statements run and applies it once the
// transaction commits:
//
//	var queue []item
//	var queueXact uint64
//
//	var flush = xact.BeforeCommit(func() error {
//		var live []item
//		for _, it := range queue {
//			if !xact.RolledBack(it.sp) {
//				live = append(live, it)
//			}
//		}
//		queue = nil
//		return publish(live)
//	})
//
//	func enqueue(it item) {
//		if queueXact != xact.ID() { // left over from a transaction that aborted
//			queue, queueXact = nil, xact.ID()
//		}
//		it.sp = xact.Current()
//		queue = append(queue, it)
//		flush.Schedule()
//	}
//
// Callbacks run in the transaction, like the statements before COMMIT:
// they can run SQL (gopgext/pg), and an error (or panic) aborts the
// transaction, which COMMIT then reports. They also run before PREPARE
// TRANSACTION. Nothing runs when a transaction aborts: state for it is
// recognized as stale by its ID.
package xact

import (
	"gopgext/fmgr"
)

// ID identifies the current transaction among those of this backend: it
// changes whenever one ends (commits, aborts or is prepared), so state
// tagged with the ID of an earlier transaction is left over.
func ID() uint64 {
	install()
	return id()
}

// Savepoint identifies a subtransaction of the current transaction:
// ROLLBACK TO SAVEPOINT, exception blocks of PL/pgSQL functions and
// failed SPI queries roll subtransactions back.
type Savepoint uint32

// Current returns the current (sub)transaction, whose fate RolledBack
// then tells. It must be called in a transaction.
func Current() Savepoint {
	install()
	return Savepoint(current())
}

// Cached subtransaction log, while neither the transaction nor the log
// changed.
var (
	logID      uint64
	logLen     int64
	logParent  map[uint32]uint32 // released into their parent
	logAborted map[uint32]bool
)

// RolledBack reports whether savepoint s (from Current, in the current
// transaction) was rolled back, itself or one of the savepoints it was
// released into. Savepoints of earlier transactions are meaningless: compare
// IDs first.
func RolledBack(s Savepoint) bool {
	install()
	n := events()
	if n == 0 {
		return false
	}
	if logID != id() || logLen != n {
		logID, logLen = id(), n
		logParent, logAborted = map[uint32]uint32{}, map[uint32]bool{}
		for i := int64(0); i < n; i++ {
			var sub, parent uint32
			var aborted bool
			event(i, &sub, &parent, &aborted)
			if aborted {
				logAborted[sub] = true
			} else {
				logParent[sub] = parent
			}
		}
	}
	for sub := uint32(s); ; {
		if logAborted[sub] {
			return true
		}
		p, released := logParent[sub]
		if !released {
			return false
		}
		sub = p
	}
}

// InTransaction reports whether a transaction is in progress (and not
// aborted).
func InTransaction() bool { return inProgress() }

// Callback is a function to run before transactions commit, when
// scheduled.
type Callback struct {
	f     func() error
	index int
}

var callbacks []*Callback

// BeforeCommit returns a callback running f before the transactions it's
// scheduled for commit. Create callbacks once (e.g. in a package variable
// or in main), at most 64 per extension.
func BeforeCommit(f func() error) *Callback {
	if len(callbacks) == 64 {
		panic("xact: too many callbacks")
	}
	install()
	c := &Callback{f: f, index: len(callbacks)}
	callbacks = append(callbacks, c)
	return c
}

// Schedule makes the callback run before the current transaction commits,
// once however many times it's scheduled (until it runs: a callback can
// schedule itself again, it then runs again). Savepoints rolled back don't
// unschedule it. It's cheap: schedule it whenever there's work for it.
func (c *Callback) Schedule() { schedule(int64(c.index)) }

// ---- C side (frameworks/xact/xact.c) ----------------------------------------

var installed bool

func install() {
	if !installed {
		installed = true
		cinstall()
	}
}

//export gopgext_xact_install
func cinstall()

//export gopgext_xact_schedule
func schedule(index int64)

//export gopgext_xact_id
func id() uint64

//export gopgext_xact_current
func current() uint32

//export gopgext_xact_events
func events() int64

//export gopgext_xact_event
func event(i int64, sub, parent *uint32, aborted *bool)

//export gopgext_xact_in_progress
func inProgress() bool

//export gopgext_go_xact_precommit
func precommit(c *fmgr.Call) {
	defer c.End(c.Begin(false))
	d, _ := c.Datum(0)
	for _, cb := range callbacks {
		if d&(1<<uint(cb.index)) == 0 {
			continue
		}
		if err := cb.f(); err != nil {
			c.Fail(err)
			return
		}
	}
}
