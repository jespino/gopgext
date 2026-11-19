// inventory shows gopgext's database APIs: queries through SPI (with error
// handling), settings, logging, nested calls and query cancellation. Its
// table is created by sql/schema.sql, part of the install script.
package main

import (
	"errors"
	"fmt"
	"iter"
	"strings"
	"time"

	"gopgext/pg"
)

// Settings: inventory.greeting, inventory.low_stock, ...
var (
	greeting   = pg.NewStringSetting("greeting", "Hello", "Greeting used by greet().")
	lowStock   = pg.NewIntSetting("low_stock", 5, 0, 1000000, "Quantity below which items are low on stock.")
	logActions = pg.NewBoolSetting("log_actions", false, "Log what inventory functions do.")
	markup     = pg.NewFloatSetting("markup", 1.25, 1, 10, "Price multiplier used by price_of().", pg.Superuser)
)

var loads int

func main() {
	loads++
	pg.Debugf("inventory loaded at %s", time.Now().Format(time.RFC3339))
}

//gopgext:function stable
func Greet(name string) string { return greeting.Get() + ", " + name + "!" }

// AddItem inserts an item and returns its id.
//
//gopgext:function
func AddItem(name string, qty int32, price *float64) (int64, error) {
	var id int64
	err := pg.QueryRow("INSERT INTO items (name, qty, price) VALUES ($1, $2, $3) RETURNING id",
		name, qty, price).Scan(&id)
	if err == nil && logActions.Get() {
		pg.Noticef("added %s (id %d)", name, id)
	}
	return id, err
}

// Restock adds qty to an item; an unknown item is created. A failed
// statement (unique violation) is rolled back and handled in Go.
//
//gopgext:function
func Restock(name string, qty int32) (string, error) {
	_, err := pg.Exec("INSERT INTO items (name, qty) VALUES ($1, $2)", name, qty)
	var pe *pg.Error
	switch {
	case errors.As(err, &pe) && pe.Code == pg.CodeUniqueViolation:
		var total int32
		err := pg.QueryRow("UPDATE items SET qty = qty + $2 WHERE name = $1 RETURNING qty", name, qty).Scan(&total)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("restocked %s: %d", name, total), nil
	case err != nil:
		return "", err
	}
	return "new item " + name, nil
}

// LowStock lists items with a quantity below inventory.low_stock.
//
//gopgext:function stable
func LowStock() (iter.Seq[string], error) {
	rows, err := pg.Query("SELECT name, qty FROM items WHERE qty < $1 ORDER BY name", int32(lowStock.Get()))
	if err != nil {
		return nil, err
	}
	var out []string
	for rows.Next() {
		var name string
		var qty int64
		if err := rows.Scan(&name, &qty); err != nil {
			return nil, err
		}
		out = append(out, fmt.Sprintf("%s (%d)", name, qty))
	}
	return func(yield func(string) bool) {
		for _, s := range out {
			if !yield(s) {
				return
			}
		}
	}, nil
}

// PriceOf returns an item's price with the markup, NULL if it has none.
//
//gopgext:function stable
func PriceOf(name string) (*float64, error) {
	var price *float64
	if err := pg.QueryRow("SELECT price FROM items WHERE name = $1", name).Scan(&price); err != nil {
		if errors.Is(err, pg.ErrNoRows) {
			return nil, pg.Errorf(pg.CodeNoDataFound, "no item %q", name)
		}
		return nil, err
	}
	if price != nil {
		p := *price * markup.Get()
		price = &p
	}
	return price, nil
}

// Render runs a query and formats its rows: values of any type, NULLs and
// column names.
//
//gopgext:function
func Render(sql string) (string, error) {
	rows, err := pg.Query(sql)
	if err != nil {
		return "", err
	}
	var sb strings.Builder
	sb.WriteString(strings.Join(rows.Columns(), "|"))
	for rows.Next() {
		sb.WriteByte('\n')
		for i, v := range rows.Values() {
			if i > 0 {
				sb.WriteByte('|')
			}
			if v == nil {
				sb.WriteString("NULL")
			} else {
				fmt.Fprint(&sb, v)
			}
		}
	}
	return sb.String(), nil
}

// Run executes sql and returns its error unchanged: PostgreSQL re-raises
// the original error (SQLSTATE, position, context, ...).
//
//gopgext:function
func Run(sql string) error {
	_, err := pg.Exec(sql)
	return err
}

// WrapError returns a wrapped error: raised with the original SQLSTATE and
// the wrapped message.
//
//gopgext:function
func WrapError(sql string) error {
	if _, err := pg.Exec(sql); err != nil {
		return fmt.Errorf("wrap_error: %w", err)
	}
	return nil
}

// TryWrite tries to write from a STABLE function, which SPI refuses.
//
//gopgext:function stable
func TryWrite() string {
	_, err := pg.Exec("INSERT INTO items (name, qty) VALUES ('nope', 1)")
	return fmt.Sprint(err)
}

// Depth calls itself through SQL n times: Go -> SPI -> Go -> ...
//
//gopgext:function
func Depth(n int32) (int64, error) {
	if n == 0 {
		return 0, nil
	}
	var d int64
	if err := pg.QueryRow("SELECT depth($1)", n-1).Scan(&d); err != nil {
		return 0, err
	}
	return d + 1, nil
}

// Busy loops for up to seconds, checking for cancellation.
//
//gopgext:function
func Busy(seconds float64) (int64, error) {
	deadline := time.Now().Add(time.Duration(seconds * float64(time.Second)))
	var n int64
	for time.Now().Before(deadline) {
		n++
		if n%1000 == 0 {
			if err := pg.CheckInterrupts(); err != nil {
				return n, err
			}
		}
	}
	return n, nil
}

//gopgext:function
func Sleep(ms int32) int64 {
	start := time.Now()
	time.Sleep(time.Duration(ms) * time.Millisecond)
	return time.Since(start).Milliseconds()
}

//gopgext:function stable
func Loads() int32 { return int32(loads) }

// DefineLate tries to define a setting after load, which panics.
//
//gopgext:function
func DefineLate() (msg string) {
	defer func() { msg = fmt.Sprint(recover()) }()
	pg.NewBoolSetting("late", false, "too late")
	return "defined"
}
