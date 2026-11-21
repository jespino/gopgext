package main

// OpenPGP (RFC 4880) encryption: the context and its options, algorithm
// tables, string-to-key and the OpenPGP CFB mode (pgp.c, pgp-s2k.c,
// pgp-cfb.c).

import (
	"fmt"
	"hash"
	"strings"

	"gopgext/pg"
)

// Packet tags.
const (
	pgpPktPubencryptedSesskey = 1
	pgpPktSignature           = 2
	pgpPktSymencryptedSesskey = 3
	pgpPktSecretKey           = 5
	pgpPktPublicKey           = 6
	pgpPktSecretSubkey        = 7
	pgpPktCompressedData      = 8
	pgpPktSymencryptedData    = 9
	pgpPktMarker              = 10
	pgpPktLiteralData         = 11
	pgpPktTrust               = 12
	pgpPktUserID              = 13
	pgpPktPublicSubkey        = 14
	pgpPktUserAttr            = 17
	pgpPktSymencryptedDataMDC = 18
	pgpPktMDC                 = 19
	pgpPktPriv61              = 61
)

// Public-key algorithms.
const (
	pgpPubRSAEncryptSign = 1
	pgpPubRSAEncrypt     = 2
	pgpPubRSASign        = 3
	pgpPubElgEncrypt     = 16
	pgpPubDSASign        = 17
)

// Symmetric ciphers, compression and digests used directly.
const (
	pgpSymBlowfish = 4
	pgpSymAES128   = 7

	pgpComprNone  = 0
	pgpComprZIP   = 1
	pgpComprZLIB  = 2
	pgpComprBZIP2 = 3

	pgpDigestSHA1 = 2
)

// S2K modes.
const (
	pgpS2KSimple  = 0
	pgpS2KSalted  = 1
	pgpS2KISalted = 3
)

const pgpMaxKey = 32

type pgpDigestInfo struct {
	name string
	code int
}

var pgpDigests = []pgpDigestInfo{
	{"md5", 1}, {"sha1", 2}, {"sha-1", 2}, {"ripemd160", 3},
	{"sha256", 8}, {"sha384", 9}, {"sha512", 10},
}

type pgpCipherInfo struct {
	name     string
	code     int
	intName  string
	keyLen   int
	blockLen int
}

var pgpCiphers = []pgpCipherInfo{
	{"3des", 2, "3des-ecb", 24, 8},
	{"cast5", 3, "cast5-ecb", 16, 8},
	{"bf", 4, "bf-ecb", 16, 8},
	{"blowfish", 4, "bf-ecb", 16, 8},
	{"aes", 7, "aes-ecb", 16, 16},
	{"aes128", 7, "aes-ecb", 16, 16},
	{"aes192", 8, "aes-ecb", 24, 16},
	{"aes256", 9, "aes-ecb", 32, 16},
	{"twofish", 10, "twofish-ecb", 32, 16},
}

func pgpCipherByCode(code int) *pgpCipherInfo {
	for i := range pgpCiphers {
		if pgpCiphers[i].code == code {
			return &pgpCiphers[i]
		}
	}
	return nil
}

// pgpDigestCode is pgp_get_digest_code (negative: an error).
func pgpDigestCode(name string) int {
	for _, d := range pgpDigests {
		if strings.EqualFold(d.name, name) {
			return d.code
		}
	}
	return int(pxePgpUnsupportedHash)
}

// pgpCipherCode is pgp_get_cipher_code (negative: an error).
func pgpCipherCode(name string) int {
	for _, c := range pgpCiphers {
		if strings.EqualFold(c.name, name) {
			return c.code
		}
	}
	return int(pxePgpUnsupportedCipher)
}

func pgpCipherKeySize(code int) int {
	if c := pgpCipherByCode(code); c != nil {
		return c.keyLen
	}
	return 0
}

func pgpCipherBlockSize(code int) int {
	if c := pgpCipherByCode(code); c != nil {
		return c.blockLen
	}
	return 0
}

// pgpLoadCipher is pgp_load_cipher.
func pgpLoadCipher(code int) (*pxCipher, error) {
	i := pgpCipherByCode(code)
	if i == nil {
		return nil, pxePgpCorruptData
	}
	c, err := findCipher(i.intName)
	if err != nil {
		return nil, pxePgpUnsupportedCipher
	}
	return c, nil
}

// pgpLoadDigest is pgp_load_digest.
func pgpLoadDigest(code int) (hash.Hash, error) {
	for _, d := range pgpDigests {
		if d.code == code {
			a, err := findDigest(d.name)
			if err != nil {
				return nil, pxePgpUnsupportedHash
			}
			return a.new(), nil
		}
	}
	return nil, pxePgpCorruptData
}

// pgpDebugOn makes pgpDebug report (the "debug" option).
var pgpDebugOn bool

// pgpDebug is px_debug: a NOTICE with debug=1.
func pgpDebug(format string, args ...any) {
	if pgpDebugOn {
		pg.Notice("dbg: " + fmt.Sprintf(format, args...))
	}
}

// pgpS2K is a string-to-key specifier and the key it derived.
type pgpS2K struct {
	mode       byte
	digestAlgo byte
	salt       [8]byte
	iter       byte
	key        []byte
}

// s2kDecodeCount is s2k_decode_count (RFC 4880 3.7.1.3).
func s2kDecodeCount(c byte) int {
	return (16 + int(c&15)) << (int(c>>4) + 6)
}

// pgpContext is a PGP_Context: options, and state of an operation.
type pgpContext struct {
	s2k                 pgpS2K
	s2kMode             int
	s2kCount            int
	s2kDigestAlgo       int
	s2kCipherAlgo       int
	cipherAlgo          int
	compressAlgo        int
	compressLevel       int
	disableMDC          int
	useSessKey          int
	textMode            bool
	convertCRLF         bool
	unicodeMode         int
	ignoreCipherFailure bool

	corruptPrefix    bool
	unsupportedCompr bool
	unexpectedBinary bool
	inMDCPkt         bool
	useMDCBufFilter  bool
	mdc              hash.Hash
	pubKey           *pgpPubKey
	symKey           []byte // nil: none
	sessKey          []byte
}

// newPGPContext is pgp_init.
func newPGPContext() *pgpContext {
	return &pgpContext{
		cipherAlgo:    pgpSymAES128,
		s2kCipherAlgo: -1,
		s2kMode:       pgpS2KISalted,
		s2kCount:      -1,
		s2kDigestAlgo: pgpDigestSHA1,
		compressAlgo:  pgpComprNone,
		compressLevel: 6,
	}
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

func (ctx *pgpContext) setS2KMode(mode int) error {
	switch mode {
	case pgpS2KSimple, pgpS2KSalted, pgpS2KISalted:
		ctx.s2kMode = mode
		return nil
	}
	return pxeArgumentError
}

func (ctx *pgpContext) setS2KCount(count int) error {
	if ctx.s2kMode == pgpS2KISalted && count >= 1024 && count <= 65011712 {
		ctx.s2kCount = count
		return nil
	}
	return pxeArgumentError
}

func (ctx *pgpContext) setCompressAlgo(algo int) error {
	switch algo {
	case pgpComprNone, pgpComprZIP, pgpComprZLIB, pgpComprBZIP2:
		ctx.compressAlgo = algo
		return nil
	}
	return pxeArgumentError
}

func (ctx *pgpContext) setCompressLevel(level int) error {
	if level >= 0 && level <= 9 {
		ctx.compressLevel = level
		return nil
	}
	return pxeArgumentError
}

func (ctx *pgpContext) setSymKey(key []byte) error {
	if len(key) < 1 {
		return pxeArgumentError
	}
	ctx.symKey = key
	return nil
}

// ---- S2K -----------------------------------------------------------------

// pgpS2KFill is pgp_s2k_fill: a new specifier for encryption.
func pgpS2KFill(s2k *pgpS2K, mode, digestAlgo, count int) error {
	s2k.mode = byte(mode)
	s2k.digestAlgo = byte(digestAlgo)
	switch mode {
	case pgpS2KSimple:
	case pgpS2KSalted:
		if err := randomBytes(s2k.salt[:]); err != nil {
			return pxeNoRandom
		}
	case pgpS2KISalted:
		if err := randomBytes(s2k.salt[:]); err != nil {
			return pxeNoRandom
		}
		var tmp [1]byte
		if err := randomBytes(tmp[:]); err != nil {
			return pxeNoRandom
		}
		s2k.iter = decideS2KIter(tmp[0], count)
	default:
		return pxePgpBadS2KMode
	}
	return nil
}

// decideS2KIter is decide_s2k_iter: the encoded count for count
// iterations (-1: 65536 to 262144, at random).
func decideS2KIter(randByte byte, count int) byte {
	if count == -1 {
		return 96 + randByte&0x1f
	}
	for iter := 0; iter <= 255; iter++ {
		if s2kDecodeCount(byte(iter)) >= count {
			return byte(iter)
		}
	}
	return 255
}

// pgpS2KRead is pgp_s2k_read.
func pgpS2KRead(src *pullFilter, s2k *pgpS2K) error {
	var err error
	if s2k.mode, err = src.getByte(); err != nil {
		return err
	}
	if s2k.digestAlgo, err = src.getByte(); err != nil {
		return err
	}
	switch s2k.mode {
	case 0:
	case 1, 3:
		salt, err := src.readFixed(8)
		if err != nil {
			return err
		}
		copy(s2k.salt[:], salt)
		if s2k.mode == 3 {
			if s2k.iter, err = src.getByte(); err != nil {
				return err
			}
		}
	default:
		return pxePgpBadS2KMode
	}
	return nil
}

// pgpS2KProcess is pgp_s2k_process: derives the key for cipher.
func pgpS2KProcess(s2k *pgpS2K, cipher int, key []byte) error {
	keyLen := pgpCipherKeySize(cipher)
	if keyLen <= 0 {
		return pxePgpUnsupportedCipher
	}
	if _, err := pgpLoadDigest(int(s2k.digestAlgo)); err != nil {
		return err
	}
	if s2k.mode != 0 && s2k.mode != 1 && s2k.mode != 3 {
		return pxePgpBadS2KMode
	}
	count := 0
	if s2k.mode == 3 {
		count = s2kDecodeCount(s2k.iter)
	}
	s2k.key = make([]byte, 0, keyLen)
	for preload := 0; len(s2k.key) < keyLen; preload++ {
		md, _ := pgpLoadDigest(int(s2k.digestAlgo))
		md.Write(make([]byte, preload))
		switch s2k.mode {
		case 0:
			md.Write(key)
		case 1:
			md.Write(s2k.salt[:])
			md.Write(key)
		case 3:
			md.Write(s2k.salt[:])
			md.Write(key)
			cur := 8 + len(key)
			for cur < count {
				c := 8
				if cur+8 >= count {
					c = count - cur
				}
				md.Write(s2k.salt[:c])
				cur += c
				if cur+len(key) < count {
					c = len(key)
				} else if cur < count {
					c = count - cur
				} else {
					break
				}
				md.Write(key[:c])
				cur += c
			}
		}
		buf := md.Sum(nil)
		if n := keyLen - len(s2k.key); len(buf) > n {
			buf = buf[:n]
		}
		s2k.key = append(s2k.key, buf...)
	}
	return nil
}

// ---- CFB -----------------------------------------------------------------

// pgpCFB is OpenPGP's CFB mode: plain, or with the resync after the
// prefix of old symmetrically encrypted data packets.
type pgpCFB struct {
	ciph                *pxCipher
	bs, pos, blockNo    int
	resync              bool
	ignoreCipherFailure bool
	fr, fre, encbuf     []byte
}

// newPGPCFB is pgp_cfb_create.
func newPGPCFB(algo int, key []byte, resync bool, iv []byte, ignoreCipherFailure bool) (*pgpCFB, error) {
	c, err := pgpLoadCipher(algo)
	if err != nil {
		return nil, err
	}
	if err := c.Init(key, nil); err != nil {
		return nil, err
	}
	bs := c.BlockSize()
	ctx := &pgpCFB{ciph: c, bs: bs, resync: resync, ignoreCipherFailure: ignoreCipherFailure,
		fr: make([]byte, bs), fre: make([]byte, bs), encbuf: make([]byte, bs)}
	copy(ctx.fr, iv)
	return ctx, nil
}

// mix combines data with the keystream (fre) into dst.
func (ctx *pgpCFB) mix(data, dst []byte, decrypt bool) int {
	n := len(data)
	if ctx.resync && ctx.blockNo == 2 {
		// block #2 is 2 bytes long
		m := 2 - ctx.pos
		if n < m {
			m = n
		}
		ctx.mixBytes(data[:m], dst, decrypt)
		if ctx.pos == 2 {
			copy(ctx.fr, ctx.encbuf[2:ctx.bs])
			copy(ctx.fr[ctx.bs-2:], ctx.encbuf[:2])
			ctx.pos = 0
			return m
		}
		data, dst = data[m:], dst[m:]
	}
	ctx.mixBytes(data, dst, decrypt)
	return n
}

func (ctx *pgpCFB) mixBytes(data, dst []byte, decrypt bool) {
	for i, b := range data {
		j := ctx.pos + i
		if decrypt {
			ctx.encbuf[j] = b
			dst[i] = ctx.fre[j] ^ b
		} else {
			dst[i] = ctx.fre[j] ^ b
			ctx.encbuf[j] = dst[i]
		}
	}
	ctx.pos += len(data)
}

// process is cfb_process.
func (ctx *pgpCFB) process(data, dst []byte, decrypt bool) error {
	for len(data) > 0 && ctx.pos > 0 {
		n := min(ctx.bs-ctx.pos, len(data))
		n = ctx.mix(data[:n], dst, decrypt)
		data, dst = data[n:], dst[n:]
		if ctx.pos == ctx.bs {
			copy(ctx.fr, ctx.encbuf)
			ctx.pos = 0
		}
	}
	for len(data) > 0 {
		fre, err := ctx.ciph.Encrypt(false, ctx.fr)
		if err != nil {
			if !(decrypt && ctx.ignoreCipherFailure) {
				return pg.Errorf(codeExternalRoutine, "encrypt error: %s", err.Error())
			}
		} else {
			copy(ctx.fre, fre)
		}
		if ctx.blockNo < 5 {
			ctx.blockNo++
		}
		n := min(ctx.bs, len(data))
		n = ctx.mix(data[:n], dst, decrypt)
		data, dst = data[n:], dst[n:]
		if ctx.pos == ctx.bs {
			copy(ctx.fr, ctx.encbuf)
			ctx.pos = 0
		}
	}
	return nil
}

func (ctx *pgpCFB) encrypt(data, dst []byte) error { return ctx.process(data, dst, false) }
func (ctx *pgpCFB) decrypt(data, dst []byte) error { return ctx.process(data, dst, true) }
