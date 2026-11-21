-- Checks of the tablefunc port (upstream's regression test runs with
-- pg_regress, see README.md).
CREATE EXTENSION IF NOT EXISTS tablefunc;

CREATE TABLE ct(rowid text, attribute text, val float8, b bytea, f4 real);
INSERT INTO ct VALUES ('r1', 'a', 1.5, '\x01', 0.1), ('r1', 'b', 1e20, '\xff', 1e7),
	('r2', 'a', 'NaN', NULL, -0.25), ('r3', 'b', 0.0001, '\x', 3);

CREATE TABLE tree(id int, parent int, name text);
INSERT INTO tree VALUES (1, NULL, 'root'), (2, 1, 'b'), (3, 1, 'a'), (4, 2, 'c');

DO $$
DECLARE
	r record;
	n int;
	t text;
BEGIN
	SELECT count(*), avg(x) INTO r FROM normal_rand(1001, 50, 0.1) x;
	ASSERT r.count = 1001 AND abs(r.avg - 50) < 1, r::text;
	ASSERT (SELECT count(*) FROM normal_rand(0, 1, 1)) = 0;

	-- crosstab with typed columns, values as PostgreSQL formats them
	SELECT string_agg(c::text, ';' ORDER BY rowid) INTO t FROM crosstab(
		'SELECT rowid, attribute, val FROM ct ORDER BY 1, 2') AS c(rowid text, a float8, b float8);
	ASSERT t = '(r1,1.5,1e+20);(r2,NaN,);(r3,0.0001,)', t;
	SELECT string_agg(c::text, ';' ORDER BY rowid) INTO t FROM crosstab(
		'SELECT rowid, attribute, b FROM ct ORDER BY 1, 2') AS c(rowid text, a bytea, b bytea);
	ASSERT t = '(r1,"\\x01","\\xff");(r2,,);(r3,"\\x",)', t;
	SELECT string_agg(c::text, ';' ORDER BY rowid) INTO t FROM crosstab(
		'SELECT rowid, attribute, f4 FROM ct ORDER BY 1, 2', 'VALUES (''a''), (''b'')')
		AS c(rowid text, a real, b text);
	ASSERT t = '(r1,0.1,1e+07);(r2,-0.25,);(r3,,3)', t;
	SELECT string_agg(c::text, ';' ORDER BY row_name) INTO t FROM crosstab3(
		'SELECT rowid, attribute, val::text FROM ct ORDER BY 1, 2') c;
	ASSERT t = '(r1,1.5,1e+20,);(r2,NaN,,);(r3,0.0001,,)', t;

	-- column definition lists that don't match
	BEGIN
		PERFORM * FROM crosstab('SELECT rowid, attribute, val FROM ct') AS c(rowid text, a text);
		RAISE 'expected an error';
	EXCEPTION WHEN datatype_mismatch THEN
		GET STACKED DIAGNOSTICS t = PG_EXCEPTION_DETAIL;
		ASSERT t = 'Source value datatype double precision does not match return value datatype text in column 2.', t;
	END;
	BEGIN
		PERFORM crosstab('SELECT rowid, attribute, val FROM ct');
		RAISE 'expected an error';
	EXCEPTION WHEN feature_not_supported THEN
	END;
	BEGIN
		PERFORM * FROM crosstab('SELECT rowid, attribute, val FROM ct', 'VALUES (''a'')') AS c(rowid text, a varchar(2));
		RAISE 'expected an error';
	EXCEPTION WHEN string_data_right_truncation THEN
	END;

	-- connectby
	SELECT string_agg(c::text, ';' ORDER BY s) INTO t FROM connectby('tree', 'id', 'parent', 'name', '1', 0, '/')
		AS c(id int, parent int, level int, branch text, s int);
	ASSERT t = '(1,,0,1,1);(3,1,1,1/3,2);(2,1,1,1/2,3);(4,2,2,1/2/4,4)', t;
	SELECT count(*) INTO n FROM connectby('tree', 'id', 'parent', '1', 1) AS c(id int, parent int, level int);
	ASSERT n = 3, n::text;
	SELECT string_agg(c::text, ';') INTO t FROM connectby('tree', 'name', 'name', 'x''\', 0) AS c(id text, parent text, level int);
	ASSERT t = '("x''\\",,0)', t;
END $$;
