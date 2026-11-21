-- Differential test: compares this Go port with the original C cube on a
-- generated corpus. Needs the original library installed as
-- $libdir/cube_c.so (copy cube.so before installing the port over it) and
-- this port installed as cube. Both libraries' functions are declared on
-- the port's cube type (the binary layouts are the same) and compared on
-- their exact results: cubes as their send() bytes, so point flags and NaN
-- bits count, and errors with their SQLSTATE, message and detail. Prints
-- the number of mismatches per function (all must be 0) and a few
-- examples of each.
--
--   psql -v ON_ERROR_STOP=1 -v n=20000 -f differential.sql

\if :{?n}
\else
\set n 5000
\endif

SET client_min_messages = warning;
DROP SCHEMA IF EXISTS g CASCADE;
DROP SCHEMA IF EXISTS c CASCADE;
CREATE EXTENSION IF NOT EXISTS cube;
CREATE SCHEMA g;
CREATE SCHEMA c;

-- Both implementations: name, symbol, arguments, result.
DO $$
DECLARE
	lib text;
	f text[];
BEGIN
	FOREACH lib IN ARRAY ARRAY['g:cube', 'c:cube_c'] LOOP
		FOREACH f SLICE 1 IN ARRAY ARRAY[
			['cube_in', 'cube_in', 'cstring', 'cube'],
			['cube_out', 'cube_out', 'cube', 'cstring'],
			['cube_send', 'cube_send', 'cube', 'bytea'],
			['cube_a_f8_f8', 'cube_a_f8_f8', 'float8[], float8[]', 'cube'],
			['cube_a_f8', 'cube_a_f8', 'float8[]', 'cube'],
			['cube_f8', 'cube_f8', 'float8', 'cube'],
			['cube_f8_f8', 'cube_f8_f8', 'float8, float8', 'cube'],
			['cube_c_f8', 'cube_c_f8', 'cube, float8', 'cube'],
			['cube_c_f8_f8', 'cube_c_f8_f8', 'cube, float8, float8', 'cube'],
			['cube_subset', 'cube_subset', 'cube, int4[]', 'cube'],
			['cube_cmp', 'cube_cmp', 'cube, cube', 'int4'],
			['cube_contains', 'cube_contains', 'cube, cube', 'bool'],
			['cube_overlap', 'cube_overlap', 'cube, cube', 'bool'],
			['cube_union', 'cube_union', 'cube, cube', 'cube'],
			['cube_inter', 'cube_inter', 'cube, cube', 'cube'],
			['cube_size', 'cube_size', 'cube', 'float8'],
			['cube_distance', 'cube_distance', 'cube, cube', 'float8'],
			['distance_taxicab', 'distance_taxicab', 'cube, cube', 'float8'],
			['distance_chebyshev', 'distance_chebyshev', 'cube, cube', 'float8'],
			['cube_dim', 'cube_dim', 'cube', 'int4'],
			['cube_is_point', 'cube_is_point', 'cube', 'bool'],
			['cube_ll_coord', 'cube_ll_coord', 'cube, int4', 'float8'],
			['cube_ur_coord', 'cube_ur_coord', 'cube, int4', 'float8'],
			['cube_coord', 'cube_coord', 'cube, int4', 'float8'],
			['cube_coord_llur', 'cube_coord_llur', 'cube, int4', 'float8'],
			['cube_enlarge', 'cube_enlarge', 'cube, float8, int4', 'cube']] LOOP
			EXECUTE format('CREATE FUNCTION %s.%s(%s) RETURNS %s AS %L, %L LANGUAGE C IMMUTABLE STRICT',
				split_part(lib, ':', 1), f[1], f[3], f[4], '$libdir/' || split_part(lib, ':', 2), f[2]);
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

-- A number in one of the scanner's forms, or an odd one.
CREATE FUNCTION pg_temp.num() RETURNS text AS $$
	SELECT CASE floor(random() * 12)::int
		WHEN 0 THEN (floor(random() * 200) - 100)::text
		WHEN 1 THEN round((random() * 2000 - 1000)::numeric, floor(random() * 5)::int)::text
		WHEN 2 THEN pg_temp.pick('{"","+","-"}') || floor(random() * 100)::text || pg_temp.pick('{e,E}')
			|| pg_temp.pick('{"","+","-"}') || floor(random() * 400)::text
		WHEN 3 THEN pg_temp.pick('{"",-}') || '.' || floor(random() * 1000)::text
		WHEN 4 THEN floor(random() * 100)::text || '.'
		WHEN 5 THEN pg_temp.pick('{inf,-inf,+inf,Infinity,-INFINITY,iNf,nan,NaN,-nan,infin,in,na}')
		WHEN 6 THEN pg_temp.pick('{0,-0,0.0,-0e5,1e-320,-4.9e-324,2.5e-324,1e-324,1.7976931348623157e308,1.8e308,1e-310}')
		WHEN 7 THEN (random() * 1e6)::text
		WHEN 8 THEN (random() * 1e-6)::text
		WHEN 9 THEN '1' || repeat('0', floor(random() * 40)::int) || '.5'
		WHEN 10 THEN pg_temp.pick('{1.5e,e5,1e+,.,+,-,--1,1..2,0x10,1_000}')
		ELSE (random() * 10)::numeric(10, 9)::text
	END
$$ LANGUAGE sql VOLATILE;

-- A list of n numbers, with random spacing.
CREATE FUNCTION pg_temp.list(n int) RETURNS text AS $$
	SELECT coalesce(string_agg(pg_temp.num(), pg_temp.pick('{",",", "," ,","	,"}')), '') FROM generate_series(1, n)
$$ LANGUAGE sql VOLATILE;

CREATE FUNCTION pg_temp.ndim() RETURNS int AS $$
	SELECT CASE WHEN random() < 0.02 THEN 99 + floor(random() * 3)::int ELSE floor(random() * 5)::int END
$$ LANGUAGE sql VOLATILE;

SELECT setseed(0.4242);
CREATE TEMP TABLE inputs AS
SELECT i, CASE i % 8
	WHEN 0 THEN '(' || pg_temp.list(pg_temp.ndim()) || ')'
	WHEN 1 THEN pg_temp.list(1 + pg_temp.ndim())
	WHEN 2 THEN (SELECT '(' || pg_temp.list(d) || '),(' || pg_temp.list(d + CASE WHEN random() < 0.1 THEN 1 ELSE 0 END) || ')'
		FROM (SELECT pg_temp.ndim() d) x)
	WHEN 3 THEN (SELECT '[(' || pg_temp.list(d) || '),(' || pg_temp.list(d) || ')]' FROM (SELECT pg_temp.ndim() d) x)
	-- equal corners: points
	WHEN 4 THEN (SELECT '(' || l || '),(' || l || ')' FROM (SELECT pg_temp.list(pg_temp.ndim()) l) x)
	-- token soup
	WHEN 5 THEN (SELECT string_agg(pg_temp.pick('{(,),[,],",",1,-2.5,e,x,"  ",inf,nan,(),.}'), '')
		FROM generate_series(1, 1 + floor(random() * 10)::int))
	-- well-formed with trailing garbage
	WHEN 6 THEN '(' || pg_temp.list(2) || '),(' || pg_temp.list(2) || ')' || pg_temp.pick('{x," ]",",(1)",")",é}')
	ELSE pg_temp.pick('{"","  ","()","(),()","[(),()]","[(1),(2)]x","(1)x","1 2","(1,2","[(1,2),(3,4)","[(1,2),(3,4)] 5"}')
	END AS s
FROM generate_series(1, :n) i;

CREATE TEMP TABLE mismatches (fn text, input text, go text, c text);
CREATE TEMP TABLE compared (fn text, n int);

-- Compares expr (%s: the schema; $1, $2, $3: input columns a, b, x) on rows.
CREATE FUNCTION pg_temp.compare(fn text, expr text, rows text) RETURNS void AS $$
BEGIN
	EXECUTE format($q$
		INSERT INTO mismatches
		SELECT %L, input, go, cr FROM (
			SELECT input, pg_temp.try(e, 'g') AS go, pg_temp.try(e, 'c') AS cr
			FROM (SELECT input, format(%L, a, b, x) AS e FROM (%s) r) r) x
		WHERE go IS DISTINCT FROM cr$q$, fn, expr, rows);
	EXECUTE format('INSERT INTO compared SELECT %L, count(*) FROM (%s) r', fn, rows);
END $$ LANGUAGE plpgsql;

-- Parsing.
SELECT pg_temp.compare('cube_in', $e$SELECT encode(%%s.cube_send(%%s.cube_in(%L)), 'hex')$e$,
	'SELECT s AS input, s AS a, NULL AS b, NULL AS x FROM inputs');

-- Valid values (parsed by C), and pairs of them.
CREATE TEMP TABLE vals AS
SELECT DISTINCT r AS v FROM (SELECT pg_temp.try(format('SELECT %%s.cube_in(%L)::text', s), 'c') AS r FROM inputs) x
WHERE r !~ '^ERROR';
INSERT INTO vals VALUES ('(1, 2),(2, 1)'), ('(5),(-5)'), ('()'), ('(1),(1)'), ('(NaN),(NaN)'), ('(-0),(0)');
CREATE TEMP TABLE pairs AS
SELECT a.v AS a, b.v AS b FROM (SELECT v, row_number() OVER (ORDER BY random()) AS k FROM vals) a
JOIN (SELECT v, row_number() OVER (ORDER BY random()) AS k FROM vals) b ON a.k = b.k
UNION ALL
SELECT v, v FROM vals;

-- Output, with every precision setting.
DO $$
DECLARE
	efd int;
BEGIN
	FOREACH efd IN ARRAY ARRAY[1, 0, 3, -5, -15] LOOP
		PERFORM set_config('extra_float_digits', efd::text, false);
		PERFORM pg_temp.compare('cube_out (extra_float_digits ' || efd || ')', 'SELECT %%s.cube_out(%L::cube)',
			'SELECT v AS input, v AS a, NULL AS b, NULL AS x FROM vals');
	END LOOP;
	PERFORM set_config('extra_float_digits', '3', false);
END $$;

-- Functions of a cube.
SELECT pg_temp.compare(fn, expr, 'SELECT v || '' '' || x AS input, v AS a, NULL AS b, x FROM vals, (VALUES (-3), (-1), (0), (1), (2), (3), (4), (5), (7), (201)) n(x)')
FROM (VALUES
	('cube_ll_coord', 'SELECT %%s.cube_ll_coord(%L::cube, %3$s)'),
	('cube_ur_coord', 'SELECT %%s.cube_ur_coord(%L::cube, %3$s)'),
	('cube_coord', 'SELECT %%s.cube_coord(%L::cube, %3$s)'),
	('cube_coord_llur', 'SELECT %%s.cube_coord_llur(%L::cube, %3$s)'),
	('cube_c_f8', $e$SELECT encode(%%s.cube_send(%%s.cube_c_f8(%L::cube, %3$s / 2.0)), 'hex')$e$),
	('cube_c_f8_f8', $e$SELECT encode(%%s.cube_send(%%s.cube_c_f8_f8(%L::cube, %3$s, 2)), 'hex')$e$),
	('cube_enlarge', $e$SELECT encode(%%s.cube_send(%%s.cube_enlarge(%L::cube, %3$s / 3.0 - 1, %3$s)), 'hex')$e$),
	('cube_subset', $e$SELECT encode(%%s.cube_send(%%s.cube_subset(%L::cube, ARRAY[%3$s, 1, mod(%3$s, 3)])), 'hex')$e$)) f(fn, expr);
SELECT pg_temp.compare(fn, expr, 'SELECT v AS input, v AS a, NULL AS b, NULL AS x FROM vals')
FROM (VALUES
	('cube_send', $e$SELECT encode(%%s.cube_send(%L::cube), 'hex')$e$),
	('cube_size', 'SELECT %%s.cube_size(%L::cube)'),
	('cube_dim', 'SELECT %%s.cube_dim(%L::cube)'),
	('cube_is_point', 'SELECT %%s.cube_is_point(%L::cube)')) f(fn, expr);

-- Functions of two cubes.
SELECT pg_temp.compare(fn, expr, 'SELECT a || '' '' || b AS input, a, b, NULL AS x FROM pairs')
FROM (VALUES
	('cube_cmp', 'SELECT %%s.cube_cmp(%L::cube, %L::cube)'),
	('cube_contains', 'SELECT %%s.cube_contains(%L::cube, %L::cube)'),
	('cube_overlap', 'SELECT %%s.cube_overlap(%L::cube, %L::cube)'),
	('cube_union', $e$SELECT encode(%%s.cube_send(%%s.cube_union(%L::cube, %L::cube)), 'hex')$e$),
	('cube_inter', $e$SELECT encode(%%s.cube_send(%%s.cube_inter(%L::cube, %L::cube)), 'hex')$e$),
	('cube_distance', 'SELECT %%s.cube_distance(%L::cube, %L::cube)'),
	('distance_taxicab', 'SELECT %%s.distance_taxicab(%L::cube, %L::cube)'),
	('distance_chebyshev', 'SELECT %%s.distance_chebyshev(%L::cube, %L::cube)')) f(fn, expr);

-- Constructors from numbers and arrays.
CREATE TEMP TABLE nums AS
SELECT i, (SELECT array_agg(CASE WHEN random() < 0.03 THEN NULL ELSE (random() * 10)::int / 2.0 END)
	FROM generate_series(1, CASE WHEN i % 50 = 0 THEN 101 ELSE i % 5 END)) AS xs,
	(SELECT array_agg((random() * 10)::int / 2.0) FROM generate_series(1, CASE WHEN i % 7 = 0 THEN 2 ELSE i % 5 END)) AS ys
FROM generate_series(1, :n / 5) i;
SELECT pg_temp.compare(fn, expr, 'SELECT xs::text || '' '' || ys::text AS input, xs::text AS a, ys::text AS b, NULL AS x FROM nums')
FROM (VALUES
	('cube_a_f8_f8', $e$SELECT encode(%%s.cube_send(%%s.cube_a_f8_f8(%L::float8[], %L::float8[])), 'hex')$e$),
	('cube_a_f8', $e$SELECT encode(%%s.cube_send(%%s.cube_a_f8(%L::float8[])), 'hex')$e$),
	('cube_f8', $e$SELECT encode(%%s.cube_send(%%s.cube_f8((%2$L::float8[])[1])), 'hex')$e$),
	('cube_f8_f8', $e$SELECT encode(%%s.cube_send(%%s.cube_f8_f8((%2$L::float8[])[1], (%2$L::float8[])[2])), 'hex')$e$)) f(fn, expr);

-- Coverage: how varied the compared inputs were.
SELECT (SELECT count(*) FROM inputs) AS inputs,
	(SELECT count(*) FROM vals) AS distinct_values,
	(SELECT count(DISTINCT c) FROM (SELECT pg_temp.try(format('SELECT %%s.cube_in(%L)', s), 'c') AS c FROM inputs) x
		WHERE c ~ '^ERROR') AS distinct_errors;

SELECT c.fn, c.n AS compared, count(m.fn) AS mismatches
FROM compared c LEFT JOIN mismatches m ON m.fn = c.fn GROUP BY c.fn, c.n ORDER BY c.fn;

SELECT fn, input, go, c FROM (
	SELECT *, row_number() OVER (PARTITION BY fn) AS rn FROM mismatches) x
WHERE rn <= 3;
