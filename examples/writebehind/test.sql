CREATE EXTENSION writebehind;
CREATE FUNCTION ok(cond boolean, what text) RETURNS void LANGUAGE plpgsql AS $$
BEGIN
	IF cond IS NOT TRUE THEN
		RAISE 'check failed: %', what;
	END IF;
END $$;
-- The row count of an UPDATE (the library intercepts once loaded).
CREATE FUNCTION updated(sql text) RETURNS bigint LANGUAGE plpgsql AS $$
DECLARE
	n bigint;
BEGIN
	EXECUTE sql;
	GET DIAGNOSTICS n = ROW_COUNT;
	RETURN n;
END $$;
SELECT writebehind_pending();

CREATE TABLE items (id int PRIMARY KEY, name text, seen_at timestamptz, hits int);
INSERT INTO items SELECT g, 'item ' || g, NULL, 0 FROM generate_series(1, 10) g;
SELECT writebehind_enable('items', '{seen_at,hits}');

-- Intercepted: counted, not written; kept once committed.
SELECT ok(updated($$UPDATE items SET seen_at = '2020-01-02 03:04:05+00' WHERE id = 1$$) = 1, 'count 1');
SELECT ok(updated($$UPDATE items SET hits = 7 WHERE id IN (2, 3)$$) = 2, 'count 2');
SELECT ok(updated($$UPDATE items SET hits = 1 WHERE id = 99$$) = 0, 'missing row: 0');
SELECT ok((SELECT seen_at IS NULL AND hits = 0 FROM items WHERE id = 1), 'row 1 unchanged');
SELECT ok((SELECT hits FROM items WHERE id = 2) = 0, 'row 2 unchanged');
SELECT ok(writebehind_pending() = 3, 'pending 3');
SET TimeZone = 'UTC';
SELECT ok(writebehind_value('items'::regclass, '1', 'seen_at') = '2020-01-02 03:04:05+00', 'value of row 1');
SELECT ok(writebehind_value('items'::regclass, '2', 'hits') = '7', 'value of row 2');

-- Rows matched several times (FROM) count once.
SELECT ok(updated($$UPDATE items SET hits = 5 FROM generate_series(1, 3) g WHERE id = 4$$) = 1, 'join: once');

-- Savepoints rolled back and aborted transactions are forgotten.
BEGIN;
UPDATE items SET hits = 50 WHERE id = 5;
SAVEPOINT s;
UPDATE items SET hits = 60 WHERE id = 6;
ROLLBACK TO s;
SAVEPOINT t;
UPDATE items SET hits = 70 WHERE id = 7;
RELEASE t;
COMMIT;
SELECT ok(writebehind_value('items'::regclass, '5', 'hits') = '50', 'kept before the savepoint');
SELECT ok(writebehind_value('items'::regclass, '6', 'hits') IS NULL, 'rolled back savepoint');
SELECT ok(writebehind_value('items'::regclass, '7', 'hits') = '70', 'released savepoint');
BEGIN;
UPDATE items SET hits = 80 WHERE id = 8;
ROLLBACK;
SELECT ok(writebehind_value('items'::regclass, '8', 'hits') IS NULL, 'aborted');
-- Exception blocks roll back their subtransaction.
DO $$
BEGIN
	BEGIN
		UPDATE items SET hits = 90 WHERE id = 9;
		RAISE 'boom';
	EXCEPTION WHEN OTHERS THEN
		NULL;
	END;
END $$;
SELECT ok(writebehind_value('items'::regclass, '9', 'hits') IS NULL, 'exception block');

-- Not intercepted: other columns, expressions reading the columns.
SELECT ok(updated($$UPDATE items SET name = 'renamed' WHERE id = 10$$) = 1, 'other column');
SELECT ok((SELECT name FROM items WHERE id = 10) = 'renamed', 'other column written');
UPDATE items SET hits = hits + 1 WHERE id = 10;
SELECT ok((SELECT hits FROM items WHERE id = 10) = 1, 'hits + 1 written');

-- The setting and the trigger turn it off.
SET writebehind.enabled = off;
UPDATE items SET hits = 11 WHERE id = 1;
SELECT ok((SELECT hits FROM items WHERE id = 1) = 11, 'setting off: written');
RESET writebehind.enabled;
ALTER TABLE items DISABLE TRIGGER writebehind;
UPDATE items SET hits = 12 WHERE id = 1;
SELECT ok((SELECT hits FROM items WHERE id = 1) = 12, 'trigger disabled: written');
ALTER TABLE items ENABLE TRIGGER writebehind;
SELECT ok(updated($$UPDATE items SET hits = 13 WHERE id = 1$$) = 1, 'enabled again');
SELECT ok((SELECT hits FROM items WHERE id = 1) = 12, 'enabled again: kept');

-- Flushing writes them (as text in this session's settings), not intercepted.
SET DateStyle = 'SQL, DMY';
SELECT ok(writebehind_flush() = 6, 'flushed 6 rows');
RESET DateStyle;
SELECT ok(writebehind_pending() = 0, 'nothing pending');
SELECT ok((SELECT seen_at = '2020-01-02 03:04:05+00' AND hits = 13 FROM items WHERE id = 1), 'row 1 written');
SELECT ok((SELECT hits FROM items WHERE id = 3) = 7, 'row 3 written');
SELECT ok((SELECT hits FROM items WHERE id = 4) = 5, 'row 4 written');
SELECT ok((SELECT hits FROM items WHERE id = 5) = 50, 'row 5 written');
SELECT ok((SELECT hits FROM items WHERE id = 7) = 70, 'row 7 written');

-- Writes of watched columns forget the values they replace: of their rows
-- (plain UPDATEs), or of the whole table (ON CONFLICT, RETURNING...).
UPDATE items SET hits = 100 WHERE id IN (2, 3);
SELECT ok(writebehind_value('items'::regclass, '2', 'hits') = '100', 'pending again');
UPDATE items SET hits = 8, name = 'x' WHERE id = 2;
SELECT ok(writebehind_value('items'::regclass, '2', 'hits') IS NULL, 'forgotten by an UPDATE');
SELECT ok(writebehind_value('items'::regclass, '3', 'hits') = '100', 'other rows kept');
INSERT INTO items VALUES (1, 'one', NULL, 0) ON CONFLICT (id) DO UPDATE SET hits = 0;
SELECT ok(writebehind_value('items'::regclass, '3', 'hits') IS NULL, 'forgotten by ON CONFLICT (whole table)');
UPDATE items SET hits = 100 WHERE id = 3;
UPDATE items SET hits = 3 WHERE id = 10 RETURNING hits;
SELECT ok(writebehind_value('items'::regclass, '3', 'hits') IS NULL, 'forgotten by RETURNING (whole table)');
UPDATE items SET hits = 100 WHERE id = 3;
BEGIN;
UPDATE items SET hits = 4, name = 'y' WHERE id = 3;
ROLLBACK;
SELECT ok(writebehind_value('items'::regclass, '3', 'hits') = '100', 'a rolled back write forgets nothing');
SELECT ok(writebehind_flush() = 1, 'flushed row 3');

-- Keyed: WHERE pk = value isn't read.
SELECT writebehind_enable('items', '{hits}', true);
SELECT ok(updated($$UPDATE items SET hits = 1 WHERE id = 99$$) = 1, 'keyed: assumed to exist');
SELECT ok(updated($$UPDATE items SET hits = 1 WHERE id > 98$$) = 0, 'keyed: other WHERE reads');
PREPARE up(int, int) AS UPDATE items SET hits = $2 WHERE id = $1;
EXECUTE up(1, 21);
EXECUTE up(1, 22);
SELECT ok(writebehind_value('items'::regclass, '1', 'hits') = '22', 'keyed prepared');
SELECT ok(writebehind_flush() = 1, 'keyed flush: the missing row is not written');
SELECT ok((SELECT hits FROM items WHERE id = 1) = 22, 'keyed written');

-- Tables with row-level security or other UPDATE triggers aren't intercepted.
CREATE TABLE guarded (id int PRIMARY KEY, hits int);
INSERT INTO guarded VALUES (1, 0);
SELECT writebehind_enable('guarded', '{hits}');
ALTER TABLE guarded ENABLE ROW LEVEL SECURITY;
UPDATE guarded SET hits = 1 WHERE id = 1;
SELECT ok((SELECT hits FROM guarded) = 1, 'rls: written');
ALTER TABLE guarded DISABLE ROW LEVEL SECURITY;
CREATE FUNCTION noop() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN RETURN NEW; END$$;
CREATE TRIGGER noop BEFORE UPDATE ON guarded FOR EACH ROW EXECUTE FUNCTION noop();
UPDATE guarded SET hits = 2 WHERE id = 1;
SELECT ok((SELECT hits FROM guarded) = 2, 'trigger: written');
DROP TRIGGER noop ON guarded;
SELECT ok(updated($$UPDATE guarded SET hits = 3 WHERE id = 1$$) = 1, 'no trigger: intercepted');
SELECT ok((SELECT hits FROM guarded) = 2, 'no trigger: kept');

-- Partitions whose columns have other numbers (a dropped column).
CREATE TABLE events (id int, dropme int, region text, hits int, PRIMARY KEY (id, region)) PARTITION BY LIST (region);
ALTER TABLE events DROP COLUMN dropme;
CREATE TABLE events_eu PARTITION OF events FOR VALUES IN ('eu');
CREATE TABLE events_us (id int NOT NULL, region text NOT NULL, hits int);
ALTER TABLE events ATTACH PARTITION events_us FOR VALUES IN ('us');
INSERT INTO events VALUES (1, 'eu', 0), (1, 'us', 0);
SELECT writebehind_enable('events', '{hits}');
SELECT ok(updated($$UPDATE events SET hits = 5 WHERE id = 1$$) = 2, 'partitions: 2 rows');
SELECT ok(writebehind_value('events_us'::regclass, '1,us', 'hits') = '5', 'partition value');
SELECT ok(writebehind_flush() = 3, 'partitions flushed');
SELECT ok((SELECT sum(hits) FROM events) = 10, 'partitions written');

-- pg.LookupRelation.
SELECT ok(writebehind_describe('items'::regclass) =
	'public.items kind=r rls=false pk=id cols=[1:id integer not null, 2:name text, 3:seen_at timestamp with time zone, 4:hits integer] triggers=[writebehind(before statement update=true when=true fires=true args=keyed|hits)]',
	'describe: ' || writebehind_describe('items'::regclass));
SELECT ok(writebehind_describe(0) IS NULL, 'describe: no relation');

-- Images.
SELECT ok(writebehind_roundtrip(42) = '42', 'image int');
SELECT ok(writebehind_roundtrip(12.5::numeric) = '12.5', 'image numeric');
SELECT ok(writebehind_roundtrip('hello'::text) = 'hello', 'image text');
SELECT ok(writebehind_roundtrip(repeat('x', 100000)) = repeat('x', 100000), 'image long text');
SELECT ok(writebehind_roundtrip('2020-01-02'::date) = '2020-01-02', 'image date');
SELECT ok(writebehind_roundtrip(point(1, 2)) = '(1,2)', 'image point');
SELECT ok(writebehind_roundtrip('abc'::name) = 'abc', 'image name');
SELECT ok(writebehind_roundtrip(ARRAY[1, 2]) = '{1,2}', 'image array');

-- pg.RunAs.
CREATE ROLE writebehind_r;
SELECT ok(writebehind_run_as('writebehind_r'::regrole) = 'writebehind_r pg_catalog, pg_temp', 'run as: ' || writebehind_run_as('writebehind_r'::regrole));
SELECT ok(current_user = 'postgres' AND current_setting('search_path') = '"$user", public', 'run as restored');
DROP ROLE writebehind_r;
