-- Differential test: compares this Go port with the original C hstore on
-- generated inputs. Needs the original library installed as
-- $libdir/hstore_c.so:
--
--   cp $(pg_config --pkglibdir)/hstore.so $(pg_config --pkglibdir)/hstore_c.so
--
-- and this port installed as hstore (its library is $libdir/hstore). Both
-- libraries' functions are declared here on bytea (an hstore's varlena), so
-- the binary representations themselves are compared:
--
--   1. hstore_in on random text (token soup, mostly invalid, and generated
--      valid input with quoting, escapes, NULLs, duplicates, non-ASCII):
--      the same bytes, or the same error message
--   2. on every value parsed: hstore_out, hstore_send, the JSON
--      conversions, hash functions, akeys/avals/matrix, fetchval
--   3. on pairs of values: ||, delete, @>, comparison; hstore(text[]) and
--      hstore(text[], text[]) on the values' arrays
--
--   psql -v ON_ERROR_STOP=1 [-v n=20000] -f differential.sql
--
-- It prints the number of mismatches per check (all must be 0) and a few
-- examples of each. The C library of PostgreSQL 15 and older has other
-- syntax error messages (XX000 "Syntax error near ..."): there only the
-- error messages differ.

\if :{?n}
\else
\set n 20000
\endif

SET client_min_messages = warning;
CREATE EXTENSION IF NOT EXISTS hstore;
DROP SCHEMA IF EXISTS c CASCADE;
DROP SCHEMA IF EXISTS g CASCADE;
CREATE SCHEMA c;
CREATE SCHEMA g;

-- The same functions from both libraries: c (C original), g (Go port).
DO $$
DECLARE
	lib text;
	s text;
BEGIN
	FOREACH s IN ARRAY ARRAY['c', 'g'] LOOP
		lib := CASE s WHEN 'c' THEN '$libdir/hstore_c' ELSE '$libdir/hstore' END;
		EXECUTE format($f$
			CREATE FUNCTION %1$I.hin(cstring) RETURNS bytea AS %2$L, 'hstore_in' LANGUAGE C STRICT;
			CREATE FUNCTION %1$I.hout(bytea) RETURNS cstring AS %2$L, 'hstore_out' LANGUAGE C STRICT;
			CREATE FUNCTION %1$I.hsend(bytea) RETURNS bytea AS %2$L, 'hstore_send' LANGUAGE C STRICT;
			CREATE FUNCTION %1$I.json(bytea) RETURNS json AS %2$L, 'hstore_to_json' LANGUAGE C STRICT;
			CREATE FUNCTION %1$I.json_loose(bytea) RETURNS json AS %2$L, 'hstore_to_json_loose' LANGUAGE C STRICT;
			CREATE FUNCTION %1$I.jsonb(bytea) RETURNS jsonb AS %2$L, 'hstore_to_jsonb' LANGUAGE C STRICT;
			CREATE FUNCTION %1$I.jsonb_loose(bytea) RETURNS jsonb AS %2$L, 'hstore_to_jsonb_loose' LANGUAGE C STRICT;
			CREATE FUNCTION %1$I.hash(bytea) RETURNS int AS %2$L, 'hstore_hash' LANGUAGE C STRICT;
			CREATE FUNCTION %1$I.hash_ext(bytea, int8) RETURNS int8 AS %2$L, 'hstore_hash_extended' LANGUAGE C STRICT;
			CREATE FUNCTION %1$I.akeys(bytea) RETURNS text[] AS %2$L, 'hstore_akeys' LANGUAGE C STRICT;
			CREATE FUNCTION %1$I.avals(bytea) RETURNS text[] AS %2$L, 'hstore_avals' LANGUAGE C STRICT;
			CREATE FUNCTION %1$I.matrix(bytea) RETURNS text[] AS %2$L, 'hstore_to_matrix' LANGUAGE C STRICT;
			CREATE FUNCTION %1$I.fetchval(bytea, text) RETURNS text AS %2$L, 'hstore_fetchval' LANGUAGE C STRICT;
			CREATE FUNCTION %1$I.concat(bytea, bytea) RETURNS bytea AS %2$L, 'hstore_concat' LANGUAGE C STRICT;
			CREATE FUNCTION %1$I.del(bytea, bytea) RETURNS bytea AS %2$L, 'hstore_delete_hstore' LANGUAGE C STRICT;
			CREATE FUNCTION %1$I.del_keys(bytea, text[]) RETURNS bytea AS %2$L, 'hstore_delete_array' LANGUAGE C STRICT;
			CREATE FUNCTION %1$I.contains(bytea, bytea) RETURNS bool AS %2$L, 'hstore_contains' LANGUAGE C STRICT;
			CREATE FUNCTION %1$I.cmp(bytea, bytea) RETURNS int AS %2$L, 'hstore_cmp' LANGUAGE C STRICT;
			CREATE FUNCTION %1$I.from_array(text[]) RETURNS bytea AS %2$L, 'hstore_from_array' LANGUAGE C STRICT;
			CREATE FUNCTION %1$I.from_arrays(text[], text[]) RETURNS bytea AS %2$L, 'hstore_from_arrays' LANGUAGE C;
			CREATE FUNCTION %1$I.try_in(s text, OUT v bytea, OUT err text) AS $b$
			BEGIN
				v := %1$I.hin(s::cstring);
			EXCEPTION WHEN OTHERS THEN
				err := SQLSTATE || ' ' || SQLERRM;
			END $b$ LANGUAGE plpgsql;
		$f$, s, lib);
	END LOOP;
END $$;

-- Random input: a soup of tokens (mostly invalid), or generated pairs.
CREATE FUNCTION pg_temp.soup() RETURNS text AS $$
	SELECT coalesce(string_agg((ARRAY['a', 'b', 'ab', 'key', '"', '\', ' ', '=>', '=', '>', ',', ', ',
		'NULL', 'null', 'NuLl', '"NULL"', 'é', 'ü€', E'\t', E'\n', '""', '"x y"', '1', '-2.5e3', 't'])
		[1 + floor(random() * 25)::int], ''), '')
	FROM generate_series(1, floor(random() * 14)::int)
$$ LANGUAGE sql VOLATILE;

CREATE FUNCTION pg_temp.word() RETURNS text AS $$
	SELECT coalesce(string_agg((ARRAY['a', 'b', 'c', 'k', '"', '\', ' ', '=', '>', ',', 'é', 'NULL', '0', '12', '.', 'e', 't', 'f', E'\t'])
		[1 + floor(random() * 19)::int], ''), '')
	FROM generate_series(1, floor(random() * 4)::int)
$$ LANGUAGE sql VOLATILE;

CREATE FUNCTION pg_temp.quote(s text) RETURNS text AS $$
	SELECT CASE WHEN random() < 0.6 THEN '"' || replace(replace(s, '\', '\\'), '"', '\"') || '"'
		ELSE regexp_replace(s, '([" \\=>,\t])', '\\\1', 'g') END
$$ LANGUAGE sql VOLATILE;

CREATE FUNCTION pg_temp.valid() RETURNS text AS $$
	SELECT coalesce(string_agg(pg_temp.quote(pg_temp.word()) || (ARRAY['=>', ' => ', '=> '])[1 + floor(random() * 3)::int]
		|| CASE WHEN random() < 0.15 THEN (ARRAY['NULL', 'null', 'Null'])[1 + floor(random() * 3)::int]
			ELSE pg_temp.quote(pg_temp.word()) END, (ARRAY[',', ', ', ' ,'])[1 + floor(random() * 3)::int]), '')
	FROM generate_series(1, floor(random() * 8)::int)
$$ LANGUAGE sql VOLATILE;

CREATE TEMP TABLE input AS
	SELECT i, CASE WHEN i % 3 = 0 THEN pg_temp.soup() ELSE pg_temp.valid() END AS s
	FROM generate_series(1, :n) i;

CREATE TEMP TABLE parsed AS
	SELECT i, s, (c.try_in(s)).v AS cv, (c.try_in(s)).err AS cerr, (g.try_in(s)).v AS gv, (g.try_in(s)).err AS gerr
	FROM input;

\echo 1. hstore_in: inputs, valid ones, mismatches (values, accepted/rejected, error messages)
SELECT count(*) AS inputs, count(cv) AS valid,
	count(*) FILTER (WHERE cv IS DISTINCT FROM gv) AS values,
	count(*) FILTER (WHERE (cerr IS NULL) <> (gerr IS NULL)) AS accepted,
	count(*) FILTER (WHERE cerr IS DISTINCT FROM gerr) AS messages
FROM parsed;
SELECT s, cv, gv, cerr, gerr FROM parsed
WHERE cv IS DISTINCT FROM gv OR cerr IS DISTINCT FROM gerr LIMIT 5;

CREATE TEMP TABLE val AS SELECT i, cv AS v FROM parsed WHERE cv IS NOT NULL AND cv = gv;

\echo 2. functions of one value: mismatches per function
SELECT count(*) AS values,
	count(*) FILTER (WHERE c.hout(v)::text IS DISTINCT FROM g.hout(v)::text) AS hstore_out,
	count(*) FILTER (WHERE c.hsend(v) IS DISTINCT FROM g.hsend(v)) AS hstore_send,
	count(*) FILTER (WHERE c.json(v)::text IS DISTINCT FROM g.json(v)::text) AS to_json,
	count(*) FILTER (WHERE c.json_loose(v)::text IS DISTINCT FROM g.json_loose(v)::text) AS to_json_loose,
	count(*) FILTER (WHERE c.jsonb(v)::text IS DISTINCT FROM g.jsonb(v)::text) AS to_jsonb,
	count(*) FILTER (WHERE c.jsonb_loose(v)::text IS DISTINCT FROM g.jsonb_loose(v)::text) AS to_jsonb_loose,
	count(*) FILTER (WHERE c.hash(v) IS DISTINCT FROM g.hash(v) OR c.hash_ext(v, i) IS DISTINCT FROM g.hash_ext(v, i)) AS hash,
	count(*) FILTER (WHERE c.akeys(v) IS DISTINCT FROM g.akeys(v) OR c.avals(v) IS DISTINCT FROM g.avals(v)
		OR c.matrix(v) IS DISTINCT FROM g.matrix(v)) AS arrays,
	count(*) FILTER (WHERE c.fetchval(v, k) IS DISTINCT FROM g.fetchval(v, k)) AS fetchval
FROM val, LATERAL (SELECT pg_temp.word() AS k) w;
SELECT c.hout(v), g.hout(v), c.jsonb_loose(v), g.jsonb_loose(v) FROM val
WHERE c.hout(v)::text IS DISTINCT FROM g.hout(v)::text OR c.jsonb_loose(v)::text IS DISTINCT FROM g.jsonb_loose(v)::text LIMIT 5;

\echo 3. pairs of values and constructors: mismatches per function
SELECT count(*) AS pairs,
	count(*) FILTER (WHERE c.concat(a.v, b.v) IS DISTINCT FROM g.concat(a.v, b.v)) AS concat,
	count(*) FILTER (WHERE c.del(a.v, b.v) IS DISTINCT FROM g.del(a.v, b.v)) AS delete_hstore,
	count(*) FILTER (WHERE c.del_keys(a.v, c.akeys(b.v)) IS DISTINCT FROM g.del_keys(a.v, c.akeys(b.v))) AS delete_array,
	count(*) FILTER (WHERE c.contains(a.v, b.v) IS DISTINCT FROM g.contains(a.v, b.v)
		OR c.contains(a.v, c.concat(b.v, a.v)) IS DISTINCT FROM g.contains(a.v, c.concat(b.v, a.v))) AS contains,
	count(*) FILTER (WHERE c.cmp(a.v, b.v) IS DISTINCT FROM g.cmp(a.v, b.v)) AS cmp,
	count(*) FILTER (WHERE c.from_array(c.matrix(a.v) || c.matrix(b.v)) IS DISTINCT FROM g.from_array(c.matrix(a.v) || c.matrix(b.v))
		OR c.from_arrays(c.akeys(a.v) || c.akeys(b.v), c.avals(a.v) || c.avals(b.v))
			IS DISTINCT FROM g.from_arrays(c.akeys(a.v) || c.akeys(b.v), c.avals(a.v) || c.avals(b.v))
		OR c.from_arrays(c.akeys(a.v), NULL) IS DISTINCT FROM g.from_arrays(c.akeys(a.v), NULL)) AS constructors
FROM val a JOIN val b ON b.i = (a.i * 7919) % :n + 1;
