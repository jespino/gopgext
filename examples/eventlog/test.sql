CREATE EXTENSION eventlog;

CREATE FUNCTION expect_error(sql text, code text, msg text) RETURNS void LANGUAGE plpgsql AS $$
BEGIN
	EXECUTE sql;
	RAISE 'expected an error from %', sql;
EXCEPTION WHEN OTHERS THEN
	IF SQLSTATE <> code OR SQLERRM <> msg THEN
		RAISE 'got % "%", want % "%" (%)', SQLSTATE, SQLERRM, code, msg, sql;
	END IF;
END $$;

DO $$
BEGIN
	ASSERT '2024-05-01T10:00:00Z'::epoch::text = '2024-05-01T10:00:00.000Z';
	ASSERT '2024-05-01T12:00:00.5+02:00'::epoch::text = '2024-05-01T10:00:00.500Z';
	ASSERT epoch_millis('1970-01-01T00:00:01Z') = 1000;
	ASSERT epoch(1000) = '1970-01-01T00:00:01Z'::epoch;
	ASSERT epoch(1) < epoch(2) AND epoch(2) >= epoch(2) AND NOT epoch(3) <= epoch(2);
	ASSERT 'dev-1'::code = 'dev-1' AND NOT 'dev-1'::code = 'dev-2';
END $$;

SELECT expect_error($$SELECT 'yesterday'::epoch$$, '22P02', 'invalid input syntax for type epoch: "yesterday"');
SELECT expect_error($$SELECT 'a b'::code$$, '22P02', 'invalid input syntax for type code: "a b"');

-- An event log in insertion order: times and sources correlate with the
-- physical order of the rows. Some NULLs too.
CREATE TABLE events (id int, at epoch, src code, payload text);
INSERT INTO events
	SELECT i, epoch(1700000000000 + i * 1000::bigint), ('dev-' || (i / 2000))::code, repeat('x', 40)
	FROM generate_series(1, 60000) i;
INSERT INTO events VALUES (0, NULL, NULL, 'nulls');
CREATE INDEX events_at ON events USING brin (at) WITH (pages_per_range = 2);
CREATE INDEX events_src ON events USING brin (src) WITH (pages_per_range = 2);
-- Rows added after the index: summarized by inserts (ranges already
-- summarized) or by brin_summarize_new_values (new ranges).
INSERT INTO events
	SELECT i, epoch(1700000000000 + i * 1000::bigint), ('dev-' || (i / 2000))::code, repeat('y', 40)
	FROM generate_series(60001, 80000) i;
INSERT INTO events VALUES (-1, epoch(1700000000000 + 30000 * 1000::bigint), 'late', 'out of order');
INSERT INTO events VALUES (-2, NULL, 'dev-3', 'null time');
VACUUM ANALYZE events;
SELECT brin_summarize_new_values('events_at') >= 0, brin_summarize_new_values('events_src') >= 0;

-- same_results runs sql with the BRIN index (a bitmap scan on it) and with
-- a sequential scan, and checks both return the same rows.
CREATE FUNCTION same_results(sql text, idx text) RETURNS bigint LANGUAGE plpgsql AS $$
DECLARE
	plan text := '';
	line text;
	with_index text;
	without text;
	n bigint;
BEGIN
	SET LOCAL enable_seqscan = off;
	SET LOCAL enable_indexscan = off;
	FOR line IN EXECUTE 'EXPLAIN (COSTS OFF) ' || sql LOOP
		plan := plan || line || E'\n';
	END LOOP;
	ASSERT plan LIKE '%Bitmap Index Scan on ' || idx || '%', plan;
	EXECUTE 'SELECT string_agg(r::text, '';'') FROM (' || sql || ') r' INTO with_index;
	SET LOCAL enable_seqscan = on;
	SET LOCAL enable_bitmapscan = off;
	EXECUTE 'SELECT string_agg(r::text, '';''), count(*) FROM (' || sql || ') r' INTO without, n;
	ASSERT with_index IS NOT DISTINCT FROM without, format('%s: index %s, scan %s', sql, with_index, without);
	RESET enable_seqscan;
	RESET enable_indexscan;
	RESET enable_bitmapscan;
	RETURN n;
END $$;

CREATE FUNCTION check_all(counts boolean DEFAULT true) RETURNS void LANGUAGE plpgsql AS $$
DECLARE
	t bigint;
	op text;
	c text;
BEGIN
	FOREACH t IN ARRAY ARRAY[1, 500, 29999, 30000, 30001, 59999, 60000, 60001, 70000, 80000, 80001, 0, -5]::bigint[] LOOP
		FOREACH op IN ARRAY ARRAY['<', '<=', '=', '>=', '>'] LOOP
			PERFORM same_results(format('SELECT id FROM events WHERE at %s epoch(%s) ORDER BY id',
				op, 1700000000000 + t * 1000), 'events_at');
		END LOOP;
	END LOOP;
	ASSERT same_results(format('SELECT id FROM events WHERE at >= epoch(%s) AND at < epoch(%s) ORDER BY id',
		1700000000000 + 100 * 1000::bigint, 1700000000000 + 200 * 1000::bigint), 'events_at') = 100 OR NOT counts;
	ASSERT same_results(format('SELECT id FROM events WHERE at = epoch(%s) ORDER BY id',
		1700000000000 + 30000 * 1000::bigint), 'events_at') = 2 OR NOT counts;
	FOREACH c IN ARRAY ARRAY['dev-0', 'dev-3', 'dev-15', 'dev-29', 'dev-40', 'late', 'nope'] LOOP
		PERFORM same_results(format('SELECT id FROM events WHERE src = %L ORDER BY id', c), 'events_src');
	END LOOP;
	ASSERT same_results($q$SELECT id FROM events WHERE src = 'dev-3' ORDER BY id$q$, 'events_src') = 2001 OR NOT counts;
	ASSERT same_results($q$SELECT id FROM events WHERE src = 'late' ORDER BY id$q$, 'events_src') = 1 OR NOT counts;
	-- IS NULL is handled by BRIN itself.
	ASSERT same_results('SELECT id FROM events WHERE at IS NULL ORDER BY id', 'events_at') = 2 OR NOT counts;
	-- Both columns (either index, or both).
	PERFORM same_results(format($q$SELECT id FROM events WHERE src = 'dev-20' AND at > epoch(%s) ORDER BY id$q$,
		1700000000000 + 41000 * 1000::bigint), 'events_');
END $$;

SELECT check_all();

-- The summaries skip most of the table: a narrow range reads few pages.
DO $$
DECLARE
	plan text := '';
	line text;
BEGIN
	SET LOCAL enable_seqscan = off;
	FOR line IN EXECUTE format('EXPLAIN (ANALYZE, COSTS OFF, TIMING OFF) SELECT count(*) FROM events WHERE at = epoch(%s)',
		1700000000000 + 12345 * 1000::bigint) LOOP
		plan := plan || line || E'\n';
	END LOOP;
	ASSERT plan ~ 'Heap Blocks: lossy=[0-9]\M', plan;
END $$;

-- Desummarizing and resummarizing ranges, REINDEX: same answers.
SELECT brin_desummarize_range('events_at', 0), brin_desummarize_range('events_at', 100),
	brin_desummarize_range('events_src', 10);
SELECT check_all();
SELECT brin_summarize_range('events_at', 0) >= 0, brin_summarize_range('events_at', 100) >= 0,
	brin_summarize_range('events_src', 10) >= 0;
SELECT check_all();
DELETE FROM events WHERE id % 7 = 0;
VACUUM events;
REINDEX TABLE events;
SELECT check_all(false);

-- An operator class whose family has no brin.OpClass registered.
CREATE OPERATOR CLASS epoch_unregistered_ops FOR TYPE epoch USING brin AS
	OPERATOR 3 = ,
	FUNCTION 1 eventlog_brin_opcinfo(internal),
	FUNCTION 2 eventlog_brin_add_value(internal, internal, internal, internal),
	FUNCTION 3 eventlog_brin_consistent(internal, internal, internal, integer),
	FUNCTION 4 eventlog_brin_union(internal, internal, internal);
SELECT expect_error($$CREATE INDEX ON events USING brin (at epoch_unregistered_ops)$$, '55000',
	'BRIN operator family "epoch_unregistered_ops" has no brin.OpClass');
