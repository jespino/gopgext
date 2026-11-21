package main

// The OpenPGP SQL functions (pgp-pgsql.c): option strings, text handling
// and the wrappers.

import (
	"fmt"
	"iter"
	"strings"
	"unicode/utf8"

	"gopgext/pg"
)

// pgpExpect holds the expect-* debugging options.
type pgpExpect struct {
	debug, expect                                               bool
	cipherAlgo, s2kMode, s2kCount, s2kCipherAlgo, s2kDigestAlgo int
	compressAlgo, useSessKey, disableMDC, unicodeMode           int
}

func newPGPExpect() *pgpExpect {
	return &pgpExpect{cipherAlgo: -1, s2kMode: -1, s2kCount: -1, s2kCipherAlgo: -1,
		s2kDigestAlgo: -1, compressAlgo: -1, useSessKey: -1, disableMDC: -1, unicodeMode: -1}
}

// check is check_expect: a NOTICE for each value that isn't as expected.
func (ex *pgpExpect) check(ctx *pgpContext) {
	c := func(name string, want, got int) {
		if want >= 0 && want != got {
			pg.Notice(fmt.Sprintf("pgp_decrypt: unexpected %s: expected %d got %d", name, want, got))
		}
	}
	c("cipher_algo", ex.cipherAlgo, ctx.cipherAlgo)
	c("s2k_mode", ex.s2kMode, ctx.s2kMode)
	c("s2k_count", ex.s2kCount, ctx.s2kCount)
	c("s2k_digest_algo", ex.s2kDigestAlgo, ctx.s2kDigestAlgo)
	c("use_sess_key", ex.useSessKey, ctx.useSessKey)
	if ctx.useSessKey != 0 {
		c("s2k_cipher_algo", ex.s2kCipherAlgo, ctx.s2kCipherAlgo)
	}
	c("disable_mdc", ex.disableMDC, ctx.disableMDC)
	c("compress_algo", ex.compressAlgo, ctx.compressAlgo)
	c("unicode_mode", ex.unicodeMode, ctx.unicodeMode)
}

// pgpAtoi is C's atoi (glibc: strtol, cast to int).
func pgpAtoi(s string) int {
	i, neg := 0, false
	if i < len(s) && (s[i] == '+' || s[i] == '-') {
		neg = s[i] == '-'
		i++
	}
	var n uint64
	over := false
	for ; i < len(s) && s[i] >= '0' && s[i] <= '9'; i++ {
		n = n*10 + uint64(s[i]-'0')
		if n > 1<<63 {
			over = true
			n = 1 << 63
		}
	}
	var v int64
	switch {
	case neg && (over || n == 1<<63):
		v = -1 << 63
	case over || n >= 1<<63:
		v = 1<<63 - 1
	case neg:
		v = -int64(n)
	default:
		v = int64(n)
	}
	return int(int32(v))
}

// setArg is set_arg.
func (ctx *pgpContext) setArg(key, val string, ex *pgpExpect) error {
	switch key {
	case "cipher-algo":
		code := pgpCipherCode(val)
		if code < 0 {
			return pxError(code)
		}
		ctx.cipherAlgo = code
	case "disable-mdc":
		ctx.disableMDC = b2i(pgpAtoi(val) != 0)
	case "sess-key":
		ctx.useSessKey = b2i(pgpAtoi(val) != 0)
	case "s2k-mode":
		return ctx.setS2KMode(pgpAtoi(val))
	case "s2k-count":
		return ctx.setS2KCount(pgpAtoi(val))
	case "s2k-digest-algo":
		code := pgpDigestCode(val)
		if code < 0 {
			return pxError(code)
		}
		ctx.s2kDigestAlgo = code
	case "s2k-cipher-algo":
		code := pgpCipherCode(val)
		if code < 0 {
			return pxError(code)
		}
		ctx.s2kCipherAlgo = code
	case "compress-algo":
		return ctx.setCompressAlgo(pgpAtoi(val))
	case "compress-level":
		return ctx.setCompressLevel(pgpAtoi(val))
	case "convert-crlf":
		ctx.convertCRLF = pgpAtoi(val) != 0
	case "unicode-mode":
		ctx.unicodeMode = b2i(pgpAtoi(val) != 0)
	case "ignore-cipher-failure":
		ctx.ignoreCipherFailure = pgpAtoi(val) != 0
	case "debug":
		ex.debug = pgpAtoi(val) != 0
	case "expect-cipher-algo":
		ex.expect, ex.cipherAlgo = true, pgpCipherCode(val)
	case "expect-disable-mdc":
		ex.expect, ex.disableMDC = true, pgpAtoi(val)
	case "expect-sess-key":
		ex.expect, ex.useSessKey = true, pgpAtoi(val)
	case "expect-s2k-mode":
		ex.expect, ex.s2kMode = true, pgpAtoi(val)
	case "expect-s2k-count":
		ex.expect, ex.s2kCount = true, pgpAtoi(val)
	case "expect-s2k-digest-algo":
		ex.expect, ex.s2kDigestAlgo = true, pgpDigestCode(val)
	case "expect-s2k-cipher-algo":
		ex.expect, ex.s2kCipherAlgo = true, pgpCipherCode(val)
	case "expect-compress-algo":
		ex.expect, ex.compressAlgo = true, pgpAtoi(val)
	case "expect-unicode-mode":
		ex.expect, ex.unicodeMode = true, pgpAtoi(val)
	default:
		return pxeArgumentError
	}
	return nil
}

func pgpIsSpace(c byte) bool { return c == ' ' || c == '\t' || c == '\n' }

// pgpGetWord is getword: a word ("=" and "," are words), skipping
// whitespace around it; returns it and the position after it.
func pgpGetWord(s string, p int) (string, int) {
	for p < len(s) && pgpIsSpace(s[p]) {
		p++
	}
	start := p
	if p < len(s) && (s[p] == '=' || s[p] == ',') {
		p++
	} else {
		for p < len(s) && !pgpIsSpace(s[p]) && s[p] != '=' && s[p] != ',' {
			p++
		}
	}
	word := s[start:p]
	for p < len(s) && pgpIsSpace(s[p]) {
		p++
	}
	return word, p
}

// parseArgs is parse_args: "key=value, key=value", case-insensitive.
func (ctx *pgpContext) parseArgs(args string, ex *pgpExpect) error {
	b := []byte(args)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + 'a' - 'A'
		}
	}
	s := string(b)
	p := 0
	var err error
	for p < len(s) {
		err = pxeArgumentError
		var key, val string
		key, p = pgpGetWord(s, p)
		if p >= len(s) || s[p] != '=' {
			break
		}
		p++
		val, p = pgpGetWord(s, p)
		if p < len(s) {
			if s[p] != ',' {
				break
			}
			p++
		}
		if key == "" || val == "" {
			break
		}
		if err = ctx.setArg(key, val, ex); err != nil {
			break
		}
	}
	return err
}

// pgpInitWork is init_work: a context with the options.
func pgpInitWork(isText bool, args *string) (*pgpContext, *pgpExpect, error) {
	ctx := newPGPContext()
	ex := newPGPExpect()
	if args != nil {
		if err := ctx.parseArgs(*args, ex); err != nil {
			return nil, nil, pxThrow(err)
		}
	}
	pgpDebugOn = ex.debug
	ctx.textMode = isText
	return ctx, ex, nil
}

// ---- Text and encodings ----------------------------------------------------

var pgpServerEncoding string

// pgpEncoding returns the database encoding.
func pgpEncoding() (string, error) {
	if pgpServerEncoding == "" {
		if err := pg.QueryRow("SELECT current_setting('server_encoding')").Scan(&pgpServerEncoding); err != nil {
			return "", err
		}
	}
	return pgpServerEncoding, nil
}

// pgpVerifyUTF8 is pg_verify_mbstr for UTF8: the error for the first
// invalid character, or a NUL.
func pgpVerifyUTF8(b []byte, nulOnly bool) error {
	for i := 0; i < len(b); {
		r, size := rune(b[i]), 1
		if b[i] >= 0x80 && !nulOnly {
			r, size = utf8.DecodeRune(b[i:])
		}
		if b[i] == 0 || r == utf8.RuneError && size == 1 {
			enc := "UTF8"
			if nulOnly {
				enc = "SQL_ASCII"
			}
			l := 1
			if !nulOnly {
				switch c := b[i]; {
				case c&0xe0 == 0xc0:
					l = 2
				case c&0xf0 == 0xe0:
					l = 3
				case c&0xf8 == 0xf0:
					l = 4
				}
			}
			l = min(l, len(b)-i)
			hex := make([]string, l)
			for j := range hex {
				hex[j] = fmt.Sprintf("0x%02x", b[i+j])
			}
			return pg.Errorf("22021", "invalid byte sequence for encoding \"%s\": %s", enc, strings.Join(hex, " "))
		}
		i += size
	}
	return nil
}

// pgpConvert runs a conversion query on b, for databases neither UTF8 nor
// SQL_ASCII.
func pgpConvert(query string, b []byte) ([]byte, error) {
	var res []byte
	if err := pg.QueryRow(query, b).Scan(&res); err != nil {
		return nil, err
	}
	return res, nil
}

// pgpToServer is pg_verifymbstr on decrypted text, after converting it
// from UTF-8 if unicode is set.
func pgpToServer(b []byte, unicode bool) ([]byte, error) {
	enc, err := pgpEncoding()
	if err != nil {
		return nil, err
	}
	switch enc {
	case "UTF8":
		return b, pgpVerifyUTF8(b, false)
	case "SQL_ASCII":
		return b, pgpVerifyUTF8(b, true)
	}
	if unicode {
		return pgpConvert("SELECT convert_to(convert_from($1, 'UTF8'), current_setting('server_encoding'))", b)
	}
	return pgpConvert("SELECT convert_to(convert_from($1, current_setting('server_encoding')), current_setting('server_encoding'))", b)
}

// pgpToUTF8 is convert_to_utf8 (unicode-mode encryption of text).
func pgpToUTF8(b []byte) ([]byte, error) {
	enc, err := pgpEncoding()
	if err != nil || enc == "UTF8" || enc == "SQL_ASCII" {
		return b, err
	}
	return pgpConvert("SELECT convert_to(convert_from($1, current_setting('server_encoding')), 'UTF8')", b)
}

// pgpFromUTF8 is pg_any_to_server for UTF-8 strings (armor headers).
func pgpFromUTF8(s string) (string, error) {
	if err := pgpVerifyUTF8([]byte(s), false); err != nil {
		return "", err
	}
	enc, err := pgpEncoding()
	if err != nil || enc == "UTF8" || enc == "SQL_ASCII" {
		return s, err
	}
	var res string
	err = pg.QueryRow("SELECT convert_from($1, 'UTF8')", []byte(s)).Scan(&res)
	return res, err
}

// ---- Encryption and decryption ---------------------------------------------

// pgpEncryptInternal is encrypt_internal.
func pgpEncryptInternal(pub, isText bool, data, key []byte, args *string) ([]byte, error) {
	ctx, _, err := pgpInitWork(isText, args)
	if err != nil {
		return nil, err
	}
	defer func() { pgpDebugOn = false }()
	if isText && ctx.unicodeMode != 0 {
		if data, err = pgpToUTF8(data); err != nil {
			return nil, err
		}
	}
	if pub {
		ctx.pubKey, err = pgpReadKey(newBytesReader(key), nil, false)
	} else {
		err = ctx.setSymKey(key)
	}
	var res []byte
	if err == nil {
		err = pgpEncrypt(ctx, data, &res)
	}
	if err != nil {
		return nil, pxThrow(err)
	}
	return res, nil
}

// pgpDecryptInternal is decrypt_internal.
func pgpDecryptInternal(pub, needText bool, data, key []byte, psw *string, args *string) ([]byte, error) {
	ctx, ex, err := pgpInitWork(needText, args)
	if err != nil {
		return nil, err
	}
	defer func() { pgpDebugOn = false }()
	if pub {
		var p []byte
		if psw != nil {
			p = append(make([]byte, 0, len(*psw)), *psw...)
		}
		ctx.pubKey, err = pgpReadKey(newBytesReader(key), p, true)
	} else {
		err = ctx.setSymKey(key)
	}
	var res []byte
	gotUnicode := false
	if err == nil {
		err = pgpDecrypt(ctx, data, &res)
		if ex.expect {
			ex.check(ctx)
		}
		gotUnicode = ctx.unicodeMode != 0
	}
	if err != nil {
		return nil, pxThrow(err)
	}
	if needText {
		pgpDebugOn = false
		return pgpToServer(res, gotUnicode)
	}
	return res, nil
}

func strp(s string) *string { return &s }

// PgpSymEncrypt encrypts text with a password.
//
//gopgext:function name=pgp_sym_encrypt parallel=safe
func PgpSymEncrypt(data, psw string) ([]byte, error) {
	return pgpEncryptInternal(false, true, []byte(data), []byte(psw), nil)
}

// PgpSymEncryptArgs encrypts text with a password and options.
//
//gopgext:function name=pgp_sym_encrypt parallel=safe
func PgpSymEncryptArgs(data, psw, options string) ([]byte, error) {
	return pgpEncryptInternal(false, true, []byte(data), []byte(psw), &options)
}

// PgpSymEncryptBytea encrypts binary data with a password.
//
//gopgext:function name=pgp_sym_encrypt_bytea parallel=safe
func PgpSymEncryptBytea(data []byte, psw string) ([]byte, error) {
	return pgpEncryptInternal(false, false, data, []byte(psw), nil)
}

// PgpSymEncryptByteaArgs encrypts binary data with a password and options.
//
//gopgext:function name=pgp_sym_encrypt_bytea parallel=safe
func PgpSymEncryptByteaArgs(data []byte, psw, options string) ([]byte, error) {
	return pgpEncryptInternal(false, false, data, []byte(psw), &options)
}

// PgpSymDecrypt decrypts a message encrypted with a password, as text.
//
//gopgext:function name=pgp_sym_decrypt immutable parallel=safe
func PgpSymDecrypt(msg []byte, psw string) (string, error) {
	b, err := pgpDecryptInternal(false, true, msg, []byte(psw), nil, nil)
	return string(b), err
}

// PgpSymDecryptArgs decrypts a message encrypted with a password, as text.
//
//gopgext:function name=pgp_sym_decrypt immutable parallel=safe
func PgpSymDecryptArgs(msg []byte, psw, options string) (string, error) {
	b, err := pgpDecryptInternal(false, true, msg, []byte(psw), nil, &options)
	return string(b), err
}

// PgpSymDecryptBytea decrypts a message encrypted with a password.
//
//gopgext:function name=pgp_sym_decrypt_bytea immutable parallel=safe
func PgpSymDecryptBytea(msg []byte, psw string) ([]byte, error) {
	return pgpDecryptInternal(false, false, msg, []byte(psw), nil, nil)
}

// PgpSymDecryptByteaArgs decrypts a message encrypted with a password.
//
//gopgext:function name=pgp_sym_decrypt_bytea immutable parallel=safe
func PgpSymDecryptByteaArgs(msg []byte, psw, options string) ([]byte, error) {
	return pgpDecryptInternal(false, false, msg, []byte(psw), nil, &options)
}

// PgpPubEncrypt encrypts text with a public key.
//
//gopgext:function name=pgp_pub_encrypt parallel=safe
func PgpPubEncrypt(data string, key []byte) ([]byte, error) {
	return pgpEncryptInternal(true, true, []byte(data), key, nil)
}

// PgpPubEncryptArgs encrypts text with a public key and options.
//
//gopgext:function name=pgp_pub_encrypt parallel=safe
func PgpPubEncryptArgs(data string, key []byte, options string) ([]byte, error) {
	return pgpEncryptInternal(true, true, []byte(data), key, &options)
}

// PgpPubEncryptBytea encrypts binary data with a public key.
//
//gopgext:function name=pgp_pub_encrypt_bytea parallel=safe
func PgpPubEncryptBytea(data, key []byte) ([]byte, error) {
	return pgpEncryptInternal(true, false, data, key, nil)
}

// PgpPubEncryptByteaArgs encrypts binary data with a public key and
// options.
//
//gopgext:function name=pgp_pub_encrypt_bytea parallel=safe
func PgpPubEncryptByteaArgs(data, key []byte, options string) ([]byte, error) {
	return pgpEncryptInternal(true, false, data, key, &options)
}

// PgpPubDecrypt decrypts a message with a secret key, as text.
//
//gopgext:function name=pgp_pub_decrypt immutable parallel=safe
func PgpPubDecrypt(msg, key []byte) (string, error) {
	b, err := pgpDecryptInternal(true, true, msg, key, nil, nil)
	return string(b), err
}

// PgpPubDecryptPsw decrypts a message with a password-protected secret
// key, as text.
//
//gopgext:function name=pgp_pub_decrypt immutable parallel=safe
func PgpPubDecryptPsw(msg, key []byte, psw string) (string, error) {
	b, err := pgpDecryptInternal(true, true, msg, key, &psw, nil)
	return string(b), err
}

// PgpPubDecryptArgs decrypts a message with a secret key and options, as
// text.
//
//gopgext:function name=pgp_pub_decrypt immutable parallel=safe
func PgpPubDecryptArgs(msg, key []byte, psw, options string) (string, error) {
	b, err := pgpDecryptInternal(true, true, msg, key, &psw, &options)
	return string(b), err
}

// PgpPubDecryptBytea decrypts a message with a secret key.
//
//gopgext:function name=pgp_pub_decrypt_bytea immutable parallel=safe
func PgpPubDecryptBytea(msg, key []byte) ([]byte, error) {
	return pgpDecryptInternal(true, false, msg, key, nil, nil)
}

// PgpPubDecryptByteaPsw decrypts a message with a password-protected
// secret key.
//
//gopgext:function name=pgp_pub_decrypt_bytea immutable parallel=safe
func PgpPubDecryptByteaPsw(msg, key []byte, psw string) ([]byte, error) {
	return pgpDecryptInternal(true, false, msg, key, &psw, nil)
}

// PgpPubDecryptByteaArgs decrypts a message with a secret key and options.
//
//gopgext:function name=pgp_pub_decrypt_bytea immutable parallel=safe
func PgpPubDecryptByteaArgs(msg, key []byte, psw, options string) ([]byte, error) {
	return pgpDecryptInternal(true, false, msg, key, &psw, &options)
}

// PgpKeyID returns the ID of the key of a message or a key: hex digits,
// "SYMKEY" or "ANYKEY".
//
//gopgext:function name=pgp_key_id immutable parallel=safe
func PgpKeyID(data []byte) (string, error) {
	id, err := pgpGetKeyID(data)
	if err != nil {
		return "", pxThrow(err)
	}
	return id, nil
}

// Armor wraps binary data in PGP ASCII armor.
//
//gopgext:function name=armor immutable parallel=safe
func Armor(data []byte) string { return armorEncode(data, nil, nil) }

// ArmorHeaders wraps binary data in PGP ASCII armor, with headers. It's
// declared in sql/armor.sql: the arrays are pg.AnyArray to check their
// dimensions like upstream.
//
//gopgext:function manual
func ArmorHeaders(data []byte, keyArray, valueArray pg.AnyArray) (string, error) {
	nk, nv := len(keyArray.Dims()), len(valueArray.Dims())
	if nk > 1 || nk != nv {
		return "", pg.Errorf("2202E", "wrong number of array subscripts")
	}
	if nk == 0 {
		return armorEncode(data, nil, nil), nil
	}
	keys, values := armorStrings(keyArray), armorStrings(valueArray)
	if len(keys) != len(values) {
		return "", pg.Errorf("2202E", "mismatched array dimensions")
	}
	ks := make([]string, len(keys))
	vs := make([]string, len(values))
	for i := range keys {
		if keys[i] == nil {
			return "", pg.Errorf(pg.CodeNullValueNotAllowed, "null value not allowed for header key")
		}
		k := *keys[i]
		switch {
		case !pgpIsASCII(k):
			return "", pg.Errorf(pg.CodeInvalidParameterValue, "header key must not contain non-ASCII characters")
		case strings.Contains(k, ": "):
			return "", pg.Errorf(pg.CodeInvalidParameterValue, "header key must not contain \": \"")
		case strings.Contains(k, "\n"):
			return "", pg.Errorf(pg.CodeInvalidParameterValue, "header key must not contain newlines")
		}
		if values[i] == nil {
			return "", pg.Errorf(pg.CodeNullValueNotAllowed, "null value not allowed for header value")
		}
		v := *values[i]
		switch {
		case !pgpIsASCII(v):
			return "", pg.Errorf(pg.CodeInvalidParameterValue, "header value must not contain non-ASCII characters")
		case strings.Contains(v, "\n"):
			return "", pg.Errorf(pg.CodeInvalidParameterValue, "header value must not contain newlines")
		}
		ks[i], vs[i] = k, v
	}
	return armorEncode(data, ks, vs), nil
}

// armorStrings returns the elements of a text array (nil for NULL).
func armorStrings(a pg.AnyArray) []*string {
	elems := a.Elements()
	out := make([]*string, len(elems))
	for i, e := range elems {
		if e != nil {
			s := e.String()
			out[i] = &s
		}
	}
	return out
}

func pgpIsASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}

// Dearmor decodes PGP ASCII armor.
//
//gopgext:function name=dearmor immutable parallel=safe
func Dearmor(data string) ([]byte, error) {
	b, err := armorDecode([]byte(data))
	if err != nil {
		return nil, pxThrow(err)
	}
	return b, nil
}

// pgpArmorHeader is a header of an armored message.
type pgpArmorHeader struct {
	Key   string
	Value string
}

// PgpArmorHeaders returns the headers of an armored message.
//
//gopgext:function name=pgp_armor_headers immutable parallel=safe
func PgpArmorHeaders(data string) (iter.Seq[pgpArmorHeader], error) {
	keys, values, err := armorHeaders([]byte(data))
	if err != nil {
		return nil, pxThrow(err)
	}
	hdrs := make([]pgpArmorHeader, len(keys))
	for i := range keys {
		if hdrs[i].Key, err = pgpFromUTF8(keys[i]); err != nil {
			return nil, err
		}
		if hdrs[i].Value, err = pgpFromUTF8(values[i]); err != nil {
			return nil, err
		}
	}
	return func(yield func(pgpArmorHeader) bool) {
		for _, h := range hdrs {
			if !yield(h) {
				return
			}
		}
	}, nil
}
