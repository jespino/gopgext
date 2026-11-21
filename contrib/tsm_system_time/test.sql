-- Checks of the tsm_system_time port (upstream's regression test runs with
-- pg_regress, see README.md).
CREATE EXTENSION IF NOT EXISTS tsm_system_time;

CREATE TABLE t AS SELECT i, repeat('x', 200) AS pad FROM generate_series(1, 10000) i;
ANALYZE t;

DO $$
BEGIN
	ASSERT (SELECT count(*) FROM t TABLESAMPLE system_time (0)) = 0;
	ASSERT (SELECT count(*) FROM t TABLESAMPLE system_time (100000)) = 10000;
	ASSERT (SELECT count(DISTINCT i) FROM t TABLESAMPLE system_time (100000)) = 10000;
	BEGIN
		PERFORM * FROM t TABLESAMPLE system_time (-1);
		RAISE 'expected an error';
	EXCEPTION WHEN invalid_tablesample_argument THEN
	END;
	BEGIN
		PERFORM * FROM t TABLESAMPLE system_time ('NaN');
		RAISE 'expected an error';
	EXCEPTION WHEN invalid_tablesample_argument THEN
	END;
	BEGIN
		PERFORM * FROM t TABLESAMPLE system_time (10) REPEATABLE (1);
		RAISE 'expected an error';
	EXCEPTION WHEN feature_not_supported THEN
	END;
END $$;

CREATE TABLE empty_t(i int);
SELECT count(*) FROM empty_t TABLESAMPLE system_time (10);
