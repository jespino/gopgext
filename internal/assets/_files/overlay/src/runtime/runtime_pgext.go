//go:build pgext

// Runtime platform support for TinyGo code running inside a PostgreSQL
// backend, as part of an extension's shared library. gopgext adds this file
// to its mirror of TinyGo's src/runtime.
//
// The C half lives in csrc/runtime.c: every function below declared with
// //export and no body is implemented there.
//
// A backend is a single-threaded process, so there is no scheduler
// (goroutines aren't available): Go code runs on the backend's own stack,
// only while PostgreSQL is calling into the extension. Memory is TinyGo's
// conservative collector over a heap reserved with mmap that grows on
// demand. The target is hosted Linux (GOOS=linux without the baremetal tag),
// so syscall and os use the backend's libc for files, environment, etc.
// Because Go runs on the backend's stack, the collector scans the
// stack from the current position up to the frame of the outermost entry
// from C (stackTop, set by gopgext_enter).

package runtime

import (
	"internal/task"
	"unsafe"
)

// Implemented in csrc/runtime.c.
//
//export gopgext_print
func pgextPrint(buf *byte, n int)

//export gopgext_fatal
func pgextFatal()

//export gopgext_monotonic_ns
func pgextMonotonicNs() int64

//export gopgext_realtime_ns
func pgextRealtimeNs() int64

//export gopgext_sleep_us
func pgextSleepUs(us int64)

//export gopgext_heap_reserve
func pgextHeapReserve(size uintptr) uintptr

// putchar is used by the runtime's println and panic output, and by
// os.Stdout/os.Stderr. Bytes are collected into a line buffer and sent to the
// server log one line at a time.
var lineBuf [512]byte
var lineLen int

func putchar(c byte) {
	if c == '\n' {
		flushLine()
		return
	}
	lineBuf[lineLen] = c
	lineLen++
	if lineLen == len(lineBuf) {
		flushLine()
	}
}

func flushLine() {
	pgextPrint(&lineBuf[0], lineLen)
	lineLen = 0
}

func getchar() byte { return 0 }

func buffered() int { return 0 }

// abort is called on a fatal runtime error (heap exhausted, unrecoverable
// panic). The message has been printed to the server log; the backend exits
// with FATAL (the session ends, other backends are unaffected).
func abort() {
	if lineLen > 0 {
		flushLine()
	}
	pgextFatal()
}

func exit(code int) {
	abort()
}

//go:linkname procPin sync/atomic.runtime_procPin
func procPin() {}

//go:linkname procUnpin sync/atomic.runtime_procUnpin
func procUnpin() {}

// Time: ticks are nanoseconds of CLOCK_MONOTONIC.
func ticks() timeUnit { return timeUnit(pgextMonotonicNs()) }

func nanosecondsToTicks(ns int64) timeUnit { return timeUnit(ns) }

func ticksToNanoseconds(t timeUnit) int64 { return int64(t) }

func sleepTicks(d timeUnit) {
	us := int64(d) / 1000
	if us <= 0 {
		us = 1
	}
	pgextSleepUs(us)
}

//go:linkname now time.now
func now() (sec int64, nsec int32, mono int64) {
	ns := pgextRealtimeNs()
	sec = ns / 1e9
	nsec = int32(ns - sec*1e9)
	mono = nanotime()
	return
}

//go:linkname syscall_Exit syscall.Exit
func syscall_Exit(code int) {
	exit(code)
}

// os.Args is empty: the backend's command line isn't the extension's.
//
//go:linkname os_runtime_args os.runtime_args
func os_runtime_args() []string { return nil }

//go:extern environ
var environ *unsafe.Pointer

// syscall_runtime_envs gives os.Getenv and friends the backend's environment
// (the postmaster's, inherited at fork).
//
//go:linkname syscall_runtime_envs syscall.runtime_envs
func syscall_runtime_envs() []string {
	n := 0
	for env := environ; *env != nil; env = (*unsafe.Pointer)(unsafe.Add(unsafe.Pointer(env), unsafe.Sizeof(environ))) {
		n++
	}
	envs := make([]string, 0, n)
	for env := environ; *env != nil; env = (*unsafe.Pointer)(unsafe.Add(unsafe.Pointer(env), unsafe.Sizeof(environ))) {
		s := _string{ptr: (*byte)(*env), length: strlen(*env)}
		envs = append(envs, *(*string)(unsafe.Pointer(&s)))
	}
	return envs
}

// Signals belong to PostgreSQL (SIGTERM, SIGINT, SIGUSR1... drive the
// backend), so os/signal can't take them over: these are TinyGo's stubs for
// platforms without signals.

//go:linkname signal_disable os/signal.signal_disable
func signal_disable(uint32) {}

//go:linkname signal_enable os/signal.signal_enable
func signal_enable(uint32) {}

//go:linkname signal_ignore os/signal.signal_ignore
func signal_ignore(uint32) {}

//go:linkname signal_waitUntilIdle os/signal.signalWaitUntilIdle
func signal_waitUntilIdle() {}

//go:linkname signal_recv os/signal.signal_recv
func signal_recv() uint32 {
	deadlock()
	return 0
}

// The heap: a range of address space reserved at load (heap_max bytes, only
// touched pages use memory), of which the first heapSize bytes are in use.
var (
	heapStart, heapEnd uintptr
	heapSize           uintptr
	heapMaxSize        uintptr
)

// Highest address of the Go-relevant part of the stack: the frame of the
// outermost call from C into Go.
var stackTop uintptr

func growHeap() bool {
	if heapSize == heapMaxSize {
		return false
	}
	heapSize = (heapSize * 3 / 2) &^ 4095 // grow by 50%
	if heapSize > heapMaxSize {
		heapSize = heapMaxSize
	}
	setHeapEnd(heapStart + heapSize)
	return true
}

// tinygo_pgext_init reserves and initializes the heap. Called once from
// _PG_init, before any Go code runs.
//
//export tinygo_pgext_init
func pgextInit(size, max uintptr) bool {
	start := pgextHeapReserve(max)
	if start == 0 {
		return false
	}
	heapStart = start
	heapSize = size
	heapMaxSize = max
	heapEnd = heapStart + heapSize
	initRand()
	initHeap()
	return true
}

// pgextRunMain runs package initializers and main.main. It's called (through
// gopgext/fmgr) by the generated _PG_init entry point, under its recover.
func pgextRunMain() {
	initAll()
	callMain()
}

// pgextEntry is the state saved by gopgext_enter (struct gopgext_entry in
// gopgext.h).
type pgextEntry struct {
	top        uintptr
	deferFrame unsafe.Pointer
}

var pgextDepth int

// tinygo_pgext_enter is called by C before every call into Go, with the
// address of the caller's frame. The outermost entry sets the top of the
// stack range the collector scans; nested entries (Go -> SPI -> Go) are
// within it.
//
//export tinygo_pgext_enter
func pgextEnter(sp uintptr, e *pgextEntry) {
	e.top = stackTop
	e.deferFrame = task.Current().DeferFrame
	if pgextDepth == 0 {
		stackTop = sp
	}
	pgextDepth++
}

// tinygo_pgext_leave undoes tinygo_pgext_enter. Restoring the defer frame
// chain keeps the runtime consistent even if a PostgreSQL error unwound Go
// frames (C helpers catch errors so that doesn't happen, but if one slipped
// through, Go's panic machinery must not point into dead frames).
//
//export tinygo_pgext_leave
func pgextLeave(e *pgextEntry) {
	pgextDepth--
	stackTop = e.top
	task.Current().DeferFrame = e.deferFrame
}
