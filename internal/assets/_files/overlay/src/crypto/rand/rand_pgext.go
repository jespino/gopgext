//go:build pgext

// crypto/rand for PostgreSQL extensions: PostgreSQL's pg_strong_random
// (OpenSSL's RAND_bytes, or /dev/urandom). gopgext adds this file to its
// mirror of TinyGo's src/crypto/rand.

package rand

import (
	"errors"
	"unsafe"
)

func init() {
	Reader = reader{}
}

type reader struct{}

// pg_strong_random reads at most this much per call.
const maxRead = 1 << 20

func (reader) Read(b []byte) (int, error) {
	for n := 0; n < len(b); n += maxRead {
		chunk := b[n:min(n+maxRead, len(b))]
		if !pgextStrongRandom(unsafe.Pointer(unsafe.SliceData(chunk)), uintptr(len(chunk))) {
			return n, errors.New("crypto/rand: could not generate a random number")
		}
	}
	return len(b), nil
}

// Implemented in csrc/runtime.c.
//
//export gopgext_strong_random
func pgextStrongRandom(buf unsafe.Pointer, n uintptr) bool
