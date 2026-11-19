CREATE EXTENSION textutil;

DO $$
BEGIN
	ASSERT (SELECT array_agg(m) FROM go_matches('a1 b22 c333', '[0-9]+') m) = ARRAY['1', '22', '333'];
	ASSERT (SELECT count(*) FROM go_matches('none', '[0-9]+')) = 0;
	BEGIN
		PERFORM go_matches('x', '(');
		RAISE 'expected an error';
	EXCEPTION WHEN invalid_parameter_value THEN
		ASSERT SQLERRM LIKE 'invalid regular expression:%', SQLERRM;
	END;
	ASSERT slugify('Héllo, World! Ñandú 2026') = 'hello-world-nandu-2026', slugify('Héllo, World! Ñandú 2026');
	ASSERT levenshtein('kitten', 'sitting') = 3;
	ASSERT levenshtein('', 'añb') = 3;
	ASSERT sha256_hex('abc') = encode(sha256('abc'), 'hex');
	ASSERT json_keys('{"b": 1, "a": [2], "c": {}}') = ARRAY['a', 'b', 'c'];
	ASSERT person_json('Ana', 30, ARRAY['x', 'y'])::jsonb = '{"name": "Ana", "age": 30, "tags": ["x", "y"]}'::jsonb;
	ASSERT person_json('Bo', 1, '{}') = '{"name":"Bo","age":1}', person_json('Bo', 1, '{}');
	BEGIN
		PERFORM json_keys('[1]');
		RAISE 'expected an error';
	EXCEPTION WHEN invalid_text_representation THEN
	END;
END $$;

-- a regexp cached across calls, many rows
SELECT count(*) = 10000 AS ok FROM generate_series(1, 10000) i, go_matches(i::text || 'x', 'x') m;
-- set-returning functions in a parallel query
SET max_parallel_workers_per_gather = 2;
SET parallel_setup_cost = 0;
SET parallel_tuple_cost = 0;
SET min_parallel_table_scan_size = 0;
CREATE TABLE words AS SELECT md5(i::text) AS w FROM generate_series(1, 50000) i;
ANALYZE words;
SELECT count(DISTINCT slugify(w)) = 50000 AS parallel_ok FROM words;
