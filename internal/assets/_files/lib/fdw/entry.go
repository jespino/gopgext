package fdw

// Entry points called by the C side (frameworks/fdw/fdw.c) through
// gopgext_invoke, and the parsing of what it sends: "key=value" items.

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"gopgext/fmgr"
	"gopgext/pg"
)

func noWrapper() error {
	return pg.Errorf("HV000", "extension %q has no foreign data wrapper: call fdw.Register in main", pg.ExtensionName())
}

func splitItem(item string) (string, string) {
	k, v, _ := strings.Cut(item, "=")
	return k, v
}

// parseTable parses a table description, and returns the other items.
func parseTable(items []string) (*Table, []string) {
	t := &Table{Server: &ServerInfo{Options: map[string]string{}, User: map[string]string{}},
		Options: map[string]string{}}
	var rest []string
	for _, it := range items {
		k, v := splitItem(it)
		switch {
		case k == "table":
			t.Name = v
		case k == "server":
			t.Server.Name = v
		case strings.HasPrefix(k, "sopt."):
			t.Server.Options[k[5:]] = v
		case strings.HasPrefix(k, "topt."):
			t.Options[k[5:]] = v
		case strings.HasPrefix(k, "uopt."):
			t.Server.User[k[5:]] = v
		case k == "col":
			f := strings.SplitN(v, "\x1f", 3)
			c := ColumnInfo{Options: map[string]string{}}
			if len(f) == 3 {
				c.Name, c.Type, c.Dropped = f[0], f[1], f[2] == "1"
			}
			t.Columns = append(t.Columns, c)
		case strings.HasPrefix(k, "copt."):
			idx, name, _ := strings.Cut(k[5:], ".")
			if i, err := strconv.Atoi(idx); err == nil && i >= 0 && i < len(t.Columns) {
				t.Columns[i].Options[name] = v
			}
		default:
			rest = append(rest, it)
		}
	}
	return t, rest
}

var kinds = map[string]Kind{"text": Text, "int": Int, "float": Float, "bool": Bool, "time": Time, "interval": Interval}

func parseCondition(v string) (Condition, []string, bool) {
	f := strings.Split(v, "\x1f")
	if len(f) < 3 {
		return Condition{}, nil, false
	}
	col, err := strconv.Atoi(f[0])
	if err != nil {
		return Condition{}, nil, false
	}
	return Condition{Column: col, Op: f[1], Kind: kinds[f[2]]}, f[3:], true
}

// ---- Validation -----------------------------------------------------

var whereNames = map[string]Where{"server": OnServer, "table": OnTable, "column": OnColumn,
	"user_mapping": OnUserMapping, "wrapper": OnWrapper}

func validate(items []string) error {
	if wrapper == nil {
		return noWrapper()
	}
	var where Where
	opts := map[string]string{}
	var order []string
	for _, it := range items {
		k, v := splitItem(it)
		if k == "catalog" {
			where = whereNames[v]
		} else if name, ok := strings.CutPrefix(k, "opt."); ok {
			opts[name] = v
			order = append(order, name)
		}
	}
	valid := map[string]*Option{}
	var names []string
	for i := range wrapper.Options {
		o := &wrapper.Options[i]
		if o.Where&where != 0 {
			valid[o.Name] = o
			names = append(names, o.Name)
		}
	}
	sort.Strings(names)
	for _, name := range order {
		o := valid[name]
		if o == nil {
			hint := "There are no valid options in this context."
			if len(names) > 0 {
				hint = "Valid options in this context are: " + strings.Join(names, ", ")
			}
			return &pg.Error{Code: "HV00D", Message: fmt.Sprintf("invalid option %q", name), Hint: hint}
		}
		if o.Check != nil {
			if err := o.Check(opts[name]); err != nil {
				return pg.Errorf("HV024", "invalid value for option %q: %v", name, err)
			}
		}
	}
	for _, o := range wrapper.Options {
		if o.Required&where != 0 {
			if _, ok := opts[o.Name]; !ok {
				return pg.Errorf("HV00J", "option %q is required", o.Name)
			}
		}
	}
	return nil
}

//export gopgext_go_fdw_validate
func goValidate(c *fmgr.Call) {
	defer c.End(c.Begin(false))
	if err := validate(fmgr.ArgArray[string](c, 0, "options")); err != nil {
		c.Fail(err)
	}
}

// ---- Planning -------------------------------------------------------

func plan(items []string) []string {
	if wrapper == nil {
		panic(noWrapper())
	}
	t, rest := parseTable(items)
	var conds []Condition
	for _, it := range rest {
		k, v := splitItem(it)
		if k == "cond" {
			if c, _, ok := parseCondition(v); ok && c.Kind != 0 {
				conds = append(conds, c)
			}
		}
	}
	if wrapper.Plan == nil {
		return []string{"rows=1000"}
	}
	p, err := wrapper.Plan(t, conds)
	out := []string{"rows=" + strconv.FormatFloat(p.Rows, 'f', -1, 64)}
	if p.Rows <= 0 {
		out[0] = "rows=1000"
	}
	if err != nil {
		out = append(out, "error="+err.Error())
	}
	for i, pushed := range p.Pushed {
		if pushed && i < len(conds) {
			out = append(out, "push="+strconv.Itoa(i))
		}
	}
	return out
}

//export gopgext_go_fdw_plan
func goPlan(c *fmgr.Call) {
	defer c.End(c.Begin(true))
	fmgr.ReturnArray(c, plan(fmgr.ArgArray[string](c, 0, "items")))
}

// ---- Scans ----------------------------------------------------------

type scan struct {
	ncols int
	rows  Rows
	done  bool
}

var (
	scans  = map[int64]*scan{}
	lastID int64
)

func parseValue(c Condition, rest []string) Value {
	v := Value{Condition: c}
	if len(rest) < 2 || rest[0] == "1" {
		v.Null = true
		return v
	}
	s := rest[1]
	switch c.Kind {
	case Text:
		v.Text = s
	case Int:
		v.Int, _ = strconv.ParseInt(s, 10, 64)
	case Float:
		v.Float, _ = strconv.ParseFloat(s, 64)
	case Bool:
		v.Bool = s == "t" || s == "true"
	case Time:
		switch s {
		case "inf":
			v.Inf = 1
		case "-inf":
			v.Inf = -1
		default:
			us, _ := strconv.ParseInt(s, 10, 64)
			v.Time = time.UnixMicro(us).UTC()
		}
	case Interval:
		us, _ := strconv.ParseInt(s, 10, 64)
		v.Interval = time.Duration(us) * time.Microsecond
	}
	return v
}

func begin(items []string) (int64, error) {
	if wrapper == nil {
		return 0, noWrapper()
	}
	t, rest := parseTable(items)
	var vals []Value
	for _, it := range rest {
		k, v := splitItem(it)
		switch k {
		case "val":
			if c, more, ok := parseCondition(v); ok {
				vals = append(vals, parseValue(c, more))
			}
		case "orphan":
			id, _ := strconv.ParseInt(v, 10, 64)
			end(id)
		}
	}
	// A scan the planner had to use although Plan rejected it (no other
	// plan provides what it needs): raise Plan's error.
	if wrapper.Plan != nil {
		conds := make([]Condition, len(vals))
		for i, v := range vals {
			conds[i] = v.Condition
		}
		if _, err := wrapper.Plan(t, conds); err != nil {
			return 0, err
		}
	}
	rows, err := wrapper.Scan(t, vals)
	if err != nil {
		return 0, err
	}
	lastID++
	scans[lastID] = &scan{ncols: len(t.Columns), rows: rows}
	return lastID, nil
}

//export gopgext_go_fdw_begin
func goBegin(c *fmgr.Call) {
	defer c.End(c.Begin(false))
	id, err := begin(fmgr.ArgArray[string](c, 0, "items"))
	if err != nil {
		c.Fail(err)
		return
	}
	fmgr.Return(c, id)
}

func fetch(h int64, max int32) ([]*string, error) {
	s := scans[h]
	if s == nil || s.done {
		return nil, nil
	}
	out := make([]*string, 0, int(max)*s.ncols)
	for n := int32(0); n < max; n++ {
		row, err := s.rows.Next()
		if err != nil {
			return nil, err
		}
		if row == nil {
			s.done = true
			break
		}
		if len(row) != s.ncols {
			return nil, pg.Errorf("HV000", "foreign data wrapper returned a row of %d values for %d columns", len(row), s.ncols)
		}
		for _, v := range row {
			out = append(out, text(v))
		}
	}
	return out, nil
}

//export gopgext_go_fdw_fetch
func goFetch(c *fmgr.Call) {
	defer c.End(c.Begin(false))
	rows, err := fetch(fmgr.Arg[int64](c, 0, "handle"), fmgr.Arg[int32](c, 1, "max"))
	if err != nil {
		c.Fail(err)
		return
	}
	fmgr.ReturnArrayNullable(c, rows)
}

func end(h int64) {
	if s := scans[h]; s != nil {
		delete(scans, h)
		s.rows.Close()
	}
}

//export gopgext_go_fdw_end
func goEnd(c *fmgr.Call) {
	defer c.End(c.Begin(false))
	end(fmgr.Arg[int64](c, 0, "handle"))
}

//export gopgext_go_fdw_describe
func goDescribe(c *fmgr.Call) {
	defer c.End(c.Begin(false))
	desc := ""
	if s := scans[fmgr.Arg[int64](c, 0, "handle")]; s != nil {
		if d, ok := s.rows.(Describer); ok {
			desc = d.Describe()
		}
	}
	fmgr.Return(c, desc)
}

// ---- IMPORT FOREIGN SCHEMA ------------------------------------------

func importSchema(items []string) ([]string, error) {
	if wrapper == nil {
		return nil, noWrapper()
	}
	s := &ServerInfo{Options: map[string]string{}, User: map[string]string{}}
	var remote string
	for _, it := range items {
		k, v := splitItem(it)
		switch {
		case k == "server":
			s.Name = v
		case strings.HasPrefix(k, "sopt."):
			s.Options[k[5:]] = v
		case strings.HasPrefix(k, "uopt."):
			s.User[k[5:]] = v
		case k == "remote":
			remote = v
		}
	}
	if wrapper.Import == nil {
		return nil, pg.Errorf(pg.CodeFeatureNotSupported, "this foreign data wrapper doesn't support IMPORT FOREIGN SCHEMA")
	}
	return wrapper.Import(s, remote)
}

//export gopgext_go_fdw_import
func goImport(c *fmgr.Call) {
	defer c.End(c.Begin(false))
	stmts, err := importSchema(fmgr.ArgArray[string](c, 0, "items"))
	if err != nil {
		c.Fail(err)
		return
	}
	fmgr.ReturnArray(c, stmts)
}
