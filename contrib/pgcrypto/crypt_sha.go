package main

// SHA-crypt ("$5$" sha256crypt, "$6$" sha512crypt, Ulrich Drepper's
// specification), ported from crypt-sha.c.

import (
	"bytes"
	"crypto/sha256"
	"crypto/sha512"
	"hash"
	"math"
	"strconv"

	"gopgext/pg"
)

const (
	shacryptSaltMaxLen    = 16
	shacryptRoundsDefault = 5000
	shacryptRoundsMin     = 1000
	shacryptRoundsMax     = 999999999
)

// cryptSHAStrtoint is strtoint(s, &end, 10): the value (clamped to the
// int range) and the index of end.
func cryptSHAStrtoint(s []byte) (int32, int) {
	i := 0
	for i < len(s) && (s[i] == ' ' || s[i] >= '\t' && s[i] <= '\r') {
		i++
	}
	neg := false
	if i < len(s) && (s[i] == '+' || s[i] == '-') {
		neg = s[i] == '-'
		i++
	}
	start := i
	var v int64
	overflow := false
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		d := int64(s[i] - '0')
		if v > (math.MaxInt64-d)/10 {
			overflow = true
		} else {
			v = v*10 + d
		}
		i++
	}
	if i == start {
		return 0, 0 // no digits: end is the start of the string
	}
	switch {
	case overflow && neg:
		v = math.MinInt64
	case overflow:
		v = math.MaxInt64
	case neg:
		v = -v
	}
	return int32(max(math.MinInt32, min(v, math.MaxInt32))), i
}

// cryptSHAMblen is pg_mblen_cstr in a UTF-8 database.
func cryptSHAMblen(s []byte) int {
	n := 1
	switch c := s[0]; {
	case c&0xe0 == 0xc0:
		n = 2
	case c&0xf0 == 0xe0:
		n = 3
	case c&0xf8 == 0xf0:
		n = 4
	}
	return min(n, len(s))
}

// cryptSHA is px_crypt_shacrypt.
func cryptSHA(pw, salt []byte) ([]byte, error) {
	const roundsPrefix = "rounds="
	magic := [2]string{"$5$", "$6$"}
	if len(salt) < 3 {
		return nil, pg.Errorf(pg.CodeInvalidParameterValue, "invalid salt")
	}
	if salt[0] != '$' || salt[2] != '$' {
		return nil, &pg.Error{Code: pg.CodeInvalidParameterValue, Message: "invalid format of salt",
			Hint: "Magic byte format for shacrypt is either \"$5$\" or \"$6$\"."}
	}
	var newHash func() hash.Hash
	var bufSize int
	var out []byte
	switch string(salt[:3]) {
	case magic[0]:
		newHash, bufSize = sha256.New, 32
	case magic[1]:
		newHash, bufSize = sha512.New, 64
	default:
		return nil, pg.Errorf(pg.CodeInternalError, "unknown crypt identifier \"%c\"", salt[1])
	}
	out = append(out, salt[:3]...)
	dec := salt[3:]

	rounds := uint32(shacryptRoundsDefault)
	custom := false
	if bytes.HasPrefix(dec, []byte(roundsPrefix)) {
		num := dec[len(roundsPrefix):]
		srounds, end := cryptSHAStrtoint(num)
		if end >= len(num) || num[end] != '$' {
			return nil, pg.Errorf(pg.CodeSyntaxError, "could not parse salt options")
		}
		dec = num[end+1:]
		if srounds > shacryptRoundsMax {
			pg.Noticef("rounds=%d exceeds maximum supported value (%d), using %d instead",
				srounds, shacryptRoundsMax, shacryptRoundsMax)
			srounds = shacryptRoundsMax
		} else if srounds < shacryptRoundsMin {
			pg.Noticef("rounds=%d is below supported value (%d), using %d instead",
				srounds, shacryptRoundsMin, shacryptRoundsMin)
			srounds = shacryptRoundsMin
		}
		rounds = uint32(srounds)
		custom = true
	}
	if custom {
		out = append(out, roundsPrefix+strconv.FormatUint(uint64(rounds), 10)+"$"...)
	}

	// The salt: the characters before the next '$' (leading ones skipped),
	// at most 16 bytes of the input.
	var decoded []byte
	for ep := 0; ep < len(dec) && ep < shacryptSaltMaxLen; ep++ {
		if bytes.Contains(dec, []byte(magic[0])) || bytes.Contains(dec, []byte(magic[1])) {
			return nil, pg.Errorf(pg.CodeInternalError, "bogus magic byte found in salt string")
		}
		if bytes.Contains(dec, []byte(roundsPrefix)) {
			return nil, pg.Errorf(pg.CodeInternalError, "invalid rounds option specified in salt string")
		}
		if dec[ep] != '$' {
			if bytes.IndexByte([]byte(desA64), dec[ep]) >= 0 {
				decoded = append(decoded, dec[ep])
			} else {
				return nil, pg.Errorf(pg.CodeInvalidParameterValue, "invalid character in salt string: \"%s\"",
					dec[ep:ep+cryptSHAMblen(dec[ep:])])
			}
		} else if len(decoded) > 0 {
			break
		}
	}
	saltLen := len(decoded)
	out = append(out, decoded...)
	// The original hashes the raw salt string (dec) for digests B and DS.
	rawSalt := dec[:min(saltLen, len(dec))]

	a := newHash()
	a.Write(pw)
	a.Write(decoded)

	b := newHash()
	b.Write(pw)
	b.Write(rawSalt)
	b.Write(pw)
	shaBuf := b.Sum(nil)

	plen := len(pw)
	block := plen
	for ; block > bufSize; block -= bufSize {
		a.Write(shaBuf)
	}
	a.Write(shaBuf[:block])
	for block = plen; block != 0; block >>= 1 {
		if block&1 != 0 {
			a.Write(shaBuf)
		} else {
			a.Write(pw)
		}
	}
	shaBuf = a.Sum(nil)

	b.Reset()
	for block = plen; block > 0; block-- {
		b.Write(pw)
	}
	dp := b.Sum(nil)
	pBytes := make([]byte, plen)
	for i := 0; i < plen; i += bufSize {
		copy(pBytes[i:], dp)
	}

	b.Reset()
	for block = 16 + int(shaBuf[0]); block > 0; block-- {
		b.Write(rawSalt)
	}
	ds := b.Sum(nil)
	sBytes := make([]byte, saltLen)
	for i := 0; i < saltLen; i += bufSize {
		copy(sBytes[i:], ds)
	}

	for n := uint32(0); n < rounds; n++ {
		if n&0xfff == 0 {
			if err := pg.CheckInterrupts(); err != nil {
				return nil, err
			}
		}
		b.Reset()
		if n&1 != 0 {
			b.Write(pBytes)
		} else {
			b.Write(shaBuf)
		}
		if n%3 != 0 {
			b.Write(sBytes)
		}
		if n%7 != 0 {
			b.Write(pBytes)
		}
		if n&1 != 0 {
			b.Write(shaBuf)
		} else {
			b.Write(pBytes)
		}
		shaBuf = b.Sum(shaBuf[:0])
	}

	out = append(out, '$')
	b64 := func(b2, b1, b0 byte, n int) {
		w := uint32(b2)<<16 | uint32(b1)<<8 | uint32(b0)
		for ; n > 0; n-- {
			out = append(out, desA64[w&0x3f])
			w >>= 6
		}
	}
	s := shaBuf
	if bufSize == 32 {
		for i := 0; i < 10; i++ {
			// (0,10,20), (21,1,11), (12,22,2), ...: rotate the three indexes.
			x, y, z := i, i+10, i+20
			switch i % 3 {
			case 1:
				x, y, z = i+20, i, i+10
			case 2:
				x, y, z = i+10, i+20, i
			}
			b64(s[x], s[y], s[z], 4)
		}
		b64(0, s[31], s[30], 3)
	} else {
		for i := 0; i < 21; i++ {
			x, y, z := i, i+21, i+42
			switch i % 3 {
			case 1:
				x, y, z = i+21, i+42, i
			case 2:
				x, y, z = i+42, i, i+21
			}
			b64(s[x], s[y], s[z], 4)
		}
		b64(0, 0, s[63], 2)
	}
	return out, nil
}
