-- Checks from PostgreSQL's regression test for contrib/lo (sql/lo.sql);
-- the full upstream test runs with pg_regress, see README.md.
CREATE EXTENSION IF NOT EXISTS lo;

CREATE FUNCTION lo_exists(o oid) RETURNS boolean
	LANGUAGE sql RETURN EXISTS (SELECT FROM pg_largeobject_metadata WHERE oid = o);

CREATE TABLE image (title text, raster lo);
CREATE TRIGGER t_raster BEFORE UPDATE OR DELETE ON image
	FOR EACH ROW EXECUTE PROCEDURE lo_manage(raster);

SELECT lo_create(43213), lo_create(43214), lo_create(43215);
INSERT INTO image (title, raster) VALUES ('beautiful image', 43213), ('null', NULL);
UPDATE image SET raster = 43214 WHERE title = 'beautiful image';
DO $$ BEGIN ASSERT NOT lo_exists(43213) AND lo_exists(43214); END $$;
-- an unrelated column, or the same value, keeps the object
UPDATE image SET title = 'beautiful picture' WHERE title = 'beautiful image';
UPDATE image SET raster = raster;
DO $$ BEGIN ASSERT lo_exists(43214); END $$;
-- a NULL replacing it
UPDATE image SET raster = 43215 WHERE title = 'null';
UPDATE image SET raster = NULL WHERE title = 'null';
DO $$ BEGIN ASSERT NOT lo_exists(43215); END $$;
DELETE FROM image;
DO $$ BEGIN ASSERT NOT lo_exists(43214); END $$;

-- AFTER, deferred
DROP TRIGGER t_raster ON image;
CREATE CONSTRAINT TRIGGER t_raster AFTER UPDATE OR DELETE ON image
	DEFERRABLE INITIALLY DEFERRED
	FOR EACH ROW EXECUTE PROCEDURE lo_manage(raster);
SELECT lo_create(43223), lo_create(43224), lo_create(43225);
INSERT INTO image (title, raster) VALUES ('beautiful image', 43223);
BEGIN;
UPDATE image SET raster = 43224;
DO $$ BEGIN ASSERT lo_exists(43223); END $$;
COMMIT;
DO $$ BEGIN ASSERT NOT lo_exists(43223) AND lo_exists(43224); END $$;
BEGIN;
UPDATE image SET title = 'beautiful image' WHERE title = 'beautiful picture';
UPDATE image SET raster = 43225 WHERE title = 'beautiful image';
COMMIT;
DO $$ BEGIN ASSERT NOT lo_exists(43224) AND lo_exists(43225); END $$;
DELETE FROM image;
DO $$ BEGIN ASSERT NOT lo_exists(43225); END $$;

-- errors: a missing large object, a missing column, statement level
DROP TRIGGER t_raster ON image;
CREATE TRIGGER t_raster BEFORE DELETE ON image FOR EACH ROW EXECUTE PROCEDURE lo_manage(raster);
INSERT INTO image VALUES ('gone', 99999);
DO $$
BEGIN
	DELETE FROM image;
	RAISE 'expected an error';
EXCEPTION WHEN undefined_object THEN
	ASSERT SQLERRM = 'large object 99999 does not exist', SQLERRM;
END $$;
DROP TRIGGER t_raster ON image;
DELETE FROM image;
CREATE TRIGGER t_bad BEFORE DELETE ON image FOR EACH ROW EXECUTE PROCEDURE lo_manage(nope);
INSERT INTO image VALUES ('x', NULL);
DO $$
BEGIN
	DELETE FROM image;
	RAISE 'expected an error';
EXCEPTION WHEN internal_error THEN
	ASSERT SQLERRM = 't_bad: column "nope" does not exist', SQLERRM;
END $$;
DROP TRIGGER t_bad ON image;
CREATE TRIGGER t_stmt BEFORE DELETE ON image FOR EACH STATEMENT EXECUTE PROCEDURE lo_manage(raster);
DO $$
BEGIN
	DELETE FROM image;
	RAISE 'expected an error';
EXCEPTION WHEN internal_error THEN
	ASSERT SQLERRM = 't_stmt: must be fired for row', SQLERRM;
END $$;

DO $$ BEGIN ASSERT lo_oid(1::lo) = 1; ASSERT lo_oid(NULL) IS NULL; END $$;
DROP TABLE image;
DROP FUNCTION lo_exists;
