package main

import (
	"encoding/binary"
	"errors"
	"io/fs"
	"unsafe"

	"gopgext/pg"
	"gopgext/shmem"
)

// The statistics are saved in this file at shutdown (pg_stat_statements.save)
// and loaded at the next start. The format is this port's own: a header,
// the global statistics, then each entry's key, entry and text.
const dumpFile = "pg_stat/gopgext_pg_stat_statements.stat"

// dumpHeader identifies the format: it changes with the layout of the
// structs, which are written as they are in memory.
var dumpHeader = [4]uint64{0x7373677067707367, uint64(unsafe.Sizeof(key{})),
	uint64(unsafe.Sizeof(entry{})), uint64(unsafe.Sizeof(global{}))}

func bytesOf[T any](p *T) []byte { return unsafe.Slice((*byte)(unsafe.Pointer(p)), unsafe.Sizeof(*p)) }

// dump saves the statistics (in the postmaster, at shutdown).
func dump() {
	if !save.Get() {
		return
	}
	var b []byte
	for _, h := range dumpHeader {
		b = binary.LittleEndian.AppendUint64(b, h)
	}
	g := *globals.Ptr()
	g.Mutex = shmem.SpinLock{}
	b = append(b, bytesOf(&g)...)
	for k, e := range table.All() {
		text, ok := textFetch(e)
		if !ok {
			continue
		}
		v := *e
		v.Mutex = shmem.SpinLock{}
		b = append(b, bytesOf(&k)...)
		b = append(b, bytesOf(&v)...)
		b = append(b, text...)
	}
	if err := pg.WriteFile(dumpFile, b); err != nil {
		pg.Log("could not write pg_stat_statements file: " + err.Error())
	}
}

// load restores the statistics saved by dump (in the postmaster, when the
// shared memory is created), then removes the file so that a crash
// doesn't restore them again.
func load() {
	g := globals.Ptr()
	g.CurMedianUsage = assumedMedianInit
	g.MeanQueryLen = assumedLengthInit
	g.StatsReset = now()
	if !save.Get() {
		pg.RemoveFile(dumpFile)
		return
	}
	b, err := pg.ReadFile(dumpFile)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			pg.Log("could not read pg_stat_statements file: " + err.Error())
		}
		return
	}
	defer pg.RemoveFile(dumpFile)
	for i, h := range dumpHeader {
		if len(b) < 8*(i+1) || binary.LittleEndian.Uint64(b[8*i:]) != h {
			pg.Log("ignoring invalid data in pg_stat_statements file \"" + dumpFile + "\"")
			return
		}
	}
	b = b[8*len(dumpHeader):]
	var saved global
	if !take(&b, &saved) {
		return
	}
	g.Dealloc, g.StatsReset = saved.Dealloc, saved.StatsReset
	for len(b) > 0 {
		var k key
		var e entry
		if !take(&b, &k) || !take(&b, &e) || e.TextLen < 0 || int64(len(b)) < e.TextLen {
			pg.Log("ignoring invalid data in pg_stat_statements file \"" + dumpFile + "\"")
			return
		}
		text := string(b[:e.TextLen])
		b = b[e.TextLen:]
		if table.Len() >= maxStatements {
			entryDealloc()
		}
		off, ok := textStoreExclusive(text)
		if !ok {
			continue
		}
		p := entryAlloc(k, off, len(text), false)
		if p == nil {
			continue
		}
		p.Counters = e.Counters
		p.StatsSince, p.MinMaxStatsSince = e.StatsSince, e.MinMaxStatsSince
	}
}

// take reads a T from the front of *b.
func take[T any](b *[]byte, v *T) bool {
	n := int(unsafe.Sizeof(*v))
	if len(*b) < n {
		return false
	}
	copy(bytesOf(v), *b)
	*b = (*b)[n:]
	return true
}
