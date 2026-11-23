---
title: "gopgext/bgworker"
linkTitle: "bgworker"
description: "Background worker processes running Go."
weight: 17
---

API reference: [gopgext/bgworker](/api/bgworker/).

Background workers are server processes started by the postmaster,
independent of client sessions: for periodic jobs, queues, or writing out
state kept in shared memory. Workers are registered while the library loads
through `shared_preload_libraries`, and their `Main` runs in its own process:

```go
var worker *bgworker.Handle

func main() {
	if !pg.Preloading() {
		return
	}
	worker = bgworker.Register(bgworker.Worker{
		Name:     "myext cleaner",
		Database: "app",
		Main: func(w *bgworker.Context) error {
			for w.Wait(time.Minute) { // false once the server shuts down
				err := w.Transaction(func() error {
					_, err := pg.Exec("DELETE FROM sessions WHERE expires < now()")
					return err
				})
				if err != nil {
					pg.Warning(err.Error())
				}
			}
			return nil
		},
	})
}

//gopgext:function
func CleanNow() { worker.Wake() } // Wait returns at once
```

| API                                       | Purpose                                                    |
|-------------------------------------------|------------------------------------------------------------|
| `Register(Worker) *Handle`                | a static worker (`Name`, `Type`, `Database`, `User`, `Start`, `Restart`, `NoRestart`, `Main`) |
| `Handle.Wake()`, `PID()`                  | set the worker's latch (from any backend); its process ID (0: not running) |
| `Context.Wait(d) bool`                    | wait for a wake-up or `d` (< 0: none); false when the worker must stop |
| `Context.Transaction(f, activity...)`     | run `f` in a transaction: commits if it returns nil, aborts on errors and panics |
| `Context.Stopping()`                      | was the worker asked to stop?                              |

The forked worker inherits the Go heap of the postmaster, so `Main` can use
what `main` set up (settings, shared memory, handles). SQL works only inside
`Transaction` (outside one, queries return an error); each statement still
runs in its own subtransaction. `SIGTERM` (server shutdown,
`pg_terminate_backend`) makes `Wait` return false: `Main` can finish its work
(e.g. write out buffered state) and return. `SIGHUP` reloads the
configuration during `Wait`, which also processes interrupts (procsignal
barriers, which `DROP DATABASE` waits for, ...) and so may return early. Returning nil ends the worker until the server
restarts; returning an error (or a panic) logs it and the postmaster starts
the worker again after `Restart` (default 10 s). If the postmaster dies, the
worker exits. C memory used outside transactions lasts until the next
`Wait`. `examples/ticker` is a small example.
