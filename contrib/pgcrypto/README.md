# pgcrypto, in Go

A port of PostgreSQL's
[`contrib/pgcrypto`](https://www.postgresql.org/docs/current/pgcrypto.html)
(version 1.4) to Go, as a drop-in replacement: the same SQL functions,
results and errors, in pure Go. There's no C in the project and no OpenSSL.
Primitives come from Go's standard library (MD5, SHA-1, SHA-2, SHA-3, HMAC,
AES, DES, 3DES, `math/big`, `compress/flate`); the rest is ported.
`crypto/rand` is backed by PostgreSQL's `pg_strong_random`, as upstream's
random bytes are.

| Go source            | Ported from (PostgreSQL 19)                                       |
|----------------------|-------------------------------------------------------------------|
| `main.go`            | `pgcrypto.c` (`fips_mode`, `pgcrypto.builtin_crypto_enabled`), `px.c` (errors) |
| `digest.go`          | `digest`, `hmac`: `px-hmac.c`, OpenSSL's digest names              |
| `cipher.go`          | `encrypt`, `decrypt` and their `_iv` variants, `gen_random_bytes`: `px.c` combos and `openssl.c` ciphers, with EVP's padding rules |
| `blowfish.go`        | Blowfish, with the tables of `crypt-blowfish.c`                    |
| `cast5.go`, `ripemd160.go` | CAST5 and RIPEMD-160 from `golang.org/x/crypto` (BSD); CAST5 extended to short keys (12 rounds up to 80 bits, like RFC 2144 and OpenSSL) |
| `crypt.go`           | `crypt`, `gen_salt`: `px-crypt.c`, `crypt-gensalt.c`               |
| `crypt_des.go`, `crypt_md5.go`, `crypt_blowfish.go`, `crypt_sha.go` | `crypt-des.c` (DES, extended DES), `crypt-md5.c`, `crypt-blowfish.c` (`$2a$`, `$2x$`), `crypt-sha.c` (`$5$`, `$6$`) |
| `pgp*.go`            | `pgp.c`, `pgp-armor.c`, `pgp-cfb.c`, `pgp-compress.c`, `pgp-decrypt.c`, `pgp-encrypt.c`, `pgp-info.c`, `pgp-mpi*.c`, `pgp-pgsql.c`, `pgp-pubdec.c`, `pgp-pubenc.c`, `pgp-pubkey.c`, `pgp-s2k.c`, `mbuf.c` |
| `sql/`               | `gen_random_uuid()` (PostgreSQL's own, `LANGUAGE internal`, as upstream redirects to it), and `armor(bytea, text[], text[])` |

```sh
gopgext install -version 1.4 -trusted -comment 'cryptographic functions' contrib/pgcrypto
```

The other functions are declared by gopgext from their Go signatures, with
upstream's volatility, strictness and `PARALLEL SAFE`; their parameters
have names (`digest(data bytea, typ text)`), upstream's don't.

## Differences from upstream

- **Algorithms.** Everything upstream offers with OpenSSL's default and
  legacy providers: AES, Blowfish, DES, 3DES and CAST5 (ECB, CBC, and CFB
  for AES and Blowfish) in `encrypt()` and OpenPGP; every `crypt()`
  algorithm. Distributions whose OpenSSL lacks the legacy provider fail
  upstream's Blowfish, CAST5 and DES (`Cipher cannot be initialized`; the
  regression tests have alternative outputs for that); the port always has
  them. Digest names: OpenSSL's names and aliases for MD5, SHA-1, SHA-224
  to SHA-512, SHA-512/224, SHA-512/256, SHA3-224 to SHA3-512, RIPEMD-160
  and MD5-SHA1 (`sha256`, `SHA-256`, `sha2-256`, `sha256WithRSAEncryption`,
  OIDs, ...). Not available: `sm3`, `blake2b512`, `blake2s256`, and those of
  OpenSSL's legacy provider (`md4`, `whirlpool`, `mdc2`): they're "No such
  hash algorithm" errors.
- **FIPS.** `fips_mode()` is always false (Go's FIPS 140 mode isn't used),
  so `pgcrypto.builtin_crypto_enabled = fips` behaves like `on`; `off`
  disables `crypt()` and `gen_salt()` like upstream.
- **Compressed OpenPGP messages** (`compress-algo=1|2`): Go's deflate
  compresses differently from zlib, so the bytes differ (both decompress
  each other's messages).
- **Encodings.** Text functions (`pgp_sym_decrypt`, `armor` headers,
  `unicode-mode`) are handled in Go for UTF8 and SQL_ASCII databases; other
  server encodings are converted with `convert_from`/`convert_to` queries
  (untested).
- **Messages.** A few notices lack upstream's SQLSTATE (`crypt()`'s
  "rounds" notice for SHA crypt: `gopgext/pg` notices have no code).
  `ignore-cipher-failure` is accepted but changes nothing (no cipher is ever
  missing).

## Verification

**Upstream regression tests**: PostgreSQL's `pg_regress` with the `sql/`
and `expected/` files of `contrib/pgcrypto` unmodified passes against this
port, all 25 tests, on PostgreSQL 18 and 14:

```sh
$(pg_config --pkglibdir)/pgxs/src/test/regress/pg_regress --temp-instance=/tmp/pgcrypto \
    --bindir=$(pg_config --bindir) --inputdir=<postgresql source>/contrib/pgcrypto \
    init md5 sha1 hmac-md5 hmac-sha1 blowfish rijndael sha2 des 3des cast5 \
    crypt-des crypt-md5 crypt-blowfish crypt-xdes pgp-armor pgp-decrypt pgp-encrypt \
    pgp-encrypt-md5 pgp-compression pgp-pubkey-decrypt pgp-pubkey-encrypt \
    pgp-pubkey-session pgp-info crypt-shacrypt
# ok 1 - init ... ok 25 - crypt-shacrypt
# All 25 tests passed.
```

**Tests**: `test.sql`, `test_crypt.sql` and `test_pgp.sql` check
known-answer vectors (FIPS 180/197, RFC 2202/4231/2144, Drepper's SHA crypt
and Openwall's bcrypt vectors, GnuPG messages), errors with their SQLSTATEs,
round trips of every cipher, option and key type, and a parallel query.

**Differential test**: `differential.sql` runs this port and the original
C library (installed as `pgcrypto_c.so`, see the script) on generated
input, and decrypts each one's output with the other:

| Section (PostgreSQL 18, OpenSSL 3.5 with the legacy provider) | Checks | Mismatches |
|----------------------------------------------------------------|-------:|-----------:|
| `digest`, `hmac` (bytea and text): 36 names incl. aliases and bad ones, 400 inputs | 10,476 | 0 |
| `encrypt`, `decrypt`, `_iv` variants: 306 cipher specifications (modes, padding, aliases, malformed), keys of 0 to 69 bytes, IVs of 0 to 19 | 51,498 × 4 | 0 |
| round trips Go → C and C → Go of those | 17,368 × 3 | 0 |
| `crypt`: DES, extended DES, MD5, bcrypt, SHA crypt, with malformed salts and round counts | 9,030 | 0 |
| `gen_salt` errors and formats, salts of one used by the other | 250 | 0 |
| OpenPGP: symmetric and public-key (RSA, Elgamal, with and without password) messages encrypted by one, decrypted by the other, over every option | 14,663 | 0 |
| OpenPGP: same inputs to both, every byte of 30 messages flipped and every truncation, corrupted keys (results, errors, key ids), 41 option strings | 99,877 | 0 |
| `armor` and its errors, `dearmor` (fuzzed), `pgp_armor_headers` | 802 | 0 |

Results include error SQLSTATEs and messages. On PostgreSQL 14 the
`crypt`, `gen_salt` and OpenPGP sections also match (except 32 errors where
14's library says "Some PX error (not specified)", a code later upstream
versions removed, and the port says "Bad error code" like current
upstream), and digests and HMACs match. Its `encrypt`/`decrypt` predate
upstream's rework of the OpenSSL code: without padding they encrypt
partial blocks, they have no AES CFB, and other errors are worded
differently, so those differ where 14 differs from current upstream (with
the default padding, encryption results are the same).
