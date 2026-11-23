// Ticker: a background worker (gopgext/bgworker) that records a tick in a
// table every ticker.interval, or as soon as a backend wakes it up with
// ticker_wake().
package main

import (
	"time"

	"gopgext/bgworker"
	"gopgext/pg"
)

var (
	database = pg.NewStringSetting("database", "postgres", "Database the ticker worker connects to.", pg.Postmaster)
	interval = pg.NewIntSetting("interval", 60000, 10, 3600000, "Time between ticks.", pg.Sighup, pg.UnitMilliseconds)
	worker   *bgworker.Handle
)

func main() {
	if !pg.Preloading() {
		return
	}
	worker = bgworker.Register(bgworker.Worker{
		Name:     "ticker",
		Database: database.Get(),
		Restart:  time.Second,
		Main:     tick,
	})
}

func tick(w *bgworker.Context) error {
	n := 0
	for w.Wait(time.Duration(interval.Get()) * time.Millisecond) {
		n++
		err := w.Transaction(func() error {
			// The table is created by the extension: it may not exist yet.
			var exists bool
			if err := pg.QueryRow("SELECT to_regclass('ticker_ticks') IS NOT NULL").Scan(&exists); err != nil || !exists {
				return err
			}
			_, err := pg.Exec("INSERT INTO ticker_ticks (n) VALUES ($1)", n)
			return err
		}, "ticker: tick")
		if err != nil {
			pg.Warningf("ticker: %v", err)
		}
	}
	// Shutting down: Wait returned false.
	pg.Logf("ticker: stopping after %d ticks", n)
	return nil
}

// TickerWake wakes the ticker worker up: it ticks at once.
//
//gopgext:function
func TickerWake() error {
	if worker == nil {
		return pg.Errorf("55000", "ticker must be loaded via shared_preload_libraries")
	}
	worker.Wake()
	return nil
}

// TickerPid returns the process ID of the ticker worker (0: not running).
//
//gopgext:function
func TickerPid() int32 {
	if worker == nil {
		return 0
	}
	return int32(worker.PID())
}

// TickerCount returns the number of ticks recorded.
//
//gopgext:function
func TickerCount() (int64, error) {
	var n int64
	err := pg.QueryRow("SELECT count(*) FROM ticker_ticks").Scan(&n)
	return n, err
}
