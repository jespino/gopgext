// pg_stat_statements, in Go: a port of PostgreSQL's
// contrib/pg_stat_statements (version 1.12, PostgreSQL 18) on gopgext's
// hooks and shmem packages. It tracks planning and execution statistics
// of all SQL statements executed by the server, grouped by query
// identifier, with their normalized texts.
//
// It must be loaded by shared_preload_libraries. See README.md for the
// differences with the C original.
package main

import (
	"math"
	"time"

	"gopgext/hooks"
	"gopgext/pg"
	"gopgext/shmem"
)

// Settings (pg_stat_statements.*), defined only when preloaded.
var (
	maxEntries    *pg.IntSetting
	track         *pg.EnumSetting
	trackUtility  *pg.BoolSetting
	trackPlanning *pg.BoolSetting
	save          *pg.BoolSetting
)

// Shared state, created only when preloaded (nil otherwise).
var (
	lock          *shmem.Lock // protects the table and the texts
	table         *shmem.Table[key, entry]
	globals       *shmem.Var[global]
	texts         *shmem.Bytes
	maxStatements int // pg_stat_statements.max
)

func main() {
	if !pg.Preloading() {
		return // the SQL functions report that it must be preloaded
	}
	// Compute query identifiers with compute_query_id = auto.
	hooks.EnableQueryID()

	maxEntries = pg.NewIntSetting("max", 5000, 100, math.MaxInt32/2,
		"Sets the maximum number of statements tracked by pg_stat_statements.", pg.Postmaster)
	track = pg.NewEnumSetting("track", "top", []string{"none", "top", "all"},
		"Selects which statements are tracked by pg_stat_statements.", pg.Superuser)
	trackUtility = pg.NewBoolSetting("track_utility", true,
		"Selects whether utility commands are tracked by pg_stat_statements.", pg.Superuser)
	trackPlanning = pg.NewBoolSetting("track_planning", false,
		"Selects whether planning duration is tracked by pg_stat_statements.", pg.Superuser)
	save = pg.NewBoolSetting("save", true,
		"Save pg_stat_statements statistics across server shutdowns.", pg.Sighup)

	maxStatements = maxEntries.Get()
	lock = shmem.NewLock("pg_stat_statements")
	table = shmem.NewTable[key, entry]("pg_stat_statements hash", maxStatements)
	globals = shmem.NewVar[global]("pg_stat_statements")
	texts = shmem.NewBytes("pg_stat_statements query texts", maxStatements*textSpacePerEntry)

	shmem.OnStartup(load)
	shmem.OnShutdown(dump)
	hooks.Register(hooks.Hooks{
		Parsed:      parsed,
		Plan:        func(s *hooks.Statement) bool { return enabled(s.Nesting) && trackPlanning.Get() && s.QueryID != 0 },
		Planned:     func(s *hooks.Statement, u *hooks.Usage) { store(s, kindPlan, u, nil) },
		Execute:     func(s *hooks.Statement) bool { return enabled(s.Nesting) && s.QueryID != 0 },
		Executed:    func(s *hooks.Statement, u *hooks.Usage) { store(s, kindExec, u, nil) },
		Utility:     utility,
		UtilityDone: func(s *hooks.Statement, u *hooks.Usage) { store(s, kindExec, u, nil) },
	})
}

const (
	kindPlan = iota
	kindExec
	numKinds
)

// key identifies an entry. No padding (the table hashes its bytes).
type key struct {
	UserID   pg.Oid
	DBID     pg.Oid
	QueryID  int64
	TopLevel bool
	_        [7]byte
}

// counters are the statistics of an entry.
type counters struct {
	Calls      [numKinds]int64   // # of times planned/executed
	TotalTime  [numKinds]float64 // total planning/execution time, in msec
	MinTime    [numKinds]float64 // since the last min/max reset
	MaxTime    [numKinds]float64
	MeanTime   [numKinds]float64
	SumVarTime [numKinds]float64 // sum of variances (Welford's method)

	Rows                                                                       int64
	SharedBlksHit, SharedBlksRead, SharedBlksDirtied, SharedBlksWritten        int64
	LocalBlksHit, LocalBlksRead, LocalBlksDirtied, LocalBlksWritten            int64
	TempBlksRead, TempBlksWritten                                              int64
	SharedBlkReadTime, SharedBlkWriteTime, LocalBlkReadTime, LocalBlkWriteTime float64
	TempBlkReadTime, TempBlkWriteTime                                          float64
	Usage                                                                      float64 // usage factor
	WALRecords, WALFPI                                                         int64
	WALBytes                                                                   uint64
	WALBuffersFull                                                             int64
	JITFunctions                                                               int64
	JITGenerationTime                                                          float64
	JITInliningCount                                                           int64
	JITInliningTime                                                            float64
	JITOptimizationCount                                                       int64
	JITOptimizationTime                                                        float64
	JITEmissionCount                                                           int64
	JITEmissionTime                                                            float64
	JITDeformCount                                                             int64
	JITDeformTime                                                              float64
	ParallelWorkersToLaunch, ParallelWorkersLaunched                           int64
}

// sticky reports whether an entry was never planned or executed: created
// to keep the normalized text of a statement being run.
func (c *counters) sticky() bool { return c.Calls[kindPlan]+c.Calls[kindExec] == 0 }

// entry is the statistics of a statement.
type entry struct {
	Mutex    shmem.SpinLock // protects Counters, under a shared lock
	_        uint32
	Counters counters
	// The query text in texts (TextLen -1: lost).
	TextOffset int64
	TextLen    int64
	// Microseconds since 1970 of the entry's creation and of the last
	// min/max reset (only changed under the exclusive lock).
	StatsSince, MinMaxStatsSince int64
}

// global is the shared state besides entries.
type global struct {
	Mutex          shmem.SpinLock // protects the fields below it
	_              uint32
	CurMedianUsage float64 // current median usage of the entries
	MeanQueryLen   int64   // current mean text length
	Extent         int64   // used bytes of texts
	GCCount        int64   // text garbage collections
	Dealloc        int64   // times entries were deallocated
	StatsReset     int64   // microseconds since 1970 of the last full reset
}

const (
	usageExec            = 1.0  // usage added per call
	usageInit            = 1.0  // usage of a new entry (including initial planning)
	assumedMedianInit    = 10.0 // initial assumed median usage
	assumedLengthInit    = 1024 // initial assumed mean query length
	usageDecreaseFactor  = 0.99 // decreased every entryDealloc
	stickyDecreaseFactor = 0.50 // factor for sticky entries
	usageDeallocPercent  = 5    // free this % of entries at once
)

func now() int64 { return time.Now().UnixMicro() }

// enabled reports whether statements at a nesting level are tracked.
func enabled(level int) bool {
	t := track.Get()
	return !hooks.InParallelWorker() && (t == "all" || (t == "top" && level == 0))
}

// parsed records the normalized text of a new statement: an entry is
// created before execution if the statement has constants (the
// normalized text is the statement's text otherwise).
func parsed(s *hooks.Statement) {
	if !enabled(s.Nesting) {
		return
	}
	// EXECUTE: the statistics go to the prepared statement (and the
	// utility statement isn't tracked).
	if s.Command == "EXECUTE" {
		if trackUtility.Get() {
			s.QueryID = 0
		}
		return
	}
	if cs := s.Constants(); len(cs.List) > 0 {
		store(s, kindExec, nil, &cs)
	}
}

// utility decides whether to measure a utility statement. EXECUTE and
// PREPARE aren't: their costs go to the prepared statement.
func utility(s *hooks.Statement) bool {
	return trackUtility.Get() && enabled(s.Nesting) && s.Command != "EXECUTE" && s.Command != "PREPARE"
}

// store records a planning or an execution of a statement (u), or, with
// cs, creates its entry with its normalized text.
func store(s *hooks.Statement, kind int, u *hooks.Usage, cs *hooks.Constants) {
	if s.QueryID == 0 {
		return
	}
	k := key{UserID: pg.UserID(), DBID: pg.DatabaseID(), QueryID: s.QueryID, TopLevel: s.Nesting == 0}

	lock.RLock()
	e := table.Get(k)
	if e == nil {
		// Normalize the text without holding the lock.
		lock.Unlock()
		text := s.Text()
		if cs != nil {
			text = normalize(text, cs)
		}
		lock.RLock()

		// Store the text with the shared lock held; garbage collection
		// needs the exclusive one.
		off, gcCount, stored := textStore(text)
		doGC := needGC()

		lock.Unlock()
		lock.Lock()
		g := globals.Ptr()
		if !stored || g.GCCount != gcCount {
			if off, stored = textStoreExclusive(text); !stored {
				lock.Unlock()
				return // too long for the texts area
			}
		}
		e = entryAlloc(k, off, len(text), cs != nil)
		if doGC {
			gcTexts()
		}
	}
	if cs == nil && e != nil {
		e.Mutex.Lock()
		e.Counters.add(kind, u)
		e.Mutex.Unlock()
	}
	lock.Unlock()
}

// add accumulates a planning or an execution.
func (c *counters) add(kind int, u *hooks.Usage) {
	if c.sticky() {
		c.Usage = usageInit // no longer sticky
	}
	t := u.Time
	c.Calls[kind]++
	c.TotalTime[kind] += t
	if c.Calls[kind] == 1 {
		c.MinTime[kind], c.MaxTime[kind], c.MeanTime[kind] = t, t, t
	} else {
		// Welford's method for the variance.
		old := c.MeanTime[kind]
		c.MeanTime[kind] += (t - old) / float64(c.Calls[kind])
		c.SumVarTime[kind] += (t - old) * (t - c.MeanTime[kind])
		// min = max = 0: the min/max were reset.
		if c.MinTime[kind] == 0 && c.MaxTime[kind] == 0 {
			c.MinTime[kind], c.MaxTime[kind] = t, t
		} else {
			c.MinTime[kind] = min(c.MinTime[kind], t)
			c.MaxTime[kind] = math.Max(c.MaxTime[kind], t)
		}
	}
	c.Rows += u.Rows
	c.SharedBlksHit += u.SharedBlocksHit
	c.SharedBlksRead += u.SharedBlocksRead
	c.SharedBlksDirtied += u.SharedBlocksDirtied
	c.SharedBlksWritten += u.SharedBlocksWritten
	c.LocalBlksHit += u.LocalBlocksHit
	c.LocalBlksRead += u.LocalBlocksRead
	c.LocalBlksDirtied += u.LocalBlocksDirtied
	c.LocalBlksWritten += u.LocalBlocksWritten
	c.TempBlksRead += u.TempBlocksRead
	c.TempBlksWritten += u.TempBlocksWritten
	c.SharedBlkReadTime += u.SharedBlockReadTime
	c.SharedBlkWriteTime += u.SharedBlockWriteTime
	c.LocalBlkReadTime += u.LocalBlockReadTime
	c.LocalBlkWriteTime += u.LocalBlockWriteTime
	c.TempBlkReadTime += u.TempBlockReadTime
	c.TempBlkWriteTime += u.TempBlockWriteTime
	c.Usage += usageExec
	c.WALRecords += u.WALRecords
	c.WALFPI += u.WALFullPageImages
	c.WALBytes += u.WALBytes
	c.WALBuffersFull += u.WALBuffersFull
	if j := u.JIT; j != nil {
		c.JITFunctions += j.Functions
		c.JITGenerationTime += j.GenerationTime
		if j.DeformTime != 0 {
			c.JITDeformCount++
		}
		c.JITDeformTime += j.DeformTime
		if j.InliningTime != 0 {
			c.JITInliningCount++
		}
		c.JITInliningTime += j.InliningTime
		if j.OptimizationTime != 0 {
			c.JITOptimizationCount++
		}
		c.JITOptimizationTime += j.OptimizationTime
		if j.EmissionTime != 0 {
			c.JITEmissionCount++
		}
		c.JITEmissionTime += j.EmissionTime
	}
	c.ParallelWorkersToLaunch += u.ParallelWorkersToLaunch
	c.ParallelWorkersLaunched += u.ParallelWorkersLaunched
}

// entryAlloc returns the entry of k, creating it if needed (another
// backend may have created it meanwhile). A sticky entry gets the median
// usage, so that it's probably still there when its statement finishes.
// The exclusive lock must be held.
func entryAlloc(k key, textOff int64, textLen int, sticky bool) *entry {
	for table.Len() >= maxStatements {
		entryDealloc()
	}
	e, inserted := table.Insert(k)
	if inserted {
		e.Counters.Usage = usageInit
		if sticky {
			e.Counters.Usage = globals.Ptr().CurMedianUsage
		}
		e.TextOffset, e.TextLen = textOff, int64(textLen)
		e.StatsSince = now()
		e.MinMaxStatsSince = e.StatsSince
	}
	return e
}

// entryDealloc removes the least used entries (usageDeallocPercent of
// them, at least 10), decaying the usage of all of them. The exclusive
// lock must be held.
func entryDealloc() {
	type item struct {
		k key
		e *entry
	}
	var items []item
	totLen, nTexts := int64(0), int64(0)
	for k, e := range table.All() {
		items = append(items, item{k, e})
		if e.Counters.sticky() {
			e.Counters.Usage *= stickyDecreaseFactor
		} else {
			e.Counters.Usage *= usageDecreaseFactor
		}
		if e.TextLen >= 0 {
			totLen += e.TextLen + 1
			nTexts++
		}
	}
	sortItems(items, func(a, b item) bool { return a.e.Counters.Usage < b.e.Counters.Usage })

	g := globals.Ptr()
	if len(items) > 0 {
		g.CurMedianUsage = items[len(items)/2].e.Counters.Usage
	}
	if nTexts > 0 {
		g.MeanQueryLen = totLen / nTexts
	} else {
		g.MeanQueryLen = assumedLengthInit
	}
	victims := min(len(items), max(10, len(items)*usageDeallocPercent/100))
	for _, it := range items[:victims] {
		table.Delete(it.k)
	}
	g.Mutex.Lock()
	g.Dealloc++
	g.Mutex.Unlock()
}
