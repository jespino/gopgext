-- Checks raise errors (see test/run.sh).
CREATE EXTENSION IF NOT EXISTS cube;

CREATE FUNCTION pg_temp.check(ok boolean, what text) RETURNS void LANGUAGE plpgsql AS $$
BEGIN
	IF ok IS NOT TRUE THEN
		RAISE EXCEPTION 'check failed: %', what;
	END IF;
END $$;

SELECT pg_temp.check('(1,2),(3,4)'::cube::text = '(1, 2),(3, 4)', 'output');
SELECT pg_temp.check('[(1,2),(1,2)]'::cube::text = '(1, 2)', 'point');
SELECT pg_temp.check(cube_dim('(1,2,3)') = 3, 'dim');
SELECT pg_temp.check('(0,0),(2,2)'::cube @> '(1,1)'::cube, 'contains');
SELECT pg_temp.check('(0),(1)'::cube <-> '(3)'::cube = 2, 'distance');
SELECT pg_temp.check(('(1,2),(3,4)'::cube ~> 4) = 4, '~>');
SELECT pg_temp.check(cube_send('(1),(2)') = '\x000000013ff00000000000004000000000000000'::bytea, 'send');
CREATE TEMP TABLE bin (c cube);
COPY (SELECT '(1,2),(3,4)'::cube UNION ALL SELECT '(5,6)') TO '/tmp/gopgext_cube.bin' (FORMAT binary);
COPY bin FROM '/tmp/gopgext_cube.bin' (FORMAT binary);
SELECT pg_temp.check(array_agg(c::text) = '{"(1, 2),(3, 4)","(5, 6)"}', 'binary round trip') FROM bin;

-- Errors.
DO $$
BEGIN
	PERFORM '(1,2'::cube;
	RAISE EXCEPTION 'no error';
EXCEPTION WHEN invalid_text_representation THEN
	PERFORM pg_temp.check(SQLERRM = 'invalid input syntax for cube', SQLERRM);
END $$;

-- GiST: index scans and k-NN match sequential scans.
CREATE TABLE cubes AS
	SELECT i, cube(array[(i * 7919) % 1000, (i * 104729) % 1000]::float8[],
	               array[(i * 7919) % 1000 + i % 7, (i * 104729) % 1000 + i % 5]::float8[]) AS c
	FROM generate_series(1, 5000) i;
CREATE INDEX ON cubes USING gist (c);
ANALYZE cubes;
CREATE TEMP TABLE seq AS SELECT
	(SELECT array_agg(i ORDER BY i) FROM cubes WHERE c && '(100,100),(200,200)') AS o,
	(SELECT array_agg(i ORDER BY i) FROM cubes WHERE c <@ '(100,100),(300,300)') AS w,
	(SELECT array_agg(c <-> '(500,500)') FROM (SELECT c FROM cubes ORDER BY c <-> '(500,500)' LIMIT 10) s) AS knn,
	(SELECT array_agg(c ~> 3 ORDER BY c ~> 3) FROM (SELECT c FROM cubes ORDER BY c ~> 3 LIMIT 10) s) AS coord;
SET enable_seqscan = off;
SET enable_bitmapscan = off;
SELECT pg_temp.check((SELECT array_agg(i ORDER BY i) FROM cubes WHERE c && '(100,100),(200,200)') = o, '&& index') FROM seq;
SELECT pg_temp.check((SELECT array_agg(i ORDER BY i) FROM cubes WHERE c <@ '(100,100),(300,300)') = w, '<@ index') FROM seq;
SELECT pg_temp.check((SELECT array_agg(c ~> 3) FROM (SELECT c FROM cubes ORDER BY c ~> 3 LIMIT 10) s) = coord, '~> knn') FROM seq;
SELECT pg_temp.check((SELECT array_agg(c <-> '(500,500)') FROM (SELECT c FROM cubes ORDER BY c <-> '(500,500)' LIMIT 10) s) = knn, '<-> knn') FROM seq;
DO $$
DECLARE
	plan text;
BEGIN
	FOR plan IN EXPLAIN SELECT c FROM cubes ORDER BY c ~> 3 LIMIT 10 LOOP
		IF plan LIKE '%Index%Scan using%' THEN
			RETURN;
		END IF;
	END LOOP;
	RAISE EXCEPTION 'k-NN by ~> does not use the index';
END $$;
RESET enable_seqscan;
RESET enable_bitmapscan;
DROP TABLE cubes;
