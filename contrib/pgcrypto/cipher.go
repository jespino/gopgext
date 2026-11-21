package main

// Raw encryption: encrypt(), decrypt(), encrypt_iv(), decrypt_iv() (px.c's
// "combos": a cipher plus padding), and the block ciphers the OpenPGP code
// uses. The ciphers, aliases and key handling are those of upstream's
// openssl.c, and encryption and decryption follow OpenSSL's EVP functions
// (PKCS#7 padding, errors on bad padding or partial blocks).

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/des"
	"strings"

	"gopgext/pg"
)

// cipherAlgo is a cipher of the table: an algorithm in a mode.
type cipherAlgo struct {
	blockSize int
	keySize   int    // maximum key length
	mode      string // "ecb", "cbc" or "cfb"
	// key returns the key the cipher uses, from the one given to Init.
	key func(key []byte) ([]byte, error)
	// block creates the block cipher for that key.
	block func(key []byte) (cipher.Block, error)
}

func bfKey(key []byte) ([]byte, error) { return key, nil }

func bfBlock(key []byte) (cipher.Block, error) {
	if len(key) == 0 {
		key = []byte{0} // BF_set_key with an empty key: zeros
	}
	return newBlowfish(key), nil
}

// fixedKey zero-pads or truncates keys to n bytes (DES, 3DES).
func fixedKey(n int) func([]byte) ([]byte, error) {
	return func(key []byte) ([]byte, error) {
		k := make([]byte, n)
		copy(k, key)
		return k, nil
	}
}

func desBlock(key []byte) (cipher.Block, error)  { return des.NewCipher(key) }
func des3Block(key []byte) (cipher.Block, error) { return des.NewTripleDESCipher(key) }

func castKey(key []byte) ([]byte, error) { return key, nil }

func castBlock(key []byte) (cipher.Block, error) { return newCast5(key), nil }

// aesKey zero-pads keys to the next AES key size.
func aesKey(key []byte) ([]byte, error) {
	var n int
	switch {
	case len(key) <= 16:
		n = 16
	case len(key) <= 24:
		n = 24
	case len(key) <= 32:
		n = 32
	default:
		return nil, pxeKeyTooBig
	}
	k := make([]byte, n)
	copy(k, key)
	return k, nil
}

func aesBlock(key []byte) (cipher.Block, error) { return aes.NewCipher(key) }

var cipherAlgos = map[string]*cipherAlgo{}

// cipherAliases are openssl.c's ossl_aliases.
var cipherAliases = map[string]string{
	"bf":           "bf-cbc",
	"blowfish":     "bf-cbc",
	"blowfish-cbc": "bf-cbc",
	"blowfish-ecb": "bf-ecb",
	"blowfish-cfb": "bf-cfb",
	"des":          "des-cbc",
	"3des":         "des3-cbc",
	"3des-ecb":     "des3-ecb",
	"3des-cbc":     "des3-cbc",
	"cast5":        "cast5-cbc",
	"aes":          "aes-cbc",
	"rijndael":     "aes-cbc",
	"rijndael-cbc": "aes-cbc",
	"rijndael-ecb": "aes-ecb",
	"rijndael-cfb": "aes-cfb",
}

func init() {
	add := func(name string, blockSize, keySize int, key func([]byte) ([]byte, error),
		block func([]byte) (cipher.Block, error), modes ...string) {
		for _, m := range modes {
			cipherAlgos[name+"-"+m] = &cipherAlgo{blockSize, keySize, m, key, block}
		}
	}
	add("bf", 8, 56, bfKey, bfBlock, "cbc", "ecb", "cfb")
	add("des", 8, 8, fixedKey(8), desBlock, "ecb", "cbc")
	add("des3", 8, 24, fixedKey(24), des3Block, "ecb", "cbc")
	add("cast5", 8, 16, castKey, castBlock, "ecb", "cbc")
	add("aes", 16, 32, aesKey, aesBlock, "ecb", "cbc", "cfb")
}

// pxCipher is px_find_cipher's result: a cipher to Init with a key and
// IV, then use to encrypt or decrypt (chaining across calls, like an EVP
// context).
type pxCipher struct {
	algo  *cipherAlgo
	block cipher.Block
	iv    []byte
}

// findCipher is px_find_cipher: the cipher called name (or an alias).
func findCipher(name string) (*pxCipher, error) {
	for alias, n := range cipherAliases {
		if strings.EqualFold(alias, name) {
			name = n
			break
		}
	}
	a := cipherAlgos[name]
	if a == nil {
		return nil, pxeNoCipher
	}
	return &pxCipher{algo: a}, nil
}

func (c *pxCipher) BlockSize() int { return c.algo.blockSize }
func (c *pxCipher) KeySize() int   { return c.algo.keySize }
func (c *pxCipher) IVSize() int    { return c.algo.blockSize }

// Init sets the key and the IV (nil: zeros; otherwise IVSize bytes).
func (c *pxCipher) Init(key, iv []byte) error {
	k, err := c.algo.key(key)
	if err != nil {
		return err
	}
	if c.block, err = c.algo.block(k); err != nil {
		return pxeCipherInit
	}
	c.iv = make([]byte, c.algo.blockSize)
	copy(c.iv, iv)
	return nil
}

// Encrypt encrypts data, adding PKCS#7 padding if padding is set
// (otherwise data must be whole blocks, except in CFB mode).
func (c *pxCipher) Encrypt(padding bool, data []byte) ([]byte, error) {
	bs := c.algo.blockSize
	if c.algo.mode == "cfb" {
		out := make([]byte, len(data))
		c.cfb(out, data, false)
		return out, nil
	}
	if padding {
		n := bs - len(data)%bs
		p := make([]byte, len(data)+n)
		copy(p, data)
		for i := len(data); i < len(p); i++ {
			p[i] = byte(n)
		}
		data = p
	} else if len(data)%bs != 0 {
		return nil, pxeEncryptFailed
	}
	out := make([]byte, len(data))
	for i := 0; i < len(data); i += bs {
		if c.algo.mode == "cbc" {
			subtleXOR(c.iv, data[i:i+bs])
			c.block.Encrypt(out[i:i+bs], c.iv)
			copy(c.iv, out[i:i+bs])
		} else {
			c.block.Encrypt(out[i:i+bs], data[i:i+bs])
		}
	}
	return out, nil
}

// Decrypt decrypts data, checking and removing PKCS#7 padding if padding
// is set.
func (c *pxCipher) Decrypt(padding bool, data []byte) ([]byte, error) {
	bs := c.algo.blockSize
	if c.algo.mode == "cfb" {
		out := make([]byte, len(data))
		c.cfb(out, data, true)
		return out, nil
	}
	if len(data)%bs != 0 || padding && len(data) == 0 {
		return nil, pxeDecryptFailed
	}
	out := make([]byte, len(data))
	for i := 0; i < len(data); i += bs {
		c.block.Decrypt(out[i:i+bs], data[i:i+bs])
		if c.algo.mode == "cbc" {
			subtleXOR(out[i:i+bs], c.iv)
			copy(c.iv, data[i:i+bs])
		}
	}
	if padding {
		n := int(out[len(out)-1])
		if n == 0 || n > bs {
			return nil, pxeDecryptFailed
		}
		for _, b := range out[len(out)-n:] {
			if int(b) != n {
				return nil, pxeDecryptFailed
			}
		}
		out = out[:len(out)-n]
	}
	return out, nil
}

// cfb runs CFB mode with full-block feedback (OpenSSL's CFB64 for
// Blowfish, CFB128 for AES); the position in the block carries over calls.
func (c *pxCipher) cfb(dst, src []byte, decrypt bool) {
	bs := c.algo.blockSize
	ks := make([]byte, bs)
	for i := range src {
		if i%bs == 0 {
			c.block.Encrypt(ks, c.iv)
		}
		in := src[i]
		dst[i] = in ^ ks[i%bs]
		if decrypt {
			c.iv[i%bs] = in
		} else {
			c.iv[i%bs] = dst[i]
		}
	}
}

// subtleXOR sets dst ^= src.
func subtleXOR(dst, src []byte) {
	for i := range dst {
		dst[i] ^= src[i]
	}
}

// combo is px_find_combo's result: a cipher and its padding.
type combo struct {
	c       *pxCipher
	padding bool
}

// findCombo is px_find_combo: "cipher[/pad:pkcs|none]".
func findCombo(name string) (*combo, error) {
	parts := strings.Split(name, "/")
	pad := ""
	hasPad := false
	for _, p := range parts[1:] {
		if p == "" {
			continue
		}
		k, v, ok := strings.Cut(p, ":")
		if !ok {
			return nil, pxeBadFormat
		}
		if k != "pad" {
			return nil, pxeBadOption
		}
		pad, hasPad = v, true
	}
	c, err := findCipher(parts[0])
	if err != nil {
		return nil, pxeNoCipher
	}
	cx := &combo{c: c, padding: true}
	if hasPad {
		switch pad {
		case "pkcs":
		case "none":
			cx.padding = false
		default:
			return nil, pxeNoCipher
		}
	}
	return cx, nil
}

// init is combo_init: the key is cut to the cipher's key size, the IV
// zero-padded or cut to its block size.
func (cx *combo) init(key, iv []byte) error {
	if len(key) > cx.c.KeySize() {
		key = key[:cx.c.KeySize()]
	}
	ivbuf := make([]byte, cx.c.IVSize())
	copy(ivbuf, iv)
	return cx.c.Init(key, ivbuf)
}

// crypt runs encrypt(), decrypt() and their _iv variants: what names the
// function in errors.
func crypt(what string, decrypt bool, data, key, iv []byte, typ string) ([]byte, error) {
	cx, err := findProvider(typ, findCombo)
	if err != nil {
		return nil, err
	}
	var res []byte
	if err = cx.init(key, iv); err == nil {
		if decrypt {
			res, err = cx.c.Decrypt(cx.padding, data)
		} else {
			res, err = cx.c.Encrypt(cx.padding, data)
		}
	}
	if err != nil {
		return nil, pg.Errorf(codeExternalRoutine, "%s error: %s", what, err.Error())
	}
	return res, nil
}

// Encrypt encrypts data with key and cipher typ ("aes", "bf", "des",
// "3des", "cast5", optionally with "-ecb"/"-cbc"/"-cfb" and "/pad:none").
//
//gopgext:function name=encrypt immutable parallel=safe
func Encrypt(data, key []byte, typ string) ([]byte, error) {
	return crypt("encrypt", false, data, key, nil, typ)
}

// Decrypt decrypts data with key and cipher typ.
//
//gopgext:function name=decrypt immutable parallel=safe
func Decrypt(data, key []byte, typ string) ([]byte, error) {
	return crypt("decrypt", true, data, key, nil, typ)
}

// EncryptIV encrypts data with key, initialization vector iv and cipher
// typ.
//
//gopgext:function name=encrypt_iv immutable parallel=safe
func EncryptIV(data, key, iv []byte, typ string) ([]byte, error) {
	return crypt("encrypt_iv", false, data, key, iv, typ)
}

// DecryptIV decrypts data with key, initialization vector iv and cipher
// typ.
//
//gopgext:function name=decrypt_iv immutable parallel=safe
func DecryptIV(data, key, iv []byte, typ string) ([]byte, error) {
	return crypt("decrypt_iv", true, data, key, iv, typ)
}

// GenRandomBytes returns n (1 to 1024) cryptographically strong random
// bytes.
//
//gopgext:function name=gen_random_bytes parallel=safe
func GenRandomBytes(n int32) ([]byte, error) {
	if n < 1 || n > 1024 {
		return nil, pg.Errorf(codeExternalRoutine, "Length not in range")
	}
	b := make([]byte, n)
	if err := randomBytes(b); err != nil {
		return nil, err
	}
	return b, nil
}
