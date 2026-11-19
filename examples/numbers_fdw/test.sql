-- gopgext/fdw, offline: a foreign data wrapper over numbers generated in Go.
CREATE EXTENSION numbers_fdw;
CREATE SERVER s FOREIGN DATA WRAPPER numbers_fdw;
CREATE FOREIGN TABLE numbers (n bigint, square bigint, even bool, word text) SERVER s;
CREATE FOREIGN TABLE small (n bigint, half float8, at timestamptz, every interval, info jsonb, bytes bytea,
	num bigint OPTIONS (label 'n')) SERVER s OPTIONS (max '5');

CREATE FUNCTION plan_of(q text, what text) RETURNS text AS $$
DECLARE
	plan json;
BEGIN
	EXECUTE 'EXPLAIN (ANALYZE, FORMAT JSON, COSTS OFF, TIMING OFF, SUMMARY OFF) ' || q INTO plan;
	RETURN jsonb_path_query_first(plan::jsonb, ('strict $.**."' || what || '"')::jsonpath) #>> '{}';
END $$ LANGUAGE plpgsql;

CREATE FUNCTION err(q text) RETURNS text AS $$
BEGIN
	EXECUTE q;
	RETURN 'no error';
EXCEPTION WHEN OTHERS THEN
	RETURN SQLSTATE || ': ' || SQLERRM;
END $$ LANGUAGE plpgsql;

DO $$
DECLARE
	r record;
BEGIN
	-- pushdown: only n in [10, 20] is generated
	ASSERT (SELECT array_agg(n ORDER BY n) FROM numbers WHERE n BETWEEN 10 AND 12) = '{10,11,12}';
	ASSERT plan_of('SELECT * FROM numbers WHERE n >= 10 AND n < 21', 'Remote Request') = 'n from 10 to 20';
	ASSERT plan_of('SELECT * FROM numbers WHERE n >= 10 AND n < 21', 'Remote Filters') = 'n >= 10, n < 21';
	ASSERT (SELECT sum(square) FROM numbers WHERE n <= 10) = 385;
	ASSERT (SELECT count(*) FROM numbers WHERE n <= 1000 AND even) = 500;   -- even: checked locally
	ASSERT (SELECT word FROM numbers WHERE n = 7) = 'seven';
	ASSERT (SELECT count(*) FROM numbers WHERE n = NULL::bigint) = 0;
	ASSERT (SELECT count(*) FROM numbers WHERE n <= 5 AND n <> 3) = 4;      -- <> not pushed: local
	ASSERT (SELECT count(*) FROM numbers WHERE n <= (SELECT 3)) = 3;        -- a parameter
	ASSERT (SELECT count(*) FROM numbers WHERE n <= length(current_setting('application_name')) + 2) >= 2;

	-- the value conversions, the max option, a column option
	SELECT * INTO r FROM small WHERE n = 3;
	ASSERT r.half = 1.5 AND r.at = '1970-01-01 03:00:00+00' AND r.every = '3 minutes'
		AND r.info = '{"n": 3, "word": "three"}' AND r.bytes = '\x0300' AND r.num = 3, r::text;
	ASSERT (SELECT count(*) FROM small) = 5;

	-- an upper bound is required: a missing one is an error (with a hint)
	ASSERT err('SELECT * FROM numbers') = 'HV004: numbers_fdw: an upper bound on n is required', err('SELECT * FROM numbers');
	ASSERT err('SELECT * FROM numbers WHERE n > 5') LIKE 'HV004:%';

	-- options
	ASSERT err($q$CREATE FOREIGN TABLE x (n bigint) SERVER s OPTIONS (max '0')$q$)
		= 'HV024: invalid value for option "max": use a positive integer';
	ASSERT err($q$CREATE FOREIGN TABLE x (n bigint) SERVER s OPTIONS (bogus '1')$q$) = 'HV00D: invalid option "bogus"';
	ASSERT err($q$CREATE FOREIGN TABLE x (n bigint OPTIONS (max '1')) SERVER s$q$) = 'HV00D: invalid option "max"';
	ASSERT err($q$CREATE FOREIGN DATA WRAPPER w2 HANDLER numbers_fdw_handler VALIDATOR numbers_fdw_validator OPTIONS (max '1')$q$)
		LIKE 'HV00D: invalid option "max"%';
END $$;

-- a user mapping option (start), for the current user
CREATE USER MAPPING FOR CURRENT_USER SERVER s OPTIONS (start '4');
DO $$
BEGIN
	ASSERT (SELECT array_agg(n ORDER BY n) FROM numbers WHERE n <= 6) = '{4,5,6}';
END $$;
DROP USER MAPPING FOR CURRENT_USER SERVER s;

-- joins: the upper bound comes from the other table (parameterized nested
-- loop: the only possible plan)
CREATE TABLE limits (name text, top bigint);
INSERT INTO limits VALUES ('three', 3), ('five', 5), ('none', NULL);
ANALYZE limits;
DO $$
BEGIN
	ASSERT plan_of('SELECT * FROM limits l JOIN numbers x ON x.n <= l.top', 'Remote Requests') = '3';
	ASSERT (SELECT array_agg(name || ':' || total ORDER BY name) FROM (
		SELECT l.name, sum(x.n) AS total FROM limits l JOIN numbers x ON x.n <= l.top GROUP BY 1) s) = '{five:15,three:6}';
	ASSERT (SELECT array_agg(c ORDER BY name) FROM limits l, LATERAL (SELECT count(*) AS c FROM numbers x
		WHERE x.n BETWEEN l.top - 1 AND l.top) c) = '{2,0,2}';
	-- equality join through an equivalence class, with a local table
	ASSERT (SELECT array_agg(x.word ORDER BY l.top) FROM limits l JOIN numbers x ON x.n = l.top) = '{three,five}';
END $$;

-- IMPORT FOREIGN SCHEMA
CREATE SCHEMA imported;
IMPORT FOREIGN SCHEMA whatever FROM SERVER s INTO imported;
DO $$
BEGIN
	ASSERT (SELECT max(square) FROM imported.whatever WHERE n <= 4) = 16;
END $$;

-- cancellation while generating rows (statement_timeout)
SET statement_timeout = '300ms';
DO $$
BEGIN
	PERFORM count(*) FROM numbers WHERE n <= 1000000000;
	RAISE 'expected a timeout';
EXCEPTION WHEN query_canceled THEN
END $$;
RESET statement_timeout;
