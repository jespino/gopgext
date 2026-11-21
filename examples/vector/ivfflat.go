package main

// IVFFlat: an approximate nearest neighbor index (like pgvector's), with
// gopgext/am. CREATE INDEX clusters the rows with k-means into `lists`
// lists, each with its centroid; a scan computes the exact distance to the
// rows of the `vector.ivfflat_probes` lists whose centroids are nearest to
// the query, and returns them nearest first. With probes = lists, it's an
// exact search.
//
// Storage (gopgext/am records):
//   - meta: "IVF1", dimensions (int32), lists (int32), centroids chain
//     (int64), NULLs chain (int64)
//   - centroids chain: per list, its chain (int64) and centroid (float32s)
//   - list chains: per row, its TID (6 bytes, 2 unused) and vector
//   - NULLs chain: the TIDs of rows whose vector is NULL (so that scans
//     without ORDER BY, and ORDER BY without LIMIT, see every row)

import (
	"encoding/binary"
	"math"
	"math/rand"
	"slices"

	"gopgext/am"
	"gopgext/pg"
)

var ivfflatProbes = pg.NewIntSetting("ivfflat_probes", 1, 1, 32768,
	"Number of IVFFlat lists to probe (more: better recall, slower).")

func registerIVFFlat() {
	am.Register("ivfflat", am.Method{
		Options: []am.Option{{Name: "lists", Desc: "Number of inverted lists.", Default: 100, Min: 1, Max: 32768}},
		OrderBy: true,
		Build:   ivfBuild,
		Insert:  ivfInsert,
		Vacuum:  ivfVacuum,
		Cost:    ivfCost,
		Scan:    ivfScan,
	})
}

// metric is the distance of an operator class.
type metric int

const (
	metricL2 metric = iota
	metricIP
	metricCosine
)

func metricOf(idx *am.Index) (metric, error) {
	switch f := idx.OpFamily(0); f {
	case "vector_l2_ops":
		return metricL2, nil
	case "vector_ip_ops":
		return metricIP, nil
	case "vector_cosine_ops":
		return metricCosine, nil
	default:
		return 0, pg.Errorf(pg.CodeFeatureNotSupported, "unknown ivfflat operator class %q", f)
	}
}

// distance is the operator's value (<->, <#> or <=>): what ORDER BY sorts by.
func (m metric) distance(a, b Vector) (float64, error) {
	switch m {
	case metricIP:
		return VectorNegativeInnerProduct(a, b)
	case metricCosine:
		return CosineDistance(a, b)
	}
	return L2Distance(a, b)
}

// prepare maps a vector to the space centroids live in (unit vectors for
// cosine, where the nearest centroid by L2 is the nearest by angle).
func (m metric) prepare(xs []float32) []float32 {
	if m != metricCosine {
		return xs
	}
	var n float64
	for _, x := range xs {
		n += float64(x) * float64(x)
	}
	if n == 0 {
		return xs
	}
	n = math.Sqrt(n)
	out := make([]float32, len(xs))
	for i, x := range xs {
		out[i] = float32(float64(x) / n)
	}
	return out
}

// toCentroid is the distance from a (prepared) vector to a centroid, to
// assign rows to lists and choose the lists to probe.
func (m metric) toCentroid(x, c []float32) float32 {
	var s float32
	if m == metricIP {
		for i := range x {
			s -= x[i] * c[i]
		}
		return s
	}
	for i := range x {
		d := x[i] - c[i]
		s += d * d
	}
	return s
}

// ---- Meta and lists ------------------------------------------------------

type ivfList struct {
	chain    am.ChainID
	centroid []float32
}

type ivfMeta struct {
	dims      int
	centroids am.ChainID
	nulls     am.ChainID
	lists     []ivfList
}

func (m *ivfMeta) encode() []byte {
	b := []byte("IVF1")
	b = binary.LittleEndian.AppendUint32(b, uint32(m.dims))
	b = binary.LittleEndian.AppendUint32(b, uint32(len(m.lists)))
	b = binary.LittleEndian.AppendUint64(b, uint64(m.centroids))
	return binary.LittleEndian.AppendUint64(b, uint64(m.nulls))
}

func encodeCentroid(chain am.ChainID, c []float32) []byte {
	b := binary.LittleEndian.AppendUint64(nil, uint64(chain))
	for _, x := range c {
		b = binary.LittleEndian.AppendUint32(b, math.Float32bits(x))
	}
	return b
}

// Centroids are cached per backend, by index storage and meta blob.
type ivfCache struct {
	raw  []byte
	meta *ivfMeta
}

var ivfCaches = map[int64]*ivfCache{}

func readMeta(idx *am.Index) (*ivfMeta, error) {
	raw := idx.Meta()
	if c := ivfCaches[idx.ID()]; c != nil && string(c.raw) == string(raw) {
		return c.meta, nil
	}
	if len(raw) != 28 || string(raw[:4]) != "IVF1" {
		return nil, pg.Errorf("XX002", "index is not a valid ivfflat index")
	}
	m := &ivfMeta{
		dims:      int(binary.LittleEndian.Uint32(raw[4:])),
		centroids: am.ChainID(binary.LittleEndian.Uint64(raw[12:])),
		nulls:     am.ChainID(binary.LittleEndian.Uint64(raw[20:])),
	}
	for _, rec := range idx.Records(m.centroids) {
		c := make([]float32, (len(rec)-8)/4)
		for i := range c {
			c[i] = math.Float32frombits(binary.LittleEndian.Uint32(rec[8+4*i:]))
		}
		m.lists = append(m.lists, ivfList{am.ChainID(binary.LittleEndian.Uint64(rec)), c})
	}
	ivfCaches[idx.ID()] = &ivfCache{raw, m}
	return m, nil
}

// nearest returns the indexes of the n lists nearest to x.
func (m *ivfMeta) nearest(mt metric, x []float32, n int) []int {
	type cand struct {
		i int
		d float32
	}
	cs := make([]cand, len(m.lists))
	for i, l := range m.lists {
		cs[i] = cand{i, mt.toCentroid(x, l.centroid)}
	}
	slices.SortFunc(cs, func(a, b cand) int {
		switch {
		case a.d < b.d:
			return -1
		case a.d > b.d:
			return 1
		}
		return a.i - b.i
	})
	out := make([]int, 0, n)
	for _, c := range cs[:min(n, len(cs))] {
		out = append(out, c.i)
	}
	return out
}

func entry(tid am.TID, v Vector) []byte {
	b := append(tid.Bytes(), 0, 0)
	return append(b, v...)
}

// ---- Build, insert, vacuum -------------------------------------------------

func indexedVector(vs []am.Value) (Vector, bool) {
	if vs[0].IsNull() {
		return nil, false
	}
	return Vector(vs[0].View()), true
}

func ivfBuild(idx *am.Index, rows am.Rows) (int, error) {
	mt, err := metricOf(idx)
	if err != nil {
		return 0, err
	}
	nlists := idx.Option("lists")

	// 1. A sample of the rows (reservoir sampling), for k-means.
	maxSamples := max(nlists*50, 1000)
	rng := rand.New(rand.NewSource(42))
	var samples [][]float32
	dims, seen := 0, 0
	for _, vs := range rows {
		v, ok := indexedVector(vs)
		if !ok {
			continue
		}
		if dims == 0 {
			dims = v.Dim()
		} else if v.Dim() != dims {
			return 0, dataError("expected %d dimensions, not %d", dims, v.Dim())
		}
		seen++
		if len(samples) < maxSamples {
			samples = append(samples, mt.prepare(v.Values()))
		} else if j := rng.Intn(seen); j < maxSamples {
			samples[j] = mt.prepare(v.Values())
		}
		if err := pg.CheckInterrupts(); err != nil {
			return 0, err
		}
	}
	centroids := kmeans(samples, nlists, mt, rng)

	// 2. The lists, and the rows in them.
	meta := &ivfMeta{dims: dims, centroids: idx.NewChain(), nulls: idx.NewChain()}
	for _, c := range centroids {
		l := ivfList{idx.NewChain(), c}
		idx.Append(meta.centroids, encodeCentroid(l.chain, c))
		meta.lists = append(meta.lists, l)
	}
	idx.SetMeta(meta.encode())
	n := 0
	for tid, vs := range rows {
		v, ok := indexedVector(vs)
		if !ok {
			idx.Append(meta.nulls, tid.Bytes())
		} else {
			l := meta.lists[meta.nearest(mt, mt.prepare(v.Values()), 1)[0]]
			idx.Append(l.chain, entry(tid, v))
		}
		n++
	}
	return n, nil
}

// kmeans clusters the samples in k clusters (k-means++ seeding, then
// Lloyd's iterations).
func kmeans(samples [][]float32, k int, mt metric, rng *rand.Rand) [][]float32 {
	if len(samples) <= k {
		return samples
	}
	sq := metricL2 // seeding and clustering use squared L2 distances
	centroids := [][]float32{slices.Clone(samples[rng.Intn(len(samples))])}
	dist := make([]float64, len(samples))
	for i := range dist {
		dist[i] = math.Inf(1)
	}
	for len(centroids) < k {
		last := centroids[len(centroids)-1]
		var sum float64
		for i, s := range samples {
			dist[i] = min(dist[i], float64(sq.toCentroid(s, last)))
			sum += dist[i]
		}
		r := rng.Float64() * sum
		pick := len(samples) - 1
		for i, d := range dist {
			if r -= d; r <= 0 {
				pick = i
				break
			}
		}
		centroids = append(centroids, slices.Clone(samples[pick]))
	}
	dims := len(samples[0])
	assign := make([]int, len(samples))
	for iter := 0; iter < 10; iter++ {
		changed := false
		for i, s := range samples {
			best, bestD := 0, float32(math.Inf(1))
			for j, c := range centroids {
				if d := sq.toCentroid(s, c); d < bestD {
					best, bestD = j, d
				}
			}
			if assign[i] != best || iter == 0 {
				changed = changed || assign[i] != best
				assign[i] = best
			}
		}
		sums := make([][]float64, k)
		counts := make([]int, k)
		for i, s := range samples {
			j := assign[i]
			if sums[j] == nil {
				sums[j] = make([]float64, dims)
			}
			for d, x := range s {
				sums[j][d] += float64(x)
			}
			counts[j]++
		}
		for j := range centroids {
			if counts[j] == 0 {
				continue // keep an empty cluster's centroid
			}
			for d := range centroids[j] {
				centroids[j][d] = float32(sums[j][d] / float64(counts[j]))
			}
			centroids[j] = mt.prepare(centroids[j])
		}
		if !changed && iter > 0 {
			break
		}
	}
	return centroids
}

func ivfInsert(idx *am.Index, tid am.TID, vs []am.Value) error {
	mt, err := metricOf(idx)
	if err != nil {
		return err
	}
	meta, err := readMeta(idx)
	if err != nil {
		return err
	}
	v, ok := indexedVector(vs)
	if !ok {
		idx.Append(meta.nulls, tid.Bytes())
		return nil
	}
	if len(meta.lists) == 0 {
		// Built on a table without vectors: one list, centered on the
		// first one.
		c := mt.prepare(v.Values())
		l := ivfList{idx.NewChain(), c}
		idx.Append(meta.centroids, encodeCentroid(l.chain, c))
		grown := *meta
		grown.dims, grown.lists = v.Dim(), []ivfList{l}
		idx.SetMeta(grown.encode())
		idx.Append(l.chain, entry(tid, v))
		return nil
	}
	if v.Dim() != meta.dims {
		return dataError("expected %d dimensions, not %d", meta.dims, v.Dim())
	}
	l := meta.lists[meta.nearest(mt, mt.prepare(v.Values()), 1)[0]]
	idx.Append(l.chain, entry(tid, v))
	return nil
}

func ivfVacuum(idx *am.Index, dead func(am.TID) bool) (removed, remaining int, err error) {
	meta, err := readMeta(idx)
	if err != nil {
		return 0, 0, err
	}
	chains := []am.ChainID{meta.nulls}
	for _, l := range meta.lists {
		chains = append(chains, l.chain)
	}
	for _, chain := range chains {
		for id, rec := range idx.Records(chain) {
			if dead(am.TIDFromBytes(rec)) {
				idx.Delete(id)
				removed++
			} else {
				remaining++
			}
		}
	}
	return removed, remaining, nil
}

// ---- Scans -------------------------------------------------------------------

func probes(meta *ivfMeta) int { return min(ivfflatProbes.Get(), max(len(meta.lists), 1)) }

func ivfCost(idx *am.Index) float64 {
	meta, err := readMeta(idx)
	if err != nil || len(meta.lists) == 0 {
		return 1
	}
	return float64(probes(meta)) / float64(len(meta.lists))
}

func ivfScan(idx *am.Index, keys, orderBys []am.ScanKey) (am.Scanner, error) {
	if len(keys) == 0 && len(orderBys) == 0 {
		// A full scan (the planner only picks it when nothing else is
		// allowed): every indexed row, in no order.
		meta, err := readMeta(idx)
		if err != nil {
			return nil, err
		}
		return am.Results(meta.all(idx, false)), nil
	}
	if len(keys) > 0 || len(orderBys) != 1 || orderBys[0].Strategy != 1 {
		return nil, pg.Errorf(pg.CodeFeatureNotSupported, "ivfflat indexes only support ORDER BY one distance")
	}
	mt, err := metricOf(idx)
	if err != nil {
		return nil, err
	}
	meta, err := readMeta(idx)
	if err != nil {
		return nil, err
	}
	if orderBys[0].Arg.IsNull() {
		return am.Results(meta.all(idx, true)), nil // all distances are NULL
	}
	q := Vector(orderBys[0].Arg.Bytes())
	if len(meta.lists) > 0 && q.Dim() != meta.dims {
		return nil, dataError("different vector dimensions %d and %d", meta.dims, q.Dim())
	}
	var rs []am.Result
	for _, li := range meta.nearest(mt, mt.prepare(q.Values()), probes(meta)) {
		for _, rec := range idx.Records(meta.lists[li].chain) {
			d, err := mt.distance(Vector(rec[8:]), q)
			if err != nil {
				return nil, err
			}
			rs = append(rs, am.Result{TID: am.TIDFromBytes(rec), Distances: []float64{d}})
		}
	}
	// Nearest first; NaN (cosine of a zero vector) last, as in ORDER BY.
	slices.SortStableFunc(rs, func(a, b am.Result) int {
		x, y := a.Distances[0], b.Distances[0]
		switch {
		case x < y || (!math.IsNaN(x) && math.IsNaN(y)):
			return -1
		case x > y || (math.IsNaN(x) && !math.IsNaN(y)):
			return 1
		}
		return 0
	})
	// Rows without a vector come last, with a NULL distance.
	for _, rec := range idx.Records(meta.nulls) {
		rs = append(rs, am.Result{TID: am.TIDFromBytes(rec), NullDistance: true})
	}
	return am.Results(rs), nil
}

// all returns every indexed row, in no particular order.
func (m *ivfMeta) all(idx *am.Index, nullDistance bool) []am.Result {
	var rs []am.Result
	chains := []am.ChainID{m.nulls}
	for _, l := range m.lists {
		chains = append(chains, l.chain)
	}
	for _, chain := range chains {
		for _, rec := range idx.Records(chain) {
			rs = append(rs, am.Result{TID: am.TIDFromBytes(rec), NullDistance: nullDistance})
		}
	}
	return rs
}
