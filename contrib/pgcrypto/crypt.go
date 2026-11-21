package main

// crypt() and gen_salt(): password hashing, ported from px-crypt.c and
// crypt-gensalt.c (Solar Designer, public domain). The algorithms are in
// crypt_des.go, crypt_md5.go, crypt_blowfish.go and crypt_sha.go.

import (
	"strconv"
	"strings"

	"gopgext/pg"
)

// cryptAlgos is px_crypt_list: the algorithm is chosen by the salt's
// prefix; anything else is traditional DES. "$2$" isn't supported.
var cryptAlgos = []struct {
	id    string
	crypt func(pw, salt []byte) ([]byte, error)
}{
	{"$2a$", cryptBlowfish},
	{"$2x$", cryptBlowfish},
	{"$2$", nil},
	{"$1$", func(pw, salt []byte) ([]byte, error) { return cryptMD5(pw, salt), nil }},
	{"$5$", cryptSHA},
	{"$6$", cryptSHA},
	{"_", cryptDES},
	{"", cryptDES},
}

// Crypt hashes password with salt (from gen_salt, or a previous hash of
// password) in crypt(3)'s format.
//
//gopgext:function name=crypt immutable parallel=safe
func Crypt(password, salt string) (string, error) {
	if err := checkBuiltinCrypto(); err != nil {
		return "", err
	}
	for _, a := range cryptAlgos {
		if !strings.HasPrefix(salt, a.id) {
			continue
		}
		var res []byte
		var err error
		if a.crypt != nil {
			res, err = a.crypt([]byte(password), []byte(salt))
		}
		if err != nil {
			return "", err
		}
		if res == nil {
			return "", pg.Errorf(codeExternalRoutine, "crypt(3) returned NULL")
		}
		return string(res), nil
	}
	panic("unreachable")
}

const cryptItoa64 = desA64

// cryptGenerators is gen_list: name, random bytes, default, minimum and
// maximum rounds (no default: rounds are only checked by the generator).
var cryptGenerators = []struct {
	name                  string
	gen                   func(count uint64, input []byte) []byte
	inputLen              int
	defRounds, minR, maxR int
}{
	{"des", genSaltDES, 2, 0, 0, 0},
	{"md5", genSaltMD5, 6, 0, 0, 0},
	{"xdes", genSaltXDES, 3, 29 * 25, 1, 0xFFFFFF},
	{"bf", genSaltBF, 16, 6, 4, 31},
	{"sha256crypt", genSaltSHA("$5$"), shacryptSaltMaxLen, shacryptRoundsDefault, shacryptRoundsMin, shacryptRoundsMax},
	{"sha512crypt", genSaltSHA("$6$"), shacryptSaltMaxLen, shacryptRoundsDefault, shacryptRoundsMin, shacryptRoundsMax},
}

// genSalt is px_gen_salt.
func genSalt(typ string, rounds int32) (string, error) {
	if err := checkBuiltinCrypto(); err != nil {
		return "", err
	}
	// text_to_cstring_buffer into PX_MAX_SALT_LEN + 1 bytes.
	if len(typ) > 128 {
		typ = typ[:128]
	}
	fail := func(e pxError) error {
		return pg.Errorf(pg.CodeInvalidParameterValue, "gen_salt: %s", e.Error())
	}
	for _, g := range cryptGenerators {
		if !cryptASCIIEqualFold(g.name, typ) {
			continue
		}
		r := int(rounds)
		if g.defRounds != 0 {
			if r == 0 {
				r = g.defRounds
			}
			if r < g.minR || r > g.maxR {
				return "", fail(pxeBadSaltRounds)
			}
		}
		input := make([]byte, g.inputLen)
		if err := randomBytes(input); err != nil {
			return "", err
		}
		// The C code passes rounds as an unsigned long.
		res := g.gen(uint64(int64(r)), input)
		clear(input)
		if res == nil {
			return "", fail(pxeBadSaltRounds)
		}
		return string(res), nil
	}
	return "", fail(pxeUnknownSaltAlgo)
}

// GenSalt generates a random salt for crypt(): type is des, xdes, md5,
// bf, sha256crypt or sha512crypt.
//
//gopgext:function name=gen_salt parallel=safe
func GenSalt(typ string) (string, error) { return genSalt(typ, 0) }

// GenSaltRounds generates a random salt for crypt() with the given
// iteration count (xdes, bf, sha256crypt, sha512crypt).
//
//gopgext:function name=gen_salt parallel=safe
func GenSaltRounds(typ string, rounds int32) (string, error) { return genSalt(typ, rounds) }

// genSaltDES is _crypt_gensalt_traditional_rn.
func genSaltDES(count uint64, input []byte) []byte {
	if count != 0 && count != 25 {
		return nil
	}
	return []byte{cryptItoa64[input[0]&0x3f], cryptItoa64[input[1]&0x3f]}
}

func cryptTo64x4(dst []byte, v uint32) []byte {
	return append(dst, cryptItoa64[v&0x3f], cryptItoa64[(v>>6)&0x3f], cryptItoa64[(v>>12)&0x3f], cryptItoa64[(v>>18)&0x3f])
}

func le24(b []byte) uint32 { return uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16 }

// genSaltXDES is _crypt_gensalt_extended_rn: odd counts only.
func genSaltXDES(count uint64, input []byte) []byte {
	if count != 0 && (count > 0xffffff || count&1 == 0) {
		return nil
	}
	if count == 0 {
		count = 725
	}
	out := cryptTo64x4([]byte{'_'}, uint32(count))
	return cryptTo64x4(out, le24(input))
}

// genSaltMD5 is _crypt_gensalt_md5_rn.
func genSaltMD5(count uint64, input []byte) []byte {
	if count != 0 && count != 1000 {
		return nil
	}
	out := cryptTo64x4([]byte("$1$"), le24(input))
	return cryptTo64x4(out, le24(input[3:]))
}

// genSaltBF is _crypt_gensalt_blowfish_rn.
func genSaltBF(count uint64, input []byte) []byte {
	if count != 0 && (count < 4 || count > 31) {
		return nil
	}
	if count == 0 {
		count = 5
	}
	out := []byte{'$', '2', 'a', '$', byte('0' + count/10), byte('0' + count%10), '$'}
	return bcryptEncode(out, input[:16])
}

// genSaltSHA is _crypt_gensalt_sha256_rn/_crypt_gensalt_sha512_rn.
func genSaltSHA(magic string) func(uint64, []byte) []byte {
	return func(count uint64, input []byte) []byte {
		out := []byte(magic + "rounds=" + strconv.FormatUint(count, 10) + "$")
		for _, c := range input {
			out = append(out, cryptItoa64[c&0x3f])
		}
		return out
	}
}

// cryptASCIIEqualFold is pg_strcasecmp(a, b) == 0 (ASCII letters only).
func cryptASCIIEqualFold(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		x, y := a[i], b[i]
		if x >= 'A' && x <= 'Z' {
			x += 'a' - 'A'
		}
		if y >= 'A' && y <= 'Z' {
			y += 'a' - 'A'
		}
		if x != y {
			return false
		}
	}
	return true
}
