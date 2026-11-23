-- The worker connects to ticker.database (default postgres): test there.
\c postgres
CREATE EXTENSION ticker;
DO $$
DECLARE
	pid int;
BEGIN
	-- The worker starts with the server.
	FOR i IN 1..100 LOOP
		pid := ticker_pid();
		EXIT WHEN pid <> 0;
		PERFORM pg_sleep(0.05);
	END LOOP;
	IF pid = 0 THEN
		RAISE 'ticker worker not running';
	END IF;
	IF NOT EXISTS (SELECT FROM pg_stat_activity a WHERE a.pid = ticker_pid() AND backend_type = 'ticker') THEN
		RAISE 'ticker worker not in pg_stat_activity';
	END IF;
END $$;
-- Waking it up makes it tick at once (the interval is a minute).
SELECT ticker_wake();
DO $$
BEGIN
	FOR i IN 1..100 LOOP
		EXIT WHEN ticker_count() > 0;
		PERFORM pg_sleep(0.05);
	END LOOP;
	IF ticker_count() = 0 THEN
		RAISE 'the worker did not tick when woken up';
	END IF;
END $$;
SELECT ticker_wake();
DO $$
BEGIN
	FOR i IN 1..100 LOOP
		EXIT WHEN ticker_count() > 1;
		PERFORM pg_sleep(0.05);
	END LOOP;
	IF ticker_count() < 2 THEN
		RAISE 'the worker did not tick a second time';
	END IF;
END $$;
-- The worker absorbs procsignal barriers while it waits: DROP DATABASE
-- waits for every process to (it would hang otherwise).
SET statement_timeout = '20s';
CREATE DATABASE ticker_tmp;
DROP DATABASE ticker_tmp;
RESET statement_timeout;
-- SIGTERM asks the worker to stop: Wait returns false and it logs it.
SELECT pg_terminate_backend(ticker_pid());
SELECT pg_sleep(0.5);
DROP EXTENSION ticker;
