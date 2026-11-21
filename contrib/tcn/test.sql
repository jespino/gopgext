-- PostgreSQL's isolation spec for contrib/tcn (specs/tcn.spec), as a psql
-- script: psql prints the notifications a session receives after each
-- command, so they go to a temporary file (\o) that the server reads back
-- (pg_read_file: the test runs as superuser on the same machine). The
-- upstream spec itself runs with pg_isolation_regress, see README.md.
CREATE EXTENSION IF NOT EXISTS tcn;

CREATE TABLE mytable (key int PRIMARY KEY, value text);
CREATE TRIGGER tcntrig AFTER INSERT OR UPDATE OR DELETE ON mytable
	FOR EACH ROW EXECUTE FUNCTION triggered_change_notification(mychannel);
-- Default channel, composite key with quotes in names and values.
CREATE TABLE "my""table" ("k""1" text, k2 int, v text, PRIMARY KEY ("k""1", k2));
CREATE TRIGGER tcntrig AFTER INSERT OR UPDATE OR DELETE ON "my""table"
	FOR EACH ROW EXECUTE FUNCTION triggered_change_notification();

\set out `mktemp`
\o :out
LISTEN mychannel;
LISTEN tcn;
INSERT INTO mytable VALUES(1, 'one');
INSERT INTO mytable VALUES(2, 'two');
UPDATE mytable SET value = 'foo' WHERE key = 2;
DELETE FROM mytable;
INSERT INTO "my""table" VALUES ('it''s', 7, 'x');
UPDATE "my""table" SET k2 = 8;
BEGIN;
INSERT INTO mytable VALUES(3, 'rolled back');
ROLLBACK;
UNLISTEN *;
\o

SELECT set_config('tcn_test.got', string_agg(regexp_replace(l, ' from server process with PID [0-9]+', ''), E'\n'), false) IS NULL
	FROM regexp_split_to_table(pg_read_file(:'out'), E'\n') l WHERE l LIKE 'Asynchronous notification%';
\! rm -f :out
DO $$
DECLARE
	want text := $want$Asynchronous notification "mychannel" with payload ""mytable",I,"key"='1'" received.
Asynchronous notification "mychannel" with payload ""mytable",I,"key"='2'" received.
Asynchronous notification "mychannel" with payload ""mytable",U,"key"='2'" received.
Asynchronous notification "mychannel" with payload ""mytable",D,"key"='1'" received.
Asynchronous notification "mychannel" with payload ""mytable",D,"key"='2'" received.
Asynchronous notification "tcn" with payload ""my""table",I,"k""1"='it''s',"k2"='7'" received.
Asynchronous notification "tcn" with payload ""my""table",U,"k""1"='it''s',"k2"='7'" received.$want$;
BEGIN
	IF current_setting('tcn_test.got') IS DISTINCT FROM want THEN
		RAISE 'notifications:% want:%', E'\n' || current_setting('tcn_test.got'), E'\n' || want;
	END IF;
END $$;
