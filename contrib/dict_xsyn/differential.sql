-- Differential test: compares this Go port with the original C dict_xsyn
-- with every combination of options, on the words of xsyn_sample.rules and
-- others. Needs the original library installed as $libdir/dict_xsyn_c.so
-- and this port installed as dict_xsyn. Prints the number of mismatches
-- (must be 0) and a few examples.
--
--   psql -v ON_ERROR_STOP=1 -f differential.sql

SET client_min_messages = warning;
DROP EXTENSION IF EXISTS dict_xsyn CASCADE;
DROP SCHEMA IF EXISTS c CASCADE;
CREATE EXTENSION dict_xsyn;
CREATE SCHEMA c;
CREATE FUNCTION c.dxsyn_init(internal) RETURNS internal
	AS '$libdir/dict_xsyn_c', 'dxsyn_init' LANGUAGE C STRICT;
CREATE FUNCTION c.dxsyn_lexize(internal, internal, internal, internal) RETURNS internal
	AS '$libdir/dict_xsyn_c', 'dxsyn_lexize' LANGUAGE C STRICT;
CREATE TEXT SEARCH TEMPLATE c.xsyn_template (INIT = c.dxsyn_init, LEXIZE = c.dxsyn_lexize);

CREATE TEMP TABLE opts AS
	SELECT k, format('matchorig = %s, keeporig = %s, matchsynonyms = %s, keepsynonyms = %s',
		(k & 1 = 1)::text, (k & 2 = 2)::text, (k & 4 = 4)::text, (k & 8 = 8)::text) AS o
	FROM generate_series(0, 15) k;
DO $$
DECLARE
	r record;
BEGIN
	FOR r IN SELECT * FROM opts LOOP
		EXECUTE format('CREATE TEXT SEARCH DICTIONARY xsyn%s (TEMPLATE = xsyn_template, RULES = xsyn_sample, %s)', r.k, r.o);
		EXECUTE format('CREATE TEXT SEARCH DICTIONARY c.d%s (TEMPLATE = c.xsyn_template, RULES = xsyn_sample, %s)', r.k, r.o);
	END LOOP;
END $$;

CREATE TEMP TABLE corpus (w text);
INSERT INTO corpus VALUES ('supernova'), ('SuperNova'), ('sn'), ('SNE'), ('sne'), ('1987a'), ('1987A'),
	('grb'), (''), ('#'), ('word'), ('super'), ('nova'), ('sn sne'), ('supernovae');

CREATE TEMP TABLE results AS
	SELECT k, w, ts_lexize(('xsyn' || k)::regdictionary, w) AS go, ts_lexize(('c.d' || k)::regdictionary, w) AS c
	FROM corpus, opts;
SELECT count(*) AS lexizes, count(*) FILTER (WHERE go IS DISTINCT FROM c) AS mismatches,
	count(*) FILTER (WHERE c IS NOT NULL) AS found FROM results;
SELECT * FROM results WHERE go IS DISTINCT FROM c LIMIT 10;

-- Option errors.
DO $$
DECLARE
	o text;
	ge text;
	ce text;
BEGIN
	FOREACH o IN ARRAY ARRAY['rules = nosuchfile', 'rules = ''../x''', 'keeporig = maybe', 'foo = 1',
			'rules', 'matchorig', 'keeporig = 0'] LOOP
		BEGIN
			EXECUTE format('CREATE TEXT SEARCH DICTIONARY ge (TEMPLATE = xsyn_template, %s)', o);
			ge := 'ok';
			DROP TEXT SEARCH DICTIONARY ge;
		EXCEPTION WHEN OTHERS THEN
			ge := SQLSTATE || ' ' || replace(SQLERRM, '_c', '');
		END;
		BEGIN
			EXECUTE format('CREATE TEXT SEARCH DICTIONARY c.ce (TEMPLATE = c.xsyn_template, %s)', o);
			ce := 'ok';
			DROP TEXT SEARCH DICTIONARY c.ce;
		EXCEPTION WHEN OTHERS THEN
			ce := SQLSTATE || ' ' || replace(SQLERRM, '_c', '');
		END;
		IF ge IS DISTINCT FROM ce THEN
			RAISE WARNING 'option %: go % / c %', o, ge, ce;
		END IF;
	END LOOP;
END $$;
