package main

// pgQsort is PostgreSQL's qsort (lib/sort_template.h, which replaces the C
// library's): which of equal elements comes first decides which duplicate
// key hstore keeps, so the port sorts exactly like it.
func pgQsort[T any](a []T, cmp func(x, y *T) int) {
	for {
		n := len(a)
		if n < 7 {
			for m := 1; m < n; m++ {
				for l := m; l > 0 && cmp(&a[l-1], &a[l]) > 0; l-- {
					a[l], a[l-1] = a[l-1], a[l]
				}
			}
			return
		}
		presorted := true
		for m := 1; m < n; m++ {
			if cmp(&a[m-1], &a[m]) > 0 {
				presorted = false
				break
			}
		}
		if presorted {
			return
		}
		med3 := func(x, y, z int) int {
			if cmp(&a[x], &a[y]) < 0 {
				if cmp(&a[y], &a[z]) < 0 {
					return y
				} else if cmp(&a[x], &a[z]) < 0 {
					return z
				}
				return x
			}
			if cmp(&a[y], &a[z]) > 0 {
				return y
			} else if cmp(&a[x], &a[z]) < 0 {
				return x
			}
			return z
		}
		pm := n / 2
		if n > 7 {
			pl, pn := 0, n-1
			if n > 40 {
				d := n / 8
				pl = med3(pl, pl+d, pl+2*d)
				pm = med3(pm-d, pm, pm+d)
				pn = med3(pn-2*d, pn-d, pn)
			}
			pm = med3(pl, pm, pn)
		}
		a[0], a[pm] = a[pm], a[0]
		pa, pb := 1, 1
		pc, pd := n-1, n-1
		for {
			for pb <= pc {
				r := cmp(&a[pb], &a[0])
				if r > 0 {
					break
				}
				if r == 0 {
					a[pa], a[pb] = a[pb], a[pa]
					pa++
				}
				pb++
			}
			for pb <= pc {
				r := cmp(&a[pc], &a[0])
				if r < 0 {
					break
				}
				if r == 0 {
					a[pc], a[pd] = a[pd], a[pc]
					pd--
				}
				pc--
			}
			if pb > pc {
				break
			}
			a[pb], a[pc] = a[pc], a[pb]
			pb++
			pc--
		}
		swapn := func(x, y, k int) {
			for i := 0; i < k; i++ {
				a[x+i], a[y+i] = a[y+i], a[x+i]
			}
		}
		d1 := min(pa, pb-pa)
		swapn(0, pb-d1, d1)
		d1 = min(pd-pc, n-pd-1)
		swapn(pb, n-d1, d1)
		d1 = pb - pa
		d2 := pd - pc
		if d1 <= d2 {
			if d1 > 1 {
				pgQsort(a[:d1], cmp)
			}
			if d2 <= 1 {
				return
			}
			a = a[n-d2:]
		} else {
			if d2 > 1 {
				pgQsort(a[n-d2:], cmp)
			}
			if d1 <= 1 {
				return
			}
			a = a[:d1]
		}
	}
}
