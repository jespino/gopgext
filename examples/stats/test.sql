CREATE EXTENSION stats;

CREATE TABLE m (g int, x float8, t text);
INSERT INTO m
	SELECT i % 100, (i * 7919) % 10007 / 7.0, 'v' || ((i * 31) % 17) % 5
	FROM generate_series(1, 100000) i;
INSERT INTO m VALUES (1000, NULL, NULL), (1001, NULL, NULL), (1001, 5, NULL);

-- The Go aggregates agree with the built-in ones.
DO $$
BEGIN
	ASSERT (SELECT median(x) FROM m) = (SELECT percentile_cont(0.5) WITHIN GROUP (ORDER BY x) FROM m);
	ASSERT (SELECT percentile(x, 0.9) FROM m) = (SELECT percentile_cont(0.9) WITHIN GROUP (ORDER BY x) FROM m);
	ASSERT (SELECT mode_of(t) FROM m) = (SELECT mode() WITHIN GROUP (ORDER BY t) FROM m);
	ASSERT (SELECT median(x) FROM m WHERE false) IS NULL;
	ASSERT (SELECT median(x) FROM m WHERE g = 1000) IS NULL;
	ASSERT (SELECT median(x) FROM m WHERE g = 1001) = 5;
	ASSERT (SELECT median(v) FROM (VALUES (1.0), (2.0), (10.0), (20.0)) s(v)) = 6;
	-- Per group, sorted and hashed.
	SET LOCAL enable_hashagg = off;
	ASSERT NOT EXISTS (
		SELECT g FROM m GROUP BY g
		HAVING median(x) IS DISTINCT FROM percentile_cont(0.5) WITHIN GROUP (ORDER BY x)
		    OR mode_of(t) IS DISTINCT FROM mode() WITHIN GROUP (ORDER BY t));
	SET LOCAL enable_hashagg = on;
	SET LOCAL enable_sort = off;
	ASSERT (SELECT count(*) FROM (SELECT g, median(x) FROM m GROUP BY g) s) = 102;
	ASSERT NOT EXISTS (
		SELECT * FROM (SELECT g, median(x) AS a FROM m GROUP BY g) s1
		JOIN (SELECT g, percentile_cont(0.5) WITHIN GROUP (ORDER BY x) AS b FROM m GROUP BY g) s2 USING (g)
		WHERE a IS DISTINCT FROM b);
	-- As a window function.
	ASSERT (SELECT array_agg(med ORDER BY v) FROM
		(SELECT v, median(v) OVER (ORDER BY v) AS med FROM (VALUES (1.0), (3.0), (2.0), (10.0)) s(v)) w) = '{1,1.5,2,2.5}';
END $$;

-- States are freed with their aggregate (and after errors).
DO $$
BEGIN
	ASSERT live_states() = 0, live_states()::text;
END $$;
DO $$
BEGIN
	PERFORM percentile(x, 2) FROM m;
	RAISE 'expected an error';
EXCEPTION WHEN numeric_value_out_of_range THEN
	ASSERT SQLERRM = 'percentile 2 is not between 0 and 1', SQLERRM;
END $$;
DO $$
BEGIN
	ASSERT live_states() = 0, live_states()::text;
END $$;

-- Parallel aggregation: partial states are serialized in the workers and
-- combined in the leader.
SET parallel_setup_cost = 0;
SET parallel_tuple_cost = 0;
SET min_parallel_table_scan_size = 0;
SET max_parallel_workers_per_gather = 2;
DO $$
DECLARE
	plan text := '';
	line text;
BEGIN
	FOR line IN EXPLAIN (COSTS OFF) SELECT median(x) FROM m LOOP
		plan := plan || line || E'\n';
	END LOOP;
	ASSERT plan LIKE '%Partial Aggregate%' AND plan LIKE '%Gather%', plan;
	ASSERT (SELECT median(x) FROM m) = (SELECT percentile_cont(0.5) WITHIN GROUP (ORDER BY x) FROM m);
	ASSERT (SELECT array_agg(med ORDER BY g) FROM (SELECT g, median(x) AS med FROM m GROUP BY g) s) =
		(SELECT array_agg(med ORDER BY g) FROM
			(SELECT g, percentile_cont(0.5) WITHIN GROUP (ORDER BY x) AS med FROM m GROUP BY g) s);
END $$;
RESET parallel_setup_cost;
RESET parallel_tuple_cost;
RESET min_parallel_table_scan_size;
RESET max_parallel_workers_per_gather;

-- Selectivity: estimates from the statistics are close to the actual
-- numbers of rows.
CREATE TABLE n (v bigint, w bigint);
INSERT INTO n SELECT (i * 7919) % 10000, i % 500 FROM generate_series(1, 100000) i;
INSERT INTO n SELECT 4242, 7 FROM generate_series(1, 20000);
INSERT INTO n SELECT NULL, NULL FROM generate_series(1, 5000);
ANALYZE n;
CREATE TABLE n2 AS SELECT i::bigint AS v FROM generate_series(1, 2000) i;
ANALYZE n2;

CREATE FUNCTION estimate(q text) RETURNS float8 LANGUAGE plpgsql AS $$
DECLARE
	j json;
BEGIN
	EXECUTE 'EXPLAIN (FORMAT JSON) ' || q INTO j;
	RETURN (j->0->'Plan'->>'Plan Rows')::float8;
END $$;

DO $$
DECLARE
	q text;
	est float8;
	actual float8;
BEGIN
	FOREACH q IN ARRAY ARRAY[
		'SELECT * FROM n WHERE v ~~= 4242',
		'SELECT * FROM n WHERE v ~~= 100',
		'SELECT * FROM n WHERE 5000 ~~= v',
		'SELECT * FROM n WHERE v ~~= -500',
		'SELECT * FROM n WHERE w ~~= 7',
		'SELECT * FROM n JOIN n2 ON n.w ~~= n2.v'
	] LOOP
		est := estimate(q);
		EXECUTE 'SELECT count(*) FROM (' || q || ') s' INTO actual;
		ASSERT est BETWEEN actual / 2 - 10 AND actual * 2 + 10, format('%s: estimated %s, actual %s', q, est, actual);
	END LOOP;
	-- A NULL constant matches nothing; a non-constant gets the default guess.
	ASSERT estimate('SELECT * FROM n WHERE v ~~= NULL') <= 1;
	ASSERT estimate('SELECT * FROM n WHERE v ~~= w') = round(125000 * 0.005);
END $$;

DO $$
BEGIN
	PERFORM estimate('SELECT * FROM n WHERE v ~~~ 5');
	RAISE 'expected an error';
EXCEPTION WHEN object_not_in_prerequisite_state THEN
	ASSERT SQLERRM = 'no restriction selectivity estimator "unregistered_sel"', SQLERRM;
END $$;
