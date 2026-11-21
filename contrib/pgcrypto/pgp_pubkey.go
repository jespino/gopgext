package main

// Public-key encryption (pgp-pubkey.c, pgp-pubenc.c, pgp-pubdec.c,
// pgp-mpi.c, pgp-mpi-openssl.c with math/big, pgp-info.c): reading keys,
// RSA and Elgamal session key packets, pgp_key_id.

import (
	"bytes"
	"crypto/sha1"
	"math/big"
)

// pgpMPI is a multiprecision integer as stored: its bit count and bytes.
type pgpMPI struct {
	bits int
	data []byte
}

func newPGPMPI(data []byte, bits int) (*pgpMPI, error) {
	if bits < 0 || bits > 0xffff {
		pgpDebug("pgp_mpi_alloc: unreasonable request: bits=%d", bits)
		return nil, pxePgpCorruptData
	}
	n := (bits + 7) / 8
	return &pgpMPI{bits, append([]byte(nil), data[:n]...)}, nil
}

// pgpMPIRead is pgp_mpi_read.
func pgpMPIRead(src *pullFilter) (*pgpMPI, error) {
	hdr, err := src.readFixed(2)
	if err != nil {
		return nil, err
	}
	bits := int(hdr[0])<<8 | int(hdr[1])
	d, err := src.readFixed((bits + 7) / 8)
	if err != nil {
		return nil, err
	}
	return &pgpMPI{bits, append([]byte(nil), d...)}, nil
}

func (n *pgpMPI) write(dst *pushFilter) error {
	if err := dst.write([]byte{byte(n.bits >> 8), byte(n.bits)}); err != nil {
		return err
	}
	return dst.write(n.data)
}

func (n *pgpMPI) hash(md interface{ Write([]byte) (int, error) }) {
	md.Write([]byte{byte(n.bits >> 8), byte(n.bits)})
	md.Write(n.data)
}

func (n *pgpMPI) cksum(sum int) int {
	sum += n.bits>>8 + n.bits&0xff
	for _, b := range n.data {
		sum += int(b)
	}
	return sum & 0xffff
}

// int is mpi_to_bn: nil if the bit count is wrong.
func (n *pgpMPI) int() *big.Int {
	x := new(big.Int).SetBytes(n.data)
	if x.BitLen() != n.bits {
		pgpDebug("mpi_to_bn: bignum conversion failed: mpi=%d, bn=%d", n.bits, x.BitLen())
		return nil
	}
	return x
}

// pgpMPIFromInt is bn_to_mpi.
func pgpMPIFromInt(x *big.Int) (*pgpMPI, error) {
	return newPGPMPI(x.Bytes(), x.BitLen())
}

// pgpPubKey is a public key, with its secret part if read from a secret
// key.
type pgpPubKey struct {
	ver        byte
	time       []byte
	algo       byte
	pub        []*pgpMPI // elg: p, g, y; rsa: n, e; dsa: p, q, g, y
	sec        []*pgpMPI // elg: x; rsa: d, p, q, u; dsa: x
	keyID      [8]byte
	canEncrypt bool
}

// calcKeyID is calc_key_id: the low 64 bits of the v4 fingerprint.
func (pk *pgpPubKey) calcKeyID() {
	n := 1 + 4 + 1
	for _, m := range pk.pub {
		n += 2 + len(m.data)
	}
	md := sha1.New()
	md.Write([]byte{0x99, byte(n >> 8), byte(n)})
	md.Write([]byte{pk.ver})
	md.Write(pk.time)
	md.Write([]byte{pk.algo})
	for _, m := range pk.pub {
		m.hash(md)
	}
	copy(pk.keyID[:], md.Sum(nil)[12:])
}

// pgpReadPublicKey is _pgp_read_public_key.
func pgpReadPublicKey(pkt *pullFilter) (*pgpPubKey, error) {
	pk := &pgpPubKey{}
	var err error
	if pk.ver, err = pkt.getByte(); err != nil {
		return nil, err
	}
	if pk.ver != 4 {
		return nil, pxePgpNotV4Keypkt
	}
	t, err := pkt.readFixed(4)
	if err != nil {
		return nil, err
	}
	pk.time = append([]byte(nil), t...)
	if pk.algo, err = pkt.getByte(); err != nil {
		return nil, err
	}
	var count int
	switch pk.algo {
	case pgpPubDSASign:
		count = 4
	case pgpPubRSASign, pgpPubRSAEncrypt, pgpPubRSAEncryptSign:
		count = 2
		pk.canEncrypt = pk.algo != pgpPubRSASign
	case pgpPubElgEncrypt:
		count = 3
		pk.canEncrypt = true
	default:
		pgpDebug("unknown public algo: %d", pk.algo)
		return nil, pxePgpUnknownPubalgo
	}
	for i := 0; i < count; i++ {
		m, err := pgpMPIRead(pkt)
		if err != nil {
			return nil, err
		}
		pk.pub = append(pk.pub, m)
	}
	pk.calcKeyID()
	return pk, nil
}

const (
	pgpHideClear = 0
	pgpHideCksum = 255
	pgpHideSHA1  = 254
)

// pgpProcessSecretKey is process_secret_key.
func pgpProcessSecretKey(pkt *pullFilter, psw []byte) (*pgpPubKey, error) {
	pk, err := pgpReadPublicKey(pkt)
	if err != nil {
		return nil, err
	}
	hideType, err := pkt.getByte()
	if err != nil {
		return nil, err
	}
	pfKey := pkt
	switch hideType {
	case pgpHideSHA1, pgpHideCksum:
		if psw == nil {
			return nil, pxePgpNeedSecretPsw
		}
		cipherAlgo, err := pkt.getByte()
		if err != nil {
			return nil, err
		}
		var s2k pgpS2K
		if err := pgpS2KRead(pkt, &s2k); err != nil {
			return nil, err
		}
		if err := pgpS2KProcess(&s2k, int(cipherAlgo), psw); err != nil {
			return nil, err
		}
		bs := pgpCipherBlockSize(int(cipherAlgo))
		if bs == 0 {
			pgpDebug("unknown cipher algo=%d", cipherAlgo)
			return nil, pxePgpUnsupportedCipher
		}
		iv, err := pkt.readFixed(bs)
		if err != nil {
			return nil, err
		}
		cfb, err := newPGPCFB(int(cipherAlgo), s2k.key, false, iv, false)
		if err != nil {
			return nil, err
		}
		pfKey = pgpDecryptFilter(cfb, pkt)
	case pgpHideClear:
	default:
		pgpDebug("unknown hide type")
		return nil, pxePgpKeypktCorrupt
	}
	var count int
	switch pk.algo {
	case pgpPubRSASign, pgpPubRSAEncrypt, pgpPubRSAEncryptSign:
		count = 4
	case pgpPubElgEncrypt, pgpPubDSASign:
		count = 1
	default:
		pgpDebug("unknown public algo: %d", pk.algo)
		return nil, pxePgpKeypktCorrupt
	}
	for i := 0; i < count; i++ {
		m, err := pgpMPIRead(pfKey)
		if err != nil {
			return nil, err
		}
		pk.sec = append(pk.sec, m)
	}
	if hideType == pgpHideSHA1 {
		got, err := pfKey.readFixed(20)
		if err != nil {
			return nil, err
		}
		md := sha1.New()
		for _, m := range pk.sec {
			m.hash(md)
		}
		if !bytes.Equal(md.Sum(nil), got) {
			pgpDebug("key sha1 check failed")
			return nil, pxePgpKeypktCorrupt
		}
	} else {
		got, err := pfKey.readFixed(2)
		if err != nil {
			return nil, err
		}
		sum := 0
		for _, m := range pk.sec {
			sum = m.cksum(sum)
		}
		if sum != int(got[0])<<8|int(got[1]) {
			pgpDebug("key cksum check failed")
			return nil, pxePgpKeypktCorrupt
		}
	}
	if err := pgpExpectPacketEnd(pfKey); err != nil {
		return nil, err
	}
	return pk, nil
}

// pgpReadKey is internal_read_key: the encryption subkey of a key (public
// for encryption, secret for decryption).
func pgpReadKey(src *pullFilter, psw []byte, secret bool) (*pgpPubKey, error) {
	var encKey *pgpPubKey
	gotMainKey := false
	for {
		tag, n, typ, err := pgpParsePktHdr(src, false)
		if err != nil {
			return nil, err
		}
		if typ == 0 {
			break
		}
		pkt := pgpPktReader(src, n, typ)
		var pk *pgpPubKey
		switch tag {
		case pgpPktPublicKey, pgpPktSecretKey:
			if gotMainKey {
				err = pxePgpMultipleKeys
				break
			}
			gotMainKey = true
			err = pgpSkipPacket(pkt)
		case pgpPktPublicSubkey:
			if secret {
				err = pxePgpExpectSecretKey
			} else {
				pk, err = pgpReadPublicKey(pkt)
			}
		case pgpPktSecretSubkey:
			if !secret {
				err = pxePgpExpectPublicKey
			} else {
				pk, err = pgpProcessSecretKey(pkt, psw)
			}
		case pgpPktSignature, pgpPktMarker, pgpPktTrust, pgpPktUserID, pgpPktUserAttr, pgpPktPriv61:
			err = pgpSkipPacket(pkt)
		default:
			pgpDebug("unknown/unexpected packet: %d", tag)
			err = pxePgpUnexpectedPkt
		}
		if pk != nil && err == nil && pk.canEncrypt {
			if encKey == nil {
				encKey = pk
			} else {
				err = pxePgpMultipleSubkeys
			}
		}
		if err != nil {
			return nil, err
		}
	}
	if encKey == nil {
		return nil, pxePgpNoUsableKey
	}
	return encKey, nil
}

// ---- Session key packets ---------------------------------------------------

// pgpPadPKCS1 is pad_eme_pkcs1_v15: 02 || non-zero random || 00 || data.
func pgpPadPKCS1(data []byte, resLen int) ([]byte, error) {
	padLen := resLen - 2 - len(data)
	if padLen < 8 {
		return nil, pxeBug
	}
	buf := make([]byte, resLen)
	buf[0] = 2
	if err := randomBytes(buf[1 : 1+padLen]); err != nil {
		return nil, pxeNoRandom
	}
	for i := 1; i < 1+padLen; {
		if buf[i] == 0 {
			if err := randomBytes(buf[i : i+1]); err != nil {
				return nil, pxeNoRandom
			}
		}
		if buf[i] != 0 {
			i++
		}
	}
	copy(buf[padLen+2:], data)
	return buf, nil
}

// pgpCreateSecmsg is create_secmsg: the padded session key as an MPI.
func pgpCreateSecmsg(ctx *pgpContext, fullBytes int) (*pgpMPI, error) {
	cksum := 0
	for _, b := range ctx.sessKey {
		cksum += int(b)
	}
	msg := append([]byte{byte(ctx.cipherAlgo)}, ctx.sessKey...)
	msg = append(msg, byte(cksum>>8), byte(cksum))
	padded, err := pgpPadPKCS1(msg, fullBytes)
	if err != nil {
		return nil, err
	}
	return newPGPMPI(padded, fullBytes*8-6)
}

// pgpElgamalKBits is decide_k_bits.
func pgpElgamalKBits(pBits int) int {
	if pBits <= 5120 {
		return pBits/10 + 160
	}
	return (pBits/8 + 200) * 3 / 2
}

// pgpRandomInt is BN_rand(bits, top bit set).
func pgpRandomInt(bits int) (*big.Int, error) {
	b := make([]byte, (bits+7)/8)
	if err := randomBytes(b); err != nil {
		return nil, err
	}
	k := new(big.Int).SetBytes(b)
	k.Rsh(k, uint(len(b)*8-bits))
	return k.SetBit(k, bits-1, 1), nil
}

// pgpExpMod is BN_mod_exp (nil for an invalid modulus).
func pgpExpMod(x, y, m *big.Int) *big.Int {
	if m.Sign() <= 0 {
		return nil
	}
	return new(big.Int).Exp(x, y, m)
}

func pgpWritePubencSesskey(ctx *pgpContext, dst *pushFilter) error {
	pk := ctx.pubKey
	pkt, err := pgpCreatePktWriter(dst, pgpPktPubencryptedSesskey)
	if err != nil {
		return err
	}
	if err := pkt.write([]byte{3}); err != nil {
		return err
	}
	if err := pkt.write(pk.keyID[:]); err != nil {
		return err
	}
	if err := pkt.write([]byte{pk.algo}); err != nil {
		return err
	}
	var out []*big.Int
	switch pk.algo {
	case pgpPubElgEncrypt:
		m, err := pgpCreateSecmsg(ctx, len(pk.pub[0].data)-1)
		if err != nil {
			return err
		}
		mi, p, g, y := m.int(), pk.pub[0].int(), pk.pub[1].int(), pk.pub[2].int()
		if mi == nil || p == nil || g == nil || y == nil {
			return pxePgpMathFailed
		}
		k, err := pgpRandomInt(pgpElgamalKBits(p.BitLen()))
		if err != nil {
			return pxePgpMathFailed
		}
		c1, yk := pgpExpMod(g, k, p), pgpExpMod(y, k, p)
		if c1 == nil || yk == nil {
			return pxePgpMathFailed
		}
		c2 := new(big.Int).Mul(mi, yk)
		out = []*big.Int{c1, c2.Mod(c2, p)}
	case pgpPubRSAEncrypt, pgpPubRSAEncryptSign:
		m, err := pgpCreateSecmsg(ctx, len(pk.pub[0].data)-1)
		if err != nil {
			return err
		}
		mi, n, e := m.int(), pk.pub[0].int(), pk.pub[1].int()
		if mi == nil || n == nil || e == nil {
			return pxePgpMathFailed
		}
		c := pgpExpMod(mi, e, n)
		if c == nil {
			return pxePgpMathFailed
		}
		out = []*big.Int{c}
	}
	for _, x := range out {
		m, err := pgpMPIFromInt(x)
		if err != nil {
			return pxePgpMathFailed
		}
		if err := m.write(pkt); err != nil {
			return err
		}
	}
	return pkt.flushAll()
}

// pgpCheckPKCS1 is check_eme_pkcs1_v15: the message, or nil.
func pgpCheckPKCS1(data []byte) []byte {
	if len(data) < 1+8+1 || data[0] != 2 {
		return nil
	}
	i := 1
	for i < len(data) && data[i] != 0 {
		i++
	}
	if i == len(data) || i-1 < 8 {
		return nil
	}
	return data[i+1:]
}

// pgpParsePubencSesskey is pgp_parse_pubenc_sesskey.
func pgpParsePubencSesskey(ctx *pgpContext, pkt *pullFilter) error {
	pk := ctx.pubKey
	if pk == nil {
		pgpDebug("no pubkey?")
		return pxeBug
	}
	ver, err := pkt.getByte()
	if err != nil {
		return err
	}
	if ver != 3 {
		pgpDebug("unknown pubenc_sesskey pkt ver=%d", ver)
		return pxePgpCorruptData
	}
	keyID, err := pkt.readFixed(8)
	if err != nil {
		return err
	}
	if !bytes.Equal(keyID, make([]byte, 8)) && !bytes.Equal(keyID, pk.keyID[:]) {
		pgpDebug("key_id's does not match")
		return pxePgpWrongKey
	}
	algo, err := pkt.getByte()
	if err != nil {
		return err
	}
	var m *big.Int
	switch algo {
	case pgpPubElgEncrypt:
		if pk.algo != pgpPubElgEncrypt {
			return pxePgpWrongKey
		}
		c1m, err := pgpMPIRead(pkt)
		if err != nil {
			return err
		}
		c2m, err := pgpMPIRead(pkt)
		if err != nil {
			return err
		}
		c1, c2, p, x := c1m.int(), c2m.int(), pk.pub[0].int(), pk.sec[0].int()
		if c1 == nil || c2 == nil || p == nil || x == nil {
			return pxePgpMathFailed
		}
		c1x := pgpExpMod(c1, x, p)
		if c1x == nil {
			return pxePgpMathFailed
		}
		div := new(big.Int).ModInverse(c1x, p)
		if div == nil {
			return pxePgpMathFailed
		}
		m = div.Mul(div, c2)
		m.Mod(m, p)
	case pgpPubRSAEncrypt, pgpPubRSAEncryptSign:
		if pk.algo != pgpPubRSAEncrypt && pk.algo != pgpPubRSAEncryptSign {
			return pxePgpWrongKey
		}
		cm, err := pgpMPIRead(pkt)
		if err != nil {
			return err
		}
		c, d, n := cm.int(), pk.sec[0].int(), pk.pub[0].int()
		if c == nil || d == nil || n == nil {
			return pxePgpMathFailed
		}
		if m = pgpExpMod(c, d, n); m == nil {
			return pxePgpMathFailed
		}
	default:
		return pxePgpUnknownPubalgo
	}
	mm, err := pgpMPIFromInt(m)
	if err != nil {
		return pxePgpMathFailed
	}
	msg := pgpCheckPKCS1(mm.data)
	if msg == nil {
		pgpDebug("check_eme_pkcs1_v15 failed")
		return pxePgpWrongKey
	}
	if len(msg) < 3 {
		return pxePgpWrongKey
	}
	sum := 0
	for _, b := range msg[1 : len(msg)-2] {
		sum += int(b)
	}
	if sum&0xffff != int(msg[len(msg)-2])<<8|int(msg[len(msg)-1]) {
		pgpDebug("pubenc cksum failed")
		return pxePgpWrongKey
	}
	if len(msg)-3 > pgpMaxKey {
		pgpDebug("incorrect session key length=%d", len(msg)-3)
		return pxePgpKeyTooBig
	}
	ctx.cipherAlgo = int(msg[0])
	ctx.sessKey = append([]byte(nil), msg[1:len(msg)-2]...)
	return pgpExpectPacketEnd(pkt)
}

// ---- pgp_key_id --------------------------------------------------------

// pgpGetKeyID is pgp_get_keyid.
func pgpGetKeyID(data []byte) (string, error) {
	src := newBytesReader(data)
	var keyID []byte
	gotPubKey, gotSymencKey, gotPubencKey := 0, 0, 0
	gotMainKey := false
	for {
		tag, n, typ, err := pgpParsePktHdr(src, false)
		if err != nil {
			return "", err
		}
		if typ == 0 {
			break
		}
		pkt := pgpPktReader(src, n, typ)
		gotData := false
		switch tag {
		case pgpPktSecretKey, pgpPktPublicKey:
			if !gotMainKey {
				gotMainKey = true
				err = pgpSkipPacket(pkt)
			} else {
				err = pxePgpMultipleKeys
			}
		case pgpPktSecretSubkey, pgpPktPublicSubkey:
			var pk *pgpPubKey
			if pk, err = pgpReadPublicKey(pkt); err == nil {
				if err = pgpSkipPacket(pkt); err == nil {
					switch pk.algo {
					case pgpPubElgEncrypt, pgpPubRSAEncrypt, pgpPubRSAEncryptSign:
						keyID = pk.keyID[:]
						gotPubKey++
					}
				}
			}
		case pgpPktPubencryptedSesskey:
			gotPubencKey++
			var ver byte
			if ver, err = pkt.getByte(); err == nil {
				if ver != 3 {
					err = pxError(-1)
				} else if keyID, err = pkt.readFixed(8); err == nil {
					err = pgpSkipPacket(pkt)
				}
			}
		case pgpPktSymencryptedData, pgpPktSymencryptedDataMDC:
			gotData = true
		case pgpPktSymencryptedSesskey:
			gotSymencKey++
			err = pgpSkipPacket(pkt)
		case pgpPktSignature, pgpPktMarker, pgpPktTrust, pgpPktUserID, pgpPktUserAttr, pgpPktPriv61:
			err = pgpSkipPacket(pkt)
		default:
			err = pxePgpCorruptData
		}
		if err != nil {
			return "", err
		}
		if gotData {
			break
		}
	}
	var err error
	if gotPubKey > 0 && gotPubencKey > 0 {
		err = pxePgpCorruptData
	}
	if gotPubKey > 1 || gotPubencKey > 1 {
		err = pxePgpMultipleKeys
	}
	if err != nil {
		return "", err
	}
	switch {
	case gotPubencKey > 0 || gotPubKey > 0:
		if bytes.Equal(keyID, make([]byte, 8)) {
			return "ANYKEY", nil
		}
		const hex = "0123456789ABCDEF"
		var s [16]byte
		for i, c := range keyID {
			s[2*i], s[2*i+1] = hex[c>>4], hex[c&15]
		}
		return string(s[:]), nil
	case gotSymencKey > 0:
		return "SYMKEY", nil
	}
	return "", pxePgpNoUsableKey
}
