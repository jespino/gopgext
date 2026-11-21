-- Differential test: compares this Go port with the original C isn on a
-- generated corpus. Needs the original library installed as
-- $libdir/cisn.so, built with its GUC renamed to cisn.weak (both libraries
-- are loaded in one backend, and a setting can only be defined once):
--
--   sed -i 's/"isn\.weak"/"cisn.weak"/g; s/MarkGUCPrefixReserved("isn")/MarkGUCPrefixReserved("cisn")/' isn.c
--
-- and this port installed as isn. Prints the number of mismatches per
-- function and mode (all must be 0) and a few examples of each.
--
--   psql -v ON_ERROR_STOP=1 -v n=20000 -f differential.sql

\if :{?n}
\else
\set n 5000
\endif

SET client_min_messages = warning;
DROP SCHEMA IF EXISTS g CASCADE;
DROP SCHEMA IF EXISTS c CASCADE;
DROP EXTENSION IF EXISTS isn;
CREATE EXTENSION isn;
CREATE SCHEMA g;
CREATE SCHEMA c;

-- Both implementations, through plain int8/cstring signatures.
DO $$
DECLARE
	lib text;
	fn text;
BEGIN
	FOREACH lib IN ARRAY ARRAY['g:isn', 'c:cisn'] LOOP
		FOREACH fn IN ARRAY ARRAY['ean13_in', 'isbn_in', 'ismn_in', 'issn_in', 'upc_in'] LOOP
			EXECUTE format('CREATE FUNCTION %s.%s(cstring) RETURNS int8 AS %L, %L LANGUAGE C STRICT',
				split_part(lib, ':', 1), fn, '$libdir/' || split_part(lib, ':', 2), fn);
		END LOOP;
		FOREACH fn IN ARRAY ARRAY['ean13_out', 'isn_out'] LOOP
			EXECUTE format('CREATE FUNCTION %s.%s(int8) RETURNS cstring AS %L, %L LANGUAGE C STRICT',
				split_part(lib, ':', 1), fn, '$libdir/' || split_part(lib, ':', 2), fn);
		END LOOP;
		FOREACH fn IN ARRAY ARRAY['isbn_cast_from_ean13', 'ismn_cast_from_ean13', 'issn_cast_from_ean13',
				'upc_cast_from_ean13', 'make_valid'] LOOP
			EXECUTE format('CREATE FUNCTION %s.%s(int8) RETURNS int8 AS %L, %L LANGUAGE C STRICT',
				split_part(lib, ':', 1), fn, '$libdir/' || split_part(lib, ':', 2), fn);
		END LOOP;
		EXECUTE format('CREATE FUNCTION %s.is_valid(int8) RETURNS bool AS %L, %L LANGUAGE C STRICT',
			split_part(lib, ':', 1), '$libdir/' || split_part(lib, ':', 2), 'is_valid');
	END LOOP;
END $$;

-- Result of fn(arg), or its error.
CREATE FUNCTION pg_temp.try(fn text, arg text, argtype text) RETURNS text AS $$
DECLARE
	r text;
BEGIN
	EXECUTE format('SELECT %s(%L::%s)::text', fn, arg, argtype) INTO r;
	RETURN coalesce(r, 'NULL');
EXCEPTION WHEN OTHERS THEN
	RETURN 'ERROR ' || SQLSTATE || ': ' || SQLERRM;
END $$ LANGUAGE plpgsql;

CREATE FUNCTION pg_temp.digits(n int) RETURNS text AS $$
	SELECT coalesce(string_agg(floor(random() * 10)::int::text, ''), '') FROM generate_series(1, n)
$$ LANGUAGE sql VOLATILE;

-- Inserts '-' or ' ' at random positions.
CREATE FUNCTION pg_temp.hyphens(s text) RETURNS text AS $$
	SELECT string_agg(ch || CASE WHEN random() < 0.15 THEN (ARRAY['-', ' '])[1 + floor(random() * 2)::int] ELSE '' END,
		'' ORDER BY i)
	FROM regexp_split_to_table(s, '') WITH ORDINALITY AS t(ch, i)
$$ LANGUAGE sql VOLATILE;

SELECT setseed(0.31337);
CREATE TEMP TABLE inputs AS
SELECT i, CASE i % 10
	-- EAN13-style: known prefixes, 13 digits (mostly bad check digits)
	WHEN 0 THEN (ARRAY['978', '979', '9790', '977', '0', '1', '5', '9'])[1 + floor(random() * 8)::int] || pg_temp.digits(13)
	-- correct check digit computed by the "?" magic, then re-entered
	WHEN 1 THEN (ARRAY['978', '979', '9790', '977', '0', '4', '8'])[1 + floor(random() * 7)::int] || pg_temp.digits(12) || '?'
	-- ISBN-10 / ISSN-8 / ISMN / UPC-12 forms with any check character
	WHEN 2 THEN pg_temp.digits(9) || (ARRAY['0', '5', '9', 'X', 'x', '?'])[1 + floor(random() * 6)::int]
	WHEN 3 THEN pg_temp.digits(7) || (ARRAY['0', '3', 'X', 'x', '?'])[1 + floor(random() * 5)::int]
	WHEN 4 THEN (ARRAY['M', 'm'])[1 + floor(random() * 2)::int] || pg_temp.digits(8) || (ARRAY['1', '?', 'X'])[1 + floor(random() * 3)::int]
	WHEN 5 THEN pg_temp.digits(11) || (ARRAY['0', '7', '?'])[1 + floor(random() * 3)::int]
	-- hyphenated, with '!' (invalid check digit marker)
	WHEN 6 THEN pg_temp.hyphens((ARRAY['978', '979', '977', '0'])[1 + floor(random() * 4)::int] || pg_temp.digits(10))
		|| (ARRAY['', '!', '?'])[1 + floor(random() * 3)::int]
	WHEN 7 THEN pg_temp.hyphens(pg_temp.digits(8 + floor(random() * 7)::int)) || (ARRAY['', '!'])[1 + floor(random() * 2)::int]
	-- odd lengths and garbage
	WHEN 8 THEN pg_temp.digits(floor(random() * 20)::int)
	ELSE substr(md5(i::text), 1, floor(random() * 16)::int) || (ARRAY['', '!', '?', 'X', ' '])[1 + floor(random() * 5)::int]
	END AS s
FROM generate_series(1, :n) i;

CREATE TEMP TABLE mismatches (fn text, input text, go text, c text);
CREATE TEMP TABLE parsed (v int8);

-- Input functions, in strict and weak mode.
DO $$
DECLARE
	mode text;
	fn text;
BEGIN
	FOREACH mode IN ARRAY ARRAY['off', 'on'] LOOP
		PERFORM set_config('isn.weak', mode, false), set_config('cisn.weak', mode, false);
		FOREACH fn IN ARRAY ARRAY['ean13_in', 'isbn_in', 'ismn_in', 'issn_in', 'upc_in'] LOOP
			EXECUTE format($q$
				INSERT INTO mismatches
				SELECT %L, s, go, cr FROM (
					SELECT s, pg_temp.try('g.%s', s, 'cstring') AS go, pg_temp.try('c.%s', s, 'cstring') AS cr
					FROM inputs) x
				WHERE go IS DISTINCT FROM cr$q$, fn || ' (weak ' || mode || ')', fn, fn);
			EXECUTE format($q$
				INSERT INTO parsed SELECT r::int8 FROM (SELECT pg_temp.try('c.%s', s, 'cstring') AS r FROM inputs) x
				WHERE r !~ '^ERROR'$q$, fn);
		END LOOP;
	END LOOP;
	PERFORM set_config('isn.weak', 'off', false), set_config('cisn.weak', 'off', false);
END $$;

-- Raw values too: random 13-digit numbers, both flag states, and out of range.
INSERT INTO parsed
SELECT (floor(random() * 1e13)::int8 << 1) | (i % 2) FROM generate_series(1, :n / 2) i;
INSERT INTO parsed VALUES (0), (1), (19999999999998), (19999999999999), (20000000000000), (9223372036854775807), (-1), (-2);

-- Output functions, casts and flag functions on all those values.
DO $$
DECLARE
	fn text;
BEGIN
	FOREACH fn IN ARRAY ARRAY['ean13_out', 'isn_out', 'isbn_cast_from_ean13', 'ismn_cast_from_ean13',
			'issn_cast_from_ean13', 'upc_cast_from_ean13', 'is_valid', 'make_valid'] LOOP
		EXECUTE format($q$
			INSERT INTO mismatches
			SELECT %L, v::text, go, cr FROM (
				SELECT v, pg_temp.try('g.%s', v::text, 'int8') AS go, pg_temp.try('c.%s', v::text, 'int8') AS cr
				FROM (SELECT DISTINCT v FROM parsed) p) x
			WHERE go IS DISTINCT FROM cr$q$, fn, fn, fn);
	END LOOP;
END $$;

-- Coverage: how varied the compared results were.
SELECT (SELECT count(*) FROM inputs) AS inputs,
	(SELECT count(DISTINCT v) FROM parsed) AS distinct_values,
	(SELECT count(*) FROM inputs WHERE pg_temp.try('g.ean13_in', s, 'cstring') !~ '^ERROR') AS valid_ean13,
	(SELECT count(DISTINCT pg_temp.try('g.isbn_in', s, 'cstring')) FROM inputs WHERE pg_temp.try('g.isbn_in', s, 'cstring') ~ '^ERROR') AS distinct_isbn_errors,
	(SELECT count(DISTINCT pg_temp.try('g.isn_out', v::text, 'int8')) FROM parsed) AS distinct_short_outputs;

SELECT f.fn, count(m.fn) AS mismatches
FROM (SELECT DISTINCT fn FROM (
	SELECT unnest(ARRAY['ean13_in', 'isbn_in', 'ismn_in', 'issn_in', 'upc_in']) || ' (weak ' || w || ')' AS fn
	FROM unnest(ARRAY['off', 'on']) w
	UNION ALL
	SELECT unnest(ARRAY['ean13_out', 'isn_out', 'isbn_cast_from_ean13', 'ismn_cast_from_ean13',
		'issn_cast_from_ean13', 'upc_cast_from_ean13', 'is_valid', 'make_valid'])) a) f
LEFT JOIN mismatches m ON m.fn = f.fn GROUP BY f.fn ORDER BY f.fn;

SELECT fn, input, go, c FROM (
	SELECT *, row_number() OVER (PARTITION BY fn) AS rn FROM mismatches) x
WHERE rn <= 3;
