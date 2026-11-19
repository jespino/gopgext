// types exercises gopgext's mapping between Go and SQL types: scalars,
// NULL-able pointers, arrays, sets, errors and panics.
package main

import (
	"errors"
	"fmt"
	"iter"
	"math"
	"strings"
	"sync"

	"gopgext/pg"
)

//gopgext:function immutable
func AddInts(a int16, b int32, c int64) int64 { return int64(a) + int64(b) + c }

//gopgext:function immutable
func Hypot(x float32, y float64) float64 { return math.Hypot(float64(x), y) }

//gopgext:function immutable
func Negate(b bool) bool { return !b }

//gopgext:function immutable
func Reverse(s string) string {
	r := []rune(s)
	for i, j := 0, len(r)-1; i < j; i, j = i+1, j-1 {
		r[i], r[j] = r[j], r[i]
	}
	return string(r)
}

//gopgext:function immutable
func XorBytes(data []byte, key int32) []byte {
	out := make([]byte, len(data))
	for i, b := range data {
		out[i] = b ^ byte(key)
	}
	return out
}

// Coalesce returns the first non-NULL argument (NULL if both are).
//
//gopgext:function immutable
func Coalesce2(a, b *int64) *int64 {
	if a != nil {
		return a
	}
	return b
}

// Describe shows how NULLs arrive (pointer parameters make it non-STRICT).
//
//gopgext:function immutable
func Describe(s *string, n int32) string {
	if s == nil {
		return fmt.Sprintf("NULL/%d", n)
	}
	return fmt.Sprintf("%q/%d", *s, n)
}

//gopgext:function immutable
func SumArray(xs []float64) float64 {
	var s float64
	for _, x := range xs {
		s += x
	}
	return s
}

//gopgext:function immutable
func SplitWords(s string) []string { return strings.Fields(s) }

//gopgext:function immutable
func Squares(xs []int32) []int64 {
	out := make([]int64, len(xs))
	for i, x := range xs {
		out[i] = int64(x) * int64(x)
	}
	return out
}

// Fib returns the first n Fibonacci numbers, as a set.
//
//gopgext:function immutable rows=20
func Fib(n int32) iter.Seq[int64] {
	return func(yield func(int64) bool) {
		a, b := int64(0), int64(1)
		for i := int32(0); i < n; i++ {
			if !yield(a) {
				return
			}
			a, b = b, a+b
		}
	}
}

//gopgext:function immutable
func Words(s string) (iter.Seq[string], error) {
	if s == "" {
		return nil, errors.New("empty input")
	}
	return strings.FieldsSeq(s), nil
}

//gopgext:function
func SafeDivide(a, b int64) (int64, error) {
	if b == 0 {
		return 0, &pg.Error{Code: pg.CodeDivisionByZero, Message: "division by zero", Hint: "Use a non-zero divisor."}
	}
	return a / b, nil
}

//gopgext:function
func Fail(msg string) error {
	return fmt.Errorf("failed: %s", msg)
}

//gopgext:function
func Panics(i int32) int32 {
	xs := []int32{1, 2, 3}
	return xs[i] // index out of range for i >= 3
}

//gopgext:function
func PanicsNil() int32 {
	var m *struct{ x int32 }
	return m.x
}

// Recovers checks that panic/recover works within Go code.
//
//gopgext:function
func Recovers() (s string) {
	defer func() {
		if r := recover(); r != nil {
			s = fmt.Sprint("recovered: ", r)
		}
	}()
	panic("boom")
}

//gopgext:function
func Nothing() {}

// Allocate makes n MiB of garbage, in 1 KiB pieces, to exercise the collector.
//
//gopgext:function
func Allocate(n int32) int64 {
	var keep [][]byte
	total := int64(0)
	for i := 0; i < int(n)*1024; i++ {
		b := make([]byte, 1024)
		b[0] = byte(i)
		total += int64(b[0])
		if i%64 == 0 {
			keep = append(keep, b)
		}
	}
	for _, b := range keep {
		total += int64(len(b))
	}
	return total
}

// Channels work as long as nothing blocks: there are no goroutines.
//
//gopgext:function
func Channels(n int32) int64 {
	ch := make(chan int64, n)
	for i := int32(0); i < n; i++ {
		ch <- int64(i)
	}
	close(ch)
	var sum int64
	for v := range ch {
		sum += v
	}
	var mu sync.Mutex
	mu.Lock()
	defer mu.Unlock()
	return sum
}

// Deadlock blocks on a channel nobody will ever send to.
//
//gopgext:function
func Deadlock() int64 {
	ch := make(chan int64)
	return <-ch
}

func main() {}
