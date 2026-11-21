// Package main is a port of PostgreSQL's contrib/tcn (triggered change
// notifications): an AFTER ROW trigger that sends a NOTIFY with the primary
// key of each changed row.
package main

import (
	"strings"

	"gopgext/pg"
	"gopgext/trigger"
)

// TriggeredChangeNotification notifies the channel given as the trigger's
// argument (default "tcn") of a change: "table",I|U|D,"col"='value',... with
// the columns of the table's primary key.
//
//gopgext:function
func TriggeredChangeNotification(t *trigger.Data) error {
	const fn = "triggered_change_notification"
	if t.When != trigger.After {
		return pg.Errorf(trigger.CodeProtocolViolated, "%s: must be called after the change", fn)
	}
	if !t.ForEachRow {
		return pg.Errorf(trigger.CodeProtocolViolated, "%s: must be called for each row", fn)
	}
	var op string
	row := t.Old // tg_trigtuple: the old row of an UPDATE
	switch t.Op {
	case trigger.Insert:
		op, row = "I", t.New
	case trigger.Update:
		op = "U"
	case trigger.Delete:
		op = "D"
	default:
		return pg.Errorf(pg.CodeInternalError, "%s: trigger fired by unrecognized operation", fn)
	}
	if len(t.Args) > 1 {
		return pg.Errorf(trigger.CodeProtocolViolated, "%s: must not be called with more than one parameter", fn)
	}
	channel := "tcn"
	if len(t.Args) == 1 {
		channel = t.Args[0]
	}

	// The key columns of the valid primary key index, in index order.
	rows, err := pg.Query(`SELECT a.attname::text
		FROM pg_index i
		CROSS JOIN LATERAL unnest(i.indkey[0:i.indnkeyatts - 1]) WITH ORDINALITY k(attnum, n)
		JOIN pg_attribute a ON a.attrelid = i.indrelid AND a.attnum = k.attnum
		WHERE i.indrelid = $1::oid AND i.indisprimary AND i.indisvalid
		ORDER BY k.n`, int64(t.TableOID))
	if err != nil {
		return err
	}
	if rows.Len() == 0 {
		return pg.Errorf(trigger.CodeProtocolViolated, "%s: must be called on a table with a primary key", fn)
	}
	var payload strings.Builder
	quoted(&payload, t.Table, '"')
	payload.WriteString("," + op)
	for rows.Next() {
		var col string
		if err := rows.Scan(&col); err != nil {
			return err
		}
		v, err := row.Text(col)
		if err != nil {
			return err
		}
		payload.WriteByte(',')
		quoted(&payload, col, '"')
		payload.WriteByte('=')
		if v != nil {
			quoted(&payload, *v, '\'')
		} else {
			payload.WriteString("''") // can't happen: primary keys are NOT NULL
		}
	}
	return pg.Notify(channel, payload.String())
}

// quoted writes s between q characters, doubling the q characters in it.
func quoted(b *strings.Builder, s string, q byte) {
	b.WriteByte(q)
	for i := 0; i < len(s); i++ {
		if s[i] == q {
			b.WriteByte(q)
		}
		b.WriteByte(s[i])
	}
	b.WriteByte(q)
}

func main() {}
