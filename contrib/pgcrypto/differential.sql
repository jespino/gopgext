-- Differential test: compares this Go port with the original C pgcrypto
-- on generated inputs. Needs the original library installed as
-- $libdir/pgcrypto_c.so, with its setting renamed (both libraries are
-- loaded in one backend, and a setting can only be defined once):
--
--   cp $(pg_config --pkglibdir)/pgcrypto.so $(pg_config --pkglibdir)/pgcrypto_c.so
--   sed -i 's/pgcrypto\.builtin_crypto_enabled/pgcryptc.builtin_crypto_enabled/' \
--       $(pg_config --pkglibdir)/pgcrypto_c.so
--
-- and this port installed as pgcrypto. Run the server with OpenSSL's legacy
-- provider enabled (OPENSSL_CONF) so the C library has Blowfish, CAST5 and
-- DES. Each section runs in its own session and prints the number of
-- mismatches per check (all must be 0) and a few examples of each:
--
--   1. digest, hmac, encrypt/decrypt(_iv): results and errors on the same
--      inputs (400 random byte strings, 36 digest names, 306 cipher
--      specifications with keys and IVs of every length), and round trips
--      from one implementation to the other
--   2. crypt and gen_salt (passwords x salts of every algorithm, malformed
--      salts, rounds at and past the limits)
--   3. OpenPGP: messages encrypted by one, decrypted by the other (password
--      and RSA/Elgamal keys, every option), corrupted messages and keys,
--      armor, dearmor, pgp_armor_headers, pgp_key_id
--
--   psql -v ON_ERROR_STOP=1 [-v ncrypt=6000] [-v npgp=100] -f differential.sql
--
-- The C library of PostgreSQL 17 and older lacks sha256crypt/sha512crypt,
-- fips_mode() and some messages of current upstream, see README.md.

\if :{?ncrypt}
\else
\set ncrypt 6000
\endif
\if :{?npgp}
\else
\set npgp 100
\endif

SET client_min_messages = warning;
DROP EXTENSION IF EXISTS pgcrypto CASCADE;
CREATE EXTENSION pgcrypto;
-- The C pgcrypto ($libdir/pgcrypto_c) in schema c.
DROP SCHEMA IF EXISTS c CASCADE;
CREATE SCHEMA c;
SET search_path = c, public;


CREATE FUNCTION digest(text, text)
RETURNS bytea
AS '$libdir/pgcrypto_c', 'pg_digest'
LANGUAGE C IMMUTABLE STRICT PARALLEL SAFE;

CREATE FUNCTION digest(bytea, text)
RETURNS bytea
AS '$libdir/pgcrypto_c', 'pg_digest'
LANGUAGE C IMMUTABLE STRICT PARALLEL SAFE;

CREATE FUNCTION hmac(text, text, text)
RETURNS bytea
AS '$libdir/pgcrypto_c', 'pg_hmac'
LANGUAGE C IMMUTABLE STRICT PARALLEL SAFE;

CREATE FUNCTION hmac(bytea, bytea, text)
RETURNS bytea
AS '$libdir/pgcrypto_c', 'pg_hmac'
LANGUAGE C IMMUTABLE STRICT PARALLEL SAFE;

CREATE FUNCTION crypt(text, text)
RETURNS text
AS '$libdir/pgcrypto_c', 'pg_crypt'
LANGUAGE C IMMUTABLE STRICT PARALLEL SAFE;

CREATE FUNCTION gen_salt(text)
RETURNS text
AS '$libdir/pgcrypto_c', 'pg_gen_salt'
LANGUAGE C VOLATILE STRICT PARALLEL SAFE;

CREATE FUNCTION gen_salt(text, int4)
RETURNS text
AS '$libdir/pgcrypto_c', 'pg_gen_salt_rounds'
LANGUAGE C VOLATILE STRICT PARALLEL SAFE;

CREATE FUNCTION encrypt(bytea, bytea, text)
RETURNS bytea
AS '$libdir/pgcrypto_c', 'pg_encrypt'
LANGUAGE C IMMUTABLE STRICT PARALLEL SAFE;

CREATE FUNCTION decrypt(bytea, bytea, text)
RETURNS bytea
AS '$libdir/pgcrypto_c', 'pg_decrypt'
LANGUAGE C IMMUTABLE STRICT PARALLEL SAFE;

CREATE FUNCTION encrypt_iv(bytea, bytea, bytea, text)
RETURNS bytea
AS '$libdir/pgcrypto_c', 'pg_encrypt_iv'
LANGUAGE C IMMUTABLE STRICT PARALLEL SAFE;

CREATE FUNCTION decrypt_iv(bytea, bytea, bytea, text)
RETURNS bytea
AS '$libdir/pgcrypto_c', 'pg_decrypt_iv'
LANGUAGE C IMMUTABLE STRICT PARALLEL SAFE;

CREATE FUNCTION gen_random_bytes(int4)
RETURNS bytea
AS '$libdir/pgcrypto_c', 'pg_random_bytes'
LANGUAGE C VOLATILE STRICT PARALLEL SAFE;

CREATE FUNCTION gen_random_uuid()
RETURNS uuid
AS '$libdir/pgcrypto_c', 'pg_random_uuid'
LANGUAGE C VOLATILE PARALLEL SAFE;

--
-- pgp_sym_encrypt(data, key)
--
CREATE FUNCTION pgp_sym_encrypt(text, text)
RETURNS bytea
AS '$libdir/pgcrypto_c', 'pgp_sym_encrypt_text'
LANGUAGE C STRICT PARALLEL SAFE;

CREATE FUNCTION pgp_sym_encrypt_bytea(bytea, text)
RETURNS bytea
AS '$libdir/pgcrypto_c', 'pgp_sym_encrypt_bytea'
LANGUAGE C STRICT PARALLEL SAFE;

--
-- pgp_sym_encrypt(data, key, args)
--
CREATE FUNCTION pgp_sym_encrypt(text, text, text)
RETURNS bytea
AS '$libdir/pgcrypto_c', 'pgp_sym_encrypt_text'
LANGUAGE C STRICT PARALLEL SAFE;

CREATE FUNCTION pgp_sym_encrypt_bytea(bytea, text, text)
RETURNS bytea
AS '$libdir/pgcrypto_c', 'pgp_sym_encrypt_bytea'
LANGUAGE C STRICT PARALLEL SAFE;

--
-- pgp_sym_decrypt(data, key)
--
CREATE FUNCTION pgp_sym_decrypt(bytea, text)
RETURNS text
AS '$libdir/pgcrypto_c', 'pgp_sym_decrypt_text'
LANGUAGE C IMMUTABLE STRICT PARALLEL SAFE;

CREATE FUNCTION pgp_sym_decrypt_bytea(bytea, text)
RETURNS bytea
AS '$libdir/pgcrypto_c', 'pgp_sym_decrypt_bytea'
LANGUAGE C IMMUTABLE STRICT PARALLEL SAFE;

--
-- pgp_sym_decrypt(data, key, args)
--
CREATE FUNCTION pgp_sym_decrypt(bytea, text, text)
RETURNS text
AS '$libdir/pgcrypto_c', 'pgp_sym_decrypt_text'
LANGUAGE C IMMUTABLE STRICT PARALLEL SAFE;

CREATE FUNCTION pgp_sym_decrypt_bytea(bytea, text, text)
RETURNS bytea
AS '$libdir/pgcrypto_c', 'pgp_sym_decrypt_bytea'
LANGUAGE C IMMUTABLE STRICT PARALLEL SAFE;

--
-- pgp_pub_encrypt(data, key)
--
CREATE FUNCTION pgp_pub_encrypt(text, bytea)
RETURNS bytea
AS '$libdir/pgcrypto_c', 'pgp_pub_encrypt_text'
LANGUAGE C STRICT PARALLEL SAFE;

CREATE FUNCTION pgp_pub_encrypt_bytea(bytea, bytea)
RETURNS bytea
AS '$libdir/pgcrypto_c', 'pgp_pub_encrypt_bytea'
LANGUAGE C STRICT PARALLEL SAFE;

--
-- pgp_pub_encrypt(data, key, args)
--
CREATE FUNCTION pgp_pub_encrypt(text, bytea, text)
RETURNS bytea
AS '$libdir/pgcrypto_c', 'pgp_pub_encrypt_text'
LANGUAGE C STRICT PARALLEL SAFE;

CREATE FUNCTION pgp_pub_encrypt_bytea(bytea, bytea, text)
RETURNS bytea
AS '$libdir/pgcrypto_c', 'pgp_pub_encrypt_bytea'
LANGUAGE C STRICT PARALLEL SAFE;

--
-- pgp_pub_decrypt(data, key)
--
CREATE FUNCTION pgp_pub_decrypt(bytea, bytea)
RETURNS text
AS '$libdir/pgcrypto_c', 'pgp_pub_decrypt_text'
LANGUAGE C IMMUTABLE STRICT PARALLEL SAFE;

CREATE FUNCTION pgp_pub_decrypt_bytea(bytea, bytea)
RETURNS bytea
AS '$libdir/pgcrypto_c', 'pgp_pub_decrypt_bytea'
LANGUAGE C IMMUTABLE STRICT PARALLEL SAFE;

--
-- pgp_pub_decrypt(data, key, psw)
--
CREATE FUNCTION pgp_pub_decrypt(bytea, bytea, text)
RETURNS text
AS '$libdir/pgcrypto_c', 'pgp_pub_decrypt_text'
LANGUAGE C IMMUTABLE STRICT PARALLEL SAFE;

CREATE FUNCTION pgp_pub_decrypt_bytea(bytea, bytea, text)
RETURNS bytea
AS '$libdir/pgcrypto_c', 'pgp_pub_decrypt_bytea'
LANGUAGE C IMMUTABLE STRICT PARALLEL SAFE;

--
-- pgp_pub_decrypt(data, key, psw, arg)
--
CREATE FUNCTION pgp_pub_decrypt(bytea, bytea, text, text)
RETURNS text
AS '$libdir/pgcrypto_c', 'pgp_pub_decrypt_text'
LANGUAGE C IMMUTABLE STRICT PARALLEL SAFE;

CREATE FUNCTION pgp_pub_decrypt_bytea(bytea, bytea, text, text)
RETURNS bytea
AS '$libdir/pgcrypto_c', 'pgp_pub_decrypt_bytea'
LANGUAGE C IMMUTABLE STRICT PARALLEL SAFE;

--
-- PGP key ID
--
CREATE FUNCTION pgp_key_id(bytea)
RETURNS text
AS '$libdir/pgcrypto_c', 'pgp_key_id_w'
LANGUAGE C IMMUTABLE STRICT PARALLEL SAFE;

--
-- pgp armor
--
CREATE FUNCTION armor(bytea)
RETURNS text
AS '$libdir/pgcrypto_c', 'pg_armor'
LANGUAGE C IMMUTABLE STRICT PARALLEL SAFE;

CREATE FUNCTION armor(bytea, text[], text[])
RETURNS text
AS '$libdir/pgcrypto_c', 'pg_armor'
LANGUAGE C IMMUTABLE STRICT PARALLEL SAFE;

CREATE FUNCTION dearmor(text)
RETURNS bytea
AS '$libdir/pgcrypto_c', 'pg_dearmor'
LANGUAGE C IMMUTABLE STRICT PARALLEL SAFE;

CREATE FUNCTION pgp_armor_headers(text, key OUT text, value OUT text)
RETURNS SETOF record
AS '$libdir/pgcrypto_c', 'pgp_armor_headers'
LANGUAGE C IMMUTABLE STRICT PARALLEL SAFE;
RESET search_path;


-- 1. digest, hmac, encrypt, decrypt
\connect
-- digest, hmac, encrypt/decrypt: port (public) vs C (schema c).
SET client_min_messages = warning;
CREATE OR REPLACE FUNCTION pg_temp.try(q text) RETURNS text AS $$
DECLARE r text;
BEGIN
	EXECUTE q INTO r;
	RETURN coalesce(r, 'NULL');
EXCEPTION WHEN OTHERS THEN
	RETURN 'ERROR ' || SQLSTATE || ': ' || SQLERRM;
END $$ LANGUAGE plpgsql;
SELECT setseed(0.42);
CREATE TEMP TABLE inputs AS
	SELECT i, decode(string_agg(lpad(to_hex((random() * 255)::int), 2, '0'), ''), 'hex') AS b
	FROM generate_series(1, 400) i, LATERAL generate_series(1, CASE WHEN i <= 80 THEN i - 1 WHEN i <= 390 THEN (random() * 300)::int ELSE 5000 END + 0 * i) j
	GROUP BY i
	UNION ALL SELECT 0, ''::bytea;
CREATE TEMP TABLE names(n text);
INSERT INTO names VALUES ('md5'),('sha1'),('sha224'),('sha256'),('sha384'),('sha512'),('sha512-224'),('sha512-256'),
	('sha3-224'),('sha3-256'),('sha3-384'),('sha3-512'),('ripemd160'),('rmd160'),('ripemd'),('ripemd-160'),('md5-sha1'),
	('SHA256'),('Sha-1'),('sha2-256'),('sha-512/224'),('sha2-512/256'),('rsa-sha256'),('sha256withrsaencryption'),('ssl3-md5'),
	('2.16.840.1.101.3.4.2.1'),('shake128'),('shake256'),('foo'),(''),(' sha1'),('sha1 '),('sha512/224'),('sha'),
	(repeat('x', 70)),(repeat('é', 40));
CREATE TEMP TABLE d AS
SELECT 'digest' AS f, n, i,
	pg_temp.try(format('SELECT encode(digest(%L::bytea, %L), ''hex'')', b, n)) AS g,
	pg_temp.try(format('SELECT encode(c.digest(%L::bytea, %L), ''hex'')', b, n)) AS cc
FROM names, inputs WHERE i % 7 = 0 OR i < 70
UNION ALL
SELECT 'hmac', n, i,
	pg_temp.try(format('SELECT encode(hmac(%L::bytea, %L::bytea, %L), ''hex'')', b, substr(b, 1, i % 200), n)),
	pg_temp.try(format('SELECT encode(c.hmac(%L::bytea, %L::bytea, %L), ''hex'')', b, substr(b, 1, i % 200), n))
FROM names, inputs WHERE i % 5 = 0 OR i < 70
UNION ALL
SELECT 'digest(text)', n, i,
	pg_temp.try(format('SELECT encode(digest(%L::text, %L), ''hex'')', 'héllo' || i, n)),
	pg_temp.try(format('SELECT encode(c.digest(%L::text, %L), ''hex'')', 'héllo' || i, n))
FROM names, inputs WHERE i < 20
UNION ALL
SELECT 'hmac(text)', n, i,
	pg_temp.try(format('SELECT encode(hmac(%L::text, %L::text, %L), ''hex'')', 'héllo' || i, repeat('k', i), n)),
	pg_temp.try(format('SELECT encode(c.hmac(%L::text, %L::text, %L), ''hex'')', 'héllo' || i, repeat('k', i), n))
FROM names, inputs WHERE i < 20;

CREATE TEMP TABLE ciphers(n text);
INSERT INTO ciphers SELECT a || p FROM unnest(ARRAY['aes','aes-ecb','aes-cbc','aes-cfb','rijndael','rijndael-ecb','rijndael-cbc','rijndael-cfb',
	'bf','bf-ecb','bf-cbc','bf-cfb','blowfish','blowfish-ecb','blowfish-cbc','blowfish-cfb',
	'des','des-ecb','des-cbc','3des','3des-ecb','3des-cbc','des3-ecb','des3-cbc','cast5','cast5-ecb','cast5-cbc',
	'AES','Bf-Cbc','aes-ofb','des3','cast5-cfb','foo','']) a,
	unnest(ARRAY['','/pad:pkcs','/pad:none','/pad:foo','/foo:bar','/pad','//pad:none/','/pad:none/pad:pkcs','/pad:pkcs:x']) p;
CREATE TEMP TABLE e AS
SELECT n, i, k, v,
	pg_temp.try(format('SELECT encode(encrypt(%L::bytea, %L::bytea, %L), ''hex'')', b, key, n)) AS g,
	pg_temp.try(format('SELECT encode(c.encrypt(%L::bytea, %L::bytea, %L), ''hex'')', b, key, n)) AS cc,
	pg_temp.try(format('SELECT encode(encrypt_iv(%L::bytea, %L::bytea, %L::bytea, %L), ''hex'')', b, key, iv, n)) AS giv,
	pg_temp.try(format('SELECT encode(c.encrypt_iv(%L::bytea, %L::bytea, %L::bytea, %L), ''hex'')', b, key, iv, n)) AS civ,
	pg_temp.try(format('SELECT encode(decrypt(%L::bytea, %L::bytea, %L), ''hex'')', b, key, n)) AS gd,
	pg_temp.try(format('SELECT encode(c.decrypt(%L::bytea, %L::bytea, %L), ''hex'')', b, key, n)) AS cd,
	pg_temp.try(format('SELECT encode(decrypt_iv(%L::bytea, %L::bytea, %L::bytea, %L), ''hex'')', b, key, iv, n)) AS gdiv,
	pg_temp.try(format('SELECT encode(c.decrypt_iv(%L::bytea, %L::bytea, %L::bytea, %L), ''hex'')', b, key, iv, n)) AS cdiv,
	b, key, iv
FROM ciphers, inputs,
	LATERAL (SELECT (i * 7 + length(n)) % 70 AS k, (i * 3) % 20 AS v) kv,
	LATERAL (SELECT substr(decode(md5(i::text) || md5(n) || md5(k::text) || md5(v::text) || md5(n || i), 'hex'), 1, k) AS key,
		substr(decode(md5(n || k) || md5(i::text), 'hex'), 1, v) AS iv) kiv
WHERE i % 3 = 0 OR i < 50 OR n IN ('aes', 'bf', 'aes-cfb');
-- round trips through the other implementation
CREATE TEMP TABLE rt AS
SELECT n, i,
	pg_temp.try(format('SELECT encode(c.decrypt(encrypt(%L::bytea, %L::bytea, %L), %L::bytea, %L), ''hex'')', b, key, n, key, n)) AS g2c,
	pg_temp.try(format('SELECT encode(decrypt(c.encrypt(%L::bytea, %L::bytea, %L), %L::bytea, %L), ''hex'')', b, key, n, key, n)) AS c2g,
	pg_temp.try(format('SELECT encode(decrypt_iv(c.encrypt_iv(%L::bytea, %L::bytea, %L::bytea, %L), %L::bytea, %L::bytea, %L), ''hex'')', b, key, iv, n, key, iv, n)) AS c2giv,
	encode(b, 'hex') AS plain
FROM e WHERE g NOT LIKE 'ERROR%';
SELECT f, count(*) AS total, count(*) FILTER (WHERE g <> cc) AS mismatches, count(*) FILTER (WHERE g LIKE 'ERROR%') AS errors FROM d GROUP BY f ORDER BY f;
SELECT f, n, i, left(g, 70) g, left(cc, 70) c FROM d WHERE g <> cc ORDER BY f, n, i LIMIT 20;
SELECT count(*) AS total,
	count(*) FILTER (WHERE g <> cc) AS enc_mismatch, count(*) FILTER (WHERE giv <> civ) AS enc_iv_mismatch,
	count(*) FILTER (WHERE gd <> cd) AS dec_mismatch, count(*) FILTER (WHERE gdiv <> cdiv) AS dec_iv_mismatch,
	count(*) FILTER (WHERE g LIKE 'ERROR%') AS enc_errors, count(*) FILTER (WHERE gd LIKE 'ERROR%') AS dec_errors
FROM e;
SELECT n, i, k, v, left(g, 60) g, left(cc, 60) c, left(gd, 60) gd, left(cd, 60) cd FROM e
	WHERE g <> cc OR giv <> civ OR gd <> cd OR gdiv <> cdiv ORDER BY n, i LIMIT 20;
SELECT count(*) AS round_trips, count(*) FILTER (WHERE g2c <> plain) AS go_to_c_bad, count(*) FILTER (WHERE c2g <> plain) AS c_to_go_bad,
	count(*) FILTER (WHERE c2giv <> plain) AS c_to_go_iv_bad FROM rt;
SELECT n, count(*) FILTER (WHERE g <> cc) enc_mismatch, count(*) FILTER (WHERE gd <> cd) dec_mismatch,
	min(k) FILTER (WHERE g <> cc) mink, max(k) FILTER (WHERE g <> cc) maxk FROM e GROUP BY n HAVING count(*) FILTER (WHERE g <> cc OR gd <> cd) > 0 ORDER BY n;
SELECT DISTINCT ON (n) n, i, k, left(g, 50), left(cc, 50) FROM e WHERE g <> cc ORDER BY n, i;


-- 2. crypt, gen_salt
\connect
SELECT current_setting('server_version_num')::int < 180000 AS old_c \gset
-- crypt() and gen_salt(): the port (public) vs the C original (schema c).
SET client_min_messages = warning;
SELECT setseed(0.42);

CREATE FUNCTION pg_temp.try(q text, VARIADIC args text[]) RETURNS text AS $$
DECLARE r text;
BEGIN
	EXECUTE q INTO r USING args[1], args[2];
	RETURN coalesce(r, 'NULL');
EXCEPTION WHEN OTHERS THEN
	RETURN 'ERROR ' || SQLSTATE || ': ' || SQLERRM;
END $$ LANGUAGE plpgsql;

-- random string of n characters from alphabet a
CREATE FUNCTION pg_temp.rs(a text, n int) RETURNS text AS $$
	SELECT coalesce(string_agg(substr(a, 1 + floor(random() * length(a))::int, 1), ''), '')
	FROM generate_series(1, n)
$$ LANGUAGE sql VOLATILE;

CREATE TEMP TABLE pw (p text);
INSERT INTO pw VALUES (''), ('a'), ('password'), ('foox'), ('ñandú €uro'), (repeat('x', 72)), (repeat('y', 71) || 'z'),
	(repeat('abcdefgh', 13)), ('Ünïcödé pässwörd ' || repeat('é', 40));
INSERT INTO pw SELECT pg_temp.rs('abcXYZ019 !$_./éü€', (random() * 90)::int) FROM generate_series(1, 20);

CREATE TEMP TABLE salts (kind text, s text);
\set alpha './0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz'
\set bad '!$é€~ :'
-- des
INSERT INTO salts SELECT 'des', pg_temp.rs(:'alpha' || :'bad', (random() * 4)::int) FROM generate_series(1, :ncrypt / 10);
-- xdes, small iteration counts
INSERT INTO salts SELECT 'xdes', '_' || pg_temp.rs(:'alpha' || '!', 1) || pg_temp.rs('./0', 1) || '..'
	|| pg_temp.rs(:'alpha' || :'bad', (random() * 6)::int) FROM generate_series(1, :ncrypt / 10);
INSERT INTO salts SELECT 'xdes', '_' || pg_temp.rs(:'alpha', (random() * 9)::int) FROM generate_series(1, 20);
DELETE FROM salts WHERE kind = 'xdes' AND length(s) >= 4 AND substr(s, 4, 2) <> '..'; -- at most 4095 iterations
INSERT INTO salts VALUES ('xdes', '_........'), ('xdes', '_..!!!!!!'), ('xdes', '_J9..BWH'), ('xdes', '_J9..j2zz');
-- md5
INSERT INTO salts SELECT 'md5', '$1$' || pg_temp.rs(:'alpha' || :'bad', (random() * 12)::int) FROM generate_series(1, :ncrypt / 10);
-- bcrypt and friends, costs 4-5 to keep it fast, plus invalid costs
INSERT INTO salts SELECT 'bf', (ARRAY['$2a$', '$2x$', '$2b$', '$2y$', '$2$', '$2a', '$2'])[1 + floor(random() * 7)::int]
	|| (ARRAY['04', '05', '04', '05', '00', '03', '32', '40', '3x', '4$', '4'])[1 + floor(random() * 11)::int]
	|| (ARRAY['$', '$', '$', '$', '', '.'])[1 + floor(random() * 6)::int]
	|| CASE WHEN random() < 0.85 THEN pg_temp.rs(:'alpha', 22) ELSE pg_temp.rs(:'alpha' || :'bad', (random() * 25)::int) END
	|| CASE WHEN random() < 0.3 THEN pg_temp.rs(:'alpha', (random() * 40)::int) ELSE '' END
	FROM generate_series(1, :ncrypt / 10);
-- sha256crypt/sha512crypt, small or out-of-range round counts
INSERT INTO salts SELECT 'sha', (ARRAY['$5$', '$6$'])[1 + floor(random() * 2)::int]
	|| CASE WHEN random() < 0.6 THEN 'rounds=' || (ARRAY['1000', '1001', '10', '-5', '', 'abc', ' 1500', '+2000', '-4294967296',
		'-99999999999999999999', '1200x', '0'])[1 + floor(random() * 12)::int] || (ARRAY['$', '$', '$', ''])[1 + floor(random() * 4)::int]
		ELSE '' END
	|| (ARRAY['', '$', '$$'])[1 + floor(random() * 3)::int]
	|| pg_temp.rs(:'alpha' || '$', (random() * 20)::int)
	|| CASE WHEN random() < 0.1 THEN pg_temp.rs(:'bad' || 'rounds=$5$6', (random() * 5)::int) ELSE '' END
	|| CASE WHEN random() < 0.3 THEN '$' || pg_temp.rs(:'alpha', 43) ELSE '' END
	FROM generate_series(1, :ncrypt / 10);
-- PostgreSQL < 18's C pgcrypto has no shacrypt: psql -v noshacrypt=1
\if :old_c
DELETE FROM salts WHERE kind = 'sha';
\endif

CREATE TEMP TABLE cases AS SELECT kind, p, s FROM salts, LATERAL (SELECT p FROM pw ORDER BY random() LIMIT 3) x;
CREATE TEMP TABLE res AS SELECT kind, p, s, pg_temp.try('SELECT public.crypt($1, $2)', p, s) AS g FROM cases;
ALTER TABLE res ADD COLUMN c text;
UPDATE res SET c = pg_temp.try('SELECT c.crypt($1, $2)', p, s);

SELECT kind, count(*) AS cases, count(*) FILTER (WHERE g LIKE 'ERROR%') AS errors,
	count(*) FILTER (WHERE g IS DISTINCT FROM c) AS mismatches FROM res GROUP BY kind ORDER BY kind;
SELECT kind, p, s, g, c FROM res WHERE g IS DISTINCT FROM c LIMIT 10;

-- gen_salt: errors and the format of results, and both crypt()s agree on them.
CREATE TEMP TABLE gs AS
SELECT t, r, pg_temp.try('SELECT public.gen_salt($1, $2::int)', t, r::text) AS g,
	pg_temp.try('SELECT c.gen_salt($1, $2::int)', t, r::text) AS c
FROM unnest(ARRAY['des', 'md5', 'xdes', 'bf', 'DES', 'Bf', 'foo', '', 'bf ', 'sha256crypt', 'SHA512CRYPT']) t,
	unnest(ARRAY[0, -1, 1, 2, 3, 4, 5, 25, 31, 32, 724, 725, 1000, 1001, 16777215, 16777216, 999999999, 1000000000]) r;
\if :old_c
DELETE FROM gs WHERE t ILIKE 'sha%';
\endif
ALTER TABLE gs ADD COLUMN bad bool;
UPDATE gs SET bad = (g LIKE 'ERROR%' OR c LIKE 'ERROR%') AND g IS DISTINCT FROM c
	OR g NOT LIKE 'ERROR%' AND (length(g) <> length(c) OR regexp_replace(g, '[^$]*$', '') <> regexp_replace(c, '[^$]*$', '')
		OR g LIKE '\_%' AND left(g, 5) <> left(c, 5) OR g !~ '^[$_./0-9A-Za-z=]*$');
SELECT count(*) AS gen_salt_cases, count(*) FILTER (WHERE g LIKE 'ERROR%') AS errors,
	count(*) FILTER (WHERE bad) AS mismatches FROM gs;
SELECT t, r, g, c FROM gs WHERE bad LIMIT 10;
-- salts from either side hash the same with both, for these r (bf costs kept low)
SELECT count(*) AS cross_cases, count(*) FILTER (WHERE public.crypt('pw' || r, s) <> c.crypt('pw' || r, s)) AS mismatches
FROM gs, LATERAL (VALUES (g), (c)) v(s)
WHERE g NOT LIKE 'ERROR%' AND (t ILIKE 'bf' AND r IN (0, 4, 5) OR t NOT ILIKE 'bf' AND (t NOT ILIKE 'xdes' OR r < 2000) AND (t NOT ILIKE 'sha%' OR r <= 1001));


-- 3. OpenPGP
\connect

SET client_min_messages = warning;
DROP TABLE IF EXISTS keytbl, encdata;

create temp table keytbl (
	id int4,
	name text,
	pubkey text,
	seckey text
);
create temp table encdata (
	id int4,
	data text
);

insert into keytbl (id, name, pubkey, seckey)
values (1, 'elg1024', '
-----BEGIN PGP PUBLIC KEY BLOCK-----
Version: GnuPG v1.4.1 (GNU/Linux)

mQGiBELIIUgRBACp401L6jXrLB28c3YA4sM3OJKnxM1GT9YTkWyE3Vyte65H8WU9
tGPBX7OMuaX5eGZ84LFUGvaP0k7anfmXcDkCO3P9GgL+ro/dS2Ps/vChQPZqHaxE
xpKDUt47B7DGdRJrC8DRnIR4wbSyQA6ma3S1yFqC5pJhSs+mqf9eExOjiwCgntth
klRxIYw352ZX9Ov9oht/p/ED/1Xi4PS+tkXVvyIw5aZfa61bT6XvDkoPI0Aj3GE5
YmCHJlKA/IhEr8QJOLV++5VEv4l6KQ1/DFoJzoNdr1AGJukgTc6X/WcQRzfQtUic
PHQme5oAWoHa6bVQZOwvbJh3mOXDq/Tk/KF22go8maM44vMn4bvv+SBbslviYLiL
jZJ1A/9JXF1esNq+X9HehJyqHHU7LEEf/ck6zC7o2erM3/LZlZuLNPD2cv3oL3Nv
saEgcTSZl+8XmO8pLmzjKIb+hi70qVx3t2IhMqbb4B/dMY1Ck62gPBKa81/Wwi7v
IsEBQLEtyBmGmI64YpzoRNFeaaF9JY+sAKqROqe6dLjJ7vebQLQfRWxnYW1hbCAx
MDI0IDx0ZXN0QGV4YW1wbGUub3JnPoheBBMRAgAeBQJCyCFIAhsDBgsJCAcDAgMV
AgMDFgIBAh4BAheAAAoJEBwpvA0YF3NkOtsAniI9W2bC3CxARTpYrev7ihreDzFc
AJ9WYLQxDQAi5Ec9AQoodPkIagzZ4LkBDQRCyCFKEAQAh5SNbbJMAsJ+sQbcWEzd
ku8AdYB5zY7Qyf9EOvn0g39bzANhxmmb6gbRlQN0ioymlDwraTKUAfuCZgNcg/0P
sxFGb9nDcvjIV8qdVpnq1PuzMFuBbmGI6weg7Pj01dlPiO0wt1lLX+SubktqbYxI
+h31c3RDZqxj+KAgxR8YNGMAAwYD+wQs2He1Z5+p4OSgMERiNzF0acZUYmc0e+/9
6gfL0ft3IP+SSFo6hEBrkKVhZKoPSSRr5KpNaEobhdxsnKjUaw/qyoaFcNMzb4sF
k8wq5UlCkR+h72u6hv8FuleCV8SJUT1U2JjtlXJR2Pey9ifh8rZfu57UbdwdHa0v
iWc4DilhiEkEGBECAAkFAkLIIUoCGwwACgkQHCm8DRgXc2TtrwCfdPom+HlNVE9F
ig3hGY1Rb4NEk1gAn1u9IuQB+BgDP40YHHz6bKWS/x80
=RWci
-----END PGP PUBLIC KEY BLOCK-----
', '
-----BEGIN PGP PRIVATE KEY BLOCK-----
Version: GnuPG v1.4.1 (GNU/Linux)

lQG7BELIIUgRBACp401L6jXrLB28c3YA4sM3OJKnxM1GT9YTkWyE3Vyte65H8WU9
tGPBX7OMuaX5eGZ84LFUGvaP0k7anfmXcDkCO3P9GgL+ro/dS2Ps/vChQPZqHaxE
xpKDUt47B7DGdRJrC8DRnIR4wbSyQA6ma3S1yFqC5pJhSs+mqf9eExOjiwCgntth
klRxIYw352ZX9Ov9oht/p/ED/1Xi4PS+tkXVvyIw5aZfa61bT6XvDkoPI0Aj3GE5
YmCHJlKA/IhEr8QJOLV++5VEv4l6KQ1/DFoJzoNdr1AGJukgTc6X/WcQRzfQtUic
PHQme5oAWoHa6bVQZOwvbJh3mOXDq/Tk/KF22go8maM44vMn4bvv+SBbslviYLiL
jZJ1A/9JXF1esNq+X9HehJyqHHU7LEEf/ck6zC7o2erM3/LZlZuLNPD2cv3oL3Nv
saEgcTSZl+8XmO8pLmzjKIb+hi70qVx3t2IhMqbb4B/dMY1Ck62gPBKa81/Wwi7v
IsEBQLEtyBmGmI64YpzoRNFeaaF9JY+sAKqROqe6dLjJ7vebQAAAnj4i4st+s+C6
WKTIDcL1Iy0Saq8lCp60H0VsZ2FtYWwgMTAyNCA8dGVzdEBleGFtcGxlLm9yZz6I
XgQTEQIAHgUCQsghSAIbAwYLCQgHAwIDFQIDAxYCAQIeAQIXgAAKCRAcKbwNGBdz
ZDrbAJ9cp6AsjOhiLxwznsMJheGf4xkH8wCfUPjMCLm4tAEnyYn2hDNt7CB8B6Kd
ATEEQsghShAEAIeUjW2yTALCfrEG3FhM3ZLvAHWAec2O0Mn/RDr59IN/W8wDYcZp
m+oG0ZUDdIqMppQ8K2kylAH7gmYDXIP9D7MRRm/Zw3L4yFfKnVaZ6tT7szBbgW5h
iOsHoOz49NXZT4jtMLdZS1/krm5Lam2MSPod9XN0Q2asY/igIMUfGDRjAAMGA/sE
LNh3tWefqeDkoDBEYjcxdGnGVGJnNHvv/eoHy9H7dyD/kkhaOoRAa5ClYWSqD0kk
a+SqTWhKG4XcbJyo1GsP6sqGhXDTM2+LBZPMKuVJQpEfoe9ruob/BbpXglfEiVE9
VNiY7ZVyUdj3svYn4fK2X7ue1G3cHR2tL4lnOA4pYQAA9030E4u2ZKOfJBpUM+EM
m9VmsGjaQZV4teB0R/q3W8sRIYhJBBgRAgAJBQJCyCFKAhsMAAoJEBwpvA0YF3Nk
7a8AniFFotw1x2X+oryu3Q3nNtmxoKHpAJ9HU7jw7ydg33dI9J8gVkrmsSZ2/w==
=nvqq
-----END PGP PRIVATE KEY BLOCK-----
');

insert into keytbl (id, name, pubkey, seckey)
values (2, 'elg2048', '
-----BEGIN PGP PUBLIC KEY BLOCK-----
Version: GnuPG v1.4.1 (GNU/Linux)

mQGiBELIIgoRBAC1onBpxKYgDvrgCaUWPY34947X3ogxGOfCN0p6Eqrx+2PUhm4n
vFvmczpMT4iDc0mUO+iwnwsEkXQI1eC99g8c0jnZAvzJZ5miAHL8hukMAMfDkYke
5aVvcPPc8uPDlItpszGmH0rM0V9TIt/i9QEXetpyNWhk4jj5qnohYhLeZwCgkOdO
RFAdNi4vfFPivvtAp2ffjU8D/R3x/UJCvkzi7i9rQHGo313xxmQu5BuqIjANBUij
8IE7LRPI/Qhg2hYy3sTJwImDi7VkS+fuvNVk0d6MTWplAXYU96bn12JaD21R9sKl
Fzcc+0iZI1wYA1PczisUkoTISE+dQFUsoGHfpDLhoBuesXQrhBavI8t8VPd+nkdt
J+oKA/9iRQ87FzxdYTkh2drrv69FZHc3Frsjw9nPcBq/voAvXH0MRilqyCg7HpW/
T9naeOERksa+Rj4R57IF1l4e5oiiGJo9QmaKZcsCsXrREJCycrlEtMqXfSPy+bi5
0yDZE/Qm1dwu13+OXOsRvkoNYjO8Mzo9K8wU12hMqN0a2bu6a7QjRWxnYW1hbCAy
MDQ4IDx0ZXN0MjA0OEBleGFtcGxlLm9yZz6IXgQTEQIAHgUCQsgiCgIbAwYLCQgH
AwIDFQIDAxYCAQIeAQIXgAAKCRBI6c1W/qZo29PDAKCG724enIxRog1j+aeCp/uq
or6mbwCePuKy2/1kD1FvnhkZ/R5fpm+pdm25Ag0EQsgiIhAIAJI3Gb2Ehtz1taQ9
AhPY4Avad2BsqD3S5X/R11Cm0KBE/04D29dxn3f8QfxDsexYvNIZjoJPBqqZ7iMX
MhoWyw8ZF5Zs1mLIjFGVorePrm94N3MNPWM7x9M36bHUjx0vCZKFIhcGY1g+htE/
QweaJzNVeA5z4qZmik41FbQyQSyHa3bOkTZu++/U6ghP+iDp5UDBjMTkVyqITUVN
gC+MR+da/I60irBVhue7younh4ovF+CrVDQJC06HZl6CAJJyA81SmRfi+dmKbbjZ
LF6rhz0norPjISJvkIqvdtM4VPBKI5wpgwCzpEqjuiKrAVujRT68zvBvJ4aVqb11
k5QdJscAAwUH/jVJh0HbWAoiFTe+NvohfrA8vPcD0rtU3Y+siiqrabotnxJd2NuC
bxghJYGfNtnx0KDjFbCRKJVeTFok4UnuVYhXdH/c6i0/rCTNdeW2D6pmR4GfBozR
Pw/ARf+jONawGLyUj7uq13iquwMSE7VyNuF3ycL2OxXjgOWMjkH8c+zfHHpjaZ0R
QsetMq/iNBWraayKZnWUd+eQqNzE+NUo7w1jAu7oDpy+8a1eipxzK+O0HfU5LTiF
Z1Oe4Um0P2l3Xtx8nEgj4vSeoEkl2qunfGW00ZMMTCWabg0ZgxPzMfMeIcm6525A
Yn2qL+X/qBJTInAl7/hgPz2D1Yd7d5/RdWaISQQYEQIACQUCQsgiIgIbDAAKCRBI
6c1W/qZo25ZSAJ98WTrtl2HiX8ZqZq95v1+9cHtZPQCfZDoWQPybkNescLmXC7q5
1kNTmEU=
=8QM5
-----END PGP PUBLIC KEY BLOCK-----
', '
-----BEGIN PGP PRIVATE KEY BLOCK-----
Version: GnuPG v1.4.1 (GNU/Linux)

lQG7BELIIgoRBAC1onBpxKYgDvrgCaUWPY34947X3ogxGOfCN0p6Eqrx+2PUhm4n
vFvmczpMT4iDc0mUO+iwnwsEkXQI1eC99g8c0jnZAvzJZ5miAHL8hukMAMfDkYke
5aVvcPPc8uPDlItpszGmH0rM0V9TIt/i9QEXetpyNWhk4jj5qnohYhLeZwCgkOdO
RFAdNi4vfFPivvtAp2ffjU8D/R3x/UJCvkzi7i9rQHGo313xxmQu5BuqIjANBUij
8IE7LRPI/Qhg2hYy3sTJwImDi7VkS+fuvNVk0d6MTWplAXYU96bn12JaD21R9sKl
Fzcc+0iZI1wYA1PczisUkoTISE+dQFUsoGHfpDLhoBuesXQrhBavI8t8VPd+nkdt
J+oKA/9iRQ87FzxdYTkh2drrv69FZHc3Frsjw9nPcBq/voAvXH0MRilqyCg7HpW/
T9naeOERksa+Rj4R57IF1l4e5oiiGJo9QmaKZcsCsXrREJCycrlEtMqXfSPy+bi5
0yDZE/Qm1dwu13+OXOsRvkoNYjO8Mzo9K8wU12hMqN0a2bu6awAAn2F+iNBElfJS
8azqO/kEiIfpqu6/DQG0I0VsZ2FtYWwgMjA0OCA8dGVzdDIwNDhAZXhhbXBsZS5v
cmc+iF0EExECAB4FAkLIIgoCGwMGCwkIBwMCAxUCAwMWAgECHgECF4AACgkQSOnN
Vv6maNvTwwCYkpcJmpl3aHCQdGomz7dFohDgjgCgiThZt2xTEi6GhBB1vuhk+f55
n3+dAj0EQsgiIhAIAJI3Gb2Ehtz1taQ9AhPY4Avad2BsqD3S5X/R11Cm0KBE/04D
29dxn3f8QfxDsexYvNIZjoJPBqqZ7iMXMhoWyw8ZF5Zs1mLIjFGVorePrm94N3MN
PWM7x9M36bHUjx0vCZKFIhcGY1g+htE/QweaJzNVeA5z4qZmik41FbQyQSyHa3bO
kTZu++/U6ghP+iDp5UDBjMTkVyqITUVNgC+MR+da/I60irBVhue7younh4ovF+Cr
VDQJC06HZl6CAJJyA81SmRfi+dmKbbjZLF6rhz0norPjISJvkIqvdtM4VPBKI5wp
gwCzpEqjuiKrAVujRT68zvBvJ4aVqb11k5QdJscAAwUH/jVJh0HbWAoiFTe+Nvoh
frA8vPcD0rtU3Y+siiqrabotnxJd2NuCbxghJYGfNtnx0KDjFbCRKJVeTFok4Unu
VYhXdH/c6i0/rCTNdeW2D6pmR4GfBozRPw/ARf+jONawGLyUj7uq13iquwMSE7Vy
NuF3ycL2OxXjgOWMjkH8c+zfHHpjaZ0RQsetMq/iNBWraayKZnWUd+eQqNzE+NUo
7w1jAu7oDpy+8a1eipxzK+O0HfU5LTiFZ1Oe4Um0P2l3Xtx8nEgj4vSeoEkl2qun
fGW00ZMMTCWabg0ZgxPzMfMeIcm6525AYn2qL+X/qBJTInAl7/hgPz2D1Yd7d5/R
dWYAAVQKFPXbRaxbdArwRVXMzSD3qj/+VwwhwEDt8zmBGnlBfwVdkjQQrDUMmV1S
EwyISQQYEQIACQUCQsgiIgIbDAAKCRBI6c1W/qZo25ZSAJ4sgUfHTVsG/x3p3fcM
3b5R86qKEACggYKSwPWCs0YVRHOWqZY0pnHtLH8=
=3Dgk
-----END PGP PRIVATE KEY BLOCK-----
');

insert into keytbl (id, name, pubkey, seckey)
values (3, 'elg4096', '
-----BEGIN PGP PUBLIC KEY BLOCK-----
Version: GnuPG v1.4.1 (GNU/Linux)

mQGiBELII7wRBACFuaAvb11cIvjJK9LkZr4cYuYhLWh3DJdojNNnLNiym5OEksvY
05cw8OgqKtPzICU7o/mHXTWhzJYUt3i50/AeYygI8Q0uATS6RnDAKNlES1EMoHKz
2a5iFbYs4bm4IwlkvYd8uWjcu+U0YLbxir39u+anIc6eT+q3WiH/q3zDRwCgkT98
cnIG8iO8PdwDSP8G4Lt6TYED/R45GvCzJ4onQALLE92KkLUz8aFWSl05r84kczEN
SxiP9Ss6m465RmwWHfwYAu4b+c4GeNyU8fIU2EM8cezchC+edEi3xu1s+pCV0Dk4
18DGC8WKCICO30vBynuNmYg7W/7Zd4wtjss454fMW7+idVDNM701mmXBtI1nsBtG
7Z4tA/9FxjFbJK9jh24RewfjHpLYqcfCo2SsUjOwsnMZ5yg2yv9KyVVQhRqwmrqt
q8MRyjGmfoD9PPdCgvqgzy0hHvAHUtTm2zUczGTG+0g4hNIklxC/Mv6J4KE+NWTh
uB4acqofHyaw2WnKOuRUsoDi6rG5AyjNMyAK/vVcEGj7J1tk27QjRWxnYW1hbCA0
MDk2IDx0ZXN0NDA5NkBleGFtcGxlLm9yZz6IXgQTEQIAHgUCQsgjvAIbAwYLCQgH
AwIDFQIDAxYCAQIeAQIXgAAKCRBj+HX2P2d0oAEDAJ9lI+CNmb42z3+a6TnVusM6
FI7oLwCfUwA1zEcRdsT3nIkoYh0iKxFSDFW5BA0EQsgkdhAQAJQbLXlgcJ/jq+Xh
Eujb77/eeftFJObNIRYD9fmJ7HFIXbUcknEpbs+cRH/nrj5dGSY3OT3jCXOUtvec
sCoX/CpZWL0oqDjAiZtNSFiulw5Gav4gHYkWKgKdSo+2rkavEPqKIVHvMeXaJtGT
d7v/AmL/P8T7gls93o5WFBOLtPbDvWqaKRy2U5TAhl1laiM0vGALRVjvSCgnGw9g
FpSnXbO3AfenUSjDzZujfGLHtU44ixHSS/D4DepiF3YaYLsN4CBqZRv6FbMZD5W3
DnJY4kS1kH0MzdcF19TlcZ3itTCcGIt1tMKf84mccPoqdMzH7vumBGTeFEly5Afp
9berJcirqh2fzlunN0GS02z6SGWnjTbDlkNDxuxPSBbpcpNyD3jpYAUqSwRsZ/+5
zkzcbGtDmvy9sJ5lAXkxGoIoQ1tEVX/LOHnh2NQHK8ourVOnr7MS0nozssITZJ5E
XqtHiREjiYEuPyZiVZKJHLWuYYaF+n40znnz3sJuXFRreHhHbbvRdlYUU5mJV+XZ
BLgKuS33NdpGeMIngnCc/9IQ6OZb6ixc94kbkd3w2PVr8CbKlu/IHTjWOO2mAo+D
+OydlYl23FiM3KOyMP1HcEOJMB/nwkMtrvd+522Lu9n77ktKfot9IPrQDIQTyXjR
3pCOFtCOBnk2tJHMPoG9jn9ah/LHAAMHEACDZ5I/MHGfmiKg2hrmqBu2J2j/deC8
CpwcyDH1ovQ0gHvb9ESa+CVRU2Wdy2CD7Q9SmtMverB5eneL418iPVRcQdwRmQ2y
IH4udlBa6ce9HTUCaecAZ4/tYBnaC0Av/9l9tz14eYcwRMDpB+bnkhgF+PZ1KAfD
9wcY2aHbtsf3lZBc5h4owPJkxpe/BNzuJxW3q4VpSbLsZhwnCZ2wg7DRwP44wFIk
00ptmoBY59gsU6I40XtzrF8JDr0cA57xND5RY21Z8lnnYRE1Tc8h5REps9ZIxW3/
yl91404bPLqxczpUHQAMSTAmBaStPYX1nS51uofOhLs5SKPCUmxfGKIOhsD0oLUn
78DnkONVGeXzBibSwwtbgfMzee4G8wSUfJ7w8WXz1TyanaGLnJ+DuKASSOrFoBCD
HEDuWZWgSL74NOQupFRk0gxOPmqU94Y8HziQWma/cETbmD83q8rxN+GM2oBxQkQG
xcbqMTHE7aVhV3tymbSWVaYhww3oIwsZS9oUIi1DnPEowS6CpVRrwdvLjLJnJzzV
O3AFPn9eZ1Q7R1tNx+zZ4OOfhvI/OlRJ3HBx2L53embkbdY9gFYCCdTjPyjKoDIx
kALgCajjCYMNUsAKNSd6mMCQ8TtvukSzkZS1RGKP27ohsdnzIVsiEAbxDMMcI4k1
ul0LExUTCXSjeIhJBBgRAgAJBQJCyCR2AhsMAAoJEGP4dfY/Z3Sg19sAn0NDS8pb
qrMpQAxSb7zRTmcXEFd9AJ435H0ttP/NhLHXC9ezgbCMmpXMOQ==
=kRxT
-----END PGP PUBLIC KEY BLOCK-----
', '
-----BEGIN PGP PRIVATE KEY BLOCK-----
Version: GnuPG v1.4.1 (GNU/Linux)

lQG7BELII7wRBACFuaAvb11cIvjJK9LkZr4cYuYhLWh3DJdojNNnLNiym5OEksvY
05cw8OgqKtPzICU7o/mHXTWhzJYUt3i50/AeYygI8Q0uATS6RnDAKNlES1EMoHKz
2a5iFbYs4bm4IwlkvYd8uWjcu+U0YLbxir39u+anIc6eT+q3WiH/q3zDRwCgkT98
cnIG8iO8PdwDSP8G4Lt6TYED/R45GvCzJ4onQALLE92KkLUz8aFWSl05r84kczEN
SxiP9Ss6m465RmwWHfwYAu4b+c4GeNyU8fIU2EM8cezchC+edEi3xu1s+pCV0Dk4
18DGC8WKCICO30vBynuNmYg7W/7Zd4wtjss454fMW7+idVDNM701mmXBtI1nsBtG
7Z4tA/9FxjFbJK9jh24RewfjHpLYqcfCo2SsUjOwsnMZ5yg2yv9KyVVQhRqwmrqt
q8MRyjGmfoD9PPdCgvqgzy0hHvAHUtTm2zUczGTG+0g4hNIklxC/Mv6J4KE+NWTh
uB4acqofHyaw2WnKOuRUsoDi6rG5AyjNMyAK/vVcEGj7J1tk2wAAoJCUNy6awTkw
XfbLbpqh0fvDst7jDLa0I0VsZ2FtYWwgNDA5NiA8dGVzdDQwOTZAZXhhbXBsZS5v
cmc+iF4EExECAB4FAkLII7wCGwMGCwkIBwMCAxUCAwMWAgECHgECF4AACgkQY/h1
9j9ndKABAwCeNEOVK87EzXYbtxYBsnjrUI948NIAn2+f3BXiBFDV5NvqPwIZ0m77
Fwy4nQRMBELIJHYQEACUGy15YHCf46vl4RLo2++/3nn7RSTmzSEWA/X5iexxSF21
HJJxKW7PnER/564+XRkmNzk94wlzlLb3nLAqF/wqWVi9KKg4wImbTUhYrpcORmr+
IB2JFioCnUqPtq5GrxD6iiFR7zHl2ibRk3e7/wJi/z/E+4JbPd6OVhQTi7T2w71q
mikctlOUwIZdZWojNLxgC0VY70goJxsPYBaUp12ztwH3p1Eow82bo3xix7VOOIsR
0kvw+A3qYhd2GmC7DeAgamUb+hWzGQ+Vtw5yWOJEtZB9DM3XBdfU5XGd4rUwnBiL
dbTCn/OJnHD6KnTMx+77pgRk3hRJcuQH6fW3qyXIq6odn85bpzdBktNs+khlp402
w5ZDQ8bsT0gW6XKTcg946WAFKksEbGf/uc5M3GxrQ5r8vbCeZQF5MRqCKENbRFV/
yzh54djUByvKLq1Tp6+zEtJ6M7LCE2SeRF6rR4kRI4mBLj8mYlWSiRy1rmGGhfp+
NM55897CblxUa3h4R2270XZWFFOZiVfl2QS4Crkt9zXaRnjCJ4JwnP/SEOjmW+os
XPeJG5Hd8Nj1a/AmypbvyB041jjtpgKPg/jsnZWJdtxYjNyjsjD9R3BDiTAf58JD
La73fudti7vZ++5LSn6LfSD60AyEE8l40d6QjhbQjgZ5NrSRzD6BvY5/WofyxwAD
BxAAg2eSPzBxn5oioNoa5qgbtido/3XgvAqcHMgx9aL0NIB72/REmvglUVNlnctg
g+0PUprTL3qweXp3i+NfIj1UXEHcEZkNsiB+LnZQWunHvR01AmnnAGeP7WAZ2gtA
L//Zfbc9eHmHMETA6Qfm55IYBfj2dSgHw/cHGNmh27bH95WQXOYeKMDyZMaXvwTc
7icVt6uFaUmy7GYcJwmdsIOw0cD+OMBSJNNKbZqAWOfYLFOiONF7c6xfCQ69HAOe
8TQ+UWNtWfJZ52ERNU3PIeURKbPWSMVt/8pfdeNOGzy6sXM6VB0ADEkwJgWkrT2F
9Z0udbqHzoS7OUijwlJsXxiiDobA9KC1J+/A55DjVRnl8wYm0sMLW4HzM3nuBvME
lHye8PFl89U8mp2hi5yfg7igEkjqxaAQgxxA7lmVoEi++DTkLqRUZNIMTj5qlPeG
PB84kFpmv3BE25g/N6vK8TfhjNqAcUJEBsXG6jExxO2lYVd7cpm0llWmIcMN6CML
GUvaFCItQ5zxKMEugqVUa8Hby4yyZyc81TtwBT5/XmdUO0dbTcfs2eDjn4byPzpU
Sdxwcdi+d3pm5G3WPYBWAgnU4z8oyqAyMZAC4Amo4wmDDVLACjUnepjAkPE7b7pE
s5GUtURij9u6IbHZ8yFbIhAG8QzDHCOJNbpdCxMVEwl0o3gAAckBdfKuasiNUn5G
L5XRnSvaOFzftr8zteOlZChCSNvzH5k+i1j7RJbWq06OeKRywPzjfjgM2MvRzI43
ICeISQQYEQIACQUCQsgkdgIbDAAKCRBj+HX2P2d0oNfbAJ9+G3SeXrk+dWwo9EGi
hqMi2GVTsgCfeoQJPsc8FLYUgfymc/3xqAVLUtg=
=Gjq6
-----END PGP PRIVATE KEY BLOCK-----
');

insert into keytbl (id, name, pubkey, seckey)
values (4, 'rsa2048', '
-----BEGIN PGP PUBLIC KEY BLOCK-----
Version: GnuPG v1.4.1 (GNU/Linux)

mQELBELIJbEBCADAIdtcoLAmQfl8pb73pPRuEYx8qW9klLfCGG5A4OUOi00JHNwP
ZaABe1PGzjoeXrgM1MTQZhoZu1Vdg+KDI6XAtiy9P6bLg7ntsXksD4wBoIKtQKc2
55pdukxTiu+xeJJG2q8ZZPOp97CV9fbQ9vPCwgnuSsDCoQlibZikDVPAyVTvp7Jx
5rz8yXsl4sxvaeMZPqqFPtA/ENeQ3cpsyR1BQXSvoZpH1Fq0b8GcZTEdWWD/w6/K
MCRC8TmgEd+z3e8kIsCwFQ+TSHbCcxRWdgZE7gE31sJHHVkrZlXtLU8MPXWqslVz
R0cX+yC8j6bXI6/BqZ2SvRndJwuunRAr4um7AAYptB5SU0EgMjA0OCA8cnNhMjA0
OEBleGFtcGxlLm9yZz6JATQEEwECAB4FAkLIJbECGwMGCwkIBwMCAxUCAwMWAgEC
HgECF4AACgkQnc+OnJvTHyQqHwf8DtzuAGmObfe3ggtn14x2wnU1Nigebe1K5liR
nrLuVlLBpdO6CWmMUzfKRvyZlx54GlA9uUQSjW+RlgejdOTQqesDrcTEukYd4yzw
bLZyM5Gb3lsE/FEmE7Dxw/0Utf59uACqzG8LACQn9J6sEgZWKxAupuYTHXd12lDP
D3dnU4uzKPhMcjnSN00pzjusP7C9NZd3OLkAx2vw/dmb4Q+/QxeZhVYYsAUuR2hv
9bgGWopumlOkt8Zu5YG6+CtTbJXprPI7pJ1jHbeE+q/29hWJQtS8Abx82AcOkzhv
S3NZKoJ/1DrGgoDAu1mGkM4KvLAxfDs/qQ9dZhtEmDbKPLTVEA==
=lR4n
-----END PGP PUBLIC KEY BLOCK-----
', '
-----BEGIN PGP PRIVATE KEY BLOCK-----
Version: GnuPG v1.4.1 (GNU/Linux)

lQOWBELIJbEBCADAIdtcoLAmQfl8pb73pPRuEYx8qW9klLfCGG5A4OUOi00JHNwP
ZaABe1PGzjoeXrgM1MTQZhoZu1Vdg+KDI6XAtiy9P6bLg7ntsXksD4wBoIKtQKc2
55pdukxTiu+xeJJG2q8ZZPOp97CV9fbQ9vPCwgnuSsDCoQlibZikDVPAyVTvp7Jx
5rz8yXsl4sxvaeMZPqqFPtA/ENeQ3cpsyR1BQXSvoZpH1Fq0b8GcZTEdWWD/w6/K
MCRC8TmgEd+z3e8kIsCwFQ+TSHbCcxRWdgZE7gE31sJHHVkrZlXtLU8MPXWqslVz
R0cX+yC8j6bXI6/BqZ2SvRndJwuunRAr4um7AAYpAAf/QZsrrz0c7dgWwGqMIpw6
fP+/lLa74+fa2CFRWtYowEiKsfDg/wN7Ua07036dNhPa8aZPsU6SRzm5PybKOURe
D9pNt0FxJkX0j5pCWfjSJgTbc1rCdqZ/oyBk/U6pQtf//zfw3PbDl7I8TC6GOt2w
5NgcXdsWHP7LAmPctOVUyzFsenevR0MFTHkMbmKI1HpFm8XN/e1Fl+qIAD+OagTF
5B32VvpoJtkh5nxnIuToNJsa9Iy7F9MM2CeFOyTMihMcjXKBBUaAYoF115irBvqu
7N/qWmzqLg8yxBZ56mh6meCF3+67VA2y7fL8rhw2QuqgLg1JFlKAVL+9crCSrn//
GQQA1kT7FytW6BNOffblFYZkrJer3icoRDqa/ljgH/yVaWoVT1igy0E9XzYO7MwP
2usj/resLy0NC1qCthk51cZ/wthooMl88e5Wb4l5FYwBEac7muSBTo4W8cAH1hFj
TWL6XAGvEzGX3Mt9pn8uYGlQLZAhJoNCAU2EOCbN1PchDvsEAOWNKYesuUVk8+sQ
St0NDNhd9BWtTWTHkCZb1dKC3JTfr9PqkTBLrWFbYjkOtvdPAW7FDaXXXZfdH1jH
WfwP3Q+I6sqgSaWpCS4dBAns3/RVtO7czVgyIwma04iIvJqderYrfvkUq95KfwP2
V8wXkhrPPPxyrg5y3wQlpY2jb5RBBAC17SK1ms+DBtck4vpdjp3SJ32SbyC/DU30
89Q12j74S7Zdu1qZlKnvy3kWPYX/hMuSzGZ+mLVJNFEqH2X01aFzppYz0hdI9PGB
9tTFEqZWQL9ZkXfjc79Cgnt12pNukRbtw0N/kyutOdIFHVT79wVAd+powqziXJsC
Kc+4xjwSCkZitB5SU0EgMjA0OCA8cnNhMjA0OEBleGFtcGxlLm9yZz6JATQEEwEC
AB4FAkLIJbECGwMGCwkIBwMCAxUCAwMWAgECHgECF4AACgkQnc+OnJvTHyQqHwf8
DtzuAGmObfe3ggtn14x2wnU1Nigebe1K5liRnrLuVlLBpdO6CWmMUzfKRvyZlx54
GlA9uUQSjW+RlgejdOTQqesDrcTEukYd4yzwbLZyM5Gb3lsE/FEmE7Dxw/0Utf59
uACqzG8LACQn9J6sEgZWKxAupuYTHXd12lDPD3dnU4uzKPhMcjnSN00pzjusP7C9
NZd3OLkAx2vw/dmb4Q+/QxeZhVYYsAUuR2hv9bgGWopumlOkt8Zu5YG6+CtTbJXp
rPI7pJ1jHbeE+q/29hWJQtS8Abx82AcOkzhvS3NZKoJ/1DrGgoDAu1mGkM4KvLAx
fDs/qQ9dZhtEmDbKPLTVEA==
=WKAv
-----END PGP PRIVATE KEY BLOCK-----
');

insert into keytbl (id, name, pubkey, seckey)
values (5, 'psw-elg1024', '
-----BEGIN PGP PUBLIC KEY BLOCK-----
Version: GnuPG v1.4.1 (GNU/Linux)

mQGiBELIIUgRBACp401L6jXrLB28c3YA4sM3OJKnxM1GT9YTkWyE3Vyte65H8WU9
tGPBX7OMuaX5eGZ84LFUGvaP0k7anfmXcDkCO3P9GgL+ro/dS2Ps/vChQPZqHaxE
xpKDUt47B7DGdRJrC8DRnIR4wbSyQA6ma3S1yFqC5pJhSs+mqf9eExOjiwCgntth
klRxIYw352ZX9Ov9oht/p/ED/1Xi4PS+tkXVvyIw5aZfa61bT6XvDkoPI0Aj3GE5
YmCHJlKA/IhEr8QJOLV++5VEv4l6KQ1/DFoJzoNdr1AGJukgTc6X/WcQRzfQtUic
PHQme5oAWoHa6bVQZOwvbJh3mOXDq/Tk/KF22go8maM44vMn4bvv+SBbslviYLiL
jZJ1A/9JXF1esNq+X9HehJyqHHU7LEEf/ck6zC7o2erM3/LZlZuLNPD2cv3oL3Nv
saEgcTSZl+8XmO8pLmzjKIb+hi70qVx3t2IhMqbb4B/dMY1Ck62gPBKa81/Wwi7v
IsEBQLEtyBmGmI64YpzoRNFeaaF9JY+sAKqROqe6dLjJ7vebQLQfRWxnYW1hbCAx
MDI0IDx0ZXN0QGV4YW1wbGUub3JnPoheBBMRAgAeBQJCyCFIAhsDBgsJCAcDAgMV
AgMDFgIBAh4BAheAAAoJEBwpvA0YF3NkOtsAniI9W2bC3CxARTpYrev7ihreDzFc
AJ9WYLQxDQAi5Ec9AQoodPkIagzZ4LkBDQRCyCFKEAQAh5SNbbJMAsJ+sQbcWEzd
ku8AdYB5zY7Qyf9EOvn0g39bzANhxmmb6gbRlQN0ioymlDwraTKUAfuCZgNcg/0P
sxFGb9nDcvjIV8qdVpnq1PuzMFuBbmGI6weg7Pj01dlPiO0wt1lLX+SubktqbYxI
+h31c3RDZqxj+KAgxR8YNGMAAwYD+wQs2He1Z5+p4OSgMERiNzF0acZUYmc0e+/9
6gfL0ft3IP+SSFo6hEBrkKVhZKoPSSRr5KpNaEobhdxsnKjUaw/qyoaFcNMzb4sF
k8wq5UlCkR+h72u6hv8FuleCV8SJUT1U2JjtlXJR2Pey9ifh8rZfu57UbdwdHa0v
iWc4DilhiEkEGBECAAkFAkLIIUoCGwwACgkQHCm8DRgXc2TtrwCfdPom+HlNVE9F
ig3hGY1Rb4NEk1gAn1u9IuQB+BgDP40YHHz6bKWS/x80
=RWci
-----END PGP PUBLIC KEY BLOCK-----
', '
-----BEGIN PGP PRIVATE KEY BLOCK-----
Version: GnuPG v1.4.1 (GNU/Linux)

lQHpBELIIUgRBACp401L6jXrLB28c3YA4sM3OJKnxM1GT9YTkWyE3Vyte65H8WU9
tGPBX7OMuaX5eGZ84LFUGvaP0k7anfmXcDkCO3P9GgL+ro/dS2Ps/vChQPZqHaxE
xpKDUt47B7DGdRJrC8DRnIR4wbSyQA6ma3S1yFqC5pJhSs+mqf9eExOjiwCgntth
klRxIYw352ZX9Ov9oht/p/ED/1Xi4PS+tkXVvyIw5aZfa61bT6XvDkoPI0Aj3GE5
YmCHJlKA/IhEr8QJOLV++5VEv4l6KQ1/DFoJzoNdr1AGJukgTc6X/WcQRzfQtUic
PHQme5oAWoHa6bVQZOwvbJh3mOXDq/Tk/KF22go8maM44vMn4bvv+SBbslviYLiL
jZJ1A/9JXF1esNq+X9HehJyqHHU7LEEf/ck6zC7o2erM3/LZlZuLNPD2cv3oL3Nv
saEgcTSZl+8XmO8pLmzjKIb+hi70qVx3t2IhMqbb4B/dMY1Ck62gPBKa81/Wwi7v
IsEBQLEtyBmGmI64YpzoRNFeaaF9JY+sAKqROqe6dLjJ7vebQP4HAwImKZ5q2QwT
D2DDAY/IQBjes7WgqZeacfLPDoB8ecD/KLoSCH6Z3etvbPHSOKiazxoJ962Ix74H
ZAE6ZbMTtl5dZW1ptB9FbGdhbWFsIDEwMjQgPHRlc3RAZXhhbXBsZS5vcmc+iF4E
ExECAB4FAkLIIUgCGwMGCwkIBwMCAxUCAwMWAgECHgECF4AACgkQHCm8DRgXc2Q6
2wCfXKegLIzoYi8cM57DCYXhn+MZB/MAn1D4zAi5uLQBJ8mJ9oQzbewgfAeinQFf
BELIIUoQBACHlI1tskwCwn6xBtxYTN2S7wB1gHnNjtDJ/0Q6+fSDf1vMA2HGaZvq
BtGVA3SKjKaUPCtpMpQB+4JmA1yD/Q+zEUZv2cNy+MhXyp1WmerU+7MwW4FuYYjr
B6Ds+PTV2U+I7TC3WUtf5K5uS2ptjEj6HfVzdENmrGP4oCDFHxg0YwADBgP7BCzY
d7Vnn6ng5KAwRGI3MXRpxlRiZzR77/3qB8vR+3cg/5JIWjqEQGuQpWFkqg9JJGvk
qk1oShuF3GycqNRrD+rKhoVw0zNviwWTzCrlSUKRH6Hva7qG/wW6V4JXxIlRPVTY
mO2VclHY97L2J+Hytl+7ntRt3B0drS+JZzgOKWH+BwMCJimeatkMEw9gRkFjt4Xa
9rX8awMBE5+vVcGKv/DNiCvJnlYvSdCj8VfuHsYFliiJo6u17NJon+K43e3yvDNk
f631VOVanGEz7TyqOkWQiEkEGBECAAkFAkLIIUoCGwwACgkQHCm8DRgXc2TtrwCe
IUWi3DXHZf6ivK7dDec22bGgoekAn0dTuPDvJ2Dfd0j0nyBWSuaxJnb/
=SNvr
-----END PGP PRIVATE KEY BLOCK-----
');

insert into keytbl (id, name, pubkey, seckey)
values (6, 'rsaenc2048', '
-----BEGIN PGP PUBLIC KEY BLOCK-----
Version: GnuPG v1.4.1 (GNU/Linux)

mQELBELr2m0BCADOrnknlnXI0EzRExf/TgoHvK7Xx/E0keWqV3KrOyC3/tY2KOrj
UVxaAX5pkFX9wdQObGPIJm06u6D16CH6CildX/vxG7YgvvKzK8JGAbwrXAfk7OIW
czO2zRaZGDynoK3mAxHRBReyTKtNv8rDQhuZs6AOozJNARdbyUO/yqUnqNNygWuT
4htFDEuLPIJwAbMSD0BvFW6YQaPdxzaAZm3EWVNbwDzjgbBUdBiUUwRdZIFUhsjJ
dirFdy5+uuZru6y6CNC1OERkJ7P8EyoFiZckAIE5gshVZzNuyLOZjc5DhWBvLbX4
NZElAnfiv+4nA6y8wQLSIbmHA3nqJaBklj85AAYptCVSU0EgMjA0OCBFbmMgPHJz
YTIwNDhlbmNAZXhhbXBsZS5vcmc+iQE0BBMBAgAeBQJC69ptAhsDBgsJCAcDAgMV
AgMDFgIBAh4BAheAAAoJEMiZ6pNEGVVZHMkIAJtGHHZ9iM8Yq1rr0zl1L6SvlQP8
JCaxHa31wH3PKqGtq2M+cpb2rXf7gAY/doHJPXggfVzkyFrysmQ1gPbDGYLyOutw
+IkhihEb5bWxQBNj+3zAFs1YX6v2HXWbSUSmyY1V9/+NTtKk03olDc/swd3lXzku
UOhcgfpBgIt3Q+MpT6M2+OIF7lVfSb1rWdpwTfGhZzW9szQOeoS4gPvxCCRyuabQ
RJ6DWH61F8fFIDJg1z+A/Obx4fqX6GOA69RzgZ3oukFBIXxNwV9PZNnAmHtZVYO8
0g/oVYBbuvOYedffDBeQarhERZ5W2TnIE+nqY61YOLBqosliygdZTXULzNi5AQsE
QuvaugEIAOuCJZdkzORA6e1lr81Lnr4JzMsVBFA+X/yIkBbV6qX/A4nVSLAZKNPX
z1YIrMTu+1rMIiy10IWbA6zgMTpzPhJRfgePONgdnCYyK5Ksh5/C5ntzKwwGwxfK
lAXIxJurCHXTbEa+YvPdn76vJ3HsXOXVEL+fLb4U3l3Ng87YM202Lh1Ha2MeS2zE
FZcAoKbFqAAjDLEai64SoOFh0W3CsD1DL4zmfp+YZrUPHTtZadsi53i4KKW/ws9U
rHlolqYNhYze/uRLyfnUx9PN4r/GhEzauyDMV0smo91uB3aewPft+eCpmeWnu0PF
JVK4xyRmhIq2rVCw16a1pBJirvGM+y0ABimJAR8EGAECAAkFAkLr2roCGwwACgkQ
yJnqk0QZVVku1wgAg1bLSjPkhw+ldG5HzumpqR84+JKyozdJaJzefu2+1iqYE0B0
WLz2PJVIiK41xiEkKhBvTOQYuXmtWqAWXptD91P5SoXoNJWLQO3TNwarANhHxkWg
w/TOUxQqoctlRUej5NDD+4eW5G9lcS1FEGuKDWtX096u80vO+TbyJjvx2eVM1k+X
dmeYsGOiNgDimCreJGYc14G7eY9jt24gw10n1sMAKI1qm6lcoHqZ9OOyla+wJdro
PYZGO7R8+1O9R22WrK6BYDT5j/1JwMZqbOESjNvDEVT0yOHClCHRN4CChbt6LhKh
CLUNdz/udIt0JAC6c/HdPLSW3HnmM3+iNj+Kug==
=pwU2
-----END PGP PUBLIC KEY BLOCK-----
', '
-----BEGIN PGP PRIVATE KEY BLOCK-----
Version: GnuPG v1.4.1 (GNU/Linux)

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
=UKh3
-----END PGP PRIVATE KEY BLOCK-----
');

insert into keytbl (id, name, pubkey, seckey)
values (7, 'rsaenc2048-psw', '
same key with password
', '
-----BEGIN PGP PRIVATE KEY BLOCK-----
Version: GnuPG v1.4.11 (GNU/Linux)

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
=QFm5
-----END PGP PRIVATE KEY BLOCK-----
');


-- elg1024 / aes128
insert into encdata (id, data) values (1, '
-----BEGIN PGP MESSAGE-----
Version: GnuPG v1.4.1 (GNU/Linux)

hQEOA9k2z2S7c/RmEAQAgVWW0DeLrZ+1thWJGBPp2WRFL9HeNqqWHbKJCXJbz1Uy
faUY7yxVvG5Eutmo+JMiY3mg23/DgVVXHQZsTWpGvGM6djgUNGKUjZDbW6Nog7Mr
e78IywattCOmgUP9vIwwg3OVjuDCN/nVirGQFnXpJBc8DzWqDMWRWDy1M0ZsK7AD
/2JTosSFxUdpON0DKtIY3GLzmh6Nk3iV0g8VgJKUBT1rhCXuMDj3snm//EMm7hTY
PlnObq4mIhgz8NqprmhooxnU0Kapofb3P3wCHPpU14zxhXY8iKO/3JhBq2uFcx4X
uBMwkW4AdNxY/mzJZELteTL8Tr0s7PISk+owb4URpG3n0jsBc0CVULxrjh5Ejkdw
wCM195J6+KbQxOOFQ0b3uOVvv4dEgd/hRERCOq5EPaFhlHegyYJ7YO842vnSDA==
=PABx
-----END PGP MESSAGE-----
');

-- elg2048 / blowfish
insert into encdata (id, data) values (2, '
-----BEGIN PGP MESSAGE-----
Version: GnuPG v1.4.1 (GNU/Linux)

hQIOAywibh/+XMfUEAf+OINhBngEsw4a/IJIeJvUgv1gTQzBwOdQEuc/runr4Oa8
Skw/Bj0X/zgABVZLem1a35NHaNwaQaCFwMQ41YyWCu+jTdsiyX/Nw0w8LKKz0rNC
vVpG6YuV7Turtsf8a5lXy1K0SHkLlgxQ6c76GS4gtSl5+bsL2+5R1gSRJ9NXqCQP
OHRipEiYwBPqr5R21ZG0FXXNKGOGkj6jt/M/wh3WVtAhYuBI+HPKRfAEjd/Pu/eD
e1zYtkH1dKKFmp44+nF0tTI274xpuso7ShfKYrOK3saFWrl0DWiWteUinjSA1YBY
m7dG7NZ8PW+g1SZWhEoPjEEEHz3kWMvlKheMRDudnQf/dDyX6kZVIAQF/5B012hq
QyVewgTGysowFIDn01uIewoEA9cASw699jw9IoJp+k5WZXnU+INllBLzQxniQCSu
iEcr0x3fYqNtj9QBfbIqyRcY6HTWcmzyOUeGaSyX76j+tRAvtVtXpraFFFnaHB70
YpXTjLkp8EBafzMghFaKDeXlr2TG/T7rbwcwWrFIwPqEAUKWN5m97Q3eyo8/ioMd
YoFD64J9ovSsgbuU5IpIGAsjxK+NKzg/2STH7zZFEVCtgcIXsTHTZfiwS98/+1H9
p1DIDaXIcUFV2ztmcKxh9gt2sXRz1W+x6D8O0k3nanU5yGG4miLKaq18fbcA0BD1
+NIzAfelq6nvvxYKcGcamBMgLo5JkZOBHvyr6RsAKIT5QYc0QTjysTk9l0Am3gYc
G2pAE+3k
=TBHV
-----END PGP MESSAGE-----
');

-- elg4096 / aes256
insert into encdata (id, data) values (3, '
-----BEGIN PGP MESSAGE-----
Version: GnuPG v1.4.1 (GNU/Linux)

hQQOA7aFBP0Sjh/5EA/+JCgncc8IZmmRjPStWnGf9tVJhgHTn+smIclibGzs0deS
SPSCitzpblwbUDvu964+/5e5Q1l7rRuNN+AgETlEd4eppv7Swn2ChdgOXxRwukcT
Nh3G+PTFvD4ayi7w1db3qvXIt0MwN4Alt436wJmK1oz2Ka9IcyO+wHWrDy1nSGSx
z5x7YEj+EZPgWc/YAvudqE8Jpzd/OT5zSHN09UFkIAk6NxisKaIstbEGFgpqtoDZ
1SJM84XAdL2IcaJ3YY7k/yzwlawhsakKd4GSd5vWmAwvyzzbSiBMfKsDE16ePLNU
ZBF7CzmlCBPZ7YrFAHLpXBXXkCQvzD2BEYOjse50ZEfJ036T7950Ozcdy1EQbGon
nyQ4Gh0PBpnMcBuiXOceWuYzhlzFOzDtlVKdNTxFRDcbEyW2jo9xQYvCCLnYy8EH
2M7S8jCtVYJBbn63a82ELv+3+kWYcsvBJv2ZVBh4ncrBu9o0P+OYS7ApoOU+j6p2
+t0RXHksqXS1YiUwYF5KSw09EbYMgNZ9G04Px/PxLU6fSC9iDrGX7Xt3kOUP0mku
C518fPckT0zzRXqfFruJNRzDytW50KxkOQZzU1/Az1YlYN9QzWeU4EtLPb2fftZo
D0qH/ln+f9Op5t6sD2fcxZVECU1b/bFtZsxvwH406YL+UQ7hU/XnZrzVVzODal8P
/j1hg7v7BdJqu1DTp9nFWUuwMFcYAczuXn29IG183NZ7Ts4whDeYEhS8eNoLPX4j
txY12ILD/w/3Q4LoW/hPa6OdfEzsn0U5GLf1WiGmJE1H6ft2U/xUnerc/u0kt+FU
WAisArd4MuKtf7B5Vu/VF3kUdrR0hTniUKUivmC4o1jSId31Dufxj4aadVyldXAr
6TNBcdyragZjxEZ6hsBCYzA0Rd1a8atd6OaQoIEEfAzCu5Ks29pydHErStYGjWJ1
KA5KPLVvjbHpDmRhlCcm8vgpYQsBYEB5gE9fx5yCTlsVhCB6y23h7hfdMqerDqkO
ZOPsO5h+tiHCdIrQ36sMjuINy1/K2rYcXd+Crh2iHcfidpU9fvDz2ihTRNQlhjuT
0cQZM5JhctEx4VXF4LDctRhit7Hn0iqsk604woQfJVvP8O673xSXT/kBY0A/v9C0
3C4YoFNeSaKwbfZQ/4u1ZFPJxK2IIJa8UGpyAUewLMlzGVVagljybv/f4Z9ERAhy
huq5sMmw8UPsrJF2TUGHz5WSIwoh0J/qovoQI09I9sdEnFczDvRavMO2Mldy3E5i
exz9oewtel6GOmsZQSYWT/vJzbYMmvHNmNpVwwoKrLV6oI3kyQ80GHBwI1WlwHoK
2iRB0w8q4VVvJeYAz8ZIp380cqC3pfO0uZsrOx4g3k4X0jsB5y7rF5xXcZfnVbvG
DYKcOy60/OHMWVvpw6trAoA+iP+cVWPtrbRvLglTVTfYmi1ToZDDipkALBhndQ==
=L/M/
-----END PGP MESSAGE-----
');

-- rsaenc2048 / aes128
insert into encdata (id, data) values (4, '
-----BEGIN PGP MESSAGE-----
Version: GnuPG v1.4.1 (GNU/Linux)

hQEMA/0CBsQJt0h1AQf+JyYnCiortj26P11zk28MKOGfWpWyAhuIgwbJXsdQ+e6r
pEyyqs9GC6gI7SNF6+J8B/gsMwvkAL4FHAQCvA4ZZ6eeXR1Of4YG22JQGmpWVWZg
DTyfhA2vkczuqfAD2tgUpMT6sdyGkQ/fnQ0lknlfHgC5GRx7aavOoAKtMqiZW5PR
yae/qR48mjX7Mb+mLvbagv9mHEgQSmHwFpaq2k456BbcZ23bvCmBnCvqV/90Ggfb
VP6gkSoFVsJ19RHsOhW1dk9ehbl51WB3zUOO5FZWwUTY9DJvKblRK/frF0+CXjE4
HfcZXHSpSjx4haGGTsMvEJ85qFjZpr0eTGOdY5cFhNJAAVP8MZfji7OhPRAoOOIK
eRGOCkao12pvPyFTFnPd5vqmyBbdNpK4Q0hS82ljugMJvM0p3vJZVzW402Kz6iBL
GQ==
=XHkF
-----END PGP MESSAGE-----
');

-- rsaenc2048 / aes128 (not from gnupg)
insert into encdata (id, data) values (5, '
-----BEGIN PGP MESSAGE-----

wcBMA/0CBsQJt0h1AQgAzxZ8j+OTeZ8IlLxfZ/mVd28/gUsCY+xigWBk/anZlK3T
p2tNU2idHzKdAttH2Hu/PWbZp4kwjl9spezYxMqCeBZqtfGED88Y+rqK0n/ul30A
7jjFHaw0XUOqFNlST1v6H2i7UXndnp+kcLfHPhnO5BIYWxB2CYBehItqtrn75eqr
C7trGzU/cr74efcWagbCDSNjiAV7GlEptlzmgVMmNikyI6w0ojEUx8lCLc/OsFz9
pJUAX8xuwjxDVv+W7xk6c96grQiQlm+FLDYGiGNXoAzx3Wi/howu3uV40dXfY+jx
3WBrhEew5Pkpt1SsWoFnJWOfJ8GLd0ec8vfRCqAIVdLgAeS7NyawQYtd6wuVrEAj
5SMg4Thb4d+g45RksuGLHUUr4qO9tiXglODa4InhmJfgNuLk+RGz4LXjq8wepEmW
vRbgFOG54+Cf4C/gC+HkreDm5JKSKjvvw4B/jC6CDxq+JoziEe2Z1uEjCuEcr+Es
/eGzeOi36BejXPMHeKxXejj5qBBHKV0pHVhZSgffR0TtlXdB967Yl/5agV0R89hI
7Gw52emfnH4Z0Y4V0au2H0k1dR/2IxXdJEWSTG7Be1JHT59p9ei2gSEOrdBMIOjP
tbYYUlmmbvD49bHfThkDiC+oc9947LgQsk3kOOLbNHcjkbrjH8R5kjII4m/SEZA1
g09T+338SzevBcVXh/cFrQ6/Et+lyyO2LJRUMs69g/HyzJOVWT2Iu8E0eS9MWevY
Qtrkrhrpkl3Y02qEp/j6M03Yu2t6ZF7dp51aJ5VhO2mmmtHaTnCyCc8Fcf72LmD8
blH2nKZC9d6fi4YzSYMepZpMOFR65M80MCMiDUGnZBB8sEADu2/iVtqDUeG8mAA=
=PHJ1
-----END PGP MESSAGE-----
');

-- CVE-2026-14663. This message was created with cipher-algo=bf using an OpenSSL
-- that didn't actually support Blowfish.
insert into encdata (id, data) values (6, '
-----BEGIN PGP MESSAGE-----

wcBOA9k2z2S7c/RmEAP8DYbU6AeEo6riMMdnf2G62BM9gC0Z32ODydewy3Ki8AnSzpwBDAHuDMcr
P6RJDWvBOVOwgxHEwR7ZHMoFRDJEXdo6rQ9dQpDtbasMLyi6Lm1q+PbEefVd9WkU7fvFAFQx8k3t
lxrlWg/byoNplc7/hFxIFO8bN+FIlLgilAdApNcD/3Mg2/nd7pczovsYoryf9ib04kQ+SVWs3iNE
StoyEXT+oaT8u1vAxiY7fzPpQX1pnlHBUXn+v1J6LQL5Bwi5CTqOyDSyaFfgU0gQwTReFjS6L4Fs
Cv+2cFwbJBGIzr1aI4DLbzSelkmVm4hbOVeET4DJVlUVhhIyy6ZfoXiTEG6s0jMB2JdRGIl0EUQR
RMsQdABqdt5jU2VjcmV0IG1zZ9MUIIP4SPiU2pM/nF/A1hrltMhn/ZI=
=Mkdj
-----END PGP MESSAGE-----
');

-- key 7 is key 6's secret key with a password
UPDATE keytbl SET pubkey = (SELECT pubkey FROM keytbl WHERE id = 6) WHERE id = 7;

-- result of a query, or its error
CREATE OR REPLACE FUNCTION pg_temp.try(q text) RETURNS text AS $$
DECLARE
	r text;
BEGIN
	EXECUTE q INTO r;
	RETURN coalesce(r, 'NULL');
EXCEPTION WHEN OTHERS THEN
	RETURN 'ERROR ' || SQLSTATE || ': ' || SQLERRM;
END $$ LANGUAGE plpgsql;

-- payloads: empty, sizes around block and packet boundaries, random
-- binary, text with line ends and non-ASCII characters
CREATE TEMP TABLE pl AS
SELECT row_number() OVER () AS id, b, t FROM (
	SELECT '\x'::bytea AS b, ''::text AS t
	UNION ALL SELECT convert_to(s, 'UTF8'), s FROM unnest(ARRAY['a', E'a\nb\r\nc\r', E'\r\n\r\n\n', 'añb€😀 ö',
		repeat(E'line\n', 4000), repeat('x', 8191), repeat('y', 8192), repeat('z', 16383), repeat('w', 16384), repeat('v', 16385)]) s
	UNION ALL SELECT decode(string_agg(lpad(to_hex((random() * 255)::int), 2, '0'), ''), 'hex'), NULL
		FROM generate_series(1, :npgp) g, generate_series(1, 1 + (g * 7919) % 3000) i GROUP BY g
) x;

CREATE TEMP TABLE opt(o text);
INSERT INTO opt VALUES (''), ('cipher-algo=bf'), ('cipher-algo=aes128'), ('cipher-algo=aes192'),
	('cipher-algo=aes256'), ('cipher-algo=3des'), ('cipher-algo=cast5'), ('disable-mdc=1'),
	('sess-key=1'), ('sess-key=1, s2k-cipher-algo=bf'), ('sess-key=1, s2k-cipher-algo=aes256, cipher-algo=3des'),
	('s2k-mode=0'), ('s2k-mode=1'), ('s2k-mode=3, s2k-count=1024'), ('s2k-count=65011712'), ('s2k-count=100000'),
	('s2k-digest-algo=md5'), ('s2k-digest-algo=sha256'), ('s2k-digest-algo=sha384'), ('s2k-digest-algo=sha512'),
	('s2k-digest-algo=ripemd160'), ('compress-algo=1'), ('compress-algo=2'), ('compress-algo=1, compress-level=1'),
	('compress-algo=2, compress-level=9'), ('compress-algo=2, compress-level=0'),
	('disable-mdc=1, compress-algo=1, cipher-algo=bf'), ('disable-mdc=1, sess-key=1, cipher-algo=cast5, compress-algo=2'),
	('convert-crlf=1'), ('unicode-mode=1'), ('convert-crlf=1, unicode-mode=1, compress-algo=2');

CREATE TEMP TABLE res(chk text, detail text, a text, b text);

-- symmetric: one encrypts, the other decrypts (bytea and text)
INSERT INTO res
SELECT 'sym ' || dir, o || ' / payload ' || id, a, b FROM (
	SELECT 'go->c' AS dir, o, id,
		pg_temp.try(format('SELECT encode(c.pgp_sym_decrypt_bytea(public.pgp_sym_encrypt_bytea(%L, %L, %L), %L), ''hex'')', b, 'pw' || id, o, 'pw' || id)) AS a,
		encode(b, 'hex') AS b FROM pl, opt
	UNION ALL SELECT 'c->go', o, id,
		pg_temp.try(format('SELECT encode(public.pgp_sym_decrypt_bytea(c.pgp_sym_encrypt_bytea(%L, %L, %L), %L), ''hex'')', b, 'pw' || id, o, 'pw' || id)),
		encode(b, 'hex') FROM pl, opt
	UNION ALL SELECT 'text go->c', o, id,
		pg_temp.try(format('SELECT c.pgp_sym_decrypt(public.pgp_sym_encrypt(%L, %L, %L), %L, %L)', t, 'pw', o, 'pw', o)),
		pg_temp.try(format('SELECT public.pgp_sym_decrypt(c.pgp_sym_encrypt(%L, %L, %L), %L, %L)', t, 'pw', o, 'pw', o))
		FROM pl, opt WHERE t IS NOT NULL
	UNION ALL SELECT 'text c->go', o, id,
		pg_temp.try(format('SELECT public.pgp_sym_decrypt(c.pgp_sym_encrypt(%L, %L, %L), %L, %L)', t, 'pw', o, 'pw', o)),
		pg_temp.try(format('SELECT c.pgp_sym_decrypt(c.pgp_sym_encrypt(%L, %L, %L), %L, %L)', t, 'pw', o, 'pw', o))
		FROM pl, opt WHERE t IS NOT NULL
	-- bytea data read as text: not text data, encoding errors
	UNION ALL SELECT 'bytea as text', o, id,
		pg_temp.try(format('SELECT public.pgp_sym_decrypt(c.pgp_sym_encrypt_bytea(%L, %L, %L), %L, %L)', b, 'pw', o, 'pw', o)),
		pg_temp.try(format('SELECT c.pgp_sym_decrypt(c.pgp_sym_encrypt_bytea(%L, %L, %L), %L, %L)', b, 'pw', o, 'pw', o))
		FROM pl, opt WHERE id % 5 = 0
) x;

-- public keys (upstream's test keys: RSA and Elgamal, with and without
-- password)
INSERT INTO res
SELECT 'pub ' || dir, name || ' ' || o || ' / payload ' || id, a, b FROM (
	SELECT 'go->c' AS dir, k.name, o, pl.id,
		pg_temp.try(format('SELECT encode(c.pgp_pub_decrypt_bytea(public.pgp_pub_encrypt_bytea(%L, dearmor(%L), %L), dearmor(%L), %L), ''hex'')', b, pubkey, o, seckey, 'parool')) AS a,
		encode(b, 'hex') AS b FROM pl, opt, keytbl k WHERE k.id IN (1, 2, 3, 5, 6, 7) AND pl.id % 7 = 1
	UNION ALL SELECT 'c->go', k.name, o, pl.id,
		pg_temp.try(format('SELECT encode(public.pgp_pub_decrypt_bytea(c.pgp_pub_encrypt_bytea(%L, dearmor(%L), %L), dearmor(%L), %L), ''hex'')', b, pubkey, o, seckey, 'parool')),
		encode(b, 'hex') FROM pl, opt, keytbl k WHERE k.id IN (1, 2, 3, 5, 6, 7) AND pl.id % 7 = 1
	UNION ALL SELECT 'text c->go', k.name, o, pl.id,
		pg_temp.try(format('SELECT public.pgp_pub_decrypt(c.pgp_pub_encrypt(%L, dearmor(%L), %L), dearmor(%L), %L, %L)', t, pubkey, o, seckey, 'parool', o)),
		pg_temp.try(format('SELECT c.pgp_pub_decrypt(c.pgp_pub_encrypt(%L, dearmor(%L), %L), dearmor(%L), %L, %L)', t, pubkey, o, seckey, 'parool', o))
		FROM pl, opt, keytbl k WHERE k.id IN (1, 3, 6) AND t IS NOT NULL AND length(t) < 100
) x;

-- the same inputs to both: upstream's messages and messages made by the
-- C version
CREATE TEMP TABLE msgs AS
SELECT 'enc' || id AS name, dearmor(data) AS m FROM encdata
UNION ALL SELECT 'sym' || pl.id || ' ' || o, c.pgp_sym_encrypt_bytea(b, 'pw', o) FROM pl, opt WHERE pl.id % 11 = 2
UNION ALL SELECT 'symtext' || pl.id || ' ' || o, c.pgp_sym_encrypt(t, 'pw', o) FROM pl, opt WHERE pl.id IN (3, 5)
UNION ALL SELECT 'pub' || pl.id || ' ' || k.name || ' ' || o, c.pgp_pub_encrypt_bytea(b, dearmor(pubkey), o)
	FROM pl, opt, keytbl k WHERE pl.id % 23 = 3 AND k.id IN (1, 3, 6);

INSERT INTO res
SELECT 'same input ' || f, name || ' ' || k, pg_temp.try(format(q, 'public', m, k)), pg_temp.try(format(q, 'c', m, k))
FROM msgs, (VALUES
	('sym_decrypt_bytea', 'SELECT encode(%s.pgp_sym_decrypt_bytea(%L, %L), ''hex'')'),
	('sym_decrypt', 'SELECT %s.pgp_sym_decrypt(%L, %L)'),
	('sym_decrypt crlf', 'SELECT %s.pgp_sym_decrypt(%L, %L, ''convert-crlf=1'')'),
	('key_id', 'SELECT %s.pgp_key_id(%L) || %L')) f(f, q),
	unnest(ARRAY['pw', 'wrong', '']) k;

INSERT INTO res
SELECT 'same input pub_decrypt', msgs.name || ' with ' || k.name || ' ' || p,
	pg_temp.try(format('SELECT encode(public.pgp_pub_decrypt_bytea(%L, dearmor(%L), %L), ''hex'')', m, seckey, p)),
	pg_temp.try(format('SELECT encode(c.pgp_pub_decrypt_bytea(%L, dearmor(%L), %L), ''hex'')', m, seckey, p))
FROM msgs, keytbl k, unnest(ARRAY['parool', 'foo']) p WHERE msgs.name NOT LIKE 'sym%';

INSERT INTO res
SELECT 'same input keys ' || f, k.name || ' ' || w,
	pg_temp.try(format(q, 'public', w, k.id)), pg_temp.try(format(q, 'c', w, k.id))
FROM keytbl k, unnest(ARRAY['pubkey', 'seckey']) w, (VALUES
	('key_id', 'SELECT %s.pgp_key_id(dearmor(%I)) FROM keytbl WHERE id = %s'),
	('encrypt with it', 'SELECT %s.pgp_pub_encrypt(''x'', dearmor(%I)) IS NOT NULL FROM keytbl WHERE id = %s'),
	('decrypt with it', 'SELECT %s.pgp_pub_decrypt(c.pgp_pub_encrypt(''x'', dearmor(pubkey)), dearmor(%I), ''parool'') FROM keytbl WHERE id = %s')) f(f, q);

-- corrupted messages: bits flipped in every byte, truncations
CREATE TEMP TABLE bad AS
SELECT name || ' flip ' || i AS name, set_byte(m, i, get_byte(m, i) # (1 << (i % 8))) AS m
FROM (SELECT * FROM msgs WHERE length(m) < 400 ORDER BY name LIMIT 30) x, generate_series(0, length(m) - 1) i
UNION ALL SELECT name || ' cut ' || i, substring(m FROM 1 FOR i)
FROM (SELECT * FROM msgs WHERE length(m) < 400 ORDER BY name LIMIT 30) x, generate_series(0, length(m) - 1) i;

INSERT INTO res
SELECT 'corrupt ' || f, name, pg_temp.try(format(q, 'public', m)), pg_temp.try(format(q, 'c', m))
FROM bad, (VALUES
	('sym_decrypt_bytea', 'SELECT encode(%s.pgp_sym_decrypt_bytea(%L, ''pw''), ''hex'')'),
	('sym_decrypt', 'SELECT %s.pgp_sym_decrypt(%L, ''pw'')'),
	('pub_decrypt', 'SELECT encode(%s.pgp_pub_decrypt_bytea(%L, (SELECT dearmor(seckey) FROM keytbl WHERE id = 1)), ''hex'')'),
	('key_id', 'SELECT %s.pgp_key_id(%L)')) f(f, q);

-- corrupted keys
INSERT INTO res
SELECT 'corrupt key ' || f, k.name || ' ' || i, pg_temp.try(format(q, 'public', km)), pg_temp.try(format(q, 'c', km))
FROM (SELECT name, i, set_byte(dearmor(seckey), i, get_byte(dearmor(seckey), i) # 85) AS km
	FROM keytbl, generate_series(0, length(dearmor(seckey)) - 1, 3) i WHERE id IN (1, 6, 7)) k, (VALUES
	('key_id', 'SELECT %s.pgp_key_id(%L)'),
	('pub_decrypt', 'SELECT %s.pgp_pub_decrypt((SELECT dearmor(data) FROM encdata WHERE id = 1), %L, ''parool'')')) f(f, q);

-- options: errors when encrypting, and what the other side sees
INSERT INTO res
SELECT 'options', o, pg_temp.try(format('SELECT c.pgp_sym_decrypt(public.pgp_sym_encrypt(''x'', ''pw'', %L), ''pw'')', o)),
	pg_temp.try(format('SELECT c.pgp_sym_decrypt(c.pgp_sym_encrypt(''x'', ''pw'', %L), ''pw'')', o))
FROM unnest(ARRAY['foo', 'foo=1', '=', ',', 'cipher-algo', 'cipher-algo=', 'cipher-algo=AES', 'CIPHER-ALGO=bf',
	'cipher-algo=aes,', 'cipher-algo=aes,,', ' cipher-algo = aes ', 'cipher-algo=aes compress-algo=1',
	'cipher-algo=twofish', 'cipher-algo=des', 's2k-mode=2', 's2k-mode=x', 's2k-count=1023', 's2k-count=65011713',
	's2k-mode=1, s2k-count=2000', 's2k-count=2000, s2k-mode=1', 's2k-count=99999999999', 's2k-digest-algo=sha224',
	's2k-digest-algo=SHA-1', 'compress-algo=3', 'compress-algo=4', 'compress-algo=-1', 'compress-level=-1',
	'compress-level=10', 'compress-algo=1, compress-level=0', 'disable-mdc=yes', 'disable-mdc=2', 'sess-key=1x',
	'convert-crlf=1', 'unicode-mode=1', 'debug=0', 'expect-cipher-algo=bf', 'ignore-cipher-failure=1',
	'cipher-algo=aes=1', E'cipher-algo=aes\t,\ncompress-algo=2', 'a=b=c', 'cipher-algo=aes;']) o;

-- armor
CREATE TEMP TABLE arm AS
SELECT 'armor ' || id AS name, public.armor(b) AS g, c.armor(b) AS cc FROM pl
UNION ALL SELECT 'armor hdr ' || id, public.armor(b, ARRAY['Version', 'Comment ' || id, ''], ARRAY['1', 'x: y', '']),
	c.armor(b, ARRAY['Version', 'Comment ' || id, ''], ARRAY['1', 'x: y', '']) FROM pl WHERE id % 3 = 0;
INSERT INTO res SELECT 'armor', name, g, cc FROM arm;

CREATE TEMP TABLE armin AS
SELECT name, cc AS a FROM arm
UNION ALL SELECT name || ' crlf', replace(cc, E'\n', E'\r\n') FROM arm WHERE name LIKE '%1'
UNION ALL SELECT 'flip ' || i, overlay(a PLACING chr(32 + (i * 37) % 95) FROM i FOR 1)
	FROM (SELECT cc AS a FROM arm WHERE name = 'armor hdr 3') x, generate_series(1, length(a)) i
UNION ALL SELECT 'cut ' || i, substring(a FROM 1 FOR i)
	FROM (SELECT cc AS a FROM arm WHERE name = 'armor hdr 3') x, generate_series(1, length(a), 3) i
UNION ALL SELECT 'case ' || n, a FROM (VALUES
	(1, E'-----BEGIN PGP MESSAGE-----\n\nem9va2E=\n=D5cR\n-----END PGP MESSAGE-----'),
	(2, E'junk\n-----BEGIN PGP MESSAGE-----\nA: b\nC: d\r\n\nem9va2E=\n=D5cR\n-----END PGP MESSAGE-----\njunk'),
	(3, E'-----BEGIN PGP MESSAGE-----\nA b\n\nem9va2E=\n=D5cR\n-----END PGP MESSAGE-----'),
	(4, E' -----BEGIN PGP MESSAGE-----\n\nem9va2E=\n=D5cR\n-----END PGP MESSAGE-----'),
	(5, E'-----BEGIN PGP MESSAGE-----\n\nem9va2E\n=D5cR\n-----END PGP MESSAGE-----'),
	(6, E'-----BEGIN PGP MESSAGE-----\n\nem9va2E=\n=D5c\n-----END PGP MESSAGE-----'),
	(7, E'-----BEGIN PGP MESSAGE-----\n\n=D5cR\n-----END PGP MESSAGE-----'),
	(8, E'-----BEGIN PGP MESSAGE-----\n\nem9va2E=\n-----END PGP MESSAGE-----'),
	(9, E'-----BEGIN PGP MESSAGE-----\n\nem9v\nYTE=\n=D5cR\n-----END PGP MESSAGE-----'),
	(10, E'-----BEGIN PGP MESSAGE-----\n\nem9va2E=\n=D5cR\n-----END PGP MESSAGE-----x'),
	(11, E'-----BEGIN FOO-----\n\nem9va2E=\n=D5cR\n-----END BAR-----'),
	(12, E'-----BEGIN PGP MESSAGE-----\nK: é\n\nem9va2E=\n=D5cR\n-----END PGP MESSAGE-----'),
	(13, E'-----BEGIN PGP MESSAGE-----\n\nem9va2E=\n=D5cR\n-----END PGP MESSAGE-----\n-----END PGP MESSAGE-----'),
	(14, E'-----BEGIN PGP MESSAGE-----\r\n\r\nem9va2E=\r\n=D5cR\r\n-----END PGP MESSAGE-----\r\n'),
	(15, E'-----BEGIN PGP MESSAGE-----\n\nem 9v\ta2E=\n= D5cR\n-----END PGP MESSAGE-----'),
	(16, E'-----BEGIN PGP MESSAGE-----\n\nem9va2E==\n=D5cR\n-----END PGP MESSAGE-----'),
	(17, E'-----BEGIN PGP MESSAGE-----\n\nem9va2=E\n=D5cR\n-----END PGP MESSAGE-----'),
	(18, E'-----BEGIN PGP MESSAGE-----\n\nem9va2E=\n=D5cR=\n-----END PGP MESSAGE-----')) v(n, a);
INSERT INTO res
SELECT 'dearmor', name, pg_temp.try(format('SELECT encode(public.dearmor(%L), ''hex'')', a)),
	pg_temp.try(format('SELECT encode(c.dearmor(%L), ''hex'')', a)) FROM armin
UNION ALL SELECT 'pgp_armor_headers', name,
	pg_temp.try(format('SELECT string_agg(key || ''='' || value, '';'') FROM public.pgp_armor_headers(%L)', a)),
	pg_temp.try(format('SELECT string_agg(key || ''='' || value, '';'') FROM c.pgp_armor_headers(%L)', a)) FROM armin;

INSERT INTO res
SELECT 'armor errors', q, pg_temp.try(format(q, 'public')), pg_temp.try(format(q, 'c')) FROM unnest(ARRAY[
	'SELECT %s.armor(''x'', ARRAY[''a''], ARRAY[''b'', ''c''])', 'SELECT %s.armor(''x'', ARRAY[]::text[], ARRAY[''b''])',
	'SELECT %s.armor(''x'', ARRAY[NULL], ARRAY[''b''])', 'SELECT %s.armor(''x'', ARRAY[''a''], ARRAY[NULL])',
	'SELECT %s.armor(''x'', ARRAY[''a: b''], ARRAY[''c''])', E'SELECT %s.armor(''x'', ARRAY[E''a\\nb''], ARRAY[''c''])',
	E'SELECT %s.armor(''x'', ARRAY[''a''], ARRAY[E''c\\nd''])', E'SELECT %s.armor(''x'', ARRAY[E''\\u00e9''], ARRAY[''c''])',
	E'SELECT %s.armor(''x'', ARRAY[''a''], ARRAY[E''\\u00e9''])', 'SELECT %s.armor(''x'', ARRAY[''a:b''], ARRAY['': c''])',
	'SELECT %s.armor(''x'', ''[2:2]={a}'', ARRAY[''b''])', 'SELECT %s.armor(''x'', ARRAY[''a'',''b''], ARRAY[''c'',''d''])']) q;

SELECT chk, count(*) AS checks, count(*) FILTER (WHERE a IS DISTINCT FROM b) AS mismatches
FROM res GROUP BY chk ORDER BY chk;
SELECT chk, detail, left(a, 120) AS go, left(b, 120) AS c FROM (
	SELECT *, row_number() OVER (PARTITION BY chk ORDER BY detail) AS rn FROM res WHERE a IS DISTINCT FROM b) x
WHERE rn <= 5 ORDER BY chk, detail;
