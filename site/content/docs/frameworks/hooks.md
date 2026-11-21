---
title: "gopgext/hooks"
linkTitle: "hooks"
description: "Observe and instrument the statements a backend runs."
weight: 10
---

API reference: [gopgext/hooks](/api/hooks/).

Observes the statements a backend processes through PostgreSQL's query
processing hooks (post parse analysis, planner, executor, utility): for
statistics, slow query logs, auditing. Decision callbacks choose what to
measure; result callbacks get the measurements:

```go
func main() {
	hooks.EnableQueryID() // compute_query_id = auto computes identifiers
	hooks.Register(hooks.Hooks{
		Parsed:   func(s *hooks.Statement) { ... s.Constants() ... }, // normalize the text
		Execute:  func(s *hooks.Statement) bool { return s.Nesting == 0 },
		Executed: func(s *hooks.Statement, u *hooks.Usage) { ... },  // u.Time, u.Rows, buffers, WAL, JIT
		Utility:  func(s *hooks.Statement) bool { return s.Command != "EXECUTE" },
		UtilityDone: func(s *hooks.Statement, u *hooks.Usage) { ... },
	})
}
```

A `Statement` has the query identifier, `Text()` (the statement, out of a
multi-statement string), its nesting level (0 for the client's statements;
functions, DO blocks, utility statements and planning add levels) and its
command tag. `Parsed` can change the query identifier, and gets the
statement's constants with their lengths (`Constants()`), to replace them
with `$n`. PostgreSQL code (planner, executor) only runs in C: Go is called
before and after. Load the library with `shared_preload_libraries` to see
every backend's statements. `contrib/pg_stat_statements` is built on it.

`Statement.ExecutorNesting` counts only the executions in progress, as
`auto_explain` does: statements of a `DO` block or the query of `CREATE
TABLE AS` are at executor level 0. `Execute` can ask for per-node
instrumentation of the execution, and `Executed` can then get its plan as
EXPLAIN prints it (`contrib/auto_explain` is built on this):

```go
hooks.Register(hooks.Hooks{
	Execute: func(s *hooks.Statement) bool {
		s.Instrument(hooks.InstrumentTimer | hooks.InstrumentBuffers) // or InstrumentRows, InstrumentWAL
		return s.ExecutorNesting == 0
	},
	Executed: func(s *hooks.Statement, u *hooks.Usage) {
		if u.Time > 100 {
			plan := s.Explain(hooks.ExplainOptions{Format: "json", Analyze: true, Buffers: true, QueryText: true})
			(&pg.Message{Level: pg.LevelLog, Text: plan, HideStatement: true}).Report()
		}
	},
})
```

`ExplainOptions` has EXPLAIN's `Format` (`text`, `xml`, `json`, `yaml`),
`Analyze`, `Timing`, `Buffers`, `WAL`, `Triggers` (these need the matching
instrumentation; without any, `Analyze` is ignored, e.g. under `EXPLAIN`
without `ANALYZE`), `Verbose`, `Settings`, `NoCosts`, `QueryText` and
`Parameters` (the values of `$n` parameters: 0 none, -1 in full, n bytes
each; PostgreSQL 16+). Instrumentation is requested before the executor
starts (the decision callback now runs before `ExecutorStart`).
