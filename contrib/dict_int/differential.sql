-- Differential test: compares this Go port with the original C dict_int on
-- generated words and options. Needs the original library installed as
-- $libdir/dict_int_c.so and this port installed as dict_int. Prints the
-- number of mismatches (must be 0) and a few examples.
--
--   psql -v ON_ERROR_STOP=1 -v n=100000 -f differential.sql

\if :{?n}
\else
\set n 20000
\endif

SET client_min_messages = warning;
DROP EXTENSION IF EXISTS dict_int CASCADE;
DROP SCHEMA IF EXISTS c CASCADE;
CREATE EXTENSION dict_int;
CREATE SCHEMA c;
CREATE FUNCTION c.dintdict_init(internal) RETURNS internal
	AS '$libdir/dict_int_c', 'dintdict_init' LANGUAGE C STRICT;
CREATE FUNCTION c.dintdict_lexize(internal, internal, internal, internal) RETURNS internal
	AS '$libdir/dict_int_c', 'dintdict_lexize' LANGUAGE C STRICT;
CREATE TEXT SEARCH TEMPLATE c.intdict_template (INIT = c.dintdict_init, LEXIZE = c.dintdict_lexize);

-- A pair of dictionaries per set of options.
CREATE TEMP TABLE opts (k int, o text);
INSERT INTO opts VALUES (1, ''), (2, 'maxlen = 3'), (3, 'maxlen = 1, absval = true'),
	(4, 'rejectlong = 1, maxlen = 10'), (5, 'absval = on, rejectlong = true'),
	(6, 'maxlen = ''4.9'', absval = off'), (7, 'maxlen = 99999999999');
DO $$
DECLARE
	r record;
BEGIN
	FOR r IN SELECT * FROM opts LOOP
		EXECUTE format('CREATE TEXT SEARCH DICTIONARY intdict%s (TEMPLATE = intdict_template%s)', r.k,
			CASE WHEN r.o <> '' THEN ', ' || r.o ELSE '' END);
		EXECUTE format('CREATE TEXT SEARCH DICTIONARY c.d%s (TEMPLATE = c.intdict_template%s)', r.k,
			CASE WHEN r.o <> '' THEN ', ' || r.o ELSE '' END);
	END LOOP;
END $$;

CREATE TEMP TABLE corpus AS
	SELECT (ARRAY['', '+', '-', ' ', 'x'])[1 + (random() * 4)::int] ||
		substr(md5(random()::text) || (random() * 1e15)::bigint, 1, (random() * 20)::int) AS w
	FROM generate_series(1, :n);
INSERT INTO corpus VALUES (''), ('+'), ('-'), ('-1'), ('+12345678901234');

CREATE TEMP TABLE results AS
	SELECT k, w, ts_lexize(('intdict' || k)::regdictionary, w) AS go, ts_lexize(('c.d' || k)::regdictionary, w) AS c
	FROM corpus, opts;
SELECT count(*) AS lexizes, count(*) FILTER (WHERE go IS DISTINCT FROM c) AS mismatches FROM results;
SELECT * FROM results WHERE go IS DISTINCT FROM c LIMIT 10;

-- Option errors.
DO $$
DECLARE
	o text;
	ge text;
	ce text;
BEGIN
	FOREACH o IN ARRAY ARRAY['maxlen = 0', 'maxlen = -5', 'absval = 2', 'absval = yes', 'foo = 1',
			'rejectlong', 'maxlen = abc', 'absval'] LOOP
		BEGIN
			EXECUTE format('CREATE TEXT SEARCH DICTIONARY ge (TEMPLATE = intdict_template, %s)', o);
			ge := 'ok';
			DROP TEXT SEARCH DICTIONARY ge;
		EXCEPTION WHEN OTHERS THEN
			ge := SQLSTATE || ' ' || SQLERRM;
		END;
		BEGIN
			EXECUTE format('CREATE TEXT SEARCH DICTIONARY c.ce (TEMPLATE = c.intdict_template, %s)', o);
			ce := 'ok';
			DROP TEXT SEARCH DICTIONARY c.ce;
		EXCEPTION WHEN OTHERS THEN
			ce := SQLSTATE || ' ' || SQLERRM;
		END;
		IF ge IS DISTINCT FROM ce THEN
			RAISE WARNING 'option %: go % / c %', o, ge, ce;
		END IF;
	END LOOP;
END $$;
