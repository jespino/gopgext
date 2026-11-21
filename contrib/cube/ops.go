package main

import "math"

// cmp is cube_cmp_v0: an order of cubes (common dimensions first, lower
// then upper bounds, extra dimensions against zero).
func cmp(a, b Cube) int32 {
	da, db := a.dim(), b.dim()
	dim := min(da, db)
	for i := range dim {
		x, y := cmin(a.ll(i), a.ur(i)), cmin(b.ll(i), b.ur(i))
		if x > y {
			return 1
		}
		if x < y {
			return -1
		}
	}
	for i := range dim {
		x, y := cmax(a.ll(i), a.ur(i)), cmax(b.ll(i), b.ur(i))
		if x > y {
			return 1
		}
		if x < y {
			return -1
		}
	}
	if da > db {
		for i := dim; i < da; i++ {
			if x := cmin(a.ll(i), a.ur(i)); x > 0 {
				return 1
			} else if x < 0 {
				return -1
			}
		}
		for i := dim; i < da; i++ {
			if x := cmax(a.ll(i), a.ur(i)); x > 0 {
				return 1
			} else if x < 0 {
				return -1
			}
		}
		return 1
	}
	if da < db {
		for i := dim; i < db; i++ {
			if x := cmin(b.ll(i), b.ur(i)); x > 0 {
				return -1
			} else if x < 0 {
				return 1
			}
		}
		for i := dim; i < db; i++ {
			if x := cmax(b.ll(i), b.ur(i)); x > 0 {
				return -1
			} else if x < 0 {
				return 1
			}
		}
		return -1
	}
	return 0
}

// CubeCmp is cube_cmp(cube, cube), the btree comparison function.
//
//gopgext:function symbol=cube_cmp manual
func CubeCmp(a, b Cube) int32 { return cmp(a, b) }

// CubeEq is cube_eq(cube, cube) (=).
//
//gopgext:function symbol=cube_eq manual
func CubeEq(a, b Cube) bool { return cmp(a, b) == 0 }

// CubeNe is cube_ne(cube, cube) (<>).
//
//gopgext:function symbol=cube_ne manual
func CubeNe(a, b Cube) bool { return cmp(a, b) != 0 }

// CubeLt is cube_lt(cube, cube) (<).
//
//gopgext:function symbol=cube_lt manual
func CubeLt(a, b Cube) bool { return cmp(a, b) < 0 }

// CubeGt is cube_gt(cube, cube) (>).
//
//gopgext:function symbol=cube_gt manual
func CubeGt(a, b Cube) bool { return cmp(a, b) > 0 }

// CubeLe is cube_le(cube, cube) (<=).
//
//gopgext:function symbol=cube_le manual
func CubeLe(a, b Cube) bool { return cmp(a, b) <= 0 }

// CubeGe is cube_ge(cube, cube) (>=).
//
//gopgext:function symbol=cube_ge manual
func CubeGe(a, b Cube) bool { return cmp(a, b) >= 0 }

// contains is cube_contains_v0: does a contain b?
func contains(a, b Cube) bool {
	da, db := a.dim(), b.dim()
	if da < db {
		// b's extra dimensions must be zero.
		for i := da; i < db; i++ {
			if b.ll(i) != 0 || b.ur(i) != 0 {
				return false
			}
		}
	}
	for i := range min(da, db) {
		if cmin(a.ll(i), a.ur(i)) > cmin(b.ll(i), b.ur(i)) {
			return false
		}
		if cmax(a.ll(i), a.ur(i)) < cmax(b.ll(i), b.ur(i)) {
			return false
		}
	}
	return true
}

// overlap is cube_overlap_v0.
func overlap(a, b Cube) bool {
	if a.dim() < b.dim() {
		a, b = b, a
	}
	for i := range b.dim() {
		if cmin(a.ll(i), a.ur(i)) > cmax(b.ll(i), b.ur(i)) {
			return false
		}
		if cmax(a.ll(i), a.ur(i)) < cmin(b.ll(i), b.ur(i)) {
			return false
		}
	}
	for i := b.dim(); i < a.dim(); i++ {
		if cmin(a.ll(i), a.ur(i)) > 0 {
			return false
		}
		if cmax(a.ll(i), a.ur(i)) < 0 {
			return false
		}
	}
	return true
}

// CubeContains is cube_contains(cube, cube) (@>).
//
//gopgext:function symbol=cube_contains manual
func CubeContains(a, b Cube) bool { return contains(a, b) }

// CubeContained is cube_contained(cube, cube) (<@).
//
//gopgext:function symbol=cube_contained manual
func CubeContained(a, b Cube) bool { return contains(b, a) }

// CubeOverlap is cube_overlap(cube, cube) (&&).
//
//gopgext:function symbol=cube_overlap manual
func CubeOverlap(a, b Cube) bool { return overlap(a, b) }

// union is cube_union_v0 (but for its shortcut for a and b being the same
// pointer, see CubeUnion): the smallest box containing both.
func union(a, b Cube) Cube {
	if a.dim() < b.dim() {
		a, b = b, a
	}
	dim := a.dim()
	r := newCube(dim, false)
	i := 0
	for ; i < b.dim(); i++ {
		r.set(i, cmin(cmin(a.ll(i), a.ur(i)), cmin(b.ll(i), b.ur(i))))
		r.set(i+dim, cmax(cmax(a.ll(i), a.ur(i)), cmax(b.ll(i), b.ur(i))))
	}
	for ; i < dim; i++ {
		r.set(i, cmin(0, cmin(a.ll(i), a.ur(i))))
		r.set(i+dim, cmax(0, cmax(a.ll(i), a.ur(i))))
	}
	return r.toPoint()
}

// inter is cube_inter: the intersection (a box with lower bounds above its
// upper bounds when they don't overlap).
func inter(a, b Cube) Cube {
	if a.dim() < b.dim() {
		a, b = b, a
	}
	dim := a.dim()
	r := newCube(dim, false)
	i := 0
	for ; i < b.dim(); i++ {
		r.set(i, cmax(cmin(a.ll(i), a.ur(i)), cmin(b.ll(i), b.ur(i))))
		r.set(i+dim, cmin(cmax(a.ll(i), a.ur(i)), cmax(b.ll(i), b.ur(i))))
	}
	for ; i < dim; i++ {
		r.set(i, cmax(0, cmin(a.ll(i), a.ur(i))))
		r.set(i+dim, cmin(0, cmax(a.ll(i), a.ur(i))))
	}
	return r.toPoint()
}

// CubeUnion is cube_union(cube, cube).
//
// Upstream returns its first argument unchanged when both arguments are
// the same pointer (e.g. cube_union(c, c) on an in-memory value); this
// always computes the union, which differs only for cubes stored with
// their corners swapped, e.g. '(2),(1)', which come out normalized.
//
//gopgext:function symbol=cube_union manual
func CubeUnion(a, b Cube) Cube { return union(a, b) }

// CubeInter is cube_inter(cube, cube).
//
//gopgext:function symbol=cube_inter manual
func CubeInter(a, b Cube) Cube { return inter(a, b) }

// size is rt_cube_size: the volume.
func size(a Cube) float64 {
	if a.isPoint() || a.dim() == 0 {
		return 0
	}
	r := 1.0
	for i := range a.dim() {
		r *= math.Abs(a.ur(i) - a.ll(i))
	}
	return r
}

// CubeSize is cube_size(cube).
//
//gopgext:function symbol=cube_size manual
func CubeSize(a Cube) float64 { return size(a) }

// distance1D is the distance between intervals [a1, a2] and [b1, b2] (in
// either order), 0 if they intersect.
func distance1D(a1, a2, b1, b2 float64) float64 {
	if a1 <= b1 && a2 <= b1 && a1 <= b2 && a2 <= b2 {
		return cmin(b1, b2) - cmax(a1, a2)
	}
	if a1 > b1 && a2 > b1 && a1 > b2 && a2 > b2 {
		return cmin(a1, a2) - cmax(b1, b2)
	}
	return 0
}

// distances calls f with the distance between a and b along each
// dimension (to zero beyond the smaller cube's dimensions).
func distances(a, b Cube, f func(d float64)) {
	if a.dim() < b.dim() {
		a, b = b, a
	}
	i := 0
	for ; i < b.dim(); i++ {
		f(distance1D(a.ll(i), a.ur(i), b.ll(i), b.ur(i)))
	}
	for ; i < a.dim(); i++ {
		f(distance1D(a.ll(i), a.ur(i), 0, 0))
	}
}

func euclid(a, b Cube) float64 {
	sum := 0.0
	distances(a, b, func(d float64) { sum += float64(d * d) }) // no FMA, like C
	return math.Sqrt(sum)
}

func taxicab(a, b Cube) float64 {
	sum := 0.0
	distances(a, b, func(d float64) { sum += math.Abs(d) })
	return sum
}

func chebyshev(a, b Cube) float64 {
	m := 0.0
	distances(a, b, func(d float64) {
		if d = math.Abs(d); d > m {
			m = d
		}
	})
	return m
}

// CubeDistance is cube_distance(cube, cube) (<->): the Euclidean distance.
//
//gopgext:function symbol=cube_distance manual
func CubeDistance(a, b Cube) float64 { return euclid(a, b) }

// DistanceTaxicab is distance_taxicab(cube, cube) (<#>).
//
//gopgext:function symbol=distance_taxicab manual
func DistanceTaxicab(a, b Cube) float64 { return taxicab(a, b) }

// DistanceChebyshev is distance_chebyshev(cube, cube) (<=>).
//
//gopgext:function symbol=distance_chebyshev manual
func DistanceChebyshev(a, b Cube) float64 { return chebyshev(a, b) }
