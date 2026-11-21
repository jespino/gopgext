-- Trigger functions (gopgext/trigger).
CREATE EXTENSION IF NOT EXISTS triggers;

CREATE TABLE audit (n serial, event text, tbl text, args text, detail text);
CREATE DOMAIN short_code AS varchar(4) CHECK (VALUE <> 'bad');
CREATE TABLE items (id int PRIMARY KEY, name varchar(8), code short_code, version bigint, "Mixed Case" text, at timestamptz);
CREATE TRIGGER a_norm BEFORE INSERT OR UPDATE ON items
	FOR EACH ROW EXECUTE FUNCTION normalize_row('name', 'code', 'Mixed Case');
CREATE TRIGGER b_audit AFTER INSERT OR UPDATE OR DELETE ON items
	FOR EACH ROW EXECUTE FUNCTION audit('x', 'y');
CREATE TRIGGER c_audit AFTER INSERT OR UPDATE OR DELETE OR TRUNCATE ON items
	FOR EACH STATEMENT EXECUTE FUNCTION audit();

INSERT INTO items (id, name, code, "Mixed Case") VALUES (1, '  Apple ', 'AB', ' MiXed ');
INSERT INTO items (id, name) VALUES (2, 'skip');
DO $$
BEGIN
	ASSERT (SELECT (name, code, version, "Mixed Case") FROM items WHERE id = 1) = ('apple'::varchar, 'ab'::short_code, 1::bigint, 'mixed'::text), (SELECT row(i.*) FROM items i);
	ASSERT NOT EXISTS (SELECT FROM items WHERE id = 2);
	ASSERT (SELECT array_agg(event || ' ' || tbl || ' ' || args ORDER BY n) FROM audit) =
		'{"b_audit AFTER INSERT row public.items x,y","c_audit AFTER INSERT statement public.items ","c_audit AFTER INSERT statement public.items "}',
		(SELECT array_agg(event || ' ' || tbl || ' ' || args ORDER BY n) FROM audit);
	ASSERT (SELECT detail FROM audit WHERE n = 1) =
		'id:integer=1 name:character varying(8)=apple code:short_code=ab version:bigint=1 Mixed Case:text=mixed at:timestamp with time zone=NULL',
		(SELECT detail FROM audit WHERE n = 1);
END $$;
TRUNCATE audit;

UPDATE items SET name = 'Apple', code = 'CD';
DO $$
BEGIN
	ASSERT (SELECT (name, code, version) FROM items) = ('apple'::varchar, 'cd'::short_code, 2::bigint);
	ASSERT (SELECT detail FROM audit WHERE event LIKE 'b_audit%') = 'code version set:name+code',
		(SELECT detail FROM audit WHERE event LIKE 'b_audit%');
END $$;

-- typmods and domains apply to SetText
DO $$
BEGIN
	UPDATE items SET name = 'much too long';
	RAISE 'expected an error';
EXCEPTION WHEN string_data_right_truncation THEN
END $$;
DO $$
BEGIN
	UPDATE items SET code = 'BAD';
	RAISE 'expected an error';
EXCEPTION WHEN check_violation THEN
END $$;

-- returning the old row
CREATE TABLE docs (id int, state text);
CREATE TRIGGER revert BEFORE UPDATE ON docs FOR EACH ROW EXECUTE FUNCTION revert('state');
INSERT INTO docs VALUES (1, 'open'), (2, 'locked');
UPDATE docs SET state = 'closed';
DO $$ BEGIN ASSERT (SELECT array_agg(state ORDER BY id) FROM docs) = '{closed,locked}'; END $$;

-- INSTEAD OF triggers on a view, with values passed to SPI as they are
CREATE VIEW v AS SELECT id, name FROM items;
CREATE TRIGGER vw INSTEAD OF INSERT OR UPDATE OR DELETE ON v FOR EACH ROW EXECUTE FUNCTION view_write();
INSERT INTO v VALUES (5, 'five');
UPDATE v SET name = 'Five' WHERE id = 5;
DO $$
DECLARE n bigint;
BEGIN
	ASSERT (SELECT name FROM items WHERE id = 5) = 'five';
	DELETE FROM v WHERE id = 5;
	GET DIAGNOSTICS n = ROW_COUNT;
	ASSERT n = 1 AND NOT EXISTS (SELECT FROM items WHERE id = 5);
END $$;

-- TRUNCATE, DELETE
TRUNCATE audit;
DELETE FROM items;
TRUNCATE items;
DO $$
BEGIN
	ASSERT (SELECT array_agg(event ORDER BY n) FROM audit) =
		'{"b_audit AFTER DELETE row","c_audit AFTER DELETE statement","c_audit AFTER TRUNCATE statement"}',
		(SELECT array_agg(event ORDER BY n) FROM audit);
END $$;

-- errors of the row API
CREATE TABLE m (id int, name text, at timestamptz);
CREATE FUNCTION misuse_check(check_name text, code text, msg text) RETURNS void LANGUAGE plpgsql AS $$
DECLARE c text; m text;
BEGIN
	EXECUTE format('CREATE TRIGGER t BEFORE INSERT ON m FOR EACH ROW EXECUTE FUNCTION misuse(%L)', check_name);
	BEGIN
		INSERT INTO m VALUES (1, 'x');
		RAISE 'expected an error';
	EXCEPTION WHEN OTHERS THEN
		GET STACKED DIAGNOSTICS c = RETURNED_SQLSTATE, m = MESSAGE_TEXT;
		ASSERT c = code AND m LIKE msg, format('%s: %s %s', check_name, c, m);
	END;
	DROP TRIGGER t ON m;
END $$;
SELECT misuse_check('column', '42703', 'column "no_such_column" of relation "m" does not exist');
SELECT misuse_check('type', '42804', 'column "name" is of type text: a int32 needs integer');
SELECT misuse_check('settype', '42804', 'column "id" is of type integer: a string needs text, varchar or char');
SELECT misuse_check('anytype', '42804', 'column "id" is of type integer, not of the value''s type (OID 25)');
SELECT misuse_check('input', '22P02', 'invalid input syntax for type integer: "not a number"');
SELECT misuse_check('panic', 'XX000', 'Go panic: %');
CREATE TRIGGER t BEFORE INSERT ON m FOR EACH ROW EXECUTE FUNCTION misuse('time');
INSERT INTO m VALUES (1, 'x');
DO $$ BEGIN ASSERT (SELECT (at, name) FROM m) = ('1970-01-01 00:00:00+00'::timestamptz, NULL::text); END $$;

-- a trigger function can't be called directly
DO $$
BEGIN
	PERFORM audit();
	RAISE 'expected an error';
EXCEPTION WHEN feature_not_supported THEN
END $$;

-- event triggers
CREATE EVENT TRIGGER ddl ON ddl_command_end EXECUTE FUNCTION log_ddl();
TRUNCATE audit;
CREATE TABLE t2 (x int);
DO $$
BEGIN
	CREATE SEQUENCE s2;
	RAISE 'expected an error';
EXCEPTION WHEN insufficient_privilege THEN
	ASSERT SQLERRM = 'CREATE SEQUENCE is not allowed (ddl_command_end)';
END $$;
DROP EVENT TRIGGER ddl;
DO $$ BEGIN ASSERT (SELECT array_agg(event || ' ' || detail) FROM audit) = '{"ddl_command_end CREATE TABLE"}'; END $$;
DROP TABLE t2, m, docs, audit CASCADE;
DROP VIEW v;
DROP TABLE items;
DROP DOMAIN short_code;
DROP FUNCTION misuse_check;
