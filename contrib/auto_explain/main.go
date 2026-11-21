// auto_explain, in Go: a port of PostgreSQL's contrib/auto_explain
// (PostgreSQL 18) on gopgext/hooks. It logs the execution plans of
// statements that run longer than auto_explain.log_min_duration, like
// EXPLAIN (or EXPLAIN ANALYZE with auto_explain.log_analyze) would print
// them. Load it with LOAD, session_preload_libraries or
// shared_preload_libraries.
package main

import (
	"math"
	"math/rand/v2"
	"strconv"

	"gopgext/hooks"
	"gopgext/pg"
)

// Settings (auto_explain.*), all changeable by superusers only.
var (
	logMinDuration = pg.NewIntSetting("log_min_duration", -1, -1, math.MaxInt32,
		"Sets the minimum execution time above which plans will be logged.",
		pg.Superuser, pg.UnitMilliseconds)
	logParameterMaxLength = pg.NewIntSetting("log_parameter_max_length", -1, -1, math.MaxInt32,
		"Sets the maximum length of query parameter values to log.", pg.Superuser, pg.UnitBytes)
	logAnalyze = pg.NewBoolSetting("log_analyze", false,
		"Use EXPLAIN ANALYZE for plan logging.", pg.Superuser)
	logSettings = pg.NewBoolSetting("log_settings", false,
		"Log modified configuration parameters affecting query planning.", pg.Superuser)
	logVerbose = pg.NewBoolSetting("log_verbose", false,
		"Use EXPLAIN VERBOSE for plan logging.", pg.Superuser)
	logBuffers  = pg.NewBoolSetting("log_buffers", false, "Log buffers usage.", pg.Superuser)
	logWAL      = pg.NewBoolSetting("log_wal", false, "Log WAL usage.", pg.Superuser)
	logTriggers = pg.NewBoolSetting("log_triggers", false,
		"Include trigger statistics in plans.", pg.Superuser)
	logFormat = pg.NewEnumSetting("log_format", "text", []string{"text", "xml", "json", "yaml"},
		"EXPLAIN format to be used for plan logging.", pg.Superuser)
	logLevel = pg.NewEnumSetting("log_level", "log", levelNames,
		"Log level for the plan.", pg.Superuser)
	logNestedStatements = pg.NewBoolSetting("log_nested_statements", false,
		"Log nested statements.", pg.Superuser)
	logTiming = pg.NewBoolSetting("log_timing", true,
		"Collect timing data, not just row counts.", pg.Superuser)
	sampleRate = pg.NewFloatSetting("sample_rate", 1, 0, 1,
		"Fraction of queries to process.", pg.Superuser)
)

// The values of auto_explain.log_level, and their levels.
var (
	levelNames = []string{"debug5", "debug4", "debug3", "debug2", "debug1", "debug",
		"info", "notice", "warning", "log"}
	levels = []pg.Level{pg.LevelDebug5, pg.LevelDebug4, pg.LevelDebug3, pg.LevelDebug2,
		pg.LevelDebug1, pg.LevelDebug2, pg.LevelInfo, pg.LevelNotice, pg.LevelWarning, pg.LevelLog}
)

// Whether the current top-level statement was sampled.
var currentQuerySampled bool

// enabled is auto_explain_enabled(): nesting is the executor nesting level.
func enabled(nesting int) bool {
	return logMinDuration.Get() >= 0 && (nesting == 0 || logNestedStatements.Get()) && currentQuerySampled
}

func main() {
	hooks.Register(hooks.Hooks{Execute: execute, Executed: executed})
}

func execute(s *hooks.Statement) bool {
	// At the beginning of each top-level statement, decide whether to
	// sample it: nested statements are all explained or none. Parallel
	// workers never are (the leader reports their data).
	if s.ExecutorNesting == 0 {
		currentQuerySampled = logMinDuration.Get() >= 0 && !hooks.InParallelWorker() &&
			rand.Float64() < sampleRate.Get()
	}
	if !enabled(s.ExecutorNesting) {
		return false
	}
	if logAnalyze.Get() {
		if logTiming.Get() {
			s.Instrument(hooks.InstrumentTimer)
		} else {
			s.Instrument(hooks.InstrumentRows)
		}
		if logBuffers.Get() {
			s.Instrument(hooks.InstrumentBuffers)
		}
		if logWAL.Get() {
			s.Instrument(hooks.InstrumentWAL)
		}
	}
	return true
}

func executed(s *hooks.Statement, u *hooks.Usage) {
	if !enabled(s.ExecutorNesting) || u.Time < float64(logMinDuration.Get()) {
		return
	}
	analyze := logAnalyze.Get()
	format := logFormat.Get()
	plan := s.Explain(hooks.ExplainOptions{
		Format:     format,
		Analyze:    analyze,
		Verbose:    logVerbose.Get(),
		Buffers:    logBuffers.Get(),
		WAL:        logWAL.Get(),
		Timing:     logTiming.Get(),
		Settings:   logSettings.Get(),
		Triggers:   logTriggers.Get(),
		QueryText:  true,
		Parameters: logParameterMaxLength.Get(),
	})
	// Remove the last line break; make JSON an object rather than an array.
	if n := len(plan); n > 0 && plan[n-1] == '\n' {
		plan = plan[:n-1]
	}
	if format == "json" && len(plan) >= 2 {
		plan = "{" + plan[1:len(plan)-1] + "}"
	}
	// The statement isn't shown: the log line's context or
	// debug_query_string identifies it, and the plan holds its text.
	level := logLevel.Get()
	for i, name := range levelNames {
		if name == level {
			m := pg.Message{Level: levels[i], HideStatement: true,
				Text: "duration: " + strconv.FormatFloat(u.Time, 'f', 3, 64) + " ms  plan:\n" + plan}
			m.Report()
			return
		}
	}
}
