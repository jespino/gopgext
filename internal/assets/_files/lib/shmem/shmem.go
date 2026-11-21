// Package shmem shares state between the backends of a server: shared
// memory and locks, for libraries loaded by shared_preload_libraries
// (statistics like pg_stat_statements, caches, counters...).
//
// Objects are created while the library loads (in main or package-level
// variables), with names unique in the server:
//
//	var (
//		lock  = shmem.NewLock("myext")
//		stats = shmem.NewTable[Key, Counters]("myext stats", 1000)
//		total = shmem.NewVar[Totals]("myext totals")
//	)
//
//	func record(k Key, d float64) {
//		lock.Lock()
//		defer lock.Unlock()
//		if c, _ := stats.Insert(k); c != nil {
//			c.Calls++
//			c.Time += d
//		}
//	}
//
// Shared memory isn't the Go heap: the types of shared values (table keys
// and values, variables) can't contain pointers, strings, slices, maps or
// interfaces, only numbers, booleans and arrays or structs of them (a
// string can be kept as a fixed-size byte array, or in a Bytes area). The
// package checks this when objects are created. Values returned point into
// shared memory: other backends see changes, so access them only while
// holding a lock that protects them (shared for reading, exclusive for
// writing); tables and areas have no lock of their own.
//
// If the library isn't loaded by shared_preload_libraries, objects can be
// created but not used: Available reports false, and using them raises an
// error saying the library must be preloaded.
package shmem

import (
	"fmt"
	"iter"
	"reflect"
	"sync/atomic"
	"time"
	"unsafe"

	"gopgext/fmgr"
	"gopgext/internal/core"
)

// Available reports whether the library was loaded by
// shared_preload_libraries, so its shared memory exists.
func Available() bool { return attached() }

type object struct {
	i    int64 // index in shmem.c's objects; -1: not preloaded
	name string
}

func register(kind int, name string, size, keysize, entrysize uintptr, entries int) object {
	if !loading() {
		panic(fmt.Sprintf("shmem: %q must be created while the library loads (in main or a package-level variable)", name))
	}
	var id int64
	core.Check(registerC(int64(kind), core.CBytes(name), int64(size), int64(keysize), int64(entrysize), int64(entries), &id))
	return object{id, name}
}

func (o object) ptr() unsafe.Pointer {
	var p unsafe.Pointer
	if o.i >= 0 {
		p = ptrC(o.i)
	}
	if p == nil {
		panic(&core.Error{Code: "55000",
			Message: fmt.Sprintf("%s must be loaded via \"shared_preload_libraries\"", core.ExtensionName())})
	}
	return p
}

// ---- Locks ----------------------------------------------------------------

// Lock is a lightweight lock (LWLock) shared by all backends. Its name is
// the wait event backends show while waiting for it (pg_stat_activity).
// Locks are released automatically if the transaction fails, but hold them
// briefly: interrupts (query cancel) wait until they are released.
type Lock struct{ o object }

// NewLock creates a lock.
func NewLock(name string) *Lock { return &Lock{register(kindLock, name, 0, 0, 0, 0)} }

// Lock acquires the lock exclusively (to write), waiting if needed.
func (l *Lock) Lock() {
	l.o.ptr()
	core.Check(lockC(l.o.i, true))
}

// RLock acquires the lock shared (to read), waiting if needed.
func (l *Lock) RLock() {
	l.o.ptr()
	core.Check(lockC(l.o.i, false))
}

// Unlock releases the lock.
func (l *Lock) Unlock() { core.Check(unlockC(l.o.i)) }

// SpinLock is a spinlock to embed in shared values: it protects a few
// fields for a few instructions (e.g. counters updated while holding a
// Lock shared), never while calling PostgreSQL or allocating. The zero
// value is unlocked.
type SpinLock struct{ state uint32 }

// Lock acquires the spinlock, spinning (then sleeping briefly) while
// another backend holds it.
func (s *SpinLock) Lock() {
	for spins := 0; !atomic.CompareAndSwapUint32(&s.state, 0, 1); spins++ {
		if spins >= 100 {
			time.Sleep(time.Duration(min(spins-99, 1000)) * time.Microsecond)
		}
	}
}

// Unlock releases the spinlock.
func (s *SpinLock) Unlock() { atomic.StoreUint32(&s.state, 0) }

// ---- Variables and areas ----------------------------------------------------

// Var is a value of type T in shared memory, zero initially.
type Var[T any] struct{ o object }

// NewVar creates a shared variable.
func NewVar[T any](name string) *Var[T] {
	t := reflect.TypeOf((*T)(nil)).Elem()
	mustBePlain(t, name)
	return &Var[T]{register(kindBytes, name, max(t.Size(), 1), 0, 0, 0)}
}

// Ptr returns the variable.
func (v *Var[T]) Ptr() *T { return (*T)(v.o.ptr()) }

// Bytes is a fixed-size area of shared memory, zero initially: for
// variable-size data (strings...) the extension lays out itself.
type Bytes struct {
	o    object
	size int
}

// NewBytes creates an area of size bytes (aligned to 8 bytes).
func NewBytes(name string, size int) *Bytes {
	if size <= 0 {
		panic(fmt.Sprintf("shmem: the size of %q must be positive", name))
	}
	return &Bytes{register(kindBytes, name, uintptr(size), 0, 0, 0), size}
}

// Slice returns the area.
func (b *Bytes) Slice() []byte { return unsafe.Slice((*byte)(b.o.ptr()), b.size) }

// ---- Tables -------------------------------------------------------------------

// Table is a hash table in shared memory (a dynahash table) with at most
// a fixed number of entries, from keys K to values V. Keys are compared
// and hashed as bytes: K can't have padding between or after its fields
// (add explicit "_ [n]byte" fields), and floating-point keys compare by
// representation.
type Table[K comparable, V any] struct {
	o      object
	valOff uintptr
	max    int
}

// NewTable creates a table of up to size entries.
func NewTable[K comparable, V any](name string, size int) *Table[K, V] {
	kt := reflect.TypeOf((*K)(nil)).Elem()
	vt := reflect.TypeOf((*V)(nil)).Elem()
	mustBePlain(kt, name)
	mustBePlain(vt, name)
	if kt.Size() == 0 {
		panic(fmt.Sprintf("shmem: the key type of %q can't be empty", name))
	}
	if padded(kt) {
		panic(fmt.Sprintf("shmem: the key type of %q (%s) has padding: add explicit _ fields", name, kt))
	}
	if size <= 0 {
		panic(fmt.Sprintf("shmem: the size of %q must be positive", name))
	}
	valOff := (kt.Size() + 7) &^ 7
	return &Table[K, V]{register(kindTable, name, 0, kt.Size(), valOff+max(vt.Size(), 1), size), valOff, size}
}

func (t *Table[K, V]) value(e unsafe.Pointer) *V { return (*V)(unsafe.Add(e, t.valOff)) }

func (t *Table[K, V]) search(k *K, action int64) (unsafe.Pointer, bool) {
	t.o.ptr()
	var found bool
	e := hashSearch(t.o.i, unsafe.Pointer(k), action, &found)
	return e, found
}

// Get returns the value of k, or nil.
func (t *Table[K, V]) Get(k K) *V {
	if e, _ := t.search(&k, 0); e != nil {
		return t.value(e)
	}
	return nil
}

// Insert returns the value of k, adding a zero value if k wasn't in the
// table (inserted). It returns nil if the table is full.
func (t *Table[K, V]) Insert(k K) (v *V, inserted bool) {
	if e, _ := t.search(&k, 0); e != nil {
		return t.value(e), false
	}
	if t.Len() >= t.max {
		return nil, false
	}
	e, found := t.search(&k, 1)
	if e == nil {
		return nil, false
	}
	v = t.value(e)
	if !found {
		var zero V
		*v = zero
	}
	return v, !found
}

// Delete removes k, reporting whether it was in the table.
func (t *Table[K, V]) Delete(k K) bool {
	e, _ := t.search(&k, 2)
	return e != nil
}

// Len returns the number of entries.
func (t *Table[K, V]) Len() int {
	t.o.ptr()
	return int(hashCount(t.o.i))
}

// Cap returns the maximum number of entries.
func (t *Table[K, V]) Cap() int { return t.max }

// All returns the entries, in no particular order: those in the table
// when called, which can be modified (not deleted) while iterating.
func (t *Table[K, V]) All() iter.Seq2[K, *V] {
	t.o.ptr()
	var p *unsafe.Pointer
	var n int64
	core.Check(hashEntries(t.o.i, &p, &n))
	var entries []unsafe.Pointer
	if n > 0 {
		entries = unsafe.Slice(p, n)
	}
	return func(yield func(K, *V) bool) {
		for _, e := range entries {
			if !yield(*(*K)(e), t.value(e)) {
				return
			}
		}
	}
}

// ---- Startup and shutdown ------------------------------------------------------

var startups, shutdowns []func()

// OnStartup registers a function the postmaster runs when it creates the
// shared memory (at server start, and again after a crash of a backend
// resets it), e.g. to load state saved by OnShutdown. Objects are usable;
// it runs without transaction or locks, and its errors are logged.
func OnStartup(f func()) { startups = append(startups, f) }

// OnShutdown registers a function the postmaster runs when the server
// shuts down cleanly (not after a crash), after the backends have exited,
// e.g. to save state to a file (pg.WriteFile). Errors are logged.
func OnShutdown(f func()) { shutdowns = append(shutdowns, f) }

//export gopgext_go_shmem_startup
func startup(c *fmgr.Call) {
	defer c.End(c.Begin(false))
	for _, f := range startups {
		f()
	}
}

//export gopgext_go_shmem_shutdown
func shutdown(c *fmgr.Call) {
	defer c.End(c.Begin(false))
	for _, f := range shutdowns {
		f()
	}
}

// ---- Type checks -----------------------------------------------------------------

// mustBePlain panics unless t's values can live in shared memory.
func mustBePlain(t reflect.Type, name string) {
	if bad := notPlain(t); bad != "" {
		panic(fmt.Sprintf("shmem: %q can't hold %s values: %s isn't allowed in shared memory (no pointers)", name, t, bad))
	}
}

func notPlain(t reflect.Type) string {
	switch t.Kind() {
	case reflect.Bool, reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64, reflect.Complex64, reflect.Complex128:
		return ""
	case reflect.Array:
		return notPlain(t.Elem())
	case reflect.Struct:
		for i := 0; i < t.NumField(); i++ {
			if bad := notPlain(t.Field(i).Type); bad != "" {
				return bad
			}
		}
		return ""
	}
	return t.String()
}

// padded reports whether t has bytes that aren't part of a field.
func padded(t reflect.Type) bool {
	switch t.Kind() {
	case reflect.Array:
		return padded(t.Elem())
	case reflect.Struct:
		end := uintptr(0)
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			if f.Offset != end || padded(f.Type) {
				return true
			}
			end = f.Offset + f.Type.Size()
		}
		return end != t.Size()
	}
	return false
}

// ---- C side (frameworks/shmem/shmem.c) ---------------------------------------------

const (
	kindLock = iota
	kindBytes
	kindTable
)

//export gopgext_loading
func loading() bool

//export gopgext_shmem_register
func registerC(kind int64, name *byte, size, keysize, entrysize, max int64, id *int64) uintptr

//export gopgext_shmem_attached
func attached() bool

//export gopgext_shmem_ptr
func ptrC(i int64) unsafe.Pointer

//export gopgext_shmem_lock
func lockC(i int64, exclusive bool) uintptr

//export gopgext_shmem_unlock
func unlockC(i int64) uintptr

//export gopgext_shmem_hash_search
func hashSearch(i int64, key unsafe.Pointer, action int64, found *bool) unsafe.Pointer

//export gopgext_shmem_hash_count
func hashCount(i int64) int64

//export gopgext_shmem_hash_entries
func hashEntries(i int64, out **unsafe.Pointer, n *int64) uintptr
