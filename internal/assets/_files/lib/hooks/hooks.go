// Package hooks observes the statements a backend processes, through
// PostgreSQL's query processing hooks (post parse analysis, planner,
// executor and utility hooks): to collect statistics like
// pg_stat_statements, log slow statements, audit...
//
// An extension registers callbacks while it loads (in main), usually from
// shared_preload_libraries so that they run in every backend:
//
//	func main() {
//		hooks.Register(hooks.Hooks{
//			Execute: func(s *hooks.Statement) bool { return s.Nesting == 0 },
//			Executed: func(s *hooks.Statement, u *hooks.Usage) {
//				if u.Time > 1000 {
//					pg.Log("slow statement: " + s.Text())
//				}
//			},
//		})
//	}
//
// Decision callbacks (Plan, Execute, Utility) choose the statements to
// measure; the matching result callbacks (Planned, Executed, UtilityDone)
// then get each one's resource usage. Parsed sees every statement after
// parse analysis, with its constants (to normalize the text), and can
// change its query identifier. Errors in callbacks (panics, pg errors)
// abort the statement. Callbacks run inside query processing: keep them
// fast, and avoid running SQL in them (its statements trigger the hooks
// again, as nested statements).
//
// The nesting level of a statement is 0 for a statement the client sent,
// and grows for statements run by functions, procedures, DO blocks,
// utility statements (CREATE TABLE AS, EXPLAIN ANALYZE...) or while
// planning. EXECUTE and PREPARE don't add a level: the prepared statement
// counts at the level of the EXECUTE. ExecutorNesting counts only the
// executions in progress (as auto_explain does): statements of a DO block or
// CREATE TABLE AS are at executor level 0, those of a function a query
// calls at level 1.
//
// Execute can ask for per-node instrumentation of the execution
// (Statement.Instrument), and Executed can then print its plan like
// EXPLAIN (ANALYZE) does (Statement.Explain):
//
//	Execute: func(s *hooks.Statement) bool {
//		s.Instrument(hooks.InstrumentTimer | hooks.InstrumentBuffers)
//		return true
//	},
//	Executed: func(s *hooks.Statement, u *hooks.Usage) {
//		pg.Log(s.Explain(hooks.ExplainOptions{Analyze: true, Buffers: true}))
//	},
package hooks

import (
	"unsafe"

	"gopgext/fmgr"
	"gopgext/internal/core"
)

// Hooks are callbacks for statement events; nil ones are skipped.
type Hooks struct {
	// Parsed is called after the parse analysis of each statement. It may
	// set s.QueryID (e.g. 0 to ignore the statement); Constants are only
	// available here.
	Parsed func(s *Statement)

	// Plan decides whether to measure the planning of a statement;
	// Planned receives the measurement.
	Plan    func(s *Statement) bool
	Planned func(s *Statement, u *Usage)

	// Execute decides whether to measure an execution of a planned
	// statement (SELECT, INSERT...); Executed receives the measurement
	// when it ends (not if it fails). Rows is the number of rows returned
	// or affected.
	Execute  func(s *Statement) bool
	Executed func(s *Statement, u *Usage)

	// Utility decides whether to measure a utility statement (DDL, COPY,
	// EXPLAIN...); UtilityDone receives the measurement when it succeeds.
	// Rows is set for COPY, FETCH, CREATE TABLE AS, SELECT INTO and
	// REFRESH MATERIALIZED VIEW. A measured utility statement includes the
	// statement it contains, which the executor hooks don't see then
	// (their query identifier is 0).
	Utility     func(s *Statement) bool
	UtilityDone func(s *Statement, u *Usage)
}

// Statement is a statement being processed. It's only valid during the
// callback.
type Statement struct {
	// QueryID is the query identifier (compute_query_id, see
	// EnableQueryID): statements that differ only in constants share it.
	// 0 if not computed.
	QueryID int64
	// Location is the offset of the statement in the source text sent by
	// the client (which may hold several statements); Text is the
	// statement.
	Location int
	// Nesting is the nesting level (0: the client's statement).
	Nesting int
	// Command is the statement's command tag: "SELECT", "INSERT",
	// "CREATE TABLE", "EXECUTE"...
	Command string
	// ExecutorNesting is the number of executions in progress around the
	// statement (Execute and Executed only): 0 unless a query (e.g. one
	// calling a function) runs it; utility statements (DO, CREATE TABLE
	// AS...) don't count.
	ExecutorNesting int

	ev         *cevent
	instrument Instrumentation
}

// Instrumentation is the per-node measurements of an execution, collected
// for Statement.Explain with Analyze.
type Instrumentation int64

const (
	// InstrumentRows counts the rows and loops of each plan node.
	InstrumentRows Instrumentation = 1 << iota
	// InstrumentTimer also times each node (EXPLAIN's TIMING; costlier).
	InstrumentTimer
	// InstrumentBuffers counts each node's buffer usage.
	InstrumentBuffers
	// InstrumentWAL counts each node's WAL usage.
	InstrumentWAL
)

// Instrument asks for per-node instrumentation of the execution (only in
// Execute, if it returns true). It is ignored for EXPLAIN without ANALYZE,
// which doesn't run the statement.
func (s *Statement) Instrument(i Instrumentation) {
	if s.ev.kind != kindExecute {
		panic("hooks: Statement.Instrument is only available in Execute")
	}
	s.instrument |= i
}

// ExplainOptions are the options of Statement.Explain, as EXPLAIN's.
type ExplainOptions struct {
	// Format is "text" (the default), "xml", "json" or "yaml".
	Format string
	// Analyze shows the measurements of the execution, if it was
	// instrumented (Statement.Instrument); Timing, Buffers and WAL show
	// the per-node times, buffers and WAL usage, if they were collected.
	// Triggers shows the time spent in each trigger.
	Analyze, Timing, Buffers, WAL, Triggers bool
	// Verbose shows output columns and qualified names; Settings the
	// modified settings that affect planning; NoCosts leaves out the
	// estimated costs (and the JIT summary).
	Verbose, Settings, NoCosts bool
	// QueryText starts the output with the statement's text ("Query
	// Text").
	QueryText bool
	// Parameters shows the values of the statement's parameters
	// ("Query Parameters", PostgreSQL 16+): 0 doesn't, -1 shows them in
	// full, n > 0 truncates each to n bytes.
	Parameters int
}

// Explain returns the plan of the execution, as EXPLAIN prints it (only in
// Executed). With Format "json" it is an array holding one plan object.
func (s *Statement) Explain(o ExplainOptions) string {
	if s.ev.kind != kindExecuted {
		panic("hooks: Statement.Explain is only available in Executed")
	}
	c := cexplain{parameters: int64(o.Parameters)}
	switch o.Format {
	case "", "text":
	case "xml":
		c.format = 1
	case "json":
		c.format = 2
	case "yaml":
		c.format = 3
	default:
		panic("hooks: unknown EXPLAIN format " + o.Format)
	}
	for i, b := range []bool{o.Analyze, o.Timing, o.Buffers, o.WAL, o.Triggers,
		o.Verbose, o.Settings, o.NoCosts, o.QueryText} {
		if b {
			c.flags |= 1 << i
		}
	}
	var p *byte
	var n int64
	core.Check(explain(s.ev, &c, &p, &n))
	return string(unsafe.Slice(p, n))
}

// Text returns the statement's text, without the source's other
// statements or surrounding whitespace.
func (s *Statement) Text() string {
	return string(unsafe.Slice((*byte)(unsafe.Add(unsafe.Pointer(s.ev.source), s.ev.location)), s.ev.length))
}

// Constant is a constant in a statement's text.
type Constant struct {
	// Location and Length locate the constant in Text (a negative
	// number includes its '-'; a squashed list of constants is the whole
	// list).
	Location, Length int
	// Squashed is a list of constants the query identifier ignores the
	// length of: "IN (1, 2, 3)" (PostgreSQL 18+).
	Squashed bool
	// Param is an external parameter ($1) rather than a constant
	// (PostgreSQL 18+).
	Param bool
}

// Constants describes the constants of a statement, for Parsed: the
// constants in text order (duplicates at the same location are left out),
// the highest $n parameter the statement uses, and whether any list was
// squashed (PostgreSQL 18+).
type Constants struct {
	List         []Constant
	HighestParam int
	Squashed     bool
}

// Constants returns the statement's constants, which its query identifier
// ignores (only in Parsed; empty if the query identifier isn't computed).
// Replacing them with $n gives a normalized text, as pg_stat_statements
// shows.
func (s *Statement) Constants() Constants {
	if s.ev.kind != kindParsed {
		panic("hooks: Statement.Constants is only available in Parsed")
	}
	var out Constants
	if s.ev.jstate == 0 {
		return out
	}
	var cs *cconstant
	var n, highest int64
	var squashed bool
	core.Check(constants(s.ev, &cs, &n, &highest, &squashed))
	out.HighestParam, out.Squashed = int(highest), squashed
	if n == 0 {
		return out
	}
	for _, c := range unsafe.Slice(cs, n) {
		if c.length < 0 {
			continue
		}
		out.List = append(out.List, Constant{Location: int(c.location), Length: int(c.length),
			Squashed: c.flags&1 != 0, Param: c.flags&2 != 0})
	}
	return out
}

// Usage is the resources a statement used. Times are in milliseconds.
type Usage struct {
	Time float64
	Rows int64

	// Blocks of shared buffers (tables and indexes), local buffers
	// (temporary tables) and temporary files: found in the buffer cache
	// (hit), read, dirtied, written.
	SharedBlocksHit, SharedBlocksRead, SharedBlocksDirtied, SharedBlocksWritten int64
	LocalBlocksHit, LocalBlocksRead, LocalBlocksDirtied, LocalBlocksWritten     int64
	TempBlocksRead, TempBlocksWritten                                           int64
	// Time reading and writing blocks (with track_io_timing). Before
	// PostgreSQL 17, the shared ones include local blocks; temporary blocks
	// are timed since 15.
	SharedBlockReadTime, SharedBlockWriteTime float64
	LocalBlockReadTime, LocalBlockWriteTime   float64
	TempBlockReadTime, TempBlockWriteTime     float64

	// WAL records, full page images, bytes, and the times the WAL buffers
	// were full (PostgreSQL 18+).
	WALRecords, WALFullPageImages int64
	WALBytes                      uint64
	WALBuffersFull                int64

	// JIT compilation (Executed; nil if not used): functions compiled, and
	// the time spent generating, inlining, optimizing and emitting code,
	// and for tuple deforming (PostgreSQL 17+).
	JIT *JIT

	// Parallel workers planned and launched (Executed, PostgreSQL 18+).
	ParallelWorkersToLaunch, ParallelWorkersLaunched int64
}

// JIT is the JIT compilation of an execution.
type JIT struct {
	Functions                                                                int64
	GenerationTime, InliningTime, OptimizationTime, EmissionTime, DeformTime float64
}

var registered []Hooks

// Register adds callbacks. Call it while the library loads (in main); to
// see every backend's statements, the library must be in
// shared_preload_libraries.
func Register(h Hooks) {
	if len(registered) == 64 {
		panic("hooks: too many registrations")
	}
	registered = append(registered, h)
	var kinds int64
	set := func(k int, f bool) {
		if f {
			kinds |= 1 << k
		}
	}
	set(kindParsed, h.Parsed != nil)
	set(kindPlan, h.Plan != nil)
	set(kindExecute, h.Execute != nil)
	set(kindUtility, h.Utility != nil)
	install(kinds)
}

// EnableQueryID asks PostgreSQL to compute query identifiers when
// compute_query_id is "auto" (its default), as pg_stat_statements does.
// Call it in main.
func EnableQueryID() { enableQueryID() }

// InParallelWorker reports whether this process is a parallel worker
// (statements in workers are also seen by their leader).
func InParallelWorker() bool { return parallelWorker() }

// ---- C side (frameworks/hooks/hooks.c) ---------------------------------

const (
	kindParsed = iota
	kindPlan
	kindPlanned
	kindExecute
	kindExecuted
	kindUtility
	kindUtilityDone
)

// cusage is struct hooks_usage.
type cusage struct {
	time                                                                float64
	rows                                                                int64
	sharedHit, sharedRead, sharedDirtied, sharedWritten                 int64
	localHit, localRead, localDirtied, localWritten                     int64
	tempRead, tempWritten                                               int64
	sharedReadTime, sharedWriteTime, localReadTime, localWriteTime      float64
	tempReadTime, tempWriteTime                                         float64
	walRecords, walFPI                                                  int64
	walBytes                                                            uint64
	walBuffersFull                                                      int64
	jit, jitFunctions                                                   int64
	jitGeneration, jitInlining, jitOptimization, jitEmission, jitDeform float64
	workersToLaunch, workersLaunched                                    int64
}

// cevent is struct hooks_event.
type cevent struct {
	kind, nesting, queryID int64
	source                 *byte
	location, length       int64
	command                *byte
	jstate                 uintptr
	mask                   uint64
	usage                  cusage
	executorNesting        int64
	instrument             int64
	queryDesc              uintptr
}

// cexplain is struct hooks_explain.
type cexplain struct{ format, flags, parameters int64 }

//export gopgext_hooks_explain
func explain(ev *cevent, o *cexplain, out **byte, n *int64) uintptr

// cconstant is struct hooks_constant.
type cconstant struct{ location, length, flags int64 }

//export gopgext_hooks_install
func install(kinds int64)

//export gopgext_hooks_parallel_worker
func parallelWorker() bool

//export gopgext_hooks_enable_query_id
func enableQueryID()

//export gopgext_hooks_constants
func constants(ev *cevent, out **cconstant, n *int64, highest *int64, squashed *bool) uintptr

func (u *cusage) usage() *Usage {
	r := &Usage{
		Time: u.time, Rows: u.rows,
		SharedBlocksHit: u.sharedHit, SharedBlocksRead: u.sharedRead,
		SharedBlocksDirtied: u.sharedDirtied, SharedBlocksWritten: u.sharedWritten,
		LocalBlocksHit: u.localHit, LocalBlocksRead: u.localRead,
		LocalBlocksDirtied: u.localDirtied, LocalBlocksWritten: u.localWritten,
		TempBlocksRead: u.tempRead, TempBlocksWritten: u.tempWritten,
		SharedBlockReadTime: u.sharedReadTime, SharedBlockWriteTime: u.sharedWriteTime,
		LocalBlockReadTime: u.localReadTime, LocalBlockWriteTime: u.localWriteTime,
		TempBlockReadTime: u.tempReadTime, TempBlockWriteTime: u.tempWriteTime,
		WALRecords: u.walRecords, WALFullPageImages: u.walFPI, WALBytes: u.walBytes,
		WALBuffersFull:          u.walBuffersFull,
		ParallelWorkersToLaunch: u.workersToLaunch, ParallelWorkersLaunched: u.workersLaunched,
	}
	if u.jit != 0 {
		r.JIT = &JIT{Functions: u.jitFunctions, GenerationTime: u.jitGeneration, InliningTime: u.jitInlining,
			OptimizationTime: u.jitOptimization, EmissionTime: u.jitEmission, DeformTime: u.jitDeform}
	}
	return r
}

//export gopgext_go_hooks_event
func event(c *fmgr.Call) {
	defer c.End(c.Begin(false))
	d, _ := c.Datum(0)
	ev := (*cevent)(unsafe.Pointer(uintptr(d)))
	s := &Statement{QueryID: ev.queryID, Location: int(ev.location), Nesting: int(ev.nesting),
		Command: core.GoString(ev.command), ExecutorNesting: int(ev.executorNesting), ev: ev}
	switch ev.kind {
	case kindParsed:
		for _, h := range registered {
			if h.Parsed != nil {
				h.Parsed(s)
			}
		}
		ev.queryID = s.QueryID
	case kindPlan, kindExecute, kindUtility:
		var mask uint64
		for i, h := range registered {
			f := h.Plan
			if ev.kind == kindExecute {
				f = h.Execute
			} else if ev.kind == kindUtility {
				f = h.Utility
			}
			s.instrument = 0
			if f != nil && f(s) {
				mask |= 1 << i
				ev.instrument |= int64(s.instrument)
			}
		}
		ev.mask = mask
	default:
		u := ev.usage.usage()
		for i, h := range registered {
			f := h.Planned
			if ev.kind == kindExecuted {
				f = h.Executed
			} else if ev.kind == kindUtilityDone {
				f = h.UtilityDone
			}
			if ev.mask&(1<<i) != 0 && f != nil {
				f(s, u)
			}
		}
	}
}
