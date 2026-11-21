package main

// bcrypt ("$2a$", and "$2x$" with the original's sign extension bug),
// ported from crypt-blowfish.c (Solar Designer, public domain).

import (
	"encoding/binary"

	"gopgext/pg"
)

const bcryptItoa64 = "./ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"

var bcryptAtoi64 = [0x60]uint8{
	64, 64, 64, 64, 64, 64, 64, 64, 64, 64, 64, 64, 64, 64, 0, 1,
	54, 55, 56, 57, 58, 59, 60, 61, 62, 63, 64, 64, 64, 64, 64, 64,
	64, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16,
	17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 64, 64, 64, 64, 64,
	64, 28, 29, 30, 31, 32, 33, 34, 35, 36, 37, 38, 39, 40, 41, 42,
	43, 44, 45, 46, 47, 48, 49, 50, 51, 52, 53, 64, 64, 64, 64, 64,
}

// "OrpheanBeholderScryDoubt"
var bcryptMagic = [6]uint32{0x4F727068, 0x65616E42, 0x65686F6C, 0x64657253, 0x63727944, 0x6F756274}

// bcryptDecode is BF_decode: size bytes from bcrypt's base64; false if a
// character is invalid.
func bcryptDecode(dst, src []byte) bool {
	si := 0
	next := func() (uint32, bool) {
		c := uint32(src[si]) - 0x20
		si++
		if c >= 0x60 || bcryptAtoi64[c] > 63 {
			return 0, false
		}
		return uint32(bcryptAtoi64[c]), true
	}
	for di := 0; di < len(dst); {
		c1, ok1 := next()
		if !ok1 {
			return false
		}
		c2, ok2 := next()
		if !ok2 {
			return false
		}
		dst[di] = byte(c1<<2 | (c2&0x30)>>4)
		if di++; di >= len(dst) {
			break
		}
		c3, ok3 := next()
		if !ok3 {
			return false
		}
		dst[di] = byte((c2&0x0f)<<4 | (c3&0x3c)>>2)
		if di++; di >= len(dst) {
			break
		}
		c4, ok4 := next()
		if !ok4 {
			return false
		}
		dst[di] = byte((c3&0x03)<<6 | c4)
		di++
	}
	return true
}

// bcryptEncode is BF_encode.
func bcryptEncode(dst, src []byte) []byte {
	for i := 0; i < len(src); {
		c1 := uint32(src[i])
		i++
		dst = append(dst, bcryptItoa64[c1>>2])
		c1 = (c1 & 0x03) << 4
		if i >= len(src) {
			return append(dst, bcryptItoa64[c1])
		}
		c2 := uint32(src[i])
		i++
		c1 |= c2 >> 4
		dst = append(dst, bcryptItoa64[c1])
		c1 = (c2 & 0x0f) << 2
		if i >= len(src) {
			return append(dst, bcryptItoa64[c1])
		}
		c2 = uint32(src[i])
		i++
		c1 |= c2 >> 6
		dst = append(dst, bcryptItoa64[c1], bcryptItoa64[c2&0x3f])
	}
	return dst
}

func bcryptInvalidSalt() error { return pg.Errorf(pg.CodeInvalidParameterValue, "invalid salt") }

// cryptBlowfish is _crypt_blowfish_rn.
func cryptBlowfish(key, setting []byte) ([]byte, error) {
	if len(setting) < 29 {
		return nil, bcryptInvalidSalt()
	}
	if setting[0] != '$' || setting[1] != '2' || setting[2] != 'a' && setting[2] != 'x' ||
		setting[3] != '$' || setting[4] < '0' || setting[4] > '3' ||
		setting[5] < '0' || setting[5] > '9' || setting[4] == '3' && setting[5] > '1' ||
		setting[6] != '$' {
		return nil, bcryptInvalidSalt()
	}
	count := uint32(1) << (uint(setting[4]-'0')*10 + uint(setting[5]-'0'))
	var saltBytes [16]byte
	if count < 16 || !bcryptDecode(saltBytes[:], setting[7:]) {
		return nil, bcryptInvalidSalt()
	}
	var salt [4]uint32
	for i := range salt {
		salt[i] = binary.BigEndian.Uint32(saltBytes[4*i:])
	}

	// BF_set_key: the key and its NUL, cyclically.
	signBug := setting[2] == 'x'
	c := &blowfish{s: bfInitS}
	var expanded [18]uint32
	ptr := 0
	at := func(i int) byte {
		if i < len(key) {
			return key[i]
		}
		return 0
	}
	for i := 0; i < 18; i++ {
		var tmp uint32
		for j := 0; j < 4; j++ {
			tmp <<= 8
			b := at(ptr)
			if signBug {
				tmp |= uint32(int32(int8(b)))
			} else {
				tmp |= uint32(b)
			}
			if b == 0 {
				ptr = 0
			} else {
				ptr++
			}
		}
		expanded[i] = tmp
		c.p[i] = bfInitP[i] ^ tmp
	}

	var l, r uint32
	for i := 0; i < 18; i += 2 {
		l ^= salt[i&2]
		r ^= salt[(i&2)+1]
		l, r = c.encrypt(l, r)
		c.p[i], c.p[i+1] = l, r
	}
	for i := 0; i < 4*256; i += 4 {
		l ^= salt[(16+2)&3]
		r ^= salt[(16+3)&3]
		l, r = c.encrypt(l, r)
		c.s[i>>8][i&0xff], c.s[i>>8][i&0xff+1] = l, r
		l ^= salt[(16+4)&3]
		r ^= salt[(16+5)&3]
		l, r = c.encrypt(l, r)
		c.s[i>>8][i&0xff+2], c.s[i>>8][i&0xff+3] = l, r
	}

	body := func() {
		var l, r uint32
		for i := 0; i < 18; i += 2 {
			l, r = c.encrypt(l, r)
			c.p[i], c.p[i+1] = l, r
		}
		for i := 0; i < 4*256; i += 2 {
			l, r = c.encrypt(l, r)
			c.s[i>>8][i&0xff], c.s[i>>8][i&0xff+1] = l, r
		}
	}
	for {
		if err := pg.CheckInterrupts(); err != nil {
			return nil, err
		}
		for i := range c.p {
			c.p[i] ^= expanded[i]
		}
		body()
		for i := range c.p {
			c.p[i] ^= salt[i&3]
		}
		body()
		if count--; count == 0 {
			break
		}
	}

	var out [24]byte
	for i := 0; i < 6; i += 2 {
		l, r := bcryptMagic[i], bcryptMagic[i+1]
		for n := 0; n < 64; n++ {
			l, r = c.encrypt(l, r)
		}
		binary.BigEndian.PutUint32(out[4*i:], l)
		binary.BigEndian.PutUint32(out[4*i+4:], r)
	}

	output := append([]byte(nil), setting[:7+22-1]...)
	output = append(output, bcryptItoa64[bcryptAtoi64[setting[7+22-1]-0x20]&0x30])
	// Bug-compatible with the original: only 23 of the 24 bytes.
	return bcryptEncode(output, out[:23]), nil
}
