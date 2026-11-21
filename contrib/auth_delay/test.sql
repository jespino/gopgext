-- auth_delay (preloaded, see the preload file): its setting, and the delay
-- of failed logins. A pg_hba.conf line asks regress_auth_delay for a
-- password; connections (psql run with \!) with a wrong one must take at
-- least auth_delay.milliseconds, others not. pg_hba.conf is restored at the
-- end.

DO $$ BEGIN ASSERT current_setting('auth_delay.milliseconds') = '0'; END $$;
DO $$
BEGIN
	SET auth_delay.milliseconds = 10;
	RAISE EXCEPTION 'auth_delay.milliseconds changed with SET';
EXCEPTION WHEN cant_change_runtime_param THEN
END $$;

SELECT current_database() AS db, current_setting('hba_file') AS hba,
	pg_read_file(current_setting('hba_file')) AS hba_orig \gset
\setenv PGDATABASE :db
SET client_min_messages = warning;
DROP ROLE IF EXISTS regress_auth_delay;
RESET client_min_messages;
CREATE ROLE regress_auth_delay LOGIN PASSWORD 'right';

-- Writes pg_hba.conf (CSV with characters that never appear as delimiter
-- and quote, to write lines as they are) and reloads it.
CREATE FUNCTION write_hba(content text) RETURNS void LANGUAGE plpgsql AS $$
BEGIN
	EXECUTE format('COPY (SELECT l FROM regexp_split_to_table(%L, E''\n'') l WHERE l <> '''') TO %L '
		'WITH (FORMAT csv, DELIMITER E''\x01'', QUOTE E''\x02'')',
		content, current_setting('hba_file'));
	PERFORM pg_reload_conf();
	PERFORM pg_sleep(0.5);
END $$;
SELECT write_hba('local all regress_auth_delay password' || E'\n' || :'hba_orig');

ALTER SYSTEM SET auth_delay.milliseconds = '1500ms';
SELECT pg_reload_conf();
SELECT pg_sleep(0.5);
DO $$ BEGIN ASSERT current_setting('auth_delay.milliseconds') = '1500ms'; END $$;

-- A wrong password: delayed.
SELECT clock_timestamp() AS t0 \gset
\! PGPASSWORD=wrong psql -X -q -U regress_auth_delay -c 'SELECT 1' 2>&1
SELECT clock_timestamp() - :'t0'::timestamptz AS failed \gset
-- The right password, and trust: not delayed.
SELECT clock_timestamp() AS t0 \gset
\! PGPASSWORD=right psql -X -q -U regress_auth_delay -c 'SELECT 1' >/dev/null 2>&1
\! psql -X -q -c 'SELECT 1' >/dev/null 2>&1
SELECT clock_timestamp() - :'t0'::timestamptz AS succeeded \gset

ALTER SYSTEM RESET auth_delay.milliseconds;
SELECT write_hba(:'hba_orig');
DROP FUNCTION write_hba;
DROP ROLE regress_auth_delay;

SELECT :'failed'::interval >= '1500ms' AS ok \gset
\if :ok
\else
\echo failed login took :failed
SELECT 1/0 AS failed_login_not_delayed;
\endif
SELECT :'succeeded'::interval < '1500ms' AS ok \gset
\if :ok
\else
\echo successful logins took :succeeded
SELECT 1/0 AS successful_login_delayed;
\endif
