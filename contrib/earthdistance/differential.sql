-- Differential test: compares this Go port's geo_distance (<@>) with the
-- original C earthdistance on generated points. Needs the original library
-- installed as $libdir/earthdistance_c.so (copy earthdistance.so before
-- installing the port over it) and this port installed as earthdistance.
-- Prints the number of mismatches (exact float8 results; must be 0) and a
-- few examples. The rest of earthdistance is SQL on cube functions (see
-- contrib/cube/differential.sql).
--
--   psql -v ON_ERROR_STOP=1 -v n=100000 -f differential.sql

\if :{?n}
\else
\set n 20000
\endif

SET client_min_messages = warning;
SET extra_float_digits = 3;
CREATE EXTENSION IF NOT EXISTS earthdistance CASCADE;
DROP FUNCTION IF EXISTS pg_temp.c_geo_distance(point, point);
CREATE FUNCTION pg_temp.c_geo_distance(point, point) RETURNS float8
	AS '$libdir/earthdistance_c', 'geo_distance' LANGUAGE C IMMUTABLE STRICT;

CREATE FUNCTION pg_temp.coord(scale float8) RETURNS float8 AS $$
	SELECT CASE floor(random() * 20)::int
		WHEN 0 THEN (ARRAY['NaN', 'Infinity', '-Infinity', '0', '-0', '180', '-180', '90', '-90', '360', '1e300', '1e-300'])
			[1 + floor(random() * 12)::int]::float8
		WHEN 1 THEN round((random() * 2 - 1) * scale)
		ELSE (random() * 2 - 1) * scale
	END
$$ LANGUAGE sql VOLATILE;

SELECT setseed(0.1618);
CREATE TEMP TABLE pts AS
SELECT point(pg_temp.coord(s), pg_temp.coord(s / 2)) AS a, point(pg_temp.coord(s), pg_temp.coord(s / 2)) AS b
FROM generate_series(1, :n) i, LATERAL (SELECT (ARRAY[360, 720, 1, 1e-6])[1 + i % 4]::float8 AS s) x;
-- antipodes and identical points
INSERT INTO pts SELECT a, point(a[0] + 180, -a[1]) FROM pts LIMIT :n / 10;
INSERT INTO pts SELECT a, a FROM pts LIMIT :n / 10;

SELECT count(*) AS compared,
	count(*) FILTER (WHERE (a <@> b)::text IS DISTINCT FROM pg_temp.c_geo_distance(a, b)::text) AS mismatches
FROM pts;

SELECT a, b, a <@> b AS go, pg_temp.c_geo_distance(a, b) AS c FROM pts
WHERE (a <@> b)::text IS DISTINCT FROM pg_temp.c_geo_distance(a, b)::text LIMIT 5;
