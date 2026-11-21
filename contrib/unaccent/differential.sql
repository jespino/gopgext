-- Differential test: compares this Go port with the original C unaccent on
-- generated strings. Needs the original library installed as
-- $libdir/unaccent_c.so and this port installed as unaccent; a UTF-8
-- database. Prints the number of mismatches (must be 0) and a few
-- examples.
--
--   psql -v ON_ERROR_STOP=1 -v n=100000 -f differential.sql

\if :{?n}
\else
\set n 20000
\endif

SET client_min_messages = warning;
DROP EXTENSION IF EXISTS unaccent CASCADE;
DROP SCHEMA IF EXISTS c CASCADE;
DROP TABLE IF EXISTS results;
CREATE EXTENSION unaccent;
CREATE SCHEMA c;
CREATE FUNCTION c.unaccent_init(internal) RETURNS internal
	AS '$libdir/unaccent_c', 'unaccent_init' LANGUAGE C;
CREATE FUNCTION c.unaccent_lexize(internal, internal, internal, internal) RETURNS internal
	AS '$libdir/unaccent_c', 'unaccent_lexize' LANGUAGE C;
CREATE TEXT SEARCH TEMPLATE c.unaccent (INIT = c.unaccent_init, LEXIZE = c.unaccent_lexize);
CREATE TEXT SEARCH DICTIONARY c.unaccent (TEMPLATE = c.unaccent, RULES = 'unaccent');

-- Characters the rules map (Latin-1, Latin Extended, combining marks,
-- letterlike symbols, punctuation...), and others.
CREATE TEMP TABLE alphabet AS
	SELECT chr(c) AS ch FROM generate_series(32, 126) c
	UNION ALL SELECT chr(c) FROM generate_series(160, 879) c
	UNION ALL SELECT chr(c) FROM generate_series(1024, 1119) c
	UNION ALL SELECT chr(c) FROM generate_series(7680, 7935) c
	UNION ALL SELECT chr(c) FROM generate_series(8192, 8591) c
	UNION ALL SELECT chr(c) FROM generate_series(12288, 12351) c
	UNION ALL SELECT chr(c) FROM generate_series(64256, 64262) c;
CREATE TEMP TABLE corpus AS
	SELECT i, (SELECT string_agg(ch, '') FROM (
		SELECT ch FROM alphabet ORDER BY random() + i * 0 LIMIT 1 + (random() * 6)::int) s) AS w
	FROM generate_series(1, :n) i;
-- Every single character too.
INSERT INTO corpus SELECT 0, ch FROM alphabet;

CREATE TABLE results AS
	SELECT w, ts_lexize('unaccent', w) AS go, ts_lexize('c.unaccent', w) AS c,
		unaccent(w) AS go_f, unaccent('c.unaccent', w) AS c_f
	FROM corpus;

SELECT count(*) AS strings,
	count(*) FILTER (WHERE go IS DISTINCT FROM c) AS lexize_mismatches,
	count(*) FILTER (WHERE go_f IS DISTINCT FROM c_f) AS unaccent_mismatches,
	count(*) FILTER (WHERE c IS NOT NULL) AS changed
FROM results;
SELECT w, go, c FROM results WHERE go IS DISTINCT FROM c OR go_f IS DISTINCT FROM c_f LIMIT 10;
