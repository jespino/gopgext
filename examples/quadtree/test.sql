CREATE EXTENSION quadtree;

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
	ASSERT '( 1 , 2.5)'::pt::text = '(1,2.5)';
	ASSERT '(3,4),(1,2)'::rect::text = '(1,2),(3,4)';
	ASSERT pt(1, 2) ~= '(1,2)';
	ASSERT pt(1, 2) <@ '(0,0),(1,2)' AND NOT pt(1, 2.1) <@ '(0,0),(1,2)';
	ASSERT pt(0, 0) <-> pt(3, 4) = 5;
END $$;

SELECT expect_error($$SELECT '(1)'::pt$$, '22P02', 'invalid input syntax for type pt: "(1)"');
SELECT expect_error($$SELECT '(1,x)'::pt$$, '22P02', 'invalid input syntax for type pt: "(1,x)"');
SELECT expect_error($$SELECT '(1,2)'::rect$$, '22P02', 'invalid input syntax for type rect: "(1,2)"');

-- Points: a pseudo-random spread, duplicates (all-the-same inner tuples),
-- and a line (equal x coordinates), inserted before (build) and after the
-- index is created; then NULLs, deletes, updates and vacuum.
CREATE TABLE pts (id int PRIMARY KEY, p pt);
INSERT INTO pts
	SELECT i, pt((i * 7919) % 1000 / 10.0, (i * 104729) % 997 / 10.0)
	FROM generate_series(1, 20000) i;
CREATE INDEX pts_p ON pts USING spgist (p);
INSERT INTO pts SELECT 20000 + i, pt(50, 50) FROM generate_series(1, 2000) i;
INSERT INTO pts SELECT 30000 + i, pt(25, i / 100.0) FROM generate_series(1, 3000) i;
INSERT INTO pts VALUES (99998, NULL), (99999, NULL);
DELETE FROM pts WHERE id % 10 = 3;
UPDATE pts SET p = pt(75.5, 12.25) WHERE id % 1000 = 7;
VACUUM ANALYZE pts;

-- same_results runs sql with the index (which must be used) and without it,
-- and checks both give the same rows.
CREATE FUNCTION same_results(sql text, scan text DEFAULT 'Scan using pts_p') RETURNS bigint LANGUAGE plpgsql AS $$
DECLARE
	plan text := '';
	line text;
	with_index text;
	without text;
	n bigint;
BEGIN
	SET LOCAL enable_seqscan = off;
	SET LOCAL enable_bitmapscan = off;
	FOR line IN EXECUTE 'EXPLAIN (COSTS OFF) ' || sql LOOP
		plan := plan || line || E'\n';
	END LOOP;
	ASSERT plan LIKE '%' || scan || '%', plan;
	EXECUTE 'SELECT string_agg(r::text, '';'') FROM (' || sql || ') r' INTO with_index;
	SET LOCAL enable_seqscan = on;
	SET LOCAL enable_indexscan = off;
	SET LOCAL enable_indexonlyscan = off;
	EXECUTE 'SELECT string_agg(r::text, '';''), count(*) FROM (' || sql || ') r' INTO without, n;
	ASSERT with_index IS NOT DISTINCT FROM without, format('%s: index %s, scan %s', sql, with_index, without);
	RESET enable_seqscan;
	RESET enable_bitmapscan;
	RESET enable_indexscan;
	RESET enable_indexonlyscan;
	RETURN n;
END $$;

DO $$
DECLARE
	r text;
	q text;
BEGIN
	FOREACH r IN ARRAY ARRAY['(10,10),(20,20)', '(0,0),(1,1)', '(49.5,49.5),(50.5,50.5)', '(25,0),(25,30)',
			'(-5,-5),(200,200)', '(1000,1000),(1001,1001)', '(50,50),(50,50)'] LOOP
		PERFORM same_results(format($q$SELECT id FROM pts WHERE p <@ %L ORDER BY id$q$, r));
	END LOOP;
	FOREACH q IN ARRAY ARRAY['(50,50)', '(25,10)', '(0,0)', '(33.3,66.6)', '(1000,-1000)', '(75.5,12.25)'] LOOP
		PERFORM same_results(format($q$SELECT id FROM pts WHERE p ~= %L ORDER BY id$q$, q));
		-- k-NN: the order of distances must match (ties may come in any order).
		PERFORM same_results(format($q$SELECT round((p <-> %L)::numeric, 9) AS d FROM pts ORDER BY p <-> %L LIMIT 30$q$, q, q));
		-- k-NN within a rectangle (conditions and ordering together).
		PERFORM same_results(format($q$SELECT round((p <-> %L)::numeric, 9) AS d FROM pts WHERE p <@ '(20,20),(60,60)' ORDER BY p <-> %L LIMIT 30$q$, q, q));
	END LOOP;
	ASSERT same_results($q$SELECT id FROM pts WHERE p <@ '(-5,-5),(200,200)' ORDER BY id$q$) =
		(SELECT count(*) FROM pts WHERE p IS NOT NULL);
	ASSERT same_results($q$SELECT id FROM pts WHERE p ~= '(50,50)' ORDER BY id$q$) > 1700;
	-- Index-only scans: the leaves hold the points (CanReturnData).
	PERFORM same_results($q$SELECT p FROM pts WHERE p <@ '(10,10),(12,12)' ORDER BY p <-> '(11,11)', p::text$q$, 'Index Only Scan using pts_p');
	PERFORM same_results($q$SELECT p FROM pts WHERE p <@ '(10,10),(30,30)'$q$ || ' ORDER BY p::text', 'Index Only Scan using pts_p');
END $$;

-- REINDEX rebuilds the index; it still answers the same.
REINDEX INDEX pts_p;
DO $$
BEGIN
	PERFORM same_results($q$SELECT id FROM pts WHERE p <@ '(10,10),(20,20)' ORDER BY id$q$);
	PERFORM same_results($q$SELECT round((p <-> '(5,5)')::numeric, 9) FROM pts ORDER BY p <-> '(5,5)' LIMIT 10$q$);
END $$;

-- An operator class whose family has no spgist.OpClass registered (its
-- support functions find their OpClass through the family they belong to:
-- a separate config function is enough to fail).
CREATE FUNCTION pt_other_config(internal, internal)
	RETURNS void AS 'quadtree', 'gopgext_spgist_config' LANGUAGE C IMMUTABLE STRICT;
CREATE OPERATOR CLASS pt_other_ops FOR TYPE pt USING spgist AS
	OPERATOR 6 ~= ,
	FUNCTION 1 pt_other_config (internal, internal),
	FUNCTION 2 pt_spg_choose (internal, internal),
	FUNCTION 3 pt_spg_picksplit (internal, internal),
	FUNCTION 4 pt_spg_inner_consistent (internal, internal),
	FUNCTION 5 pt_spg_leaf_consistent (internal, internal);
SELECT expect_error($$CREATE INDEX ON pts USING spgist (p pt_other_ops)$$, '55000',
	'SP-GiST operator family "pt_other_ops" has no spgist.OpClass');
