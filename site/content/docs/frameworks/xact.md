---
title: "gopgext/xact"
linkTitle: "xact"
description: "Work done just before transactions commit, transaction identifiers and savepoint outcomes."
weight: 19
---

API reference: [gopgext/xact](/api/xact/).

`xact` ties Go state to PostgreSQL transactions. It provides:

- callbacks that run just before a transaction commits, where an error
  still aborts it;
- an identifier that changes with each transaction;
- the outcome of savepoints, so that state kept for a transaction follows
  `ROLLBACK TO SAVEPOINT`, PL/pgSQL exception blocks and failed queries.

```go
type item struct {
	sp   xact.Savepoint
	data string
}

var (
	queue     []item
	queueXact uint64
)

var flush = xact.BeforeCommit(func() error {
	var live []item
	for _, it := range queue {
		if !xact.RolledBack(it.sp) {
			live = append(live, it)
		}
	}
	queue = nil
	return publish(live) // an error aborts the COMMIT
})

func enqueue(data string) {
	if queueXact != xact.ID() { // left over from a transaction that aborted
		queue, queueXact = nil, xact.ID()
	}
	queue = append(queue, item{xact.Current(), data})
	flush.Schedule()
}
```

| API | |
|---|---|
| `BeforeCommit(f) *Callback` | a callback, created once (at most 64 per extension) |
| `Callback.Schedule()` | run it before the current transaction commits (once, however many times scheduled; cheap) |
| `ID() uint64` | the current transaction among this backend's: changes whenever one ends |
| `Current() Savepoint` | the current (sub)transaction, for `RolledBack` |
| `RolledBack(s) bool` | whether savepoint `s` was rolled back (itself or one it was released into) |
| `InTransaction() bool` | whether a transaction is in progress |

Callbacks run in the transaction, like statements before `COMMIT`. They
can run SQL, and an error or panic aborts the transaction, which `COMMIT`
reports. They also run before `PREPARE TRANSACTION`. A callback can
schedule callbacks, including itself, which then run too.

Nothing runs when a transaction aborts. State kept for one is recognized
by its `ID` instead. Only the ends of subtransactions are logged (in C,
once `Current` was called in the transaction), and Go reads that log
lazily.
