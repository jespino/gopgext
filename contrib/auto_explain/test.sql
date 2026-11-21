-- auto_explain: upstream's TAP test (t/001_auto_explain.pl) as SQL, and
-- more. Each check runs statements in a new session (psql, with \!) that
-- loads auto_explain through session_preload_libraries and logs plans at
-- NOTICE, so that its client output (in a file) holds them; check() then
-- matches that output.

SELECT current_database() AS db, '/tmp/auto_explain_' || current_setting('port') || '.out' AS out,
	current_setting('server_version_num')::int >= 150000 AS pg15,
	current_setting('server_version_num')::int >= 160000 AS pg16 \gset
\setenv PGDATABASE :db
\setenv AE_OUT :out
\setenv PGOPTIONS '-c session_preload_libraries=auto_explain -c auto_explain.log_min_duration=0 -c auto_explain.log_analyze=on -c auto_explain.log_level=notice'

CREATE OR REPLACE FUNCTION ae_check(what text, pattern text, present bool DEFAULT true) RETURNS void
LANGUAGE plpgsql AS $$
DECLARE
	log text := pg_read_file(current_setting('ae.out'));
BEGIN
	IF (log ~ pattern) <> present THEN
		RAISE EXCEPTION '%: % % in:%', what, CASE WHEN present THEN 'no' ELSE 'unexpected' END,
			pattern, E'\n' || log;
	END IF;
END $$;
SELECT set_config('ae.out', :'out', false);

-- Simple query.
\! psql -X -q -c 'SELECT * FROM pg_class;' >$AE_OUT 2>&1
SELECT ae_check('query text logged, text mode', 'Query Text: SELECT \* FROM pg_class;');
SELECT ae_check('no query parameters logged when none, text mode', 'Query Parameters:', false);
SELECT ae_check('sequential scan logged, text mode', 'Seq Scan on pg_class');
SELECT ae_check('duration', 'NOTICE:  duration: [0-9]+\.[0-9]{3} ms  plan:\nQuery Text:');
SELECT ae_check('analyze', 'actual time=');

-- Prepared query.
\! psql -X -q -c "PREPARE get_proc(name) AS SELECT * FROM pg_proc WHERE proname = \$1;" -c "EXECUTE get_proc('int4pl');" >$AE_OUT 2>&1
SELECT ae_check('prepared query text logged, text mode',
	'Query Text: PREPARE get_proc\(name\) AS SELECT \* FROM pg_proc WHERE proname = \$1;');
SELECT ae_check('index scan logged, text mode', 'Index Scan using pg_proc_proname_args_nsp_index on pg_proc');
\if :pg16
SELECT ae_check('query parameters logged, text mode', 'Query Parameters: \$1 = ''int4pl''');

-- Prepared query with truncated parameters.
\! PGOPTIONS="$PGOPTIONS -c auto_explain.log_parameter_max_length=3" psql -X -q -c "PREPARE get_type(name) AS SELECT * FROM pg_type WHERE typname = \$1;" -c "EXECUTE get_type('float8');" >$AE_OUT 2>&1
SELECT ae_check('prepared query text logged, text mode',
	'Query Text: PREPARE get_type\(name\) AS SELECT \* FROM pg_type WHERE typname = \$1;');
SELECT ae_check('query parameters truncated, text mode', 'Query Parameters: \$1 = ''flo\.\.\.''');
\endif

-- Prepared query with parameter logging disabled.
\! PGOPTIONS="$PGOPTIONS -c auto_explain.log_parameter_max_length=0" psql -X -q -c "PREPARE get_type(name) AS SELECT * FROM pg_type WHERE typname = \$1;" -c "EXECUTE get_type('float8');" >$AE_OUT 2>&1
SELECT ae_check('prepared query text logged, text mode',
	'Query Text: PREPARE get_type\(name\) AS SELECT \* FROM pg_type WHERE typname = \$1;');
SELECT ae_check('query parameters not logged when disabled, text mode', 'Query Parameters:', false);

-- Query Identifier (printed by auto_explain since PostgreSQL 16).
\! PGOPTIONS="$PGOPTIONS -c auto_explain.log_verbose=on -c compute_query_id=on" psql -X -q -c 'SELECT * FROM pg_class;' >$AE_OUT 2>&1
\if :pg16
SELECT ae_check('query identifier logged with compute_query_id=on, text mode', 'Query Identifier:');
\endif
SELECT ae_check('verbose', 'Output: oid, relname');
\! PGOPTIONS="$PGOPTIONS -c auto_explain.log_verbose=on -c compute_query_id=regress" psql -X -q -c 'SELECT * FROM pg_class;' >$AE_OUT 2>&1
SELECT ae_check('query identifier not logged with compute_query_id=regress, text mode', 'Query Identifier:', false);

-- JSON format: an object, not an array.
\! PGOPTIONS="$PGOPTIONS -c auto_explain.log_format=json" psql -X -q -c 'SELECT * FROM pg_class;' >$AE_OUT 2>&1
SELECT ae_check('query text logged, json mode', '"Query Text": "SELECT \* FROM pg_class;"');
SELECT ae_check('query parameters not logged when none, json mode', '"Query Parameters":', false);
SELECT ae_check('sequential scan logged, json mode', '"Node Type": "Seq Scan"[^}]*"Relation Name": "pg_class"');
SELECT ae_check('json object', 'plan:\n\{\n  "Query Text"');
SELECT ae_check('json object end', '\n\}\n');

-- Prepared query in JSON format.
\! PGOPTIONS="$PGOPTIONS -c auto_explain.log_format=json" psql -X -q -c "PREPARE get_class(name) AS SELECT * FROM pg_class WHERE relname = \$1;" -c "EXECUTE get_class('pg_class');" >$AE_OUT 2>&1
SELECT ae_check('prepared query text logged, json mode',
	'"Query Text": "PREPARE get_class\(name\) AS SELECT \* FROM pg_class WHERE relname = \$1;"');
SELECT ae_check('index scan logged, json mode', '"Node Type": "Index Scan"[^}]*"Index Name": "pg_class_relname_nsp_index"');

-- XML and YAML.
\! PGOPTIONS="$PGOPTIONS -c auto_explain.log_format=xml" psql -X -q -c 'SELECT 1;' >$AE_OUT 2>&1
SELECT ae_check('xml', '<explain xmlns="http://www.postgresql.org/[0-9]+/explain">.*<Node-Type>Result</Node-Type>');
\! PGOPTIONS="$PGOPTIONS -c auto_explain.log_format=yaml" psql -X -q -c 'SELECT 1;' >$AE_OUT 2>&1
SELECT ae_check('yaml', 'Query Text: "SELECT 1;"\nPlan: \n  Node Type: "Result"');

-- Timing off: rows only. Buffers, WAL, settings, triggers.
CREATE TABLE IF NOT EXISTS ae_t (a int);
CREATE OR REPLACE FUNCTION ae_trig() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RETURN NEW; END $$;
CREATE OR REPLACE TRIGGER ae_trig BEFORE INSERT ON ae_t FOR EACH ROW EXECUTE FUNCTION ae_trig();
\! PGOPTIONS="$PGOPTIONS -c auto_explain.log_timing=off -c auto_explain.log_buffers=on -c auto_explain.log_wal=on -c auto_explain.log_triggers=on -c auto_explain.log_settings=on -c work_mem=1234kB" psql -X -q -c 'INSERT INTO ae_t SELECT generate_series(1, 10);' >$AE_OUT 2>&1
SELECT ae_check('timing off', 'actual rows=10(\.00)? loops=1');
SELECT ae_check('timing off: no time', 'actual time=', false);
SELECT ae_check('wal', 'WAL: records=');
SELECT ae_check('triggers', 'Trigger ae_trig: calls=10');
SELECT ae_check('settings', 'Settings: .*work_mem = ''1234kB''');
\! PGOPTIONS="$PGOPTIONS -c auto_explain.log_buffers=on" psql -X -q -c 'SELECT count(*) FROM ae_t;' >$AE_OUT 2>&1
SELECT ae_check('buffers', 'Buffers: shared hit=');

-- Without log_analyze: plans only.
\! PGOPTIONS="$PGOPTIONS -c auto_explain.log_analyze=off" psql -X -q -c 'SELECT count(*) FROM ae_t;' >$AE_OUT 2>&1
SELECT ae_check('no analyze', 'Aggregate  \(cost=[^)]*\)\n');
SELECT ae_check('no analyze: no actual', 'actual', false);

-- Nested statements: a function's queries (executor level 1) only with
-- log_nested_statements; statements of a DO block are at executor level
-- 0, as for upstream.
CREATE OR REPLACE FUNCTION ae_count() RETURNS bigint LANGUAGE plpgsql AS $$
BEGIN RETURN (SELECT count(*) FROM ae_t WHERE a > 5); END $$;
\! psql -X -q -c 'SELECT ae_count();' >$AE_OUT 2>&1
SELECT ae_check('top level', 'Query Text: SELECT ae_count\(\);');
SELECT ae_check('nested not logged', 'WHERE a > 5', false);
\! PGOPTIONS="$PGOPTIONS -c auto_explain.log_nested_statements=on" psql -X -q -c 'SELECT ae_count();' >$AE_OUT 2>&1
SELECT ae_check('nested logged', 'Query Text: \(SELECT count\(\*\) FROM ae_t WHERE a > 5\)');
\! psql -X -q -c 'DO $$ BEGIN PERFORM count(*) FROM ae_t WHERE a > 7; END $$;' >$AE_OUT 2>&1
SELECT ae_check('DO block statement at level 0', 'Query Text: SELECT count\(\*\) FROM ae_t WHERE a > 7');
\! psql -X -q -c 'CREATE TEMP TABLE ae_x AS SELECT * FROM ae_t WHERE a > 8;' >$AE_OUT 2>&1
SELECT ae_check('CREATE TABLE AS query at level 0', 'Seq Scan on ae_t');

-- EXPLAIN: the explained statement isn't run (no ANALYZE output).
\! psql -X -q -c 'EXPLAIN SELECT * FROM ae_t WHERE a = 3;' >$AE_OUT 2>&1
SELECT ae_check('explain', 'Query Text: EXPLAIN SELECT');
SELECT ae_check('explain not analyzed', 'actual', false);

-- Sampling and thresholds.
\! PGOPTIONS="$PGOPTIONS -c auto_explain.sample_rate=0" psql -X -q -c 'SELECT 1;' >$AE_OUT 2>&1
SELECT ae_check('sample_rate = 0', 'plan:', false);
\! PGOPTIONS="$PGOPTIONS -c auto_explain.log_min_duration=-1" psql -X -q -c 'SELECT 1;' >$AE_OUT 2>&1
SELECT ae_check('log_min_duration = -1', 'plan:', false);
\! PGOPTIONS="$PGOPTIONS -c auto_explain.log_min_duration=1min" psql -X -q -c 'SELECT 1;' >$AE_OUT 2>&1
SELECT ae_check('log_min_duration = 1min', 'plan:', false);
\! PGOPTIONS="$PGOPTIONS -c auto_explain.log_min_duration=100ms" psql -X -q -c 'SELECT pg_sleep(0.2);' >$AE_OUT 2>&1
SELECT ae_check('log_min_duration = 100ms', 'duration: [0-9.]+ ms  plan:\nQuery Text: SELECT pg_sleep');

-- log_level: hidden at debug1 (client_min_messages = notice).
\! PGOPTIONS="$PGOPTIONS -c auto_explain.log_level=debug1" psql -X -q -c 'SELECT 1;' >$AE_OUT 2>&1
SELECT ae_check('log_level = debug1', 'plan:', false);
\! PGOPTIONS="$PGOPTIONS -c auto_explain.log_level=debug -c client_min_messages=debug2" psql -X -q -c 'SELECT 1;' >$AE_OUT 2>&1
SELECT ae_check('log_level = debug', 'DEBUG:  duration: ');

-- A non-superuser can set PGC_SUSET settings only if granted (15+).
\if :pg15
CREATE USER regress_user1;
GRANT SET ON PARAMETER auto_explain.log_format TO regress_user1;
ALTER ROLE regress_user1 SET session_preload_libraries = 'auto_explain';
ALTER ROLE regress_user1 SET auto_explain.log_min_duration = 0;
ALTER ROLE regress_user1 SET auto_explain.log_level = notice;
\! PGOPTIONS='-c auto_explain.log_format=json' psql -X -q -U regress_user1 -c 'SELECT * FROM pg_database;' >$AE_OUT 2>&1
SELECT ae_check('query text logged, json mode selected by non-superuser', '"Query Text": "SELECT \* FROM pg_database;"');
\! PGOPTIONS='-c auto_explain.log_analyze=on' psql -X -q -U regress_user1 -c 'SELECT 1;' >$AE_OUT 2>&1
SELECT ae_check('permission failure', 'permission denied to set parameter "auto_explain\.log_analyze"');
REVOKE SET ON PARAMETER auto_explain.log_format FROM regress_user1;
DROP OWNED BY regress_user1;
DROP USER regress_user1;
\endif

-- Settings: units, ranges, the reserved prefix.
LOAD 'auto_explain';
SET auto_explain.log_min_duration = '2s';
DO $$ BEGIN ASSERT current_setting('auto_explain.log_min_duration') = '2s'; END $$;
SET auto_explain.log_parameter_max_length = '1kB';
DO $$ BEGIN ASSERT current_setting('auto_explain.log_parameter_max_length') = '1kB'; END $$;
DO $$
BEGIN
	SET auto_explain.sample_rate = 2;
	RAISE EXCEPTION 'sample_rate 2 accepted';
EXCEPTION WHEN invalid_parameter_value THEN
END $$;
\if :pg15
DO $$
BEGIN
	SET auto_explain.bogus = 1;
	RAISE EXCEPTION 'reserved prefix not enforced';
EXCEPTION WHEN invalid_name OR undefined_object THEN
END $$;
\endif
DO $$ BEGIN ASSERT current_setting('auto_explain.log_level') = 'log'; END $$;
RESET ALL;

DROP TABLE ae_t;
DROP FUNCTION ae_count, ae_trig, ae_check;
\! rm -f $AE_OUT
