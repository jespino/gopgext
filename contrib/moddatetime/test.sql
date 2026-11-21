-- moddatetime.example from PostgreSQL's contrib/spi, as assertions.
CREATE EXTENSION IF NOT EXISTS moddatetime;

CREATE TABLE mdt (id int4, idesc text,
	moddate timestamp DEFAULT '2000-01-01' NOT NULL,
	modtz timestamptz DEFAULT '2000-01-01' NOT NULL);
CREATE TRIGGER mdt_moddatetime BEFORE UPDATE ON mdt
	FOR EACH ROW EXECUTE PROCEDURE moddatetime (moddate);
CREATE TRIGGER mdt_modtz BEFORE UPDATE ON mdt
	FOR EACH ROW EXECUTE PROCEDURE moddatetime (modtz);
INSERT INTO mdt VALUES (1, 'first'), (2, 'second'), (3, 'third');
DO $$ BEGIN ASSERT (SELECT bool_and(moddate = '2000-01-01') FROM mdt); END $$;
BEGIN;
UPDATE mdt SET id = 4 WHERE id = 1;
DO $$
BEGIN
	ASSERT (SELECT (moddate, modtz) = (localtimestamp, now()) FROM mdt WHERE id = 4);
	ASSERT (SELECT count(*) FROM mdt WHERE moddate = '2000-01-01') = 2;
END $$;
COMMIT;

-- errors
CREATE TABLE bad (d date);
CREATE TRIGGER t BEFORE UPDATE ON bad FOR EACH ROW EXECUTE PROCEDURE moddatetime (d);
INSERT INTO bad VALUES ('2000-01-01');
DO $$
BEGIN
	UPDATE bad SET d = d;
	RAISE 'expected an error';
EXCEPTION WHEN triggered_action_exception THEN
	ASSERT SQLERRM = 'attribute "d" of "bad" must be type TIMESTAMP or TIMESTAMPTZ', SQLERRM;
END $$;
DROP TRIGGER t ON bad;
CREATE TRIGGER t BEFORE INSERT ON bad FOR EACH ROW EXECUTE PROCEDURE moddatetime (d);
DO $$
BEGIN
	INSERT INTO bad VALUES ('2000-01-01');
	RAISE 'expected an error';
EXCEPTION WHEN internal_error THEN
	ASSERT SQLERRM = 'moddatetime: cannot process INSERT events', SQLERRM;
END $$;
DROP TABLE mdt, bad;
