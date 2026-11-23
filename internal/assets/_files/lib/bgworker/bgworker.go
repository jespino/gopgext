// Package bgworker runs Go code in background worker processes: server
// processes started by the postmaster, independent of client sessions,
// for periodic jobs, queues, flushing buffers kept in shared memory...
//
// Workers are registered while the library loads through
// shared_preload_libraries (in main), and start with the server:
//
//	var worker *bgworker.Handle
//
//	func main() {
//		if !pg.Preloading() {
//			return
//		}
//		worker = bgworker.Register(bgworker.Worker{
//			Name:     "myext cleaner",
//			Database: "app",
//			Main: func(w *bgworker.Context) error {
//				for w.Wait(time.Minute) { // false once the server shuts down
//					err := w.Transaction(func() error {
//						_, err := pg.Exec("DELETE FROM sessions WHERE expires < now()")
//						return err
//					})
//					if err != nil {
//						pg.Warning(err.Error())
//					}
//				}
//				return nil
//			},
//		})
//	}
//
// A worker's Main runs once per process start. Returning nil ends the
// worker for good (until the server restarts); returning an error (or
// panicking) logs it and exits with a failure: the postmaster starts the
// worker again after Worker.Restart. Other backends can wake a worker up
// (Handle.Wake) to make its Wait return early.
//
// SQL (gopgext/pg) runs inside Context.Transaction, which commits when its
// function returns nil and aborts otherwise; statements see a snapshot
// taken when the transaction starts. Outside a transaction there is no
// database access. C memory used outside transactions (and values
// pointing into it) lasts until the next Wait.
package bgworker

import (
	"fmt"
	"time"
	"unsafe"

	"gopgext/fmgr"
	"gopgext/internal/core"
)

// Start is when the postmaster starts a worker.
type Start int

const (
	// RecoveryFinished starts the worker once the server accepts
	// read-write connections (not on a standby). The default.
	RecoveryFinished Start = iota
	// ConsistentState starts it once the server accepts read-only
	// connections (on a standby too).
	ConsistentState
	// PostmasterStart starts it with the postmaster, before recovery: it
	// can't connect to a database.
	PostmasterStart
)

// Worker describes a background worker.
type Worker struct {
	// Name is the process's name (pg_stat_activity.backend_type shows
	// Type, and the process title Name), at most 95 bytes.
	Name string
	// Type groups workers of the same kind (default: Name).
	Type string
	// Database is the database the worker connects to; "" for none (no
	// SQL). User is the role it connects as ("": the bootstrap
	// superuser).
	Database, User string
	// Start is when the worker starts.
	Start Start
	// Restart is how long the postmaster waits before starting the worker
	// again after it failed (whole seconds; 0: 10 seconds). NoRestart
	// leaves it stopped instead.
	Restart   time.Duration
	NoRestart bool
	// Main is the worker's body.
	Main func(c *Context) error
}

// Handle refers to a registered worker, from any backend.
type Handle struct {
	index int
	name  string
}

var workers []*Worker

// Register registers a background worker. It must be called while the
// library is loaded by shared_preload_libraries (pg.Preloading()), in
// main: otherwise it panics.
func Register(w Worker) *Handle {
	if w.Main == nil {
		panic(fmt.Sprintf("bgworker: worker %q has no Main", w.Name))
	}
	if w.Name == "" {
		panic("bgworker: a worker needs a Name")
	}
	if w.Database != "" && w.Start == PostmasterStart {
		panic(fmt.Sprintf("bgworker: worker %q can't connect to a database if it starts with the postmaster", w.Name))
	}
	typ := w.Type
	if typ == "" {
		typ = w.Name
	}
	restart := int64(10)
	if w.NoRestart {
		restart = -1
	} else if w.Restart > 0 {
		restart = max(int64(w.Restart/time.Second), 1)
	}
	i := len(workers)
	core.Check(register(int64(i), core.CBytes(w.Name), core.CBytes(typ), int64(w.Start), restart, w.Database != ""))
	copied := w
	workers = append(workers, &copied)
	return &Handle{index: i, name: w.Name}
}

// Name returns the worker's name.
func (h *Handle) Name() string { return h.name }

// Wake sets the worker's latch: its current or next Wait returns. It does
// nothing if the worker isn't running. It can be called from any backend
// (and from the worker itself), and never blocks.
func (h *Handle) Wake() { wake(int64(h.index)) }

// PID returns the process ID of the running worker, or 0.
func (h *Handle) PID() int { return int(pid(int64(h.index))) }

// Context is the running worker, passed to its Main.
type Context struct {
	w     *Worker
	index int
	inTx  bool
}

// Worker returns the worker's description.
func (c *Context) Worker() *Worker { return c.w }

// Handle returns the worker's handle.
func (c *Context) Handle() *Handle { return &Handle{index: c.index, name: c.w.Name} }

// Wait waits until the worker is woken up (Handle.Wake) or d passes (d < 0:
// no timeout), processes interrupts (procsignal barriers...) and reloads
// the configuration if the server was asked to (SIGHUP). It may return
// early (interrupts set the latch too): callers check what's due. It returns false, at once, when the worker should stop (the
// server is shutting down): Main should then finish its work and return.
// If the postmaster dies, the worker exits.
func (c *Context) Wait(d time.Duration) bool {
	if c.inTx {
		panic("bgworker: Wait called inside a transaction")
	}
	ms := int64(-1)
	if d >= 0 {
		ms = int64((d + time.Millisecond - 1) / time.Millisecond)
	}
	ok := wait(ms)
	// Errors from C memory freed by the wait can't be re-thrown as is.
	core.Seq = core.NextSeq()
	return ok
}

// Stopping reports whether the worker was asked to stop.
func (c *Context) Stopping() bool { return shutdownRequested() }

// Transaction runs f in a transaction: SQL through gopgext/pg works
// inside it. The transaction commits if f returns nil, and aborts if it
// returns an error (returned by Transaction) or panics (the panic goes
// on). A failed commit returns its error. activity is what
// pg_stat_activity.query shows meanwhile (optional).
func (c *Context) Transaction(f func() error, activity ...string) (err error) {
	if c.inTx {
		panic("bgworker: nested Transaction")
	}
	if c.w.Database == "" {
		return &core.Error{Code: "55000", Message: fmt.Sprintf("background worker %q isn't connected to a database", c.w.Name)}
	}
	act := c.w.Name
	if len(activity) > 0 {
		act = activity[0]
	}
	core.Check(begin(core.CBytes(act)))
	c.inTx = true
	done := false
	defer func() {
		if done {
			return
		}
		// A panic: abort, and let it go on.
		c.inTx = false
		if e := end(false); e != nil {
			freeError(e)
		}
		core.Seq = core.NextSeq()
	}()
	ferr := f()
	done = true
	c.inTx = false
	e := end(ferr == nil)
	// The transaction's memory (and errors in it) is gone.
	core.Seq = core.NextSeq()
	if ferr != nil {
		return ferr
	}
	if e != nil {
		pe := core.FromErrorData(uintptr(e))
		freeError(e)
		return &core.Error{Code: pe.Code, Message: pe.Message, Detail: pe.Detail, Hint: pe.Hint, Context: pe.Context}
	}
	return nil
}

// ---- C side (frameworks/bgworker/bgworker.c) ----------------------------

//export gopgext_bgworker_register
func register(index int64, name, typ *byte, start, restart int64, database bool) uintptr

//export gopgext_bgworker_connect
func connect(database, user *byte) uintptr

//export gopgext_bgworker_wait
func wait(timeoutMs int64) bool

//export gopgext_bgworker_begin
func begin(activity *byte) uintptr

//export gopgext_bgworker_end
func end(commit bool) unsafe.Pointer

//export gopgext_bgworker_free_error
func freeError(edata unsafe.Pointer)

//export gopgext_bgworker_wake
func wake(index int64)

//export gopgext_bgworker_pid
func pid(index int64) int64

//export gopgext_bgworker_shutdown_requested
func shutdownRequested() bool

//export gopgext_go_bgworker_main
func workerMain(c *fmgr.Call) {
	defer c.End(c.Begin(false))
	d, _ := c.Datum(0)
	i := int(d)
	if i < 0 || i >= len(workers) {
		panic(fmt.Sprintf("bgworker: no worker %d registered", i))
	}
	w := workers[i]
	if w.Database != "" {
		var user *byte
		if w.User != "" {
			user = core.CBytes(w.User)
		}
		core.Check(connect(core.CBytes(w.Database), user))
	}
	ctx := &Context{w: w, index: i}
	if err := w.Main(ctx); err != nil {
		c.Fail(err)
	}
}
