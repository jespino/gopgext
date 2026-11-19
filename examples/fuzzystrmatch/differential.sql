-- Differential test: compares every function of this Go port with the
-- original C fuzzystrmatch on a generated corpus. Needs the original
-- library installed as $libdir/cfuzzy.so (build contrib/fuzzystrmatch with
-- PGXS and copy fuzzystrmatch.so to `pg_config --pkglibdir`/cfuzzy.so), and
-- this port installed as fuzzystrmatch. Run in a UTF-8 database:
--
--   psql -v ON_ERROR_STOP=1 -v n=100000 -f differential.sql
--
-- Prints the number of mismatches per function (all must be 0) and a few
-- examples of each.

\if :{?n}
\else
\set n 20000
\endif

SET client_min_messages = warning;
DROP SCHEMA IF EXISTS c CASCADE;
DROP EXTENSION IF EXISTS fuzzystrmatch;
CREATE EXTENSION fuzzystrmatch;
CREATE SCHEMA c;
CREATE FUNCTION c.levenshtein(text, text) RETURNS int AS '$libdir/cfuzzy', 'levenshtein' LANGUAGE C IMMUTABLE STRICT;
CREATE FUNCTION c.levenshtein(text, text, int, int, int) RETURNS int AS '$libdir/cfuzzy', 'levenshtein_with_costs' LANGUAGE C IMMUTABLE STRICT;
CREATE FUNCTION c.levenshtein_less_equal(text, text, int) RETURNS int AS '$libdir/cfuzzy', 'levenshtein_less_equal' LANGUAGE C IMMUTABLE STRICT;
CREATE FUNCTION c.levenshtein_less_equal(text, text, int, int, int, int) RETURNS int AS '$libdir/cfuzzy', 'levenshtein_less_equal_with_costs' LANGUAGE C IMMUTABLE STRICT;
CREATE FUNCTION c.metaphone(text, int) RETURNS text AS '$libdir/cfuzzy', 'metaphone' LANGUAGE C IMMUTABLE STRICT;
CREATE FUNCTION c.soundex(text) RETURNS text AS '$libdir/cfuzzy', 'soundex' LANGUAGE C IMMUTABLE STRICT;
CREATE FUNCTION c.text_soundex(text) RETURNS text AS '$libdir/cfuzzy', 'soundex' LANGUAGE C IMMUTABLE STRICT;
CREATE FUNCTION c.difference(text, text) RETURNS int AS '$libdir/cfuzzy', 'difference' LANGUAGE C IMMUTABLE STRICT;
CREATE FUNCTION c.dmetaphone(text) RETURNS text AS '$libdir/cfuzzy', 'dmetaphone' LANGUAGE C IMMUTABLE STRICT;
CREATE FUNCTION c.dmetaphone_alt(text) RETURNS text AS '$libdir/cfuzzy', 'dmetaphone_alt' LANGUAGE C IMMUTABLE STRICT;
CREATE FUNCTION c.daitch_mokotoff(text) RETURNS text[] AS '$libdir/cfuzzy', 'daitch_mokotoff' LANGUAGE C IMMUTABLE STRICT;

-- Random strings over an alphabet.
CREATE FUNCTION pg_temp.rnd(alpha text, maxlen int) RETURNS text AS $$
	SELECT coalesce(string_agg(substr(alpha, 1 + floor(random() * length(alpha))::int, 1), ''), '')
	FROM generate_series(1, floor(random() * (maxlen + 1))::int)
$$ LANGUAGE sql VOLATILE;

-- Name-like strings made of the letter groups the phonetic rules look for.
CREATE FUNCTION pg_temp.name() RETURNS text AS $$
	SELECT coalesce(string_agg(CASE WHEN random() < 0.4 THEN upper(s) ELSE s END, ''), '')
	FROM (SELECT (ARRAY['a', 'e', 'i', 'o', 'u', 'y', 'b', 'c', 'd', 'f', 'g', 'h', 'j', 'k', 'l', 'm', 'n',
		'p', 'q', 'r', 's', 't', 'v', 'w', 'x', 'z', 'sch', 'ch', 'cz', 'tz', 'gh', 'ph', 'th', 'wr', 'kn',
		'gn', 'ps', 'mc', 'mac ', 'van ', 'von ', 'san ', 'jose', 'tion', 'tia', 'tch', 'dg', 'dt', 'cc',
		'cia', 'ck', 'll', 'illo', 'alle', 'ewski', 'owsky', 'wicz', 'witz', 'sugar', 'caesar', 'chia',
		'chae', 'harac', 'chore', 'ough', 'augh', 'ier', 'aggi', 'gier', 'ger', 'gy', 'danger', 'umb',
		'isl', 'sh', 'heim', 'sio', 'sian', 'sz', 'sk', 'oo', 'er', 'en', 'ai', 'oi', 'eau', 'zh', 'zz',
		'zi', 'bb', 'ff', 'jj', 'kk', 'nn', 'pp', 'qq', 'rr', 'ss', 'tt', 'vv', 'xx', 'ç', 'ñ', 'ü', 'ö',
		'ä', 'ß', 'é', 'ą', 'ę', 'ţ', 'ț', 'rz', 'ts', 'ks', 'chs', 'dz', 'drz', 'ia', 'ie', 'io', 'iu',
		'aj', 'ej', 'oj', 'uj', 'ay', 'ey', 'oy', 'uy', 'au', 'eu', 'ei', 'mn', 'nm', 'fb', 'ks', 'ii',
		' ', '''', '-', '.'])[1 + floor(random() * 142)::int] AS s
		FROM generate_series(1, 1 + floor(random() * 7)::int)) x
$$ LANGUAGE sql VOLATILE;

SELECT setseed(0.4242);
CREATE TEMP TABLE corpus AS
	SELECT i,
		CASE WHEN i % 2 = 0 THEN pg_temp.name()
			ELSE pg_temp.rnd('abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ    ''-.,0123äöüßçñéèàáíóúĄąĘęŢţȚțÇÑÉ€日', 24)
		END AS a
	FROM generate_series(1, :n) i;
CREATE TEMP TABLE pairs AS
	SELECT c1.i, c1.a, c2.a AS b,
		floor(random() * 5)::int AS ic, floor(random() * 5)::int AS dc, floor(random() * 5)::int AS sc,
		floor(random() * 14)::int - 2 AS maxd, 1 + floor(random() * 12)::int AS mlen
	FROM corpus c1 JOIN corpus c2 ON c2.i = (c1.i * 7919) % :n + 1;

CREATE TEMP TABLE mismatches (fn text, input text, go text, c text);

INSERT INTO mismatches SELECT 'levenshtein', a || ' | ' || b, public.levenshtein(a, b)::text, c.levenshtein(a, b)::text
	FROM pairs WHERE public.levenshtein(a, b) IS DISTINCT FROM c.levenshtein(a, b);
INSERT INTO mismatches SELECT 'levenshtein costs', format('%s | %s | %s %s %s', a, b, ic, dc, sc),
	public.levenshtein(a, b, ic, dc, sc)::text, c.levenshtein(a, b, ic, dc, sc)::text
	FROM pairs WHERE public.levenshtein(a, b, ic, dc, sc) IS DISTINCT FROM c.levenshtein(a, b, ic, dc, sc);
INSERT INTO mismatches SELECT 'levenshtein_less_equal', format('%s | %s | %s', a, b, maxd),
	public.levenshtein_less_equal(a, b, maxd)::text, c.levenshtein_less_equal(a, b, maxd)::text
	FROM pairs WHERE public.levenshtein_less_equal(a, b, maxd) IS DISTINCT FROM c.levenshtein_less_equal(a, b, maxd);
INSERT INTO mismatches SELECT 'levenshtein_less_equal costs', format('%s | %s | %s %s %s %s', a, b, ic, dc, sc, maxd),
	public.levenshtein_less_equal(a, b, ic, dc, sc, maxd)::text, c.levenshtein_less_equal(a, b, ic, dc, sc, maxd)::text
	FROM pairs WHERE public.levenshtein_less_equal(a, b, ic, dc, sc, maxd) IS DISTINCT FROM c.levenshtein_less_equal(a, b, ic, dc, sc, maxd);
INSERT INTO mismatches SELECT 'metaphone', a || ' | ' || mlen, public.metaphone(a, mlen), c.metaphone(a, mlen)
	FROM pairs WHERE a <> '' AND public.metaphone(a, mlen) IS DISTINCT FROM c.metaphone(a, mlen);
INSERT INTO mismatches SELECT 'soundex', a, public.soundex(a), c.soundex(a)
	FROM corpus WHERE public.soundex(a) IS DISTINCT FROM c.soundex(a);
INSERT INTO mismatches SELECT 'text_soundex', a, public.text_soundex(a), c.text_soundex(a)
	FROM corpus WHERE public.text_soundex(a) IS DISTINCT FROM c.text_soundex(a);
INSERT INTO mismatches SELECT 'difference', a || ' | ' || b, public.difference(a, b)::text, c.difference(a, b)::text
	FROM pairs WHERE public.difference(a, b) IS DISTINCT FROM c.difference(a, b);
INSERT INTO mismatches SELECT 'dmetaphone', a, public.dmetaphone(a), c.dmetaphone(a)
	FROM corpus WHERE public.dmetaphone(a) IS DISTINCT FROM c.dmetaphone(a);
INSERT INTO mismatches SELECT 'dmetaphone_alt', a, public.dmetaphone_alt(a), c.dmetaphone_alt(a)
	FROM corpus WHERE public.dmetaphone_alt(a) IS DISTINCT FROM c.dmetaphone_alt(a);
INSERT INTO mismatches SELECT 'daitch_mokotoff', a, public.daitch_mokotoff(a)::text, c.daitch_mokotoff(a)::text
	FROM corpus WHERE public.daitch_mokotoff(a) IS DISTINCT FROM c.daitch_mokotoff(a);

-- Coverage of the comparison: how many distinct results each side produced.
SELECT count(*) AS strings, count(DISTINCT public.dmetaphone(a)) AS dmetaphone_codes,
	count(DISTINCT public.daitch_mokotoff(a)) AS dm_code_sets,
	count(*) FILTER (WHERE public.daitch_mokotoff(a) IS NULL) AS dm_nulls,
	count(DISTINCT public.metaphone(a, 8)) FILTER (WHERE a <> '') AS metaphone_codes
FROM corpus;

SELECT f.fn, count(m.fn) AS mismatches
FROM unnest(ARRAY['levenshtein', 'levenshtein costs', 'levenshtein_less_equal', 'levenshtein_less_equal costs',
	'metaphone', 'soundex', 'text_soundex', 'difference', 'dmetaphone', 'dmetaphone_alt', 'daitch_mokotoff']) f(fn)
LEFT JOIN mismatches m ON m.fn = f.fn GROUP BY f.fn ORDER BY f.fn;

SELECT fn, input, go, c FROM (
	SELECT *, row_number() OVER (PARTITION BY fn) AS rn FROM mismatches) x
WHERE rn <= 3;
