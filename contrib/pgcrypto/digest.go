package main

// Hashes and HMACs: digest() and hmac(), and the digests of crypt() and
// the OpenPGP code (pgp.go). Upstream looks names up with OpenSSL's
// EVP_get_digestbyname; this table has OpenSSL's names and aliases for
// the algorithms Go provides.

import (
	"crypto/hmac"
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha3"
	"crypto/sha512"
	"hash"
	"strings"

	"gopgext/pg"
)

// digestAlgo is a hash algorithm.
type digestAlgo struct {
	new func() hash.Hash
	// xof: an extendable-output function (SHAKE), which has no fixed size
	// for digest() and hmac().
	xof bool
}

func sha3New(f func() *sha3.SHA3) func() hash.Hash { return func() hash.Hash { return f() } }

var digestAlgos = map[string]*digestAlgo{}

func init() {
	for _, d := range []struct {
		new   func() hash.Hash
		names []string
	}{
		{md5.New, []string{"md5", "ssl3-md5", "rsa-md5", "md5withrsaencryption", "1.2.840.113549.2.5"}},
		{sha1.New, []string{"sha1", "sha-1", "ssl3-sha1", "rsa-sha1", "sha1withrsaencryption", "1.3.14.3.2.26"}},
		{sha256.New224, []string{"sha224", "sha-224", "sha2-224", "rsa-sha224", "sha224withrsaencryption", "2.16.840.1.101.3.4.2.4"}},
		{sha256.New, []string{"sha256", "sha-256", "sha2-256", "rsa-sha256", "sha256withrsaencryption", "2.16.840.1.101.3.4.2.1"}},
		{sha512.New384, []string{"sha384", "sha-384", "sha2-384", "rsa-sha384", "sha384withrsaencryption", "2.16.840.1.101.3.4.2.2"}},
		{sha512.New, []string{"sha512", "sha-512", "sha2-512", "rsa-sha512", "sha512withrsaencryption", "2.16.840.1.101.3.4.2.3"}},
		{sha512.New512_224, []string{"sha512-224", "sha-512/224", "sha2-512/224", "rsa-sha512/224", "sha512-224withrsaencryption", "2.16.840.1.101.3.4.2.5"}},
		{sha512.New512_256, []string{"sha512-256", "sha-512/256", "sha2-512/256", "rsa-sha512/256", "sha512-256withrsaencryption", "2.16.840.1.101.3.4.2.6"}},
		{sha3New(sha3.New224), []string{"sha3-224", "rsa-sha3-224", "id-rsassa-pkcs1-v1_5-with-sha3-224", "2.16.840.1.101.3.4.2.7"}},
		{sha3New(sha3.New256), []string{"sha3-256", "rsa-sha3-256", "id-rsassa-pkcs1-v1_5-with-sha3-256", "2.16.840.1.101.3.4.2.8"}},
		{sha3New(sha3.New384), []string{"sha3-384", "rsa-sha3-384", "id-rsassa-pkcs1-v1_5-with-sha3-384", "2.16.840.1.101.3.4.2.9"}},
		{sha3New(sha3.New512), []string{"sha3-512", "rsa-sha3-512", "id-rsassa-pkcs1-v1_5-with-sha3-512", "2.16.840.1.101.3.4.2.10"}},
		{newRipemd160, []string{"ripemd160", "ripemd-160", "ripemd", "rmd160", "rsa-ripemd160", "ripemd160withrsa", "1.3.36.3.2.1"}},
		{newMD5SHA1, []string{"md5-sha1"}},
	} {
		a := &digestAlgo{new: d.new}
		for _, n := range d.names {
			digestAlgos[n] = a
		}
	}
	xof := &digestAlgo{xof: true}
	for _, n := range []string{"shake128", "shake-128", "2.16.840.1.101.3.4.2.11", "shake256", "shake-256", "2.16.840.1.101.3.4.2.12"} {
		digestAlgos[n] = xof
	}
}

// findDigest is px_find_digest: the hash algorithm called name. Like
// OpenSSL's lookup, it ignores up to two trailing spaces.
func findDigest(name string) (*digestAlgo, error) {
	for i := 0; i < 2 && strings.HasSuffix(name, " "); i++ {
		name = name[:len(name)-1]
	}
	if a := digestAlgos[name]; a != nil {
		return a, nil
	}
	return nil, pxeNoHash
}

// sizeError is what OpenSSL reports for the size of an XOF digest.
var sizeError = pg.Errorf(pg.CodeInternalError, "EVP_MD_CTX_size() failed")

func digest(data []byte, typ string) ([]byte, error) {
	a, err := findProvider(typ, findDigest)
	if err != nil {
		return nil, err
	}
	if a.xof {
		return nil, sizeError
	}
	h := a.new()
	h.Write(data)
	return h.Sum(nil), nil
}

func hmacOf(data, key []byte, typ string) ([]byte, error) {
	a, err := findProvider(typ, findDigest)
	if err != nil {
		return nil, err
	}
	if a.xof {
		return nil, sizeError
	}
	h := hmac.New(a.new, key)
	h.Write(data)
	return h.Sum(nil), nil
}

// Digest computes a binary hash of data with algorithm typ (md5, sha1,
// sha224, sha256, sha384, sha512, sha3-256, ripemd160...).
//
//gopgext:function name=digest immutable parallel=safe
func Digest(data []byte, typ string) ([]byte, error) { return digest(data, typ) }

// DigestText computes a binary hash of data with algorithm typ.
//
//gopgext:function name=digest immutable parallel=safe
func DigestText(data, typ string) ([]byte, error) { return digest([]byte(data), typ) }

// Hmac computes a keyed hash (HMAC) of data with key and algorithm typ.
//
//gopgext:function name=hmac immutable parallel=safe
func Hmac(data, key []byte, typ string) ([]byte, error) { return hmacOf(data, key, typ) }

// HmacText computes a keyed hash (HMAC) of data with key and algorithm typ.
//
//gopgext:function name=hmac immutable parallel=safe
func HmacText(data, key, typ string) ([]byte, error) {
	return hmacOf([]byte(data), []byte(key), typ)
}

// md5sha1 is OpenSSL's MD5-SHA1: both hashes, concatenated (TLS 1.0).
type md5sha1 struct{ md5, sha1 hash.Hash }

func newMD5SHA1() hash.Hash { return &md5sha1{md5.New(), sha1.New()} }

func (h *md5sha1) Write(p []byte) (int, error) {
	h.md5.Write(p)
	return h.sha1.Write(p)
}

func (h *md5sha1) Sum(b []byte) []byte { return h.sha1.Sum(h.md5.Sum(b)) }

func (h *md5sha1) Reset() {
	h.md5.Reset()
	h.sha1.Reset()
}

func (h *md5sha1) Size() int      { return md5.Size + sha1.Size }
func (h *md5sha1) BlockSize() int { return 64 }
