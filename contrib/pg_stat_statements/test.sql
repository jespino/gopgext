-- pg_stat_statements: normalization, counters, utility and nested
-- statements, planning, resets, privileges. The library is preloaded (see
-- the preload file).

CREATE EXTENSION pg_stat_statements;
SET compute_query_id = on;
SET pg_stat_statements.track_utility = off;
SELECT pg_stat_statements_reset() IS NOT NULL AS t;

-- Constants are replaced with $n; statements differing only in constants
-- share an entry.
SELECT 1 AS "int";
SELECT 2 AS "int";
SELECT 'hello'
  /* multi-line, with a comment */
  AS "text";
SELECT 1 + 1 AS "two", -3 AS neg;
-- Several statements in one string.
SELECT 'world' AS "text" \; SELECT 3 + 4 AS "two", -5 AS neg;
SELECT * FROM generate_series(1, 10) g;
PREPARE p(int) AS SELECT $1 * 2 AS x, 5 AS y;
EXECUTE p(1);
EXECUTE p(2);
DEALLOCATE p;
CREATE TABLE w (a int);
INSERT INTO w SELECT generate_series(1, 100);

DO $$
DECLARE
	n int;
BEGIN
	ASSERT (SELECT calls FROM pg_stat_statements WHERE query = 'SELECT $1 AS "int"') = 2;
	-- 'world' has the same query identifier: its entry has the first text.
	ASSERT (SELECT calls FROM pg_stat_statements WHERE query = E'SELECT $1\n  /* multi-line, with a comment */\n  AS "text"') = 2,
		(SELECT string_agg(query, ' | ') FROM pg_stat_statements);
	ASSERT (SELECT calls FROM pg_stat_statements WHERE query = 'SELECT $1 + $2 AS "two", $3 AS neg') = 2,
		(SELECT string_agg(query, ' | ') FROM pg_stat_statements);
	ASSERT (SELECT rows FROM pg_stat_statements WHERE query = 'SELECT * FROM generate_series($1, $2) g') = 10;
	-- Parameters of prepared statements are kept; constants are numbered
	-- after them. EXECUTE itself isn't tracked.
	ASSERT (SELECT calls FROM pg_stat_statements WHERE query = 'PREPARE p(int) AS SELECT $1 * $2 AS x, $3 AS y') = 2,
		(SELECT string_agg(query, ' | ') FROM pg_stat_statements);
	ASSERT NOT EXISTS (SELECT FROM pg_stat_statements WHERE query LIKE 'EXECUTE%' OR query LIKE 'DEALLOCATE%');
	ASSERT (SELECT rows FROM pg_stat_statements WHERE query LIKE 'INSERT INTO w%') = 100;
	ASSERT (SELECT wal_records > 0 AND wal_bytes > 0 FROM pg_stat_statements WHERE query LIKE 'INSERT INTO w%');
	-- Utility statements aren't tracked (track_utility off).
	ASSERT NOT EXISTS (SELECT FROM pg_stat_statements WHERE query LIKE 'CREATE%');
	-- Statistics.
	ASSERT (SELECT bool_and(total_exec_time >= 0 AND min_exec_time <= max_exec_time
		AND mean_exec_time BETWEEN min_exec_time AND max_exec_time AND stddev_exec_time >= 0
		AND plans = 0 AND toplevel AND userid = 'postgres'::regrole AND stats_since <= now()
		AND minmax_stats_since = stats_since AND shared_blks_hit >= 0)
		FROM pg_stat_statements);
	ASSERT (SELECT dealloc FROM pg_stat_statements_info) = 0;
	ASSERT (SELECT stats_reset FROM pg_stat_statements_info) <= now();
	ASSERT (SELECT count(*) FROM pg_stat_statements(false) WHERE query IS NOT NULL) = 0;
END $$;

-- Utility statements, planning, nested statements.
SELECT pg_stat_statements_reset() IS NOT NULL AS t;
SET pg_stat_statements.track_utility = on;
SET pg_stat_statements.track_planning = on;
SET pg_stat_statements.track = 'all';
CREATE TABLE u (a int);
CREATE FUNCTION add_rows(n int) RETURNS int LANGUAGE plpgsql AS $$
BEGIN
	INSERT INTO u SELECT generate_series(1, n);
	RETURN (SELECT count(*) FROM u);
END $$;
SELECT add_rows(5);
SELECT add_rows(7);
SELECT count(*) FROM u WHERE a > 3;
CREATE TABLE u2 AS SELECT * FROM u;
SET pg_stat_statements.track = 'top';
SET pg_stat_statements.track_planning = off;

DO $$
BEGIN
	ASSERT (SELECT calls FROM pg_stat_statements WHERE query = 'CREATE TABLE u (a int)') = 1;
	-- Nested statements (in a function) are tracked with track = all; the
	-- function's parameter is $1.
	ASSERT (SELECT calls FROM pg_stat_statements WHERE query = 'INSERT INTO u SELECT generate_series($2, n)' AND NOT toplevel) = 2,
		(SELECT string_agg(query || ':' || toplevel, ' | ') FROM pg_stat_statements);
	ASSERT (SELECT calls FROM pg_stat_statements WHERE query = 'SELECT add_rows($1)' AND toplevel) = 2;
	ASSERT (SELECT rows FROM pg_stat_statements WHERE query = 'SELECT add_rows($1)') = 2;
	-- Planning is counted for planned statements (not for utility ones).
	ASSERT (SELECT plans FROM pg_stat_statements WHERE query = 'SELECT count(*) FROM u WHERE a > $1') = 1;
	ASSERT (SELECT total_plan_time > 0 FROM pg_stat_statements WHERE query = 'SELECT count(*) FROM u WHERE a > $1');
	-- CREATE TABLE AS counts its rows, and its SELECT isn't tracked apart.
	ASSERT (SELECT rows FROM pg_stat_statements WHERE query = 'CREATE TABLE u2 AS SELECT * FROM u') = 12,
		(SELECT string_agg(query || ':' || rows, ' | ') FROM pg_stat_statements);
	ASSERT NOT EXISTS (SELECT FROM pg_stat_statements WHERE query = 'SELECT * FROM u' AND toplevel);
END $$;

-- Resets: by query identifier, min/max only, everything.
DO $$
DECLARE
	q bigint;
	t timestamptz;
BEGIN
	SELECT queryid INTO q FROM pg_stat_statements WHERE query = 'CREATE TABLE u (a int)';
	PERFORM pg_stat_statements_reset(0, 0, q);
	ASSERT NOT EXISTS (SELECT FROM pg_stat_statements WHERE queryid = q);
	ASSERT EXISTS (SELECT FROM pg_stat_statements WHERE query = 'SELECT add_rows($1)');
	SELECT queryid INTO q FROM pg_stat_statements WHERE query = 'SELECT add_rows($1)';
	t := pg_stat_statements_reset('postgres'::regrole, (SELECT oid FROM pg_database WHERE datname = current_database()), q, true);
	ASSERT (SELECT min_exec_time = 0 AND max_exec_time = 0 AND minmax_stats_since = t AND stats_since < t
		FROM pg_stat_statements WHERE queryid = q AND toplevel);
	ASSERT (SELECT calls FROM pg_stat_statements WHERE queryid = q AND toplevel) = 2;
END $$;
SELECT pg_stat_statements_reset() IS NOT NULL AS t;
SELECT 1;
DO $$
BEGIN
	ASSERT (SELECT count(*) FROM pg_stat_statements WHERE query <> 'SELECT pg_stat_statements_reset() IS NOT NULL AS t') = 1,
		(SELECT string_agg(query, ' | ') FROM pg_stat_statements);
	ASSERT (SELECT stats_reset > now() - interval '1 minute' FROM pg_stat_statements_info);
END $$;

-- Privileges: other users' query texts are hidden, reset needs a grant.
CREATE ROLE pgss_user;
GRANT SELECT ON u TO pgss_user;
SET ROLE pgss_user;
SELECT count(*) FROM u;
DO $$
BEGIN
	ASSERT (SELECT count(*) FROM pg_stat_statements WHERE query = '<insufficient privilege>' AND queryid IS NULL) >= 1;
	ASSERT (SELECT calls FROM pg_stat_statements WHERE query = 'SELECT count(*) FROM u') = 1;
END $$;
DO $$
BEGIN
	PERFORM pg_stat_statements_reset();
	RAISE 'expected an error';
EXCEPTION WHEN insufficient_privilege THEN
END $$;
RESET ROLE;
GRANT pg_read_all_stats TO pgss_user;
SET ROLE pgss_user;
DO $$
BEGIN
	ASSERT NOT EXISTS (SELECT FROM pg_stat_statements WHERE query = '<insufficient privilege>');
END $$;
RESET ROLE;
DROP TABLE u, u2, w;
DROP FUNCTION add_rows;
DROP ROLE pgss_user;

-- Settings.
DO $$
BEGIN
	ASSERT current_setting('pg_stat_statements.max') = '5000';
	ASSERT current_setting('pg_stat_statements.track') = 'top';
	ASSERT (SELECT context FROM pg_settings WHERE name = 'pg_stat_statements.max') = 'postmaster';
	ASSERT (SELECT context FROM pg_settings WHERE name = 'pg_stat_statements.save') = 'sighup';
	ASSERT (SELECT enumvals FROM pg_settings WHERE name = 'pg_stat_statements.track') = '{none,top,all}';
END $$;
DO $$
BEGIN
	SET pg_stat_statements.track = 'some';
	RAISE 'expected an error';
EXCEPTION WHEN invalid_parameter_value THEN
END $$;
