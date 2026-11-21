---
title: "gopgext/shmem"
linkTitle: "shmem"
description: "Share state between backends through shared memory."
weight: 12
---

API reference: [gopgext/shmem](/api/shmem/).

Shared memory and locks for libraries in `shared_preload_libraries`:

```go
type Key struct {
	DB    pg.Oid
	_     [4]byte // keys can't have implicit padding
	Query int64
}
type Counters struct {
	Mutex shmem.SpinLock // for updates under a shared Lock
	Calls int64
	Time  float64
}

var (
	lock  *shmem.Lock
	stats *shmem.Table[Key, Counters]
)

func main() {
	if !pg.Preloading() {
		return // functions using them will say the library must be preloaded
	}
	lock = shmem.NewLock("myext")
	stats = shmem.NewTable[Key, Counters]("myext stats", maxSetting.Get())
	shmem.OnStartup(load)  // postmaster, new memory: e.g. pg.ReadFile
	shmem.OnShutdown(save) // postmaster, clean shutdown: e.g. pg.WriteFile
}
```

| API                                     | Purpose                                                    |
|-----------------------------------------|------------------------------------------------------------|
| `NewLock(name)`: `Lock`, `RLock`, `Unlock` | an LWLock (its name is the wait event); released on errors |
| `NewTable[K, V](name, size)`: `Get`, `Insert`, `Delete`, `All`, `Len` | a shared hash table (dynahash) of at most `size` entries |
| `NewVar[T](name)`: `Ptr`                | a shared value                                             |
| `NewBytes(name, size)`: `Slice`         | a shared byte area                                         |
| `SpinLock`                              | a spinlock to embed in shared values                       |
| `OnStartup`, `OnShutdown`, `Available`  | lifecycle; is the shared memory there?                     |

Shared memory isn't the Go heap: keys, values and variables are checked to
hold only numbers, booleans and arrays/structs of them (no pointers,
strings or slices). Pointers returned point into shared memory, so access
them under a lock. Objects are created while loading in the postmaster
(their space is requested before the server allocates shared memory); if
the library isn't preloaded, using them raises "`<ext>` must be loaded via
shared_preload_libraries".
