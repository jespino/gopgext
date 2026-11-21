-- Differential test: compares this Go port with the original C seg on a
-- generated corpus. Needs the original library installed as
-- $libdir/seg_c.so (copy seg.so before installing the port over it) and
-- this port installed as seg. Both libraries' functions are declared on
-- the port's seg type (the layouts are the same) and compared on their
-- results (segs as their text and exact bounds; values are the inputs C
-- parses, since some don't survive a round trip through text, e.g. 1(+-)-2) and errors (SQLSTATE,
-- message and detail). Prints the number of mismatches per function (all
-- must be 0) and a few examples of each.
--
--   psql -v ON_ERROR_STOP=1 -v n=20000 -f differential.sql

\if :{?n}
\else
\set n 5000
\endif

SET client_min_messages = warning;
SET extra_float_digits = 3;
DROP SCHEMA IF EXISTS g CASCADE;
DROP SCHEMA IF EXISTS c CASCADE;
CREATE EXTENSION IF NOT EXISTS seg;
CREATE SCHEMA g;
CREATE SCHEMA c;

DO $$
DECLARE
	lib text;
	f text[];
BEGIN
	FOREACH lib IN ARRAY ARRAY['g:seg', 'c:seg_c'] LOOP
		FOREACH f SLICE 1 IN ARRAY ARRAY[
			['seg_in', 'cstring', 'seg'], ['seg_out', 'seg', 'cstring'],
			['seg_center', 'seg', 'float4'], ['seg_lower', 'seg', 'float4'], ['seg_upper', 'seg', 'float4'],
			['seg_size', 'seg', 'float4'], ['seg_cmp', 'seg, seg', 'int4'],
			['seg_contains', 'seg, seg', 'bool'], ['seg_contained', 'seg, seg', 'bool'],
			['seg_overlap', 'seg, seg', 'bool'], ['seg_left', 'seg, seg', 'bool'],
			['seg_over_left', 'seg, seg', 'bool'], ['seg_right', 'seg, seg', 'bool'],
			['seg_over_right', 'seg, seg', 'bool'], ['seg_same', 'seg, seg', 'bool'],
			['seg_union', 'seg, seg', 'seg'], ['seg_inter', 'seg, seg', 'seg']] LOOP
			EXECUTE format('CREATE FUNCTION %s.%s(%s) RETURNS %s AS %L, %L LANGUAGE C IMMUTABLE STRICT',
				split_part(lib, ':', 1), f[1], f[2], f[3], '$libdir/' || split_part(lib, ':', 2), f[1]);
		END LOOP;
	END LOOP;
END $$;

-- The result of expr with %s replaced by the schema (g or c), or its error.
CREATE FUNCTION pg_temp.try(expr text, schema text) RETURNS text AS $$
DECLARE
	r text;
	detail text;
BEGIN
	EXECUTE replace(expr, '%s', schema) INTO r;
	RETURN coalesce(r, 'NULL');
EXCEPTION WHEN OTHERS THEN
	GET STACKED DIAGNOSTICS detail = PG_EXCEPTION_DETAIL;
	RETURN 'ERROR ' || SQLSTATE || ': ' || SQLERRM || coalesce(' / ' || nullif(detail, ''), '');
END $$ LANGUAGE plpgsql;

CREATE FUNCTION pg_temp.pick(xs text[]) RETURNS text AS $$
	SELECT xs[1 + floor(random() * array_length(xs, 1))::int]
$$ LANGUAGE sql VOLATILE;

CREATE FUNCTION pg_temp.num() RETURNS text AS $$
	SELECT CASE floor(random() * 10)::int
		WHEN 0 THEN (floor(random() * 200) - 100)::text
		WHEN 1 THEN round((random() * 2000 - 1000)::numeric, floor(random() * 8)::int)::text
		WHEN 2 THEN pg_temp.pick('{"","+","-"}') || floor(random() * 100)::text || pg_temp.pick('{e,E}')
			|| pg_temp.pick('{"","+","-"}') || floor(random() * 50)::text
		WHEN 3 THEN pg_temp.pick('{"",-}') || '0.' || repeat('0', floor(random() * 6)::int) || floor(random() * 1000)::text
		WHEN 4 THEN repeat('0', floor(random() * 3)::int) || floor(random() * 1e9)::text
		WHEN 5 THEN pg_temp.pick('{0,-0,0.0,0.000,1e38,3.5e38,1e-46,1e-40,1e-38,1.5e-45,1e39}')
		WHEN 6 THEN (random() * 1e6)::text
		WHEN 7 THEN (random() * 1e-3)::numeric(20, 15)::text
		WHEN 8 THEN pg_temp.pick('{1.,.5,1e,+,-,1.5.5,1e5.5,inf,nan}')
		ELSE round((random() * 20 - 10)::numeric, floor(random() * 3)::int)::text
	END
$$ LANGUAGE sql VOLATILE;

CREATE FUNCTION pg_temp.bound() RETURNS text AS $$
	SELECT pg_temp.pick('{"","","","","<",">","~"," <"}') || pg_temp.num()
$$ LANGUAGE sql VOLATILE;

CREATE FUNCTION pg_temp.sp() RETURNS text AS $$
	SELECT pg_temp.pick('{""," "," ","  ","	"}')
$$ LANGUAGE sql VOLATILE;

SELECT setseed(0.2718);
CREATE TEMP TABLE inputs AS
SELECT i, CASE i % 8
	WHEN 0 THEN pg_temp.bound()
	WHEN 1 THEN pg_temp.bound() || pg_temp.sp() || pg_temp.pick('{..,...}') || pg_temp.sp() || pg_temp.bound()
	WHEN 2 THEN pg_temp.bound() || pg_temp.sp() || pg_temp.pick('{..,...}')
	WHEN 3 THEN pg_temp.pick('{..,...}') || pg_temp.sp() || pg_temp.bound()
	WHEN 4 THEN pg_temp.num() || pg_temp.sp() || pg_temp.pick($${'+-',(+-),'+-,(+-,+-}$$) || pg_temp.sp() || pg_temp.num()
	-- token soup
	WHEN 5 THEN (SELECT string_agg(pg_temp.pick($${1,2.5,..,(+-),'+-',<,>,~,x," ",-,e,.,é}$$), '')
		FROM generate_series(1, 1 + floor(random() * 6)::int))
	WHEN 6 THEN pg_temp.bound() || pg_temp.sp() || '..' || pg_temp.sp() || pg_temp.bound() || pg_temp.pick('{x," 1",..,é," é",(+-)}')
	ELSE pg_temp.pick('{"","  ","..","...","1 ..","..2","1(+-)","(+-)1","<",">1..<2","1 . . 2","1e7(+-)1e7"}')
	END AS s
FROM generate_series(1, :n) i;

CREATE TEMP TABLE mismatches (fn text, input text, go text, c text);
CREATE TEMP TABLE compared (fn text, n int);

-- Compares expr (%s: the schema; %L: column a, then b) on rows.
CREATE FUNCTION pg_temp.compare(fn text, expr text, rows text) RETURNS void AS $$
BEGIN
	EXECUTE format($q$
		INSERT INTO mismatches
		SELECT %L, input, go, cr FROM (
			SELECT input, pg_temp.try(e, 'g') AS go, pg_temp.try(e, 'c') AS cr
			FROM (SELECT input, format(%L, a, b) AS e FROM (%s) r) r) x
		WHERE go IS DISTINCT FROM cr$q$, fn, expr, rows);
	EXECUTE format('INSERT INTO compared SELECT %L, count(*) FROM (%s) r', fn, rows);
END $$ LANGUAGE plpgsql;

-- Parsing: the text and the exact bounds of the result.
SELECT pg_temp.compare('seg_in',
	'SELECT %%s.seg_out(v) || '' '' || %%s.seg_lower(v) || '' '' || %%s.seg_upper(v) FROM (SELECT %%s.seg_in(%L) v) x',
	'SELECT s AS input, s AS a, NULL AS b FROM inputs');

CREATE TEMP TABLE vals AS
SELECT DISTINCT s AS v FROM inputs WHERE pg_temp.try(format('SELECT %%s.seg_in(%L)::text', s), 'c') !~ '^ERROR';
CREATE TEMP TABLE pairs AS
SELECT a.v AS a, b.v AS b FROM (SELECT v, row_number() OVER (ORDER BY random()) AS k FROM vals) a
JOIN (SELECT v, row_number() OVER (ORDER BY random()) AS k FROM vals) b ON a.k = b.k
UNION ALL
SELECT v, v FROM vals
UNION ALL
-- same bounds, different digits and kinds
SELECT a.v, b.v FROM vals a JOIN vals b ON seg_lower(seg_in(a.v::cstring)) = seg_lower(seg_in(b.v::cstring)) AND a.v <> b.v;

SELECT pg_temp.compare(fn, expr, 'SELECT v AS input, v AS a, NULL AS b FROM vals')
FROM (VALUES
	('seg_out', 'SELECT %%s.seg_out(public.seg_in(%L))'),
	('seg_center', 'SELECT %%s.seg_center(public.seg_in(%L))'),
	('seg_lower', 'SELECT %%s.seg_lower(public.seg_in(%L))'),
	('seg_upper', 'SELECT %%s.seg_upper(public.seg_in(%L))'),
	('seg_size', 'SELECT %%s.seg_size(public.seg_in(%L))')) f(fn, expr);

SELECT pg_temp.compare(fn, expr, 'SELECT a || '' '' || b AS input, a, b FROM pairs')
FROM (VALUES
	('seg_cmp', 'SELECT %%s.seg_cmp(public.seg_in(%L), public.seg_in(%L))'),
	('seg_contains', 'SELECT %%s.seg_contains(public.seg_in(%L), public.seg_in(%L))'),
	('seg_contained', 'SELECT %%s.seg_contained(public.seg_in(%L), public.seg_in(%L))'),
	('seg_overlap', 'SELECT %%s.seg_overlap(public.seg_in(%L), public.seg_in(%L))'),
	('seg_left', 'SELECT %%s.seg_left(public.seg_in(%L), public.seg_in(%L))'),
	('seg_over_left', 'SELECT %%s.seg_over_left(public.seg_in(%L), public.seg_in(%L))'),
	('seg_right', 'SELECT %%s.seg_right(public.seg_in(%L), public.seg_in(%L))'),
	('seg_over_right', 'SELECT %%s.seg_over_right(public.seg_in(%L), public.seg_in(%L))'),
	('seg_same', 'SELECT %%s.seg_same(public.seg_in(%L), public.seg_in(%L))'),
	('seg_union', 'SELECT %%s.seg_out(v) || '' '' || %%s.seg_lower(v) || '' '' || %%s.seg_upper(v) FROM (SELECT %%s.seg_union(public.seg_in(%L), public.seg_in(%L)) v) x'),
	('seg_inter', 'SELECT %%s.seg_out(v) || '' '' || %%s.seg_lower(v) || '' '' || %%s.seg_upper(v) FROM (SELECT %%s.seg_inter(public.seg_in(%L), public.seg_in(%L)) v) x')) f(fn, expr);

SELECT (SELECT count(*) FROM inputs) AS inputs,
	(SELECT count(*) FROM vals) AS distinct_values,
	(SELECT count(DISTINCT c) FROM (SELECT pg_temp.try(format('SELECT %%s.seg_in(%L)', s), 'c') AS c FROM inputs) x
		WHERE c ~ '^ERROR') AS distinct_errors;

SELECT c.fn, c.n AS compared, count(m.fn) AS mismatches
FROM compared c LEFT JOIN mismatches m ON m.fn = c.fn GROUP BY c.fn, c.n ORDER BY c.fn;

SELECT fn, input, go, c FROM (
	SELECT *, row_number() OVER (PARTITION BY fn) AS rn FROM mismatches) x
WHERE rn <= 3;
