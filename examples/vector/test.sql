CREATE EXTENSION vector;

-- expect_error runs sql and checks it fails with SQLSTATE code and message msg.
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
	-- I/O
	ASSERT '[1,2,3]'::vector::text = '[1,2,3]';
	ASSERT ' [ 1 , 2.5 ,-3e2 ] '::vector::text = '[1,2.5,-300]';
	ASSERT '[1234567,0.00001,1.5e20,0.1]'::vector::text =
		'[' || 1234567::real || ',' || 0.00001::real || ',' || 1.5e20::real || ',' || 0.1::real || ']';
	ASSERT '[1,2,3]'::vector(3)::text = '[1,2,3]';
	ASSERT vector_dims('[1,2,3]') = 3;
	ASSERT vector_send('[1,2]') = '\x000200003f80000040000000'::bytea;

	-- Distances
	ASSERT l2_distance('[0,0]', '[3,4]') = 5;
	ASSERT '[0,0]'::vector <-> '[3,4]' = 5;
	ASSERT vector_l2_squared_distance('[0,0]', '[3,4]') = 25;
	ASSERT inner_product('[1,2]', '[3,4]') = 11;
	ASSERT '[1,2]'::vector <#> '[3,4]' = -11;
	ASSERT cosine_distance('[1,0]', '[0,1]') = 1;
	ASSERT '[1,0]'::vector <=> '[-1,0]' = 2;
	ASSERT cosine_distance('[1,1]', '[2,2]') < 1e-6;
	ASSERT cosine_distance('[0,0]', '[1,1]') = 'NaN';
	ASSERT l1_distance('[0,0]', '[3,-4]') = 7;
	ASSERT '[0,0]'::vector <+> '[3,-4]' = 7;
	ASSERT vector_norm('[3,4]') = 5;
	ASSERT l2_normalize('[3,4]')::text = '[0.6,0.8]';
	ASSERT l2_normalize('[0,0]')::text = '[0,0]';
	ASSERT subvector('[1,2,3,4,5]', 2, 3)::text = '[2,3,4]';
	ASSERT subvector('[1,2,3]', 2, 10)::text = '[2,3]';

	-- Arithmetic and comparisons
	ASSERT ('[1,2]'::vector + '[3,4]')::text = '[4,6]';
	ASSERT ('[1,2]'::vector - '[3,4]')::text = '[-2,-2]';
	ASSERT ('[1,2]'::vector * '[3,4]')::text = '[3,8]';
	ASSERT ('[1,2]'::vector || '[3]')::text = '[1,2,3]';
	ASSERT '[1,2]'::vector < '[1,3]' AND '[1,2]'::vector < '[1,2,0]' AND '[1,2]'::vector = '[1,2]';
	ASSERT '[2]'::vector > '[1,5]' AND '[1,2]'::vector <> '[2,1]' AND '[1,2]'::vector >= '[1,2]';
	ASSERT vector_cmp('[1,2]', '[1,2]') = 0;

	-- Casts
	ASSERT ARRAY[1,2,3]::vector::text = '[1,2,3]';
	ASSERT ARRAY[1.5,2.5]::real[]::vector::text = '[1.5,2.5]';
	ASSERT ARRAY[1.5,2.5]::float8[]::vector(2)::text = '[1.5,2.5]';
	ASSERT '[1,2]'::vector::real[] = ARRAY[1,2]::real[];
END $$;

-- Errors
SELECT expect_error($$SELECT '[1,2]'::vector(3)$$, '22000', 'expected 3 dimensions, not 2');
SELECT expect_error($$SELECT '[]'::vector$$, '22000', 'vector must have at least 1 dimension');
SELECT expect_error($$SELECT '1,2'::vector$$, '22P02', 'invalid input syntax for type vector: "1,2"');
SELECT expect_error($$SELECT '[1,2'::vector$$, '22P02', 'invalid input syntax for type vector: "[1,2"');
SELECT expect_error($$SELECT '[1,2] x'::vector$$, '22P02', 'invalid input syntax for type vector: "[1,2] x"');
SELECT expect_error($$SELECT '[1,,2]'::vector$$, '22P02', 'invalid input syntax for type vector: "[1,,2]"');
SELECT expect_error($$SELECT '[1,NaN]'::vector$$, '22000', 'NaN not allowed in vector');
SELECT expect_error($$SELECT '[1,inf]'::vector$$, '22000', 'infinite value not allowed in vector');
SELECT expect_error($$SELECT '[1e39]'::vector$$, '22003', '"1e39" is out of range for type vector');
SELECT expect_error($$SELECT NULL::vector(0)$$, '22023', 'dimensions for type vector must be at least 1');
SELECT expect_error($$SELECT NULL::vector(16001)$$, '22023', 'dimensions for type vector cannot exceed 16000');
SELECT expect_error($$SELECT l2_distance('[1]', '[1,2]')$$, '22000', 'different vector dimensions 1 and 2');
SELECT expect_error($$SELECT '[3e38]'::vector + '[3e38]'$$, '22003', 'value out of range: overflow');
SELECT expect_error($$SELECT ARRAY[1,2]::vector(3)$$, '22000', 'expected 3 dimensions, not 2');
-- pg_input_is_valid reports input errors softly (PostgreSQL 16+).
DO $$
BEGIN
	IF current_setting('server_version_num')::int >= 160000 THEN
		ASSERT pg_input_is_valid('[1,2]', 'vector');
		ASSERT NOT pg_input_is_valid('[1,', 'vector');
		ASSERT NOT pg_input_is_valid('[1,2]', 'vector(3)');
	END IF;
END $$;

-- A table with a typmod, aggregates, a btree index and binary COPY.
CREATE TABLE items (id int PRIMARY KEY, embedding vector(3));
INSERT INTO items VALUES (1, '[1,2,3]'), (2, '[4,5,6]'), (3, '[1,1,1]'), (4, NULL);
SELECT expect_error($$INSERT INTO items VALUES (5, '[1,2]')$$, '22000', 'expected 3 dimensions, not 2');

DO $$
BEGIN
	ASSERT (SELECT format_type(atttypid, atttypmod) FROM pg_attribute
		WHERE attrelid = 'items'::regclass AND attname = 'embedding') = 'vector(3)';
	ASSERT (SELECT array_agg(id ORDER BY embedding <-> '[1,1,2]', id) FROM items WHERE embedding IS NOT NULL) = '{3,1,2}';
	ASSERT (SELECT array_agg(id ORDER BY embedding <#> '[1,1,1]', id) FROM items WHERE embedding IS NOT NULL) = '{2,1,3}';
	ASSERT (SELECT avg(embedding) FROM items)::text = '[2,2.6666667,3.3333333]';
	ASSERT (SELECT sum(embedding) FROM items)::text = '[6,8,10]';
	ASSERT (SELECT avg(embedding) FROM items WHERE false) IS NULL;
	ASSERT (SELECT count(DISTINCT embedding) FROM items) = 3;
	ASSERT (SELECT array_agg(id ORDER BY embedding, id) FROM items) = '{3,1,2,4}';
END $$;

CREATE INDEX ON items (embedding);
SET enable_seqscan = off;
DO $$
DECLARE
	plan text;
BEGIN
	EXECUTE $q$EXPLAIN (COSTS OFF) SELECT id FROM items WHERE embedding = '[4,5,6]'$q$ INTO plan;
	ASSERT plan LIKE '%Index%', plan;
	ASSERT (SELECT id FROM items WHERE embedding = '[4,5,6]') = 2;
END $$;
RESET enable_seqscan;

-- Binary COPY goes through vector_send and vector_recv (with the typmod).
CREATE TABLE items2 (embedding vector(3));
CREATE TABLE items3 (embedding vector(2));
DO $$
DECLARE
	f text := current_setting('data_directory') || '/vector_test.bin';
BEGIN
	EXECUTE format('COPY (SELECT embedding FROM items WHERE id <= 3 ORDER BY id) TO %L (FORMAT binary)', f);
	EXECUTE format('COPY items2 FROM %L (FORMAT binary)', f);
	ASSERT (SELECT array_agg(embedding::text) FROM items2) = '{"[1,2,3]","[4,5,6]","[1,1,1]"}';
	PERFORM expect_error(format('COPY items3 FROM %L (FORMAT binary)', f), '22000', 'expected 2 dimensions, not 3');
END $$;

-- Exact k-NN over more rows (the parallel-safe aggregates, larger vectors).
CREATE TABLE many AS
	SELECT i AS id, array_to_vector(ARRAY[sin(i), cos(i), sin(2 * i), cos(3 * i)]::float8[], -1, false) AS v
	FROM generate_series(1, 5000) i;
DO $$
DECLARE
	q vector := '[0.1,0.9,0.2,-0.5]';
BEGIN
	-- The distance computed in SQL from the elements gives the same order.
	ASSERT (SELECT array_agg(id) FROM (SELECT id FROM many ORDER BY v <-> q, id LIMIT 5) s) =
		(SELECT array_agg(id) FROM (SELECT id FROM many ORDER BY
			(SELECT sum((a - b) ^ 2) FROM unnest(v::real[], q::real[]) AS t(a, b)), id LIMIT 5) s);
	ASSERT (SELECT vector_dims(avg(v)) FROM many) = 4;
	ASSERT abs((SELECT (avg(v)::real[])[1] FROM many) - (SELECT avg(sin(id)) FROM many)) < 1e-5;
END $$;

-- IVFFlat indexes (gopgext/am). Runs after test.sql, in the same database.

CREATE TABLE docs (id int PRIMARY KEY, v vector(4));
INSERT INTO docs
	SELECT i, array_to_vector(ARRAY[sin(i), cos(i), sin(2 * i) + 0.1, cos(3 * i)]::float8[], 4, false)
	FROM generate_series(1, 3000) i;
INSERT INTO docs VALUES (0, NULL);
CREATE INDEX docs_l2 ON docs USING ivfflat (v) WITH (lists = 20);
CREATE INDEX docs_ip ON docs USING ivfflat (v vector_ip_ops) WITH (lists = 10);
CREATE INDEX docs_cos ON docs USING ivfflat (v vector_cosine_ops) WITH (lists = 10);
ANALYZE docs;

-- knn returns "id:distance" of the k nearest rows to q by operator op,
-- with the index (which must be used) or without.
CREATE FUNCTION knn(op text, q vector, k int, use_index bool) RETURNS text LANGUAGE plpgsql AS $$
DECLARE
	sql text := format('SELECT string_agg(id || '':'' || d, '','' ORDER BY d, id) FROM (SELECT id, v %s %L AS d FROM docs ORDER BY v %s %L LIMIT %s) s',
		op, q, op, q, k);
	plan text := '';
	line text;
	result text;
BEGIN
	IF use_index THEN
		SET LOCAL enable_seqscan = off;
		FOR line IN EXECUTE 'EXPLAIN (COSTS OFF) ' || sql LOOP
			plan := plan || line || E'\n';
		END LOOP;
		ASSERT plan LIKE '%Index Scan using docs_%', plan;
	ELSE
		SET LOCAL enable_indexscan = off;
	END IF;
	EXECUTE sql INTO result;
	RESET enable_seqscan;
	RESET enable_indexscan;
	RETURN result;
END $$;

-- exact checks that, probing every list, the index gives the exact results.
CREATE FUNCTION exact(nq int) RETURNS void LANGUAGE plpgsql AS $$
DECLARE
	q vector;
	op text;
BEGIN
	SET LOCAL vector.ivfflat_probes = 100;
	FOR i IN 1..nq LOOP
		q := array_to_vector(ARRAY[cos(i * 0.7), sin(i * 1.3), cos(i * 0.4), sin(i)]::float8[], 4, false);
		FOREACH op IN ARRAY ARRAY['<->', '<#>', '<=>'] LOOP
			ASSERT knn(op, q, 10, true) = knn(op, q, 10, false),
				format('%s %s: index %s, exact %s', op, q, knn(op, q, 10, true), knn(op, q, 10, false));
		END LOOP;
	END LOOP;
END $$;

SELECT exact(10);

DO $$
DECLARE
	plan text := '';
	line text;
	q vector;
	hits int := 0;
	idx int[];
	ex int[];
BEGIN
	-- The planner picks the index for a k-NN query by itself.
	FOR line IN EXECUTE $q$EXPLAIN (COSTS OFF) SELECT id FROM docs ORDER BY v <-> '[0.5,0.5,0.5,0.5]' LIMIT 5$q$ LOOP
		plan := plan || line || E'\n';
	END LOOP;
	ASSERT plan LIKE '%Index Scan using docs_l2%', plan;
	-- Not for a query without ORDER BY distance.
	plan := '';
	FOR line IN EXECUTE $q$EXPLAIN (COSTS OFF) SELECT count(*) FROM docs WHERE v IS NOT NULL$q$ LOOP
		plan := plan || line || E'\n';
	END LOOP;
	ASSERT plan NOT LIKE '%docs_l2%', plan;

	-- One probe out of 20 lists: approximate, but most neighbors are found.
	SET LOCAL vector.ivfflat_probes = 1;
	FOR i IN 1..20 LOOP
		q := array_to_vector(ARRAY[cos(i * 0.7), sin(i * 1.3), cos(i * 0.4), sin(i)]::float8[], 4, false);
		SELECT array_agg(id) INTO idx FROM (SELECT id FROM docs ORDER BY v <-> q LIMIT 10) s;
		SET LOCAL enable_indexscan = off;
		SELECT array_agg(id) INTO ex FROM (SELECT id FROM docs ORDER BY v <-> q LIMIT 10) s;
		RESET enable_indexscan;
		hits := hits + (SELECT count(*) FROM unnest(idx) x WHERE x = ANY (ex));
	END LOOP;
	RAISE NOTICE 'recall@10 with 1 of 20 lists probed: %', hits / 200.0;
	ASSERT hits >= 100, format('recall %s', hits / 200.0);

	-- Every row is indexed, NULLs included: ORDER BY without LIMIT returns
	-- them last (NULL distance), and scans without ORDER BY see them.
	SET LOCAL vector.ivfflat_probes = 100;
	SET LOCAL enable_seqscan = off;
	ASSERT (SELECT count(*) FROM (SELECT id FROM docs ORDER BY v <-> '[0,0,0,0]') s) = 3001;
	ASSERT (SELECT array_agg(id) FROM (SELECT id FROM docs ORDER BY v <-> '[0,0,0,0]' OFFSET 3000) s) = '{0}';
	ASSERT (SELECT count(*) FROM (SELECT id FROM docs ORDER BY v <-> NULL) s) = 3001;
	ASSERT (SELECT count(*) FROM docs) = 3001;
END $$;

-- Inserts after the build, deletes and VACUUM (whose freed TIDs get reused:
-- the index must have dropped the old entries), then REINDEX.
INSERT INTO docs
	SELECT i, array_to_vector(ARRAY[cos(i), sin(i * 2), sin(i) - 0.2, cos(i * 5)]::float8[], 4, false)
	FROM generate_series(3001, 3600) i;
SELECT exact(5);
DELETE FROM docs WHERE id % 3 = 0;
VACUUM docs;
INSERT INTO docs
	SELECT i, array_to_vector(ARRAY[-sin(i), cos(i * 3), 0.5, sin(i * 7)]::float8[], 4, false)
	FROM generate_series(4001, 5000) i;
SELECT exact(5);
UPDATE docs SET v = v + '[0.01,0.01,0.01,0.01]' WHERE id % 7 = 1;
VACUUM docs;
SELECT exact(5);
REINDEX INDEX docs_l2;
REINDEX TABLE docs;
SELECT exact(5);

-- Errors.
SELECT expect_error($$CREATE INDEX ON docs USING ivfflat (v) WITH (lists = 0)$$, '22023',
	'value 0 out of bounds for option "lists"');
SELECT expect_error($$CREATE INDEX ON docs USING ivfflat (v) WITH (foo = 1)$$, '22023',
	'unrecognized parameter "foo"');
DO $$
BEGIN
	SET LOCAL enable_seqscan = off;
	PERFORM expect_error($q$SELECT id FROM docs ORDER BY v <-> '[1,2]' LIMIT 1$q$, '22000',
		'different vector dimensions 4 and 2');
END $$;

-- An index built on an empty table, and dimensions checked on insert.
CREATE TABLE empty_docs (id int, v vector);
CREATE INDEX empty_docs_v ON empty_docs USING ivfflat (v) WITH (lists = 5);
INSERT INTO empty_docs VALUES (1, '[1,2,3]'), (2, '[3,2,1]'), (3, '[0,0,1]');
SELECT expect_error($$INSERT INTO empty_docs VALUES (4, '[1,2]')$$, '22000', 'expected 3 dimensions, not 2');
DO $$
BEGIN
	SET LOCAL enable_seqscan = off;
	ASSERT (SELECT array_agg(id) FROM (SELECT id FROM empty_docs ORDER BY v <-> '[1,1,1]', id) s) IN ('{3,1,2}', '{3,2,1}');
	ASSERT (SELECT count(*) FROM (SELECT id FROM empty_docs ORDER BY v <-> '[1,1,1]') s) = 3;
END $$;
-- Mixed dimensions can't be indexed.
CREATE TABLE mixed (v vector);
INSERT INTO mixed VALUES ('[1,2]'), ('[1,2,3]');
SELECT expect_error($$CREATE INDEX ON mixed USING ivfflat (v)$$, '22000', 'expected 2 dimensions, not 3');

-- Unlogged tables (the index's init fork), and TRUNCATE.
CREATE UNLOGGED TABLE udocs (v vector(2));
CREATE INDEX ON udocs USING ivfflat (v) WITH (lists = 2);
INSERT INTO udocs SELECT array_to_vector(ARRAY[i, -i], 2, false) FROM generate_series(1, 100) i;
DO $$
BEGIN
	SET LOCAL enable_seqscan = off;
	SET LOCAL vector.ivfflat_probes = 2;
	ASSERT (SELECT v::text FROM udocs ORDER BY v <-> '[50.2,-50.2]' LIMIT 1) = '[50,-50]';
END $$;
TRUNCATE udocs;
INSERT INTO udocs VALUES ('[1,1]');
DO $$
BEGIN
	SET LOCAL enable_seqscan = off;
	ASSERT (SELECT count(*) FROM (SELECT v FROM udocs ORDER BY v <-> '[0,0]') s) = 1;
END $$;
