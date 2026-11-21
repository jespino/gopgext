-- Differential test: compares this Go port with the original C tablefunc
-- on generated tables (needs the original library installed as
-- $libdir/tablefunc_c.so:
--   cp $(pg_config --pkglibdir)/tablefunc.so $(pg_config --pkglibdir)/tablefunc_c.so
-- and this port installed as tablefunc). Prints the number of mismatching
-- results (or errors) per check: all must be 0.
--
--   psql -v ON_ERROR_STOP=1 -f differential.sql

CREATE EXTENSION IF NOT EXISTS tablefunc;
DROP SCHEMA IF EXISTS tf_c CASCADE;
CREATE SCHEMA tf_c;
CREATE FUNCTION tf_c.crosstab(text) RETURNS setof record AS '$libdir/tablefunc_c', 'crosstab' LANGUAGE C STABLE STRICT;
CREATE FUNCTION tf_c.crosstab(text, text) RETURNS setof record AS '$libdir/tablefunc_c', 'crosstab_hash' LANGUAGE C STABLE STRICT;
CREATE FUNCTION tf_c.connectby(text, text, text, text, int, text) RETURNS setof record
	AS '$libdir/tablefunc_c', 'connectby_text' LANGUAGE C STABLE STRICT;
CREATE FUNCTION tf_c.connectby(text, text, text, text, int) RETURNS setof record
	AS '$libdir/tablefunc_c', 'connectby_text' LANGUAGE C STABLE STRICT;
CREATE FUNCTION tf_c.connectby(text, text, text, text, text, int, text) RETURNS setof record
	AS '$libdir/tablefunc_c', 'connectby_text_serial' LANGUAGE C STABLE STRICT;
CREATE FUNCTION tf_c.connectby(text, text, text, text, text, int) RETURNS setof record
	AS '$libdir/tablefunc_c', 'connectby_text_serial' LANGUAGE C STABLE STRICT;

-- The result of a query as text, or its error.
CREATE OR REPLACE FUNCTION pg_temp.run(q text) RETURNS text LANGUAGE plpgsql AS $$
DECLARE
	r text;
BEGIN
	EXECUTE 'SELECT string_agg(x::text, E''\n'') FROM (' || q || ') x' INTO r;
	RETURN coalesce(r, '<empty>');
EXCEPTION WHEN OTHERS THEN
	RETURN 'ERROR: ' || SQLERRM;
END $$;

CREATE TEMP TABLE diffs(check_name text, query text, go text, c text);

-- Source tables: row names (some NULL), categories, values of many types.
CREATE TEMP TABLE src AS
SELECT (random() * 30)::int AS g,
	CASE WHEN random() < 0.05 THEN NULL ELSE 'r' || (random() * 40)::int END AS rowid,
	CASE WHEN random() < 0.05 THEN NULL ELSE 'c' || (random() * 6)::int END AS cat,
	(random() * 1000)::int AS i4, random() * 1e6 - 5e5 AS f8, (random() * 100)::real / 7 AS f4,
	CASE WHEN random() < 0.1 THEN NULL ELSE md5(random()::text) END AS t,
	random() < 0.5 AS b, decode(md5(random()::text), 'hex') AS by,
	(random() * 1000)::numeric(10, 3) AS n, now() - random() * interval '1000 days' AS ts,
	(10 ^ (random() * 40 - 20))::float8 AS big
FROM generate_series(1, 6000);

DO $$
DECLARE
	col text;
	typ text;
	ncat int;
	q text;
	defs text;
	g int;
BEGIN
	FOR col, typ IN VALUES ('i4', 'int4'), ('f8', 'float8'), ('f4', 'real'), ('t', 'text'), ('b', 'bool'),
		('by', 'bytea'), ('n', 'numeric'), ('ts', 'timestamptz'), ('big', 'float8'), ('t', 'varchar(10)'),
		('i4', 'text') LOOP
		FOR ncat IN 1..6 LOOP
			FOR g IN 0..4 LOOP
				SELECT string_agg(', v' || k || ' ' || typ, '') INTO defs FROM generate_series(1, ncat) k;
				-- by position
				q := format('crosstab(%L) AS c(rowid text%s)',
					format('SELECT rowid, cat, %s FROM src WHERE g %% 5 = %s ORDER BY 1, 2', col, g), defs);
				INSERT INTO diffs SELECT 'crosstab', q, pg_temp.run('SELECT * FROM ' || q),
					pg_temp.run('SELECT * FROM tf_c.' || q);
				-- by categories, with an extra column
				q := format('crosstab(%L, %L) AS c(rowid text, extra int%s)',
					format('SELECT rowid, g, cat, %s FROM src WHERE g %% 5 = %s ORDER BY 1', col, g),
					format('SELECT DISTINCT cat FROM src WHERE cat IS NOT NULL ORDER BY 1 LIMIT %s', ncat), defs);
				INSERT INTO diffs SELECT 'crosstab_hash', q, pg_temp.run('SELECT * FROM ' || q),
					pg_temp.run('SELECT * FROM tf_c.' || q);
			END LOOP;
		END LOOP;
	END LOOP;
END $$;

-- Forests: text and integer keys, cycles in some.
CREATE TEMP TABLE tree_i AS
SELECT i AS id, CASE WHEN i < 5 THEN NULL ELSE (random() * (i - 1))::int END AS parent, (random() * 100)::int AS pos
FROM generate_series(1, 400) i;
INSERT INTO tree_i VALUES (1000, 1001, 0), (1001, 1002, 1), (1002, 1000, 2), (1003, 1001, 3);
CREATE TEMP TABLE tree_t AS SELECT 'k' || id AS id, 'k' || parent AS parent, pos FROM tree_i;

DO $$
DECLARE
	q text;
	tbl text;
	typ text;
	start text;
	depth int;
BEGIN
	FOR tbl, typ IN VALUES ('tree_i', 'int'), ('tree_t', 'text') LOOP
		FOR start IN SELECT unnest(ARRAY['1', '2', '3', '7', '1000', '1003', '9999']) LOOP
			IF typ = 'text' THEN start := 'k' || start; END IF;
			FOREACH depth IN ARRAY ARRAY[0, 1, 2, 5] LOOP
				FOR q IN VALUES
					(format('connectby(%L, ''id'', ''parent'', %L, %s, ''~'') AS t(id %s, parent %s, level int, branch text)', tbl, start, depth, typ, typ)),
					(format('connectby(%L, ''id'', ''parent'', %L, %s) AS t(id %s, parent %s, level int)', tbl, start, depth, typ, typ)),
					(format('connectby(%L, ''id'', ''parent'', ''pos, id'', %L, %s, ''/'') AS t(id %s, parent %s, level int, branch text, s int)', tbl, start, depth, typ, typ)),
					(format('connectby(%L, ''id'', ''parent'', ''pos'', %L, %s) AS t(id %s, parent %s, level int, s int)', tbl, start, depth, typ, typ)),
					(format('connectby(%L, ''id'', ''parent'', %L, %s, ''~'') AS t(id text, parent %s, level int, branch text)', tbl, start, depth, typ))
				LOOP
					INSERT INTO diffs SELECT 'connectby', q, pg_temp.run('SELECT * FROM ' || q),
						pg_temp.run('SELECT * FROM tf_c.' || q);
				END LOOP;
			END LOOP;
		END LOOP;
	END LOOP;
END $$;

SELECT check_name, count(*) FILTER (WHERE go IS DISTINCT FROM c) AS mismatches, count(*) AS checks,
	count(*) FILTER (WHERE go LIKE 'ERROR%') AS errors
FROM diffs GROUP BY 1 ORDER BY 1;
SELECT check_name, query, left(go, 300) AS go, left(c, 300) AS c FROM diffs WHERE go IS DISTINCT FROM c LIMIT 5;
