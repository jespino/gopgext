// numbers_fdw is a foreign data wrapper written with gopgext/fdw, over
// numbers generated in Go: a table of n (from 1 to the max option) with
// computed columns.
//
//	CREATE SERVER s FOREIGN DATA WRAPPER numbers_fdw;
//	CREATE FOREIGN TABLE numbers (n bigint, square bigint, even bool, word text) SERVER s;
//	SELECT * FROM numbers WHERE n BETWEEN 10 AND 20;
//
// Conditions on n are pushed down (only the matching numbers are
// generated). Without the max option, an upper bound on n is required: a
// join can provide it (a parameterized nested loop).
package main

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"time"

	"gopgext/fdw"
	"gopgext/pg"
)

func main() {
	fdw.Register(fdw.Wrapper{
		Options: []fdw.Option{
			{Name: "max", Where: fdw.OnServer | fdw.OnTable, Check: positive},
			{Name: "start", Where: fdw.OnTable | fdw.OnUserMapping, Check: positive},
			{Name: "label", Where: fdw.OnColumn},
		},
		Plan:   plan,
		Scan:   scan,
		Import: importSchema,
	})
}

func positive(v string) error {
	if n, err := strconv.ParseInt(v, 10, 64); err != nil || n <= 0 {
		return errors.New("use a positive integer")
	}
	return nil
}

func column(t *fdw.Table, i int) string {
	if l, ok := t.Columns[i].Options["label"]; ok {
		return l
	}
	return t.Columns[i].Name
}

func plan(t *fdw.Table, conds []fdw.Condition) (fdw.Plan, error) {
	p := fdw.Plan{Pushed: make([]bool, len(conds))}
	_, bounded := t.Option("max")
	for i, c := range conds {
		if column(t, c.Column) == "n" && c.Kind == fdw.Int && c.Op != "<>" {
			p.Pushed[i] = true
			bounded = bounded || c.Op == "<" || c.Op == "<=" || c.Op == "="
		}
	}
	if !bounded {
		return p, &pg.Error{Code: "HV004", Message: "numbers_fdw: an upper bound on n is required",
			Hint: "Add WHERE n <= ..., or set the max option."}
	}
	p.Rows = 100
	return p, nil
}

type numbers struct {
	t        *fdw.Table
	next, to int64
	desc     string
}

func scan(t *fdw.Table, vals []fdw.Value) (fdw.Rows, error) {
	from, to := int64(1), int64(math.MaxInt64)
	if v, ok := t.Option("start"); ok {
		from, _ = strconv.ParseInt(v, 10, 64)
	}
	if v, ok := t.Option("max"); ok {
		to, _ = strconv.ParseInt(v, 10, 64)
	}
	for _, v := range vals {
		if v.Null {
			return &numbers{t: t, next: 1, to: 0, desc: "nothing (NULL)"}, nil
		}
		switch v.Op {
		case ">":
			from = max(from, v.Int+1)
		case ">=":
			from = max(from, v.Int)
		case "<":
			to = min(to, v.Int-1)
		case "<=":
			to = min(to, v.Int)
		case "=":
			from, to = max(from, v.Int), min(to, v.Int)
		}
	}
	if to == math.MaxInt64 {
		return nil, errors.New("numbers_fdw: no upper bound")
	}
	return &numbers{t: t, next: from, to: to, desc: fmt.Sprintf("n from %d to %d", from, to)}, nil
}

var words = []string{"zero", "one", "two", "three", "four", "five", "six", "seven", "eight", "nine"}

func (s *numbers) Next() ([]any, error) {
	if s.next > s.to {
		return nil, nil
	}
	n := s.next
	s.next++
	row := make([]any, len(s.t.Columns))
	for i := range row {
		switch column(s.t, i) {
		case "n":
			row[i] = n
		case "square":
			row[i] = n * n
		case "even":
			row[i] = n%2 == 0
		case "word":
			row[i] = words[n%10]
		case "half":
			row[i] = float64(n) / 2
		case "at":
			row[i] = time.Unix(n*3600, 0).UTC()
		case "every":
			row[i] = time.Duration(n) * time.Minute
		case "info":
			row[i] = map[string]any{"n": n, "word": words[n%10]}
		case "bytes":
			row[i] = []byte{byte(n), byte(n >> 8)}
		}
	}
	return row, nil
}

func (s *numbers) Close() error     { return nil }
func (s *numbers) Describe() string { return s.desc }

func importSchema(s *fdw.ServerInfo, remote string) ([]string, error) {
	return []string{fmt.Sprintf("CREATE FOREIGN TABLE %s (n bigint, square bigint, even bool, word text) SERVER %s",
		fdw.QuoteIdent(remote), fdw.QuoteIdent(s.Name))}, nil
}
