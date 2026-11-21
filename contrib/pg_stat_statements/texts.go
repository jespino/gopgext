package main

import (
	"slices"
	"strconv"
	"strings"

	"gopgext/hooks"
)

// Query texts are kept in a shared memory area (the C original appends
// them to a file), textSpacePerEntry bytes per entry: appended at the end
// (Extent), and compacted by gcTexts once enough of them belong to
// deallocated entries, or when the area is full.
const textSpacePerEntry = 2048

// textStore appends a text with the shared lock held; it returns its
// offset and the garbage collection count it belongs to, or false if the
// area is full.
func textStore(text string) (off int64, gcCount int64, ok bool) {
	area := texts.Slice()
	g := globals.Ptr()
	g.Mutex.Lock()
	off, gcCount = g.Extent, g.GCCount
	ok = off+int64(len(text)) <= int64(len(area))
	if ok {
		g.Extent += int64(len(text))
	}
	g.Mutex.Unlock()
	if ok {
		copy(area[off:], text) // our own range: no one else writes it
	}
	return off, gcCount, ok
}

// textStoreExclusive stores a text with the exclusive lock held,
// garbage collecting the area if it's full.
func textStoreExclusive(text string) (int64, bool) {
	if off, _, ok := textStore(text); ok {
		return off, true
	}
	gcTexts()
	off, _, ok := textStore(text)
	return off, ok
}

// textFetch returns the text of an entry ("", false if lost). A lock must
// be held.
func textFetch(e *entry) (string, bool) {
	area := texts.Slice()
	if e.TextLen < 0 || e.TextOffset < 0 || e.TextOffset+e.TextLen > int64(len(area)) {
		return "", false
	}
	return string(area[e.TextOffset : e.TextOffset+e.TextLen]), true
}

// needGC reports whether texts of deallocated entries likely take half of
// the used area (and it's larger than 512 bytes per entry), as the
// original decides for its file.
func needGC() bool {
	g := globals.Ptr()
	g.Mutex.Lock()
	extent := g.Extent
	g.Mutex.Unlock()
	return extent >= 512*int64(maxStatements) && extent >= g.MeanQueryLen*int64(maxStatements)*2
}

// gcTexts compacts the texts area, keeping the texts of the entries. The
// exclusive lock must be held.
func gcTexts() {
	var live []*entry
	for _, e := range table.All() {
		if e.TextLen >= 0 {
			live = append(live, e)
		}
	}
	slices.SortFunc(live, func(a, b *entry) int { return int(a.TextOffset - b.TextOffset) })
	area := texts.Slice()
	pos := int64(0)
	for _, e := range live {
		if e.TextOffset+e.TextLen > int64(len(area)) {
			e.TextLen = -1 // shouldn't happen
			continue
		}
		copy(area[pos:], area[e.TextOffset:e.TextOffset+e.TextLen])
		e.TextOffset = pos
		pos += e.TextLen
	}
	g := globals.Ptr()
	g.Mutex.Lock()
	g.Extent = pos
	g.GCCount++
	g.Mutex.Unlock()
}

// normalize replaces the constants of a statement with $n parameters
// (numbered after the statement's own), as the original's
// generate_normalized_query: "SELECT * FROM t WHERE a = 1" becomes
// "SELECT * FROM t WHERE a = $1".
func normalize(text string, cs *hooks.Constants) string {
	var b strings.Builder
	b.Grow(len(text) + 10*len(cs.List))
	pos, n := 0, 0
	for _, c := range cs.List {
		// Parameters are kept as written, unless lists were squashed.
		if c.Param && !cs.Squashed {
			continue
		}
		if c.Location < pos || c.Location+c.Length > len(text) {
			break // not a constant of this text: keep the rest as is
		}
		b.WriteString(text[pos:c.Location])
		n++
		b.WriteString("$" + strconv.Itoa(n+cs.HighestParam))
		if c.Squashed {
			b.WriteString(" /*, ... */")
		}
		pos = c.Location + c.Length
	}
	b.WriteString(text[pos:])
	return b.String()
}

func sortItems[T any](xs []T, less func(a, b T) bool) {
	slices.SortStableFunc(xs, func(a, b T) int {
		switch {
		case less(a, b):
			return -1
		case less(b, a):
			return 1
		}
		return 0
	})
}
