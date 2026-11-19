package pg

import (
	"fmt"

	"gopgext/internal/core"
)

//export gopgext_log
func cLog(level int64, msg *byte, n int64) uintptr

const (
	levelDebug = iota
	levelLog
	levelInfo
	levelNotice
	levelWarning
)

func log(level int64, msg string) {
	core.Check(cLog(level, core.StringData(msg), int64(len(msg))))
}

// Debug logs msg at level DEBUG1.
func Debug(msg string) { log(levelDebug, msg) }

// Log writes msg to the server log (level LOG).
func Log(msg string) { log(levelLog, msg) }

// Info sends msg to the client (level INFO).
func Info(msg string) { log(levelInfo, msg) }

// Notice sends msg to the client (level NOTICE), like RAISE NOTICE.
func Notice(msg string) { log(levelNotice, msg) }

// Warning sends msg to the client and the server log (level WARNING).
func Warning(msg string) { log(levelWarning, msg) }

func Debugf(format string, args ...any)   { Debug(fmt.Sprintf(format, args...)) }
func Logf(format string, args ...any)     { Log(fmt.Sprintf(format, args...)) }
func Infof(format string, args ...any)    { Info(fmt.Sprintf(format, args...)) }
func Noticef(format string, args ...any)  { Notice(fmt.Sprintf(format, args...)) }
func Warningf(format string, args ...any) { Warning(fmt.Sprintf(format, args...)) }
