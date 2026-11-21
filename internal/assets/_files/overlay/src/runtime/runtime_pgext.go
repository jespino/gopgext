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
// demand. Because Go runs on the backend's stack, the collector scans the
// stack from the current position up to the frame of the outermost entry
// from C (stackTop, set by gopgext_enter).

package runtime

import (
	"internal/task"
	"unsafe"
)

// Zero-sized allocations return this address: in the first page, which is
// never mapped in a Linux process.
const zeroSizeAllocPtr uintptr = 16

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

//export gopgext_strong_random
func pgextStrongRandom(buf unsafe.Pointer, n uintptr) bool

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

// hardwareRand seeds the runtime's random numbers (map hash seeds,
// math/rand/v2's global source...) from PostgreSQL's strong random source.
func hardwareRand() (n uint64, ok bool) {
	ok = pgextStrongRandom(unsafe.Pointer(&n), 8)
	return n, ok
}

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
	// time.Now: wall clock = monotonic + this offset.
	timeOffset.Store(pgextRealtimeNs() - nanotime())
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
