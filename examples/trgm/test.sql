CREATE EXTENSION trgm;

-- expect_error runs sql and checks it fails with SQLSTATE code (and message
-- msg, unless NULL).
CREATE FUNCTION expect_error(sql text, code text, msg text) RETURNS void LANGUAGE plpgsql AS $$
BEGIN
	EXECUTE sql;
	RAISE 'expected an error from %', sql;
EXCEPTION WHEN OTHERS THEN
	IF SQLSTATE <> code OR (msg IS NOT NULL AND SQLERRM <> msg) THEN
		RAISE 'got % "%", want % "%" (%)', SQLSTATE, SQLERRM, code, msg, sql;
	END IF;
END $$;

DO $$
BEGIN
	ASSERT show_trgm('cat') = '{"  c"," ca","at ","cat"}', show_trgm('cat')::text;
	ASSERT show_trgm('A b') = '{"  a","  b"," a "," b "}', show_trgm('A b')::text;
	ASSERT show_trgm('') = '{}';
	ASSERT similarity('word', 'words') = 4::real / 7::real, similarity('word', 'words')::text;
	ASSERT similarity('word', 'word') = 1;
	ASSERT similarity('', 'word') = 0;
	ASSERT 'word' % 'words' AND NOT ('word' % 'xyz');
	ASSERT like_trigrams('%gop_er%') = '{gop}';
	ASSERT like_trigrams('apple%') = '{"  a"," ap",app,ple,ppl}', like_trigrams('apple%')::text;
	ASSERT like_trigrams('%a%') = '{}';
	ASSERT 'Hello World' ^@@ 'wor' AND NOT ('Hello World' ^@@ 'orl');
END $$;

SELECT expect_error($$SET trgm.similarity_threshold = 2$$, '22023', NULL);

-- Rows: a few words per row from a vocabulary, with NULLs and empty texts.
CREATE TABLE docs (id int PRIMARY KEY, t text);
INSERT INTO docs
	SELECT i, CASE WHEN i % 97 = 0 THEN NULL WHEN i % 89 = 0 THEN '' ELSE
		(ARRAY['apple','banana','cherry','date','elderberry','fig','grape','Hello','World','postgres',
			'golang','gopher','tinygo','índice','búsqueda','100%','under_score'])[1 + (i * 7) % 17] || ' ' ||
		(ARRAY['red','green','blue','Apple','pie','postgresql','gophers','go','x','ÍNDICES'])[1 + (i * 13) % 10] || ' ' ||
		substr(md5(i::text), 1, 1 + i % 6) END
	FROM generate_series(1, 4000) i;

CREATE INDEX docs_trgm ON docs USING gin (t trgm_gin_ops);
CREATE INDEX docs_words ON docs USING gin (t word_prefix_gin_ops) WITH (fastupdate = on, gin_pending_list_limit = 64);

-- same_results runs sql with and without the index, checks both give the
-- same rows (the index must be used for the first), and returns the count.
CREATE FUNCTION same_results(sql text, index text) RETURNS bigint LANGUAGE plpgsql AS $$
DECLARE
	plan text := '';
	line text;
	with_index text;
	without text;
	n bigint;
BEGIN
	SET LOCAL enable_seqscan = off;
	SET LOCAL enable_indexscan = off;  -- GIN only does bitmap scans
	FOR line IN EXECUTE 'EXPLAIN (COSTS OFF) ' || sql LOOP
		plan := plan || line || E'\n';
	END LOOP;
	ASSERT plan LIKE '%Bitmap Index Scan on ' || index || '%', plan;
	EXECUTE 'SELECT string_agg(r::text, '';''), count(*) FROM (' || sql || ') r' INTO with_index, n;
	SET LOCAL enable_seqscan = on;
	SET LOCAL enable_bitmapscan = off;
	SET LOCAL enable_indexscan = off;
	EXECUTE 'SELECT string_agg(r::text, '';'') FROM (' || sql || ') r' INTO without;
	ASSERT with_index IS NOT DISTINCT FROM without, format('%s: index %s, scan %s', sql, with_index, without);
	RESET enable_seqscan;
	RESET enable_bitmapscan;
	RESET enable_indexscan;
	RETURN n;
END $$;

CREATE FUNCTION check_all() RETURNS void LANGUAGE plpgsql AS $$
DECLARE
	q text;
	total bigint := 0;
BEGIN
	FOREACH q IN ARRAY ARRAY['aple', 'postgress', 'gopher tinygo', 'xyz', 'a', 'Hello Wrld', 'índice', 'banana pie'] LOOP
		total := total + same_results(format('SELECT id FROM docs WHERE t %% %L ORDER BY id', q), 'docs_trgm');
	END LOOP;
	FOREACH q IN ARRAY ARRAY['%apple%', 'apple%', '%World', '%gop_er%', '%a%', '%postgres %', 'Hello World%',
		'%100\%%', '%under\_score%', '%nothing here%', 'date red%'] LOOP
		total := total + same_results(format('SELECT id FROM docs WHERE t LIKE %L ORDER BY id', q), 'docs_trgm');
		total := total + same_results(format('SELECT id FROM docs WHERE t ILIKE %L ORDER BY id', q), 'docs_trgm');
	END LOOP;
	total := total + same_results($q$SELECT id FROM docs WHERE t ILIKE '%ÍNDICE%' ORDER BY id$q$, 'docs_trgm');
	FOREACH q IN ARRAY ARRAY['gop', 'POST', 'zz', '', 'í', 'apple', 'b', 'under'] LOOP
		total := total + same_results(format('SELECT id FROM docs WHERE t ^@@ %L ORDER BY id', q), 'docs_words');
	END LOOP;
	ASSERT total > 10000, total;
END $$;

SELECT check_all();
SET trgm.similarity_threshold = 0.1;
SELECT check_all();
RESET trgm.similarity_threshold;

-- Inserts after the build (docs_words has a pending list), updates,
-- deletes, then flushing the pending list, VACUUM and REINDEX.
INSERT INTO docs
	SELECT i, 'late ' || (ARRAY['apple','gopher','World','tinygo'])[1 + i % 4] || ' ' || i
	FROM generate_series(4001, 5000) i;
UPDATE docs SET t = 'updated gopher text' WHERE id % 50 = 1;
DELETE FROM docs WHERE id % 31 = 0;
DO $$
BEGIN
	ASSERT same_results($q$SELECT id FROM docs WHERE t ^@@ 'late' ORDER BY id$q$, 'docs_words') > 900;
	ASSERT same_results($q$SELECT id FROM docs WHERE t ^@@ 'updat' ORDER BY id$q$, 'docs_words') =
		(SELECT count(*) FROM docs WHERE id % 50 = 1);
END $$;
SELECT check_all();
SELECT gin_clean_pending_list('docs_words') >= 0;
SELECT check_all();
VACUUM ANALYZE docs;
SELECT check_all();
REINDEX TABLE docs;
SELECT check_all();

-- An operator class whose family has no gin.OpClass.
CREATE FUNCTION bogus_extract_value(text, internal, internal)
	RETURNS internal AS '$libdir/trgm', 'gopgext_gin_extract_value' LANGUAGE C STRICT;
CREATE FUNCTION bogus_extract_query(text, internal, smallint, internal, internal, internal, internal)
	RETURNS internal AS '$libdir/trgm', 'gopgext_gin_extract_query' LANGUAGE C STRICT;
CREATE FUNCTION bogus_consistent(internal, smallint, text, integer, internal, internal, internal, internal)
	RETURNS boolean AS '$libdir/trgm', 'gopgext_gin_consistent' LANGUAGE C STRICT;
CREATE OPERATOR CLASS bogus_gin_ops FOR TYPE text USING gin AS
	OPERATOR 1 ^@@ (text, text),
	FUNCTION 2 bogus_extract_value (text, internal, internal),
	FUNCTION 3 bogus_extract_query (text, internal, smallint, internal, internal, internal, internal),
	FUNCTION 4 bogus_consistent (internal, smallint, text, integer, internal, internal, internal, internal),
	STORAGE bytea;
SELECT expect_error($$CREATE INDEX ON docs USING gin (t bogus_gin_ops)$$, '55000',
	'GIN operator family "bogus_gin_ops" has no gin.OpClass');
