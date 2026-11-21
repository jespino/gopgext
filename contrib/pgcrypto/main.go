// pgcrypto is a Go port of PostgreSQL's contrib/pgcrypto (version 1.4):
// hashes, HMACs, password hashing (crypt, gen_salt), raw encryption
// (encrypt, decrypt) and OpenPGP encryption (pgp_sym_*, pgp_pub_*, armor).
//
// It uses Go's standard library for the primitives upstream takes from
// OpenSSL (digests, AES, DES, 3DES, big numbers) and ports the rest:
// Blowfish, CAST5, RIPEMD-160, the crypt(3) algorithms and the OpenPGP
// code. Random bytes come from crypto/rand, which gopgext backs with
// PostgreSQL's pg_strong_random, like upstream.
//
// The SQL functions keep upstream's names, signatures, volatility and
// error messages; see README.md for the differences.
package main

import (
	"crypto/rand"
	"unicode/utf8"

	"gopgext/pg"
)

// pgcrypto.builtin_crypto_enabled: whether crypt() and gen_salt() (this
// module's own implementations) may be used. "fips" disables them in FIPS
// mode, which this port never runs in.
var builtinCryptoEnabled = pg.NewEnumSetting("builtin_crypto_enabled", "on", []string{"on", "off", "fips"},
	"Sets if built-in crypto functions are enabled.", pg.Superuser)

func main() {}

// FipsMode reports whether the cryptographic library runs in FIPS mode
// (upstream: OpenSSL's). Always false: Go's FIPS 140 mode isn't available.
//
//gopgext:function name=fips_mode parallel=safe
func FipsMode() bool { return false }

// checkBuiltinCrypto is CheckBuiltinCryptoMode: crypt() and gen_salt()
// call it first.
func checkBuiltinCrypto() error {
	if builtinCryptoEnabled.Get() == "off" {
		return pg.Errorf(pg.CodeInternalError, "use of built-in crypto functions is disabled")
	}
	return nil
}

// randomBytes fills b with strong random bytes (crypto/rand, backed by
// pg_strong_random).
func randomBytes(b []byte) error {
	if _, err := rand.Read(b); err != nil {
		return pxThrow(pxeNoRandom)
	}
	return nil
}

// codeExternalRoutine is external_routine_invocation_exception (39000),
// the SQLSTATE of most pgcrypto errors.
const codeExternalRoutine = "39000"

// pxError is a px error code (px.h).
type pxError int

const (
	pxeNoHash                pxError = -2
	pxeNoCipher              pxError = -3
	pxeBadOption             pxError = -5
	pxeBadFormat             pxError = -6
	pxeKeyTooBig             pxError = -7
	pxeCipherInit            pxError = -8
	pxeHashUnusableForHMAC   pxError = -9
	pxeBug                   pxError = -12
	pxeArgumentError         pxError = -13
	pxeUnknownSaltAlgo       pxError = -14
	pxeBadSaltRounds         pxError = -15
	pxeNoRandom              pxError = -17
	pxeDecryptFailed         pxError = -18
	pxeEncryptFailed         pxError = -19
	pxePgpCorruptData        pxError = -100
	pxePgpCorruptArmor       pxError = -101
	pxePgpUnsupportedCompr   pxError = -102
	pxePgpUnsupportedCipher  pxError = -103
	pxePgpUnsupportedHash    pxError = -104
	pxePgpCompressionError   pxError = -105
	pxePgpNotText            pxError = -106
	pxePgpUnexpectedPkt      pxError = -107
	pxePgpMathFailed         pxError = -109
	pxePgpShortElgamalKey    pxError = -110
	pxePgpKeyTooBig          pxError = -111
	pxePgpUnknownPubalgo     pxError = -112
	pxePgpWrongKey           pxError = -113
	pxePgpMultipleKeys       pxError = -114
	pxePgpExpectPublicKey    pxError = -115
	pxePgpExpectSecretKey    pxError = -116
	pxePgpNotV4Keypkt        pxError = -117
	pxePgpKeypktCorrupt      pxError = -118
	pxePgpNoUsableKey        pxError = -119
	pxePgpNeedSecretPsw      pxError = -120
	pxePgpBadS2KMode         pxError = -121
	pxePgpUnsupportedPubalgo pxError = -122
	pxePgpMultipleSubkeys    pxError = -123
)

var pxMessages = map[pxError]string{
	pxeNoHash:                "No such hash algorithm",
	pxeNoCipher:              "No such cipher algorithm",
	pxeBadOption:             "Unknown option",
	pxeBadFormat:             "Badly formatted type",
	pxeKeyTooBig:             "Key was too big",
	pxeCipherInit:            "Cipher cannot be initialized",
	pxeHashUnusableForHMAC:   "This hash algorithm is unusable for HMAC",
	pxeBug:                   "pgcrypto bug",
	pxeArgumentError:         "Illegal argument to function",
	pxeUnknownSaltAlgo:       "Unknown salt algorithm",
	pxeBadSaltRounds:         "Incorrect number of rounds",
	pxeNoRandom:              "Failed to generate strong random bits",
	pxeDecryptFailed:         "Decryption failed",
	pxeEncryptFailed:         "Encryption failed",
	pxePgpCorruptData:        "Wrong key or corrupt data",
	pxePgpCorruptArmor:       "Corrupt ascii-armor",
	pxePgpUnsupportedCompr:   "Unsupported compression algorithm",
	pxePgpUnsupportedCipher:  "Unsupported cipher algorithm",
	pxePgpUnsupportedHash:    "Unsupported digest algorithm",
	pxePgpCompressionError:   "Compression error",
	pxePgpNotText:            "Not text data",
	pxePgpUnexpectedPkt:      "Unexpected packet in key data",
	pxePgpMathFailed:         "Math operation failed",
	pxePgpShortElgamalKey:    "Elgamal keys must be at least 1024 bits long",
	pxePgpKeyTooBig:          "Session key too big",
	pxePgpUnknownPubalgo:     "Unknown public-key encryption algorithm",
	pxePgpWrongKey:           "Wrong key",
	pxePgpMultipleKeys:       "Several keys given - pgcrypto does not handle keyring",
	pxePgpExpectPublicKey:    "Refusing to encrypt with secret key",
	pxePgpExpectSecretKey:    "Cannot decrypt with public key",
	pxePgpNotV4Keypkt:        "Only V4 key packets are supported",
	pxePgpKeypktCorrupt:      "Corrupt key packet",
	pxePgpNoUsableKey:        "No encryption key found",
	pxePgpNeedSecretPsw:      "Need password for secret key",
	pxePgpBadS2KMode:         "Bad S2K mode",
	pxePgpUnsupportedPubalgo: "Unsupported public key algorithm",
	pxePgpMultipleSubkeys:    "Several subkeys not supported",
}

// Error is px_strerror.
func (e pxError) Error() string {
	if m, ok := pxMessages[e]; ok {
		return m
	}
	return "Bad error code"
}

// pxThrow is px_THROW_ERROR: the error raised for err, a pxError (other
// errors are returned unchanged).
func pxThrow(err error) error {
	e, ok := err.(pxError)
	switch {
	case !ok:
		return err
	case e == pxeNoRandom:
		return pg.Errorf(pg.CodeInternalError, "could not generate a random number")
	}
	return pg.Errorf(codeExternalRoutine, "%s", e.Error())
}

// providerName is find_provider's downcase_truncate_identifier: name with
// ASCII letters lower-cased, cut to 63 bytes (whole UTF-8 characters).
func providerName(name string) string {
	b := []byte(name)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + 'a' - 'A'
		}
	}
	if len(b) > 63 {
		n := 0
		for n < len(b) {
			_, size := utf8.DecodeRune(b[n:])
			if n+size > 63 {
				break
			}
			n += size
		}
		b = b[:n]
	}
	return string(b)
}

// findProvider is find_provider: looks the lower-cased name up with
// lookup, raising "Cannot use ..." if it fails.
func findProvider[T any](name string, lookup func(string) (T, error)) (T, error) {
	name = providerName(name)
	res, err := lookup(name)
	if pe, ok := err.(pxError); ok {
		return res, pg.Errorf(pg.CodeInvalidParameterValue, "Cannot use \"%s\": %s", name, pe.Error())
	}
	return res, err
}
