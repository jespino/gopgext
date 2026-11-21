package main

import (
	"iter"
	"math"
	"strconv"
	"time"

	"gopgext/pg"
)

// mustBePreloaded raises the original's error when the library wasn't
// loaded by shared_preload_libraries.
func mustBePreloaded() {
	if table == nil {
		panic(pg.Errorf("55000", "pg_stat_statements must be loaded via \"shared_preload_libraries\""))
	}
}

// Statement is a row of pg_stat_statements.
type Statement struct {
	UserID   pg.Oid `pg:"userid"`
	DBID     pg.Oid `pg:"dbid"`
	TopLevel bool   `pg:"toplevel"`
	QueryID  *int64 `pg:"queryid"`
	Query    *string

	Plans          int64
	TotalPlanTime  float64
	MinPlanTime    float64
	MaxPlanTime    float64
	MeanPlanTime   float64
	StddevPlanTime float64

	Calls          int64
	TotalExecTime  float64
	MinExecTime    float64
	MaxExecTime    float64
	MeanExecTime   float64
	StddevExecTime float64

	Rows               int64
	SharedBlksHit      int64
	SharedBlksRead     int64
	SharedBlksDirtied  int64
	SharedBlksWritten  int64
	LocalBlksHit       int64
	LocalBlksRead      int64
	LocalBlksDirtied   int64
	LocalBlksWritten   int64
	TempBlksRead       int64
	TempBlksWritten    int64
	SharedBlkReadTime  float64
	SharedBlkWriteTime float64
	LocalBlkReadTime   float64
	LocalBlkWriteTime  float64
	TempBlkReadTime    float64
	TempBlkWriteTime   float64

	WALRecords     int64      `pg:"wal_records"`
	WALFPI         int64      `pg:"wal_fpi"`
	WALBytes       pg.Numeric `pg:"wal_bytes"`
	WALBuffersFull int64      `pg:"wal_buffers_full"`

	JITFunctions         int64   `pg:"jit_functions"`
	JITGenerationTime    float64 `pg:"jit_generation_time"`
	JITInliningCount     int64   `pg:"jit_inlining_count"`
	JITInliningTime      float64 `pg:"jit_inlining_time"`
	JITOptimizationCount int64   `pg:"jit_optimization_count"`
	JITOptimizationTime  float64 `pg:"jit_optimization_time"`
	JITEmissionCount     int64   `pg:"jit_emission_count"`
	JITEmissionTime      float64 `pg:"jit_emission_time"`
	JITDeformCount       int64   `pg:"jit_deform_count"`
	JITDeformTime        float64 `pg:"jit_deform_time"`

	ParallelWorkersToLaunch int64
	ParallelWorkersLaunched int64

	StatsSince       time.Time
	MinMaxStatsSince time.Time `pg:"minmax_stats_since"`
}

// PgStatStatements returns the statistics of the tracked statements (with
// their texts if showtext). Members of pg_read_all_stats see the query
// identifiers and texts of all users' statements, others only theirs.
//
//gopgext:function name=pg_stat_statements parallel=safe
func PgStatStatements(showtext bool) iter.Seq[Statement] {
	mustBePreloaded()
	userID := pg.UserID()
	allowed := pg.HasPrivilegesOf("pg_read_all_stats")

	var rows []Statement
	lock.RLock()
	defer lock.Unlock()
	for k, e := range table.All() {
		e.Mutex.Lock()
		c := e.Counters
		e.Mutex.Unlock()
		if c.sticky() {
			continue // not executed yet
		}
		r := Statement{UserID: k.UserID, DBID: k.DBID, TopLevel: k.TopLevel}
		if allowed || k.UserID == userID {
			id := k.QueryID
			r.QueryID = &id
			if showtext {
				if t, ok := textFetch(e); ok {
					r.Query = &t
				}
			}
		} else if showtext {
			t := "<insufficient privilege>"
			r.Query = &t
		}
		stddev := func(kind int) float64 {
			if c.Calls[kind] > 1 {
				return math.Sqrt(c.SumVarTime[kind] / float64(c.Calls[kind]))
			}
			return 0
		}
		r.Plans, r.TotalPlanTime = c.Calls[kindPlan], c.TotalTime[kindPlan]
		r.MinPlanTime, r.MaxPlanTime, r.MeanPlanTime = c.MinTime[kindPlan], c.MaxTime[kindPlan], c.MeanTime[kindPlan]
		r.StddevPlanTime = stddev(kindPlan)
		r.Calls, r.TotalExecTime = c.Calls[kindExec], c.TotalTime[kindExec]
		r.MinExecTime, r.MaxExecTime, r.MeanExecTime = c.MinTime[kindExec], c.MaxTime[kindExec], c.MeanTime[kindExec]
		r.StddevExecTime = stddev(kindExec)
		r.Rows = c.Rows
		r.SharedBlksHit, r.SharedBlksRead, r.SharedBlksDirtied, r.SharedBlksWritten =
			c.SharedBlksHit, c.SharedBlksRead, c.SharedBlksDirtied, c.SharedBlksWritten
		r.LocalBlksHit, r.LocalBlksRead, r.LocalBlksDirtied, r.LocalBlksWritten =
			c.LocalBlksHit, c.LocalBlksRead, c.LocalBlksDirtied, c.LocalBlksWritten
		r.TempBlksRead, r.TempBlksWritten = c.TempBlksRead, c.TempBlksWritten
		r.SharedBlkReadTime, r.SharedBlkWriteTime = c.SharedBlkReadTime, c.SharedBlkWriteTime
		r.LocalBlkReadTime, r.LocalBlkWriteTime = c.LocalBlkReadTime, c.LocalBlkWriteTime
		r.TempBlkReadTime, r.TempBlkWriteTime = c.TempBlkReadTime, c.TempBlkWriteTime
		r.WALRecords, r.WALFPI = c.WALRecords, c.WALFPI
		r.WALBytes = pg.Numeric(strconv.FormatUint(c.WALBytes, 10))
		r.WALBuffersFull = c.WALBuffersFull
		r.JITFunctions, r.JITGenerationTime = c.JITFunctions, c.JITGenerationTime
		r.JITInliningCount, r.JITInliningTime = c.JITInliningCount, c.JITInliningTime
		r.JITOptimizationCount, r.JITOptimizationTime = c.JITOptimizationCount, c.JITOptimizationTime
		r.JITEmissionCount, r.JITEmissionTime = c.JITEmissionCount, c.JITEmissionTime
		r.JITDeformCount, r.JITDeformTime = c.JITDeformCount, c.JITDeformTime
		r.ParallelWorkersToLaunch, r.ParallelWorkersLaunched = c.ParallelWorkersToLaunch, c.ParallelWorkersLaunched
		r.StatsSince = time.UnixMicro(e.StatsSince)
		r.MinMaxStatsSince = time.UnixMicro(e.MinMaxStatsSince)
		rows = append(rows, r)
	}
	return func(yield func(Statement) bool) {
		for _, r := range rows {
			if !yield(r) {
				return
			}
		}
	}
}

// Info is the row of pg_stat_statements_info.
type Info struct {
	Dealloc    int64
	StatsReset time.Time
}

// PgStatStatementsInfo returns the number of times entries were
// deallocated to make room for new ones, and when all statistics were last
// reset.
//
//gopgext:function name=pg_stat_statements_info parallel=safe
func PgStatStatementsInfo() Info {
	mustBePreloaded()
	g := globals.Ptr()
	g.Mutex.Lock()
	defer g.Mutex.Unlock()
	return Info{Dealloc: g.Dealloc, StatsReset: time.UnixMicro(g.StatsReset)}
}

// Reset discards the statistics of the statements of a user, a database
// and a query identifier (0: any), or only their min/max times
// (minmaxOnly). It returns the time of the reset. Declared in sql/ with
// default arguments.
//
//gopgext:function manual
func Reset(userid, dbid pg.Oid, queryid int64, minmaxOnly bool) time.Time {
	mustBePreloaded()
	return entryReset(userid, dbid, queryid, minmaxOnly)
}

func entryReset(userID, dbID pg.Oid, queryID int64, minmaxOnly bool) time.Time {
	lock.Lock()
	defer lock.Unlock()
	n := table.Len()
	resetAt := now()
	removed := 0
	reset := func(k key, e *entry) {
		if minmaxOnly {
			for kind := range numKinds {
				e.Counters.MaxTime[kind], e.Counters.MinTime[kind] = 0, 0
			}
			e.MinMaxStatsSince = resetAt
			return
		}
		table.Delete(k)
		removed++
	}
	if userID != 0 && dbID != 0 && queryID != 0 {
		// The non-top-level and top-level entries.
		for _, top := range []bool{false, true} {
			k := key{UserID: userID, DBID: dbID, QueryID: queryID, TopLevel: top}
			if e := table.Get(k); e != nil {
				reset(k, e)
			}
		}
	} else {
		for k, e := range table.All() {
			if (userID == 0 || k.UserID == userID) && (dbID == 0 || k.DBID == dbID) &&
				(queryID == 0 || k.QueryID == queryID) {
				reset(k, e)
			}
		}
	}
	if removed == n {
		// All entries are gone: reset the global statistics and the texts.
		g := globals.Ptr()
		g.Mutex.Lock()
		g.Dealloc = 0
		g.StatsReset = resetAt
		g.Extent = 0
		g.GCCount++
		g.Mutex.Unlock()
	}
	return time.UnixMicro(resetAt)
}
