CREATE EXTENSION geo;

CREATE FUNCTION expect_error(sql text, code text, msg text) RETURNS void LANGUAGE plpgsql AS $$
BEGIN
	EXECUTE sql;
	RAISE 'expected an error from %', sql;
EXCEPTION WHEN OTHERS THEN
	IF SQLSTATE <> code OR SQLERRM <> msg THEN
		RAISE 'got % "%", want % "%" (%)', SQLSTATE, SQLERRM, code, msg, sql;
	END IF;
END $$;

DO $$
DECLARE
	r record;
BEGIN
	-- I/O and functions
	ASSERT 'point( 1  2.5 )'::shape::text = 'POINT(1 2.5)';
	ASSERT 'LINESTRING(0 0, 3 4,3 5)'::shape::text = 'LINESTRING(0 0,3 4,3 5)';
	ASSERT 'POLYGON((0 0, 4 0, 4 4, 0 4, 0 0))'::shape::text = 'POLYGON((0 0,4 0,4 4,0 4,0 0))';
	ASSERT st_makepoint(-1.5, 1e-7)::text = 'POINT(-1.5 1e-07)';
	ASSERT st_geometrytype('LINESTRING(0 0, 1 1)') = 'ST_LineString';
	ASSERT st_npoints('POLYGON((0 0, 4 0, 4 4, 0 4, 0 0))') = 5;
	ASSERT st_x('POINT(1 2)') = 1 AND st_y('POINT(1 2)') = 2 AND st_x('LINESTRING(0 0, 1 1)') IS NULL;
	ASSERT st_area('POLYGON((0 0, 4 0, 4 4, 0 4, 0 0))') = 16;
	ASSERT st_length('LINESTRING(0 0, 3 4, 3 5)') = 6;
	ASSERT st_perimeter('POLYGON((0 0, 4 0, 4 4, 0 4, 0 0))') = 16;
	ASSERT st_envelope('LINESTRING(0 0, 3 4)')::text = 'POLYGON((0 0,0 4,3 4,3 0,0 0))';
	ASSERT st_envelope('POINT(1 1)')::text = 'POINT(1 1)';

	-- Rows: OUT parameters (with names from struct tags) and RETURNS TABLE.
	ASSERT (shape_bbox('LINESTRING(0 0, 3 4, -1 2)')).xmin = -1;
	SELECT * INTO r FROM shape_bbox('LINESTRING(0 0, 3 4, -1 2)');
	ASSERT (r.xmin, r.ymin, r.xmax, r.ymax) = (-1::float8, 0::float8, 3::float8, 4::float8), r::text;
	ASSERT (SELECT string_agg(parameter_name::text, ',' ORDER BY ordinal_position)
		FROM information_schema.parameters
		WHERE specific_name LIKE 'shape_bbox_%' AND parameter_mode = 'OUT') = 'xmin,ymin,xmax,ymax';
	ASSERT (st_pointxy('POINT(3 4)')).y = 4;
	ASSERT st_pointxy('LINESTRING(0 0, 1 1)') IS NULL;
	ASSERT (SELECT array_agg(path ORDER BY path) FROM st_dumppoints('LINESTRING(0 0, 3 4, 3 5)')) = '{1,2,3}';
	ASSERT (SELECT sum(x) + sum(y) FROM st_dumppoints('LINESTRING(0 0, 3 4, 3 5)')) = 15;
	ASSERT (SELECT count(*) FROM st_dumppoints('POINT(1 1)')) = 1;

	-- Distance
	ASSERT st_distance('POINT(0 0)', 'POINT(3 4)') = 5;
	ASSERT 'POINT(0 0)'::shape <-> 'POINT(3 4)' = 5;
	ASSERT st_distance('POINT(1 5)', 'LINESTRING(0 0, 4 0)') = 5;
	ASSERT st_distance('POINT(2 3)', 'LINESTRING(0 0, 4 0)') = 3;
	ASSERT st_distance('POINT(1 1)', 'POLYGON((0 0, 4 0, 4 4, 0 4, 0 0))') = 0;
	ASSERT st_distance('LINESTRING(-1 2, 5 2)', 'POLYGON((0 0, 4 0, 4 4, 0 4, 0 0))') = 0;
	ASSERT st_distance('POLYGON((10 0, 11 0, 11 1, 10 1, 10 0))', 'POLYGON((0 0, 4 0, 4 4, 0 4, 0 0))') = 6;
	ASSERT st_dwithin('POINT(0 0)', 'POINT(3 4)', 5) AND NOT st_dwithin('POINT(0 0)', 'POINT(3 4)', 4.9);

	-- Bounding box operators
	ASSERT 'LINESTRING(0 0, 2 2)'::shape && 'POINT(1 1)';
	ASSERT NOT ('LINESTRING(0 0, 2 2)'::shape && 'POINT(3 1)');
	ASSERT 'POLYGON((0 0, 4 0, 4 4, 0 4, 0 0))'::shape ~ 'LINESTRING(1 1, 2 2)';
	ASSERT 'LINESTRING(1 1, 2 2)'::shape @ 'POLYGON((0 0, 4 0, 4 4, 0 4, 0 0))';
END $$;

SELECT expect_error($$SELECT 'CIRCLE(1 2)'::shape$$, '22P02', 'invalid input syntax for type shape: "CIRCLE(1 2)"');
SELECT expect_error($$SELECT 'POINT(1)'::shape$$, '22P02', 'invalid input syntax for type shape: "POINT(1)"');
SELECT expect_error($$SELECT 'POLYGON((0 0, 1 0, 1 1, 0 1))'::shape$$, '22P02',
	'invalid input syntax for type shape: "POLYGON((0 0, 1 0, 1 1, 0 1))"');

-- A GiST index: rows inserted before (index build) and after it is created
-- (inserts: penalty and picksplit), then deletes, updates and vacuum.
CREATE TABLE shapes (id int PRIMARY KEY, s shape);
INSERT INTO shapes
	SELECT i, st_makepoint((i * 7919) % 1000 / 10.0, (i * 104729) % 997 / 10.0)
	FROM generate_series(1, 20000) i;
CREATE INDEX shapes_s ON shapes USING gist (s);
INSERT INTO shapes
	SELECT 20000 + i, format('LINESTRING(%s %s, %s %s)', i % 90, i % 70, i % 90 + 3, i % 70 + 2)::shape
	FROM generate_series(1, 3000) i;
INSERT INTO shapes
	SELECT 30000 + i, format('POLYGON((%s %s, %s %s, %s %s, %s %s))',
		i % 95, i % 93, i % 95 + 2, i % 93, i % 95 + 1, i % 93 + 3, i % 95, i % 93)::shape
	FROM generate_series(1, 3000) i;
INSERT INTO shapes VALUES (99999, NULL);
DELETE FROM shapes WHERE id % 10 = 3;
UPDATE shapes SET s = st_makepoint(50, 50) WHERE id % 1000 = 7;
VACUUM ANALYZE shapes;

-- query runs sql with and without the index, and checks both give the
-- same rows (the index must be used for the first).
CREATE FUNCTION same_results(sql text) RETURNS bigint LANGUAGE plpgsql AS $$
DECLARE
	plan text;
	line text;
	with_index text;
	without text;
	n bigint;
BEGIN
	SET LOCAL enable_seqscan = off;
	SET LOCAL enable_bitmapscan = off;
	plan := '';
	FOR line IN EXECUTE 'EXPLAIN (COSTS OFF) ' || sql LOOP
		plan := plan || line || E'\n';
	END LOOP;
	ASSERT plan LIKE '%Index Scan using shapes_s%', plan;
	EXECUTE 'SELECT string_agg(r::text, '';'') FROM (' || sql || ') r' INTO with_index;
	SET LOCAL enable_seqscan = on;
	SET LOCAL enable_indexscan = off;
	EXECUTE 'SELECT string_agg(r::text, '';''), count(*) FROM (' || sql || ') r' INTO without, n;
	ASSERT with_index IS NOT DISTINCT FROM without, format('%s: index %s, scan %s', sql, with_index, without);
	RESET enable_seqscan;
	RESET enable_bitmapscan;
	RESET enable_indexscan;
	RETURN n;
END $$;

DO $$
DECLARE
	q text;
BEGIN
	FOREACH q IN ARRAY ARRAY[
		'POLYGON((10 10, 20 10, 20 20, 10 20, 10 10))',
		'POLYGON((0 0, 1 0, 1 1, 0 1, 0 0))',
		'LINESTRING(49.5 49.5, 50.5 50.5)',
		'POINT(50 50)',
		'POLYGON((-5 -5, 200 -5, 200 200, -5 200, -5 -5))',
		'POINT(1000 1000)'
	] LOOP
		PERFORM same_results(format($q$SELECT id FROM shapes WHERE s && %L ORDER BY id$q$, q));
		PERFORM same_results(format($q$SELECT id FROM shapes WHERE s @ %L ORDER BY id$q$, q));
		PERFORM same_results(format($q$SELECT id FROM shapes WHERE s ~ %L ORDER BY id$q$, q));
		-- k-NN: the order of distances must match (ties may come in any order).
		PERFORM same_results(format($q$SELECT round((s <-> %L)::numeric, 9) AS d FROM shapes ORDER BY s <-> %L LIMIT 25$q$, q, q));
	END LOOP;
	ASSERT same_results($q$SELECT id FROM shapes WHERE s && 'POLYGON((-5 -5, 200 -5, 200 200, -5 200, -5 -5))' ORDER BY id$q$) =
		(SELECT count(*) FROM shapes WHERE s IS NOT NULL);
	ASSERT (SELECT count(*) FROM shapes WHERE s ~ 'POINT(50 50)') >= 20;
END $$;

-- REINDEX rebuilds it; an index created with buffering exercises the
-- buffered build.
REINDEX INDEX shapes_s;
CREATE INDEX shapes_s2 ON shapes USING gist (s) WITH (buffering = on);
DROP INDEX shapes_s2;
DO $$
BEGIN
	PERFORM same_results($q$SELECT id FROM shapes WHERE s && 'POLYGON((10 10, 20 10, 20 20, 10 20, 10 10))' ORDER BY id$q$);
END $$;
