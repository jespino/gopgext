-- hstore: quick checks (upstream's regression suite runs with pg_regress, see README).
CREATE EXTENSION IF NOT EXISTS hstore;
DO $$
DECLARE h hstore := 'a=>1, b=>NULL, "c d"=>"x\"y"';
BEGIN
	IF h::text <> '"a"=>"1", "b"=>NULL, "c d"=>"x\"y"' THEN RAISE 'out: %', h; END IF;
	IF h -> 'a' <> '1' OR (h -> 'b') IS NOT NULL THEN RAISE 'fetch'; END IF;
	IF NOT h ? 'b' OR NOT h ?& ARRAY['a','b'] OR h ?| ARRAY['x'] THEN RAISE 'exists'; END IF;
	IF NOT h @> 'a=>1' OR h @> 'a=>2' THEN RAISE 'contains'; END IF;
	IF (h || 'a=>2') -> 'a' <> '2' THEN RAISE 'concat'; END IF;
	IF (h - 'a'::text) ? 'a' THEN RAISE 'delete'; END IF;
	IF h['c d'] <> 'x"y' THEN RAISE 'subscript'; END IF;
	h['z'] := 'new';
	IF h -> 'z' <> 'new' THEN RAISE 'subscript assign'; END IF;
	IF hstore_to_jsonb_loose('a=>1, b=>t')::text <> '{"a": 1, "b": true}' THEN RAISE 'jsonb'; END IF;
END $$;
CREATE TEMP TABLE hs (h hstore);
INSERT INTO hs SELECT hstore(ARRAY['k' || i, 'v' || (i % 7)]) || hstore('n', i::text) FROM generate_series(1, 2000) i;
CREATE INDEX hs_gist ON hs USING gist (h gist_hstore_ops(siglen = 8));
CREATE INDEX hs_gin ON hs USING gin (h);
SET enable_seqscan = off;
DO $$
BEGIN
	IF (SELECT count(*) FROM hs WHERE h ? 'k5') <> 1 THEN RAISE 'index ?'; END IF;
	IF (SELECT count(*) FROM hs WHERE h @> 'k10=>v3') <> 1 THEN RAISE 'index @>'; END IF;
	IF (SELECT count(*) FROM hs WHERE h ?| ARRAY['k1','k2','zz']) <> 2 THEN RAISE 'index ?|'; END IF;
END $$;
-- The index answers like a sequential scan.
DO $$
DECLARE q text;
BEGIN
	FOREACH q IN ARRAY ARRAY['h @> ''k10=>v3''', 'h ? ''n''', 'h ?& ARRAY[''k1'',''n'']', 'h ?| ARRAY[''k7'',''k9'']', 'h @> ''n=>5'''] LOOP
		SET LOCAL enable_seqscan = on;
		SET LOCAL enable_indexscan = off; SET LOCAL enable_bitmapscan = off;
		EXECUTE 'CREATE TEMP TABLE seq AS SELECT h FROM hs WHERE ' || q;
		SET LOCAL enable_seqscan = off; SET LOCAL enable_bitmapscan = on;
		EXECUTE 'CREATE TEMP TABLE idx AS SELECT h FROM hs WHERE ' || q;
		IF EXISTS (SELECT * FROM seq EXCEPT ALL SELECT * FROM idx) OR EXISTS (SELECT * FROM idx EXCEPT ALL SELECT * FROM seq) THEN
			RAISE 'index and scan differ for %', q;
		END IF;
		DROP TABLE seq, idx;
	END LOOP;
END $$;
RESET enable_seqscan;
-- Binary I/O (hstore_send, hstore_recv).
SELECT current_setting('data_directory') || '/hstore_test.bin' AS bin \gset
COPY (SELECT h FROM hs) TO :'bin' (FORMAT binary);
CREATE TEMP TABLE hs2 (h hstore);
COPY hs2 FROM :'bin' (FORMAT binary);
DO $$
BEGIN
	IF (SELECT count(*) FROM hs2) <> 2000 OR EXISTS (SELECT h FROM hs EXCEPT SELECT h FROM hs2) THEN
		RAISE 'binary round trip';
	END IF;
END $$;
-- Records, including a domain over a composite type.
CREATE TYPE pt AS (x int, y text, z numeric(5,2));
CREATE DOMAIN ptd AS pt CHECK ((VALUE).x > 0);
DO $$
BEGIN
	IF hstore(ROW(1, 'a', 2.5)::pt) <> 'x=>1, y=>a, z=>2.50' THEN RAISE 'hstore(record)'; END IF;
	IF hstore(NULL::pt) <> 'x=>NULL, y=>NULL, z=>NULL' THEN RAISE 'hstore(null)'; END IF;
	IF populate_record(NULL::pt, 'x=>3, z=>1.234') IS DISTINCT FROM ROW(3, NULL, 1.23)::pt THEN RAISE 'populate'; END IF;
	IF (ROW(1, 'a', 2.5)::pt #= 'y=>b') IS DISTINCT FROM ROW(1, 'b', 2.5)::pt THEN RAISE '#='; END IF;
	BEGIN
		PERFORM populate_record(ROW(1, 'a', 2.5)::pt::ptd, 'x=>-1');
		RAISE 'domain check expected';
	EXCEPTION WHEN check_violation THEN NULL;
	END;
	BEGIN
		PERFORM populate_record(1, 'x=>-1');
		RAISE 'rowtype error expected';
	EXCEPTION WHEN datatype_mismatch THEN NULL;
	END;
END $$;
