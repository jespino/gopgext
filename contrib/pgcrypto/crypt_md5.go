package main

// MD5 crypt(3) ("$1$"), ported from crypt-md5.c (Poul-Henning Kamp,
// "THE BEER-WARE LICENSE").

import "crypto/md5"

// cryptMD5To64 is _crypt_to64.
func cryptMD5To64(dst []byte, v uint32, n int) []byte {
	for ; n > 0; n-- {
		dst = append(dst, desA64[v&0x3f])
		v >>= 6
	}
	return dst
}

// cryptMD5 is px_crypt_md5.
func cryptMD5(pw, salt []byte) []byte {
	const magic = "$1$"
	sp := salt
	if len(sp) >= 3 && string(sp[:3]) == magic {
		sp = sp[3:]
	}
	// The salt stops at the first '$', max 8 characters.
	sl := 0
	for sl < len(sp) && sp[sl] != '$' && sl < 8 {
		sl++
	}
	sp = sp[:sl]

	ctx := md5.New()
	ctx.Write(pw)
	ctx.Write([]byte(magic))
	ctx.Write(sp)

	ctx1 := md5.New()
	ctx1.Write(pw)
	ctx1.Write(sp)
	ctx1.Write(pw)
	final := ctx1.Sum(nil)
	for pl := len(pw); pl > 0; pl -= md5.Size {
		ctx.Write(final[:min(pl, md5.Size)])
	}
	// The original clears final here, so the loop below adds zero bytes.
	clear(final)
	for i := len(pw); i != 0; i >>= 1 {
		if i&1 != 0 {
			ctx.Write(final[:1])
		} else {
			ctx.Write(pw[:1])
		}
	}

	passwd := append([]byte(magic), sp...)
	passwd = append(passwd, '$')
	final = ctx.Sum(nil)

	for i := 0; i < 1000; i++ {
		ctx1.Reset()
		if i&1 != 0 {
			ctx1.Write(pw)
		} else {
			ctx1.Write(final)
		}
		if i%3 != 0 {
			ctx1.Write(sp)
		}
		if i%7 != 0 {
			ctx1.Write(pw)
		}
		if i&1 != 0 {
			ctx1.Write(final)
		} else {
			ctx1.Write(pw)
		}
		final = ctx1.Sum(final[:0])
	}

	f := func(a, b, c int) uint32 { return uint32(final[a])<<16 | uint32(final[b])<<8 | uint32(final[c]) }
	passwd = cryptMD5To64(passwd, f(0, 6, 12), 4)
	passwd = cryptMD5To64(passwd, f(1, 7, 13), 4)
	passwd = cryptMD5To64(passwd, f(2, 8, 14), 4)
	passwd = cryptMD5To64(passwd, f(3, 9, 15), 4)
	passwd = cryptMD5To64(passwd, f(4, 10, 5), 4)
	passwd = cryptMD5To64(passwd, uint32(final[11]), 2)
	return passwd
}
