package main

// Traditional and extended ("_", BSDi) DES crypt(3), ported from
// crypt-des.c (FreeSec, David Burren and others; BSD license). The table
// driven DES with salt-perturbed E-box is the original's.

import "gopgext/pg"

const desA64 = "./0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

var desIP = [64]uint8{
	58, 50, 42, 34, 26, 18, 10, 2, 60, 52, 44, 36, 28, 20, 12, 4,
	62, 54, 46, 38, 30, 22, 14, 6, 64, 56, 48, 40, 32, 24, 16, 8,
	57, 49, 41, 33, 25, 17, 9, 1, 59, 51, 43, 35, 27, 19, 11, 3,
	61, 53, 45, 37, 29, 21, 13, 5, 63, 55, 47, 39, 31, 23, 15, 7,
}

var desKeyPerm = [56]uint8{
	57, 49, 41, 33, 25, 17, 9, 1, 58, 50, 42, 34, 26, 18,
	10, 2, 59, 51, 43, 35, 27, 19, 11, 3, 60, 52, 44, 36,
	63, 55, 47, 39, 31, 23, 15, 7, 62, 54, 46, 38, 30, 22,
	14, 6, 61, 53, 45, 37, 29, 21, 13, 5, 28, 20, 12, 4,
}

var desKeyShifts = [16]uint8{1, 1, 2, 2, 2, 2, 2, 2, 1, 2, 2, 2, 2, 2, 2, 1}

var desCompPerm = [48]uint8{
	14, 17, 11, 24, 1, 5, 3, 28, 15, 6, 21, 10,
	23, 19, 12, 4, 26, 8, 16, 7, 27, 20, 13, 2,
	41, 52, 31, 37, 47, 55, 30, 40, 51, 45, 33, 48,
	44, 49, 39, 56, 34, 53, 46, 42, 50, 36, 29, 32,
}

var desSbox = [8][64]uint8{
	{
		14, 4, 13, 1, 2, 15, 11, 8, 3, 10, 6, 12, 5, 9, 0, 7,
		0, 15, 7, 4, 14, 2, 13, 1, 10, 6, 12, 11, 9, 5, 3, 8,
		4, 1, 14, 8, 13, 6, 2, 11, 15, 12, 9, 7, 3, 10, 5, 0,
		15, 12, 8, 2, 4, 9, 1, 7, 5, 11, 3, 14, 10, 0, 6, 13,
	},
	{
		15, 1, 8, 14, 6, 11, 3, 4, 9, 7, 2, 13, 12, 0, 5, 10,
		3, 13, 4, 7, 15, 2, 8, 14, 12, 0, 1, 10, 6, 9, 11, 5,
		0, 14, 7, 11, 10, 4, 13, 1, 5, 8, 12, 6, 9, 3, 2, 15,
		13, 8, 10, 1, 3, 15, 4, 2, 11, 6, 7, 12, 0, 5, 14, 9,
	},
	{
		10, 0, 9, 14, 6, 3, 15, 5, 1, 13, 12, 7, 11, 4, 2, 8,
		13, 7, 0, 9, 3, 4, 6, 10, 2, 8, 5, 14, 12, 11, 15, 1,
		13, 6, 4, 9, 8, 15, 3, 0, 11, 1, 2, 12, 5, 10, 14, 7,
		1, 10, 13, 0, 6, 9, 8, 7, 4, 15, 14, 3, 11, 5, 2, 12,
	},
	{
		7, 13, 14, 3, 0, 6, 9, 10, 1, 2, 8, 5, 11, 12, 4, 15,
		13, 8, 11, 5, 6, 15, 0, 3, 4, 7, 2, 12, 1, 10, 14, 9,
		10, 6, 9, 0, 12, 11, 7, 13, 15, 1, 3, 14, 5, 2, 8, 4,
		3, 15, 0, 6, 10, 1, 13, 8, 9, 4, 5, 11, 12, 7, 2, 14,
	},
	{
		2, 12, 4, 1, 7, 10, 11, 6, 8, 5, 3, 15, 13, 0, 14, 9,
		14, 11, 2, 12, 4, 7, 13, 1, 5, 0, 15, 10, 3, 9, 8, 6,
		4, 2, 1, 11, 10, 13, 7, 8, 15, 9, 12, 5, 6, 3, 0, 14,
		11, 8, 12, 7, 1, 14, 2, 13, 6, 15, 0, 9, 10, 4, 5, 3,
	},
	{
		12, 1, 10, 15, 9, 2, 6, 8, 0, 13, 3, 4, 14, 7, 5, 11,
		10, 15, 4, 2, 7, 12, 9, 5, 6, 1, 13, 14, 0, 11, 3, 8,
		9, 14, 15, 5, 2, 8, 12, 3, 7, 0, 4, 10, 1, 13, 11, 6,
		4, 3, 2, 12, 9, 5, 15, 10, 11, 14, 1, 7, 6, 0, 8, 13,
	},
	{
		4, 11, 2, 14, 15, 0, 8, 13, 3, 12, 9, 7, 5, 10, 6, 1,
		13, 0, 11, 7, 4, 9, 1, 10, 14, 3, 5, 12, 2, 15, 8, 6,
		1, 4, 11, 13, 12, 3, 7, 14, 10, 15, 6, 8, 0, 5, 9, 2,
		6, 11, 13, 8, 1, 4, 10, 7, 9, 5, 0, 15, 14, 2, 3, 12,
	},
	{
		13, 2, 8, 4, 6, 15, 11, 1, 10, 9, 3, 14, 5, 0, 12, 7,
		1, 15, 13, 8, 10, 3, 7, 4, 12, 5, 6, 11, 0, 14, 9, 2,
		7, 11, 4, 1, 9, 12, 14, 2, 0, 6, 10, 13, 15, 3, 5, 8,
		2, 1, 14, 7, 4, 10, 8, 13, 15, 12, 9, 0, 3, 5, 6, 11,
	},
}

var desPbox = [32]uint8{
	16, 7, 20, 21, 29, 12, 28, 17, 1, 15, 23, 26, 5, 18, 31, 10,
	2, 8, 24, 14, 32, 27, 3, 9, 19, 13, 30, 6, 22, 11, 4, 25,
}

// desTables are the lookup tables des_init builds.
type desTables struct {
	mSbox                      [4][4096]uint8
	psbox                      [4][256]uint32
	ipMaskl, ipMaskr           [8][256]uint32
	fpMaskl, fpMaskr           [8][256]uint32
	keyPermMaskl, keyPermMaskr [8][128]uint32
	compMaskl, compMaskr       [8][128]uint32
	enKeysl, enKeysr           [16]uint32
	saltbits                   uint32
}

var desT *desTables

func desBit32(i int) uint32 { return 0x80000000 >> uint(i) }

// desInit is des_init.
func desInit() *desTables {
	if desT != nil {
		return desT
	}
	t := new(desTables)
	var uSbox [8][64]uint8
	for i := 0; i < 8; i++ {
		for j := 0; j < 64; j++ {
			b := (j & 0x20) | ((j & 1) << 4) | ((j >> 1) & 0xf)
			uSbox[i][j] = desSbox[i][b]
		}
	}
	for b := 0; b < 4; b++ {
		for i := 0; i < 64; i++ {
			for j := 0; j < 64; j++ {
				t.mSbox[b][(i<<6)|j] = (uSbox[b<<1][i] << 4) | uSbox[(b<<1)+1][j]
			}
		}
	}
	var initPerm, finalPerm, invKeyPerm [64]uint8
	var invCompPerm [56]uint8
	for i := 0; i < 64; i++ {
		finalPerm[i] = desIP[i] - 1
		initPerm[finalPerm[i]] = uint8(i)
		invKeyPerm[i] = 255
	}
	for i := 0; i < 56; i++ {
		invKeyPerm[desKeyPerm[i]-1] = uint8(i)
		invCompPerm[i] = 255
	}
	for i := 0; i < 48; i++ {
		invCompPerm[desCompPerm[i]-1] = uint8(i)
	}
	bits8 := func(j int) int { return 0x80 >> uint(j) }
	bits28 := func(i int) uint32 { return desBit32(i + 4) }
	bits24 := func(i int) uint32 { return desBit32(i + 8) }
	for k := 0; k < 8; k++ {
		for i := 0; i < 256; i++ {
			var il, ir, fl, fr uint32
			for j := 0; j < 8; j++ {
				inbit := 8*k + j
				if i&bits8(j) != 0 {
					if obit := int(initPerm[inbit]); obit < 32 {
						il |= desBit32(obit)
					} else {
						ir |= desBit32(obit - 32)
					}
					if obit := int(finalPerm[inbit]); obit < 32 {
						fl |= desBit32(obit)
					} else {
						fr |= desBit32(obit - 32)
					}
				}
			}
			t.ipMaskl[k][i], t.ipMaskr[k][i], t.fpMaskl[k][i], t.fpMaskr[k][i] = il, ir, fl, fr
		}
		for i := 0; i < 128; i++ {
			var il, ir uint32
			for j := 0; j < 7; j++ {
				inbit := 8*k + j
				if i&bits8(j+1) != 0 {
					obit := int(invKeyPerm[inbit])
					if obit == 255 {
						continue
					}
					if obit < 28 {
						il |= bits28(obit)
					} else {
						ir |= bits28(obit - 28)
					}
				}
			}
			t.keyPermMaskl[k][i], t.keyPermMaskr[k][i] = il, ir
			il, ir = 0, 0
			for j := 0; j < 7; j++ {
				inbit := 7*k + j
				if i&bits8(j+1) != 0 {
					obit := int(invCompPerm[inbit])
					if obit == 255 {
						continue
					}
					if obit < 24 {
						il |= bits24(obit)
					} else {
						ir |= bits24(obit - 24)
					}
				}
			}
			t.compMaskl[k][i], t.compMaskr[k][i] = il, ir
		}
	}
	var unPbox [32]uint8
	for i := 0; i < 32; i++ {
		unPbox[desPbox[i]-1] = uint8(i)
	}
	for b := 0; b < 4; b++ {
		for i := 0; i < 256; i++ {
			var p uint32
			for j := 0; j < 8; j++ {
				if i&bits8(j) != 0 {
					p |= desBit32(int(unPbox[8*b+j]))
				}
			}
			t.psbox[b][i] = p
		}
	}
	desT = t
	return t
}

// setupSalt is setup_salt: the salt's 24 bits, reversed.
func (t *desTables) setupSalt(salt uint32) {
	t.saltbits = 0
	saltbit, obit := uint32(1), uint32(0x800000)
	for i := 0; i < 24; i++ {
		if salt&saltbit != 0 {
			t.saltbits |= obit
		}
		saltbit <<= 1
		obit >>= 1
	}
}

// setKey is des_setkey, for a key as 8 bytes.
func (t *desTables) setKey(key *[8]byte) {
	rawkey0 := uint32(key[0])<<24 | uint32(key[1])<<16 | uint32(key[2])<<8 | uint32(key[3])
	rawkey1 := uint32(key[4])<<24 | uint32(key[5])<<16 | uint32(key[6])<<8 | uint32(key[7])
	k0 := t.keyPermMaskl[0][rawkey0>>25] | t.keyPermMaskl[1][(rawkey0>>17)&0x7f] |
		t.keyPermMaskl[2][(rawkey0>>9)&0x7f] | t.keyPermMaskl[3][(rawkey0>>1)&0x7f] |
		t.keyPermMaskl[4][rawkey1>>25] | t.keyPermMaskl[5][(rawkey1>>17)&0x7f] |
		t.keyPermMaskl[6][(rawkey1>>9)&0x7f] | t.keyPermMaskl[7][(rawkey1>>1)&0x7f]
	k1 := t.keyPermMaskr[0][rawkey0>>25] | t.keyPermMaskr[1][(rawkey0>>17)&0x7f] |
		t.keyPermMaskr[2][(rawkey0>>9)&0x7f] | t.keyPermMaskr[3][(rawkey0>>1)&0x7f] |
		t.keyPermMaskr[4][rawkey1>>25] | t.keyPermMaskr[5][(rawkey1>>17)&0x7f] |
		t.keyPermMaskr[6][(rawkey1>>9)&0x7f] | t.keyPermMaskr[7][(rawkey1>>1)&0x7f]
	shifts := uint(0)
	for round := 0; round < 16; round++ {
		shifts += uint(desKeyShifts[round])
		t0 := (k0 << shifts) | (k0 >> (28 - shifts))
		t1 := (k1 << shifts) | (k1 >> (28 - shifts))
		t.enKeysl[round] = t.compMaskl[0][(t0>>21)&0x7f] | t.compMaskl[1][(t0>>14)&0x7f] |
			t.compMaskl[2][(t0>>7)&0x7f] | t.compMaskl[3][t0&0x7f] |
			t.compMaskl[4][(t1>>21)&0x7f] | t.compMaskl[5][(t1>>14)&0x7f] |
			t.compMaskl[6][(t1>>7)&0x7f] | t.compMaskl[7][t1&0x7f]
		t.enKeysr[round] = t.compMaskr[0][(t0>>21)&0x7f] | t.compMaskr[1][(t0>>14)&0x7f] |
			t.compMaskr[2][(t0>>7)&0x7f] | t.compMaskr[3][t0&0x7f] |
			t.compMaskr[4][(t1>>21)&0x7f] | t.compMaskr[5][(t1>>14)&0x7f] |
			t.compMaskr[6][(t1>>7)&0x7f] | t.compMaskr[7][t1&0x7f]
	}
}

// do is do_des, encrypting count (> 0) times; it can be canceled.
func (t *desTables) do(lIn, rIn uint32, count uint32) (uint32, uint32, error) {
	l := t.ipMaskl[0][lIn>>24] | t.ipMaskl[1][(lIn>>16)&0xff] | t.ipMaskl[2][(lIn>>8)&0xff] |
		t.ipMaskl[3][lIn&0xff] | t.ipMaskl[4][rIn>>24] | t.ipMaskl[5][(rIn>>16)&0xff] |
		t.ipMaskl[6][(rIn>>8)&0xff] | t.ipMaskl[7][rIn&0xff]
	r := t.ipMaskr[0][lIn>>24] | t.ipMaskr[1][(lIn>>16)&0xff] | t.ipMaskr[2][(lIn>>8)&0xff] |
		t.ipMaskr[3][lIn&0xff] | t.ipMaskr[4][rIn>>24] | t.ipMaskr[5][(rIn>>16)&0xff] |
		t.ipMaskr[6][(rIn>>8)&0xff] | t.ipMaskr[7][rIn&0xff]
	var f uint32
	for n := uint32(0); n < count; n++ {
		if n&0x3ff == 0x3ff {
			if err := pg.CheckInterrupts(); err != nil {
				return 0, 0, err
			}
		}
		for round := 0; round < 16; round++ {
			r48l := ((r & 0x00000001) << 23) | ((r & 0xf8000000) >> 9) | ((r & 0x1f800000) >> 11) |
				((r & 0x01f80000) >> 13) | ((r & 0x001f8000) >> 15)
			r48r := ((r & 0x0001f800) << 7) | ((r & 0x00001f80) << 5) | ((r & 0x000001f8) << 3) |
				((r & 0x0000001f) << 1) | ((r & 0x80000000) >> 31)
			f = (r48l ^ r48r) & t.saltbits
			r48l ^= f ^ t.enKeysl[round]
			r48r ^= f ^ t.enKeysr[round]
			f = t.psbox[0][t.mSbox[0][r48l>>12]] | t.psbox[1][t.mSbox[1][r48l&0xfff]] |
				t.psbox[2][t.mSbox[2][r48r>>12]] | t.psbox[3][t.mSbox[3][r48r&0xfff]]
			f ^= l
			l = r
			r = f
		}
		r = l
		l = f
	}
	lOut := t.fpMaskl[0][l>>24] | t.fpMaskl[1][(l>>16)&0xff] | t.fpMaskl[2][(l>>8)&0xff] |
		t.fpMaskl[3][l&0xff] | t.fpMaskl[4][r>>24] | t.fpMaskl[5][(r>>16)&0xff] |
		t.fpMaskl[6][(r>>8)&0xff] | t.fpMaskl[7][r&0xff]
	rOut := t.fpMaskr[0][l>>24] | t.fpMaskr[1][(l>>16)&0xff] | t.fpMaskr[2][(l>>8)&0xff] |
		t.fpMaskr[3][l&0xff] | t.fpMaskr[4][r>>24] | t.fpMaskr[5][(r>>16)&0xff] |
		t.fpMaskr[6][(r>>8)&0xff] | t.fpMaskr[7][r&0xff]
	return lOut, rOut, nil
}

// desAsciiToBin is ascii_to_bin, for a (signed) char.
func desAsciiToBin(c byte) uint32 {
	ch := int8(c)
	switch {
	case ch > 'z':
		return 0
	case ch >= 'a':
		return uint32(ch - 'a' + 38)
	case ch > 'Z':
		return 0
	case ch >= 'A':
		return uint32(ch - 'A' + 12)
	case ch > '9':
		return 0
	case ch >= '.':
		return uint32(ch - '.')
	}
	return 0
}

// cryptDES is px_crypt_des; a nil result (no error) is crypt(3)
// returning NULL.
func cryptDES(key, setting []byte) ([]byte, error) {
	t := desInit()
	var keybuf [8]byte
	k := 0
	for q := range keybuf {
		if k < len(key) {
			keybuf[q] = key[k] << 1
			k++
		}
	}
	t.setKey(&keybuf)

	var count, salt uint32
	var output []byte
	if len(setting) > 0 && setting[0] == '_' {
		if len(setting) < 9 {
			return nil, pg.Errorf(pg.CodeInvalidParameterValue, "invalid salt")
		}
		for i := 1; i < 5; i++ {
			count |= desAsciiToBin(setting[i]) << ((i - 1) * 6)
		}
		for i := 5; i < 9; i++ {
			salt |= desAsciiToBin(setting[i]) << ((i - 5) * 6)
		}
		for k < len(key) {
			// Encrypt the key with itself, XOR with the next 8 characters.
			t.setupSalt(0)
			l := uint32(keybuf[0])<<24 | uint32(keybuf[1])<<16 | uint32(keybuf[2])<<8 | uint32(keybuf[3])
			r := uint32(keybuf[4])<<24 | uint32(keybuf[5])<<16 | uint32(keybuf[6])<<8 | uint32(keybuf[7])
			l, r, _ = t.do(l, r, 1)
			keybuf = [8]byte{byte(l >> 24), byte(l >> 16), byte(l >> 8), byte(l),
				byte(r >> 24), byte(r >> 16), byte(r >> 8), byte(r)}
			for q := 0; q < 8 && k < len(key); q++ {
				keybuf[q] ^= key[k] << 1
				k++
			}
			t.setKey(&keybuf)
		}
		output = append(output, setting[:9]...)
	} else {
		count = 25
		if len(setting) < 2 {
			return nil, pg.Errorf(pg.CodeInvalidParameterValue, "invalid salt")
		}
		salt = desAsciiToBin(setting[1])<<6 | desAsciiToBin(setting[0])
		output = append(output, setting[0], setting[1])
	}
	t.setupSalt(salt)
	if count == 0 {
		return nil, nil
	}
	r0, r1, err := t.do(0, 0, count)
	if err != nil {
		return nil, err
	}
	l := r0 >> 8
	output = append(output, desA64[(l>>18)&0x3f], desA64[(l>>12)&0x3f], desA64[(l>>6)&0x3f], desA64[l&0x3f])
	l = (r0 << 16) | ((r1 >> 16) & 0xffff)
	output = append(output, desA64[(l>>18)&0x3f], desA64[(l>>12)&0x3f], desA64[(l>>6)&0x3f], desA64[l&0x3f])
	l = r1 << 2
	output = append(output, desA64[(l>>12)&0x3f], desA64[(l>>6)&0x3f], desA64[l&0x3f])
	return output, nil
}
