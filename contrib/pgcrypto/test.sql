-- Hashes, HMACs, raw encryption, random data and settings, with
-- known-answer vectors (FIPS 180, RFC 2202/4231, FIPS 197, RFC 2144, ...).
-- crypt() and gen_salt() are in test_crypt.sql, OpenPGP in test_pgp*.sql;
-- upstream's own regression suite runs with pg_regress (see README.md).
CREATE EXTENSION IF NOT EXISTS pgcrypto;

CREATE FUNCTION pg_temp.err(q text) RETURNS text AS $$
BEGIN
	EXECUTE q;
	RETURN 'no error';
EXCEPTION WHEN OTHERS THEN
	RETURN SQLSTATE || ': ' || SQLERRM;
END $$ LANGUAGE plpgsql;

DO $$
DECLARE
	e text;
BEGIN
	-- digests
	ASSERT encode(digest('abc', 'md5'), 'hex') = '900150983cd24fb0d6963f7d28e17f72';
	ASSERT encode(digest('abc', 'sha1'), 'hex') = 'a9993e364706816aba3e25717850c26c9cd0d89d';
	ASSERT encode(digest('abc', 'sha224'), 'hex') = '23097d223405d8228642a477bda255b32aadbce4bda0b3f7e36c9da7';
	ASSERT encode(digest('abc', 'sha256'), 'hex') = 'ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad';
	ASSERT encode(digest('abc', 'sha384'), 'hex') = 'cb00753f45a35e8bb5a03d699ac65007272c32ab0eded1631a8b605a43ff5bed8086072ba1e7cc2358baeca134c825a7';
	ASSERT encode(digest('abc', 'SHA512'), 'hex') = 'ddaf35a193617abacc417349ae20413112e6fa4e89a97ea20a9eeee64b55d39a2192992a274fc1a836ba3c23a3feebbd454d4423643ce80e2a9ac94fa54ca49f';
	ASSERT encode(digest('abc', 'sha512-256'), 'hex') = '53048e2681941ef99b2e29b76b4c7dabe4c2d0c634fc6d46e0e2f13107e7af23';
	ASSERT encode(digest('abc', 'sha3-256'), 'hex') = '3a985da74fe225b2045c172d6bd390bd855f086e3e9d525b46bfe24511431532';
	ASSERT encode(digest('abc', 'ripemd160'), 'hex') = '8eb208f7e05d987a9b044a8e98c6b087f15a0bfc';
	ASSERT encode(digest('', 'sha1'), 'hex') = 'da39a3ee5e6b4b0d3255bfef95601890afd80709';
	ASSERT digest('abc'::bytea, 'sha-256') = sha256('abc');
	ASSERT encode(digest(repeat('a', 1000000), 'sha1'), 'hex') = '34aa973cd4c4daa4f61eeb2bdbad27316534016f';
	ASSERT pg_temp.err($q$SELECT digest('x', 'Foo')$q$) = '22023: Cannot use "foo": No such hash algorithm';

	-- HMACs (RFC 2202, RFC 4231 test case 6: a key longer than a block)
	ASSERT encode(hmac('what do ya want for nothing?', 'Jefe', 'md5'), 'hex') = '750c783e6ab0b503eaa86e310a5db738';
	ASSERT encode(hmac('what do ya want for nothing?', 'Jefe', 'sha1'), 'hex') = 'effcdf6ae5eb2fa2d27416d5f184df9c259a7c79';
	ASSERT encode(hmac('what do ya want for nothing?'::bytea, 'Jefe'::bytea, 'sha256'), 'hex') = '5bdcc146bf60754e6a042426089575c75a003f089d2739839dec58b964ec3843';
	ASSERT encode(hmac('Test Using Larger Than Block-Size Key - Hash Key First'::bytea, decode(repeat('aa', 131), 'hex'), 'sha256'), 'hex')
		= '60e431591ee0b67f0d8a26aacbf5b77f8e0bc6213728c5140546040f0ee37f54';
	ASSERT pg_temp.err($q$SELECT hmac('x', 'k', 'shake128')$q$) = 'XX000: EVP_MD_CTX_size() failed';

	-- AES (FIPS 197 appendix C), padding, IVs
	ASSERT encrypt('\x00112233445566778899aabbccddeeff', '\x000102030405060708090a0b0c0d0e0f', 'aes-ecb/pad:none') = '\x69c4e0d86a7b0430d8cdb78070b4c55a';
	ASSERT encrypt('\x00112233445566778899aabbccddeeff', '\x000102030405060708090a0b0c0d0e0f1011121314151617', 'aes-ecb/pad:none') = '\xdda97ca4864cdfe06eaf70a0ec0d7191';
	ASSERT encrypt('\x00112233445566778899aabbccddeeff', '\x000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f', 'rijndael-ecb/pad:none') = '\x8ea2b7ca516745bfeafc49904b496089';
	ASSERT decrypt('\x8ea2b7ca516745bfeafc49904b496089', '\x000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f', 'aes/pad:none') = '\x00112233445566778899aabbccddeeff';
	ASSERT encode(encrypt('hello', 'key', 'aes'), 'hex') = '5e27050b59c889fee9f953137259bf6b';
	ASSERT length(encrypt('0123456789abcdef', 'key', 'aes')) = 32;
	ASSERT decrypt(encrypt('Lorem ipsum dolor sit amet', 'secret', 'aes'), 'secret', 'aes') = 'Lorem ipsum dolor sit amet';
	ASSERT decrypt_iv(encrypt_iv('data', 'key', '\x0102030405', 'aes-cbc'), 'key', '\x0102030405', 'aes-cbc') = 'data';
	ASSERT encrypt_iv('data', 'key', '\x0102030405', 'aes') <> encrypt('data', 'key', 'aes');
	ASSERT length(encrypt('12345', 'key', 'aes-cfb')) = 5;
	ASSERT decrypt(encrypt('12345', 'key', 'aes-cfb'), 'key', 'aes-cfb') = '12345';
	ASSERT pg_temp.err($q$SELECT encrypt('12345', 'key', 'aes/pad:none')$q$) = '39000: encrypt error: Encryption failed';
	ASSERT pg_temp.err($q$SELECT decrypt('\x00112233445566778899aabbccddeeff', 'key', 'aes')$q$) = '39000: decrypt error: Decryption failed';
	ASSERT pg_temp.err($q$SELECT decrypt_iv('', 'key', '', 'aes')$q$) = '39000: decrypt_iv error: Decryption failed';
	ASSERT pg_temp.err($q$SELECT encrypt('x', 'k', 'aes-ofb')$q$) = '22023: Cannot use "aes-ofb": No such cipher algorithm';
	ASSERT pg_temp.err($q$SELECT encrypt('x', 'k', 'aes/pad:zero')$q$) = '22023: Cannot use "aes/pad:zero": No such cipher algorithm';
	ASSERT pg_temp.err($q$SELECT encrypt('x', 'k', 'aes/mode:cbc')$q$) = '22023: Cannot use "aes/mode:cbc": Unknown option';
	ASSERT pg_temp.err($q$SELECT encrypt('x', 'k', 'aes/pkcs')$q$) = '22023: Cannot use "aes/pkcs": Badly formatted type';

	-- Blowfish (Eric Young's vectors), DES, 3DES, CAST5 (RFC 2144: 128, 80, 40 bit keys)
	ASSERT encrypt('\x0000000000000000', '\x0000000000000000', 'bf-ecb/pad:none') = '\x4ef997456198dd78';
	ASSERT encrypt('\xffffffffffffffff', '\xffffffffffffffff', 'bf-ecb/pad:none') = '\x51866fd5b85ecb8a';
	ASSERT encrypt('\x1000000000000001', '\x3000000000000000', 'blowfish-ecb/pad:none') = '\x7d856f9a613063f2';
	ASSERT decrypt(encrypt('Blowfish', 'a much longer key of more than sixteen bytes', 'bf'), 'a much longer key of more than sixteen bytes', 'bf') = 'Blowfish';
	ASSERT decrypt(encrypt('Blowfish CFB', 'key', 'bf-cfb'), 'key', 'bf-cfb') = 'Blowfish CFB';
	ASSERT encrypt('\x0123456789abcdef', '\xfedcba9876543210', 'des-ecb/pad:none') = '\xed39d950fa74bcc4';
	ASSERT encrypt('\x8000000000000000', '\x010101010101010101010101010101010101010101010101', '3des-ecb/pad:none') = '\x95f8a5e5dd31d900';
	ASSERT encrypt('', 'foo', '3des') = '\x752111e37a2d7ac3';
	ASSERT encrypt('\x0123456789ABCDEF', '\x0123456712345678234567893456789A', 'cast5-ecb/pad:none') = '\x238b4fe5847e44b2';
	ASSERT encrypt('\x0123456789ABCDEF', '\x01234567123456782345', 'cast5-ecb/pad:none') = '\xeb6a711a2c02271b';
	ASSERT encrypt('\x0123456789ABCDEF', '\x0123456712', 'cast5-ecb/pad:none') = '\x7ac816d16e9b302e';
	ASSERT decrypt('\x7ac816d16e9b302e', '\x0123456712', 'cast5-ecb/pad:none') = '\x0123456789ABCDEF';

	-- random data
	ASSERT length(gen_random_bytes(1024)) = 1024;
	ASSERT (SELECT count(DISTINCT gen_random_bytes(16)) FROM generate_series(1, 1000)) = 1000;
	ASSERT pg_temp.err('SELECT gen_random_bytes(0)') = '39000: Length not in range';
	ASSERT pg_temp.err('SELECT gen_random_bytes(1025)') = '39000: Length not in range';
	ASSERT (SELECT count(DISTINCT gen_random_uuid()) FROM generate_series(1, 1000)) = 1000;
	ASSERT gen_random_uuid()::text ~ '^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$';

	-- settings
	ASSERT NOT fips_mode();
	ASSERT current_setting('pgcrypto.builtin_crypto_enabled') = 'on';
	PERFORM set_config('pgcrypto.builtin_crypto_enabled', 'FIPS', false);
	ASSERT current_setting('pgcrypto.builtin_crypto_enabled') = 'fips';
	e := pg_temp.err($q$SET pgcrypto.builtin_crypto_enabled = 'maybe'$q$);
	ASSERT e LIKE '22023: invalid value for parameter "pgcrypto.builtin_crypto_enabled": "maybe"', e;
	PERFORM set_config('pgcrypto.builtin_crypto_enabled', 'on', false);
END $$;

-- A parallel query (the functions are PARALLEL SAFE).
SET max_parallel_workers_per_gather = 2;
SET parallel_setup_cost = 0;
SET parallel_tuple_cost = 0;
SET min_parallel_table_scan_size = 0;
CREATE TABLE secrets AS SELECT i, encrypt(convert_to(i::text, 'UTF8'), 'k', 'aes') AS e FROM generate_series(1, 20000) i;
ANALYZE secrets;
DO $$
BEGIN
	ASSERT (SELECT count(*) FROM secrets WHERE convert_from(decrypt(e, 'k', 'aes'), 'UTF8')::int = i
		AND digest(e, 'sha256') = sha256(e)) = 20000;
END $$;
