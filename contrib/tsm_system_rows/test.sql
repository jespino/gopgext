-- Checks of the tsm_system_rows port (upstream's regression test runs with
-- pg_regress, see README.md).
CREATE EXTENSION IF NOT EXISTS tsm_system_rows;

CREATE TABLE t AS SELECT i, repeat('x', 200) AS pad FROM generate_series(1, 10000) i;
ANALYZE t;

DO $$
DECLARE
	n bigint;
	plan text;
BEGIN
	FOREACH n IN ARRAY ARRAY[0, 1, 37, 500, 9999, 10000, 20000] LOOP
		ASSERT (SELECT count(*) FROM t TABLESAMPLE system_rows (n)) = least(n, 10000), n::text;
	END LOOP;
	-- distinct rows, from random places
	ASSERT (SELECT count(DISTINCT i) FROM t TABLESAMPLE system_rows (1000)) = 1000;
	CREATE TEMP TABLE firsts(m int);
	FOR k IN 1..20 LOOP
		INSERT INTO firsts SELECT min(i) FROM t TABLESAMPLE system_rows (100);
	END LOOP;
	ASSERT (SELECT count(DISTINCT m) FROM firsts) > 1;
	-- rescans (a join) give the same sample
	ASSERT (SELECT count(*) FROM generate_series(1, 5) g,
		LATERAL (SELECT count(*) c FROM t TABLESAMPLE system_rows (300)) s WHERE c = 300) = 5;
	-- planner estimate
	EXECUTE 'EXPLAIN SELECT * FROM t TABLESAMPLE system_rows (123)' INTO plan;
	ASSERT plan LIKE '%rows=123 %', plan;
	BEGIN
		PERFORM * FROM t TABLESAMPLE system_rows (-1);
		RAISE 'expected an error';
	EXCEPTION WHEN invalid_tablesample_argument THEN
	END;
	-- an error in the middle of a query leaves no state behind
	BEGIN
		PERFORM 1 / (i - i) FROM t TABLESAMPLE system_rows (10);
		RAISE 'expected an error';
	EXCEPTION WHEN division_by_zero THEN
	END;
	ASSERT (SELECT count(*) FROM t TABLESAMPLE system_rows (10)) = 10;
END $$;

CREATE TABLE empty_t(i int);
SELECT count(*) FROM empty_t TABLESAMPLE system_rows (10);
