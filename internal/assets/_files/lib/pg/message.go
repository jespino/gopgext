package pg

import "gopgext/internal/core"

// Level is the severity of a Message: whether it goes to the client and
// the server log depends on client_min_messages and log_min_messages.
type Level int

const (
	LevelDebug5 Level = iota
	LevelDebug4
	LevelDebug3
	LevelDebug2
	LevelDebug1
	LevelLog
	LevelInfo
	LevelNotice
	LevelWarning
)

// Message is a message to report with more control than Log, Notice...
// give: any level, a detail and a hint, and whether the server log shows
// the statement being run and the error context with it.
type Message struct {
	Level              Level
	Text, Detail, Hint string
	// HideStatement leaves the statement being run out of the server log
	// entry (errhidestmt), e.g. when the message already shows it.
	HideStatement bool
	// HideContext leaves out the context (errhidecontext).
	HideContext bool
}

//export gopgext_report
func report(level int64, text, detail, hint *byte, flags int64) uintptr

// Report sends the message.
func (m *Message) Report() {
	if m.Level < LevelDebug5 || m.Level > LevelWarning {
		panic("pg: invalid message level")
	}
	var flags int64
	if m.HideStatement {
		flags |= 1
	}
	if m.HideContext {
		flags |= 2
	}
	cstr := func(s string) *byte {
		if s == "" {
			return nil
		}
		return core.CBytes(s)
	}
	core.Check(report(int64(m.Level), core.CBytes(m.Text), cstr(m.Detail), cstr(m.Hint), flags))
}
