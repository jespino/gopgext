-- Composite types, arrays of extension-defined types and polymorphic
-- functions.

CREATE EXTENSION IF NOT EXISTS types;

DO $$
DECLARE
	r reading;
BEGIN
	-- Arrays of an extension-defined type: arguments, results, NULLs.
	ASSERT '21.5C'::celsius::text = '21.5C';
	ASSERT warmest(ARRAY['1C', '30C', '-4C']::celsius[])::text = '30C';
	ASSERT warmest('{}'::celsius[]) IS NULL;
	ASSERT warmer(ARRAY['1C', NULL, '2.5C']::celsius[], 1)::text = '{2C,NULL,3.5C}';
	ASSERT maybe_temps(3)::text = '{0C,0.5C,1C}';
	ASSERT maybe_temps(-1) IS NULL;
	ASSERT pg_typeof(maybe_temps(1))::text = 'celsius[]';

	-- Composite arguments and results.
	ASSERT describe_reading(ROW('a', ARRAY['1C', '3C']::celsius[], 'ok', ARRAY['x', 'y'])::reading) = 'a: 2 temps, max 3C, ok [x,y]';
	ASSERT describe_reading(ROW('b', '{}', NULL, NULL)::reading) = 'b: 0 temps';
	r := calibrate(ROW('c', ARRAY['1C']::celsius[], NULL, ARRAY['t'])::reading, 0.5);
	ASSERT r.sensor = 'c' AND r.temps::text = '{1.5C}' AND r.note = 'calibrated' AND r.labels = '{t}', r::text;
	ASSERT note_of(NULL) IS NULL;
	ASSERT note_of(ROW('d', '{}', 'n', NULL)::reading) = 'n';
	ASSERT (SELECT string_agg(sensor || ':' || temps::text, ';') FROM readings(3)) = 's0:{0C,1C};s1:{1C,2C};s2:{2C,3C}';
	ASSERT (SELECT count(*) FROM readings(0)) = 0;
	-- A table of the composite type: its rows are composite arguments.
	CREATE TEMP TABLE rs OF reading;
	INSERT INTO rs VALUES ('e', '{5C}', NULL, NULL);
	ASSERT (SELECT describe_reading(rs) FROM rs) = 'e: 1 temps, max 5C';
END $$;

-- NULL in a non-pointer column of a composite argument.
DO $$
BEGIN
	PERFORM describe_reading(ROW(NULL, '{}', NULL, NULL)::reading);
	RAISE 'expected an error';
EXCEPTION WHEN null_value_not_allowed THEN
	ASSERT SQLERRM = 'column "sensor" of a composite argument must not be NULL', SQLERRM;
END $$;

-- Polymorphic functions.
DO $$
BEGIN
	ASSERT first_non_null(ARRAY[NULL, 3, 4]) = 3;
	ASSERT first_non_null(ARRAY[NULL, 'x', 'y']) = 'x';
	ASSERT first_non_null(ARRAY[NULL, NULL]::int[]) IS NULL;
	ASSERT type_oid(1) = 'integer'::regtype AND type_oid('x'::text) = 'text'::regtype;
	ASSERT type_oid('1C'::celsius) = 'celsius'::regtype;
	ASSERT as_text(42) = '42' AND as_text(now()::date - now()::date) = '0';
	ASSERT as_text('2024-01-02'::date) = '2024-01-02';
	ASSERT as_text(ROW('f', '{}', NULL, NULL)::reading) = '(f,{},,)';
	ASSERT array_text(ARRAY[1.5, 2]) = '{1.5,2}';
	ASSERT twice(7) = ARRAY[7, NULL, 7];
	ASSERT twice('a'::text) = ARRAY['a', NULL, 'a'];
	ASSERT pg_typeof(twice('1C'::celsius))::text = 'celsius[]';
	ASSERT parse_as(0, ' 42 ') = 42;
	ASSERT parse_as(0::numeric, '1.25') = 1.25;
	ASSERT (SELECT array_agg(e) FROM elements(ARRAY[1, NULL, 3]) e) = '{1,3}';
	ASSERT (SELECT array_agg(e) FROM elements(ARRAY['a', 'b']) e) = '{a,b}';
	ASSERT pick(true, 1, 2) = 1 AND pick(false, 1, 2) = 2 AND pick(true, NULL, 2) IS NULL;
END $$;

-- An input error of ParseAny is the type's own error.
DO $$
BEGIN
	PERFORM parse_as(0, 'x');
	RAISE 'expected an error';
EXCEPTION WHEN invalid_text_representation THEN
	ASSERT SQLERRM = 'invalid input syntax for type integer: "x"', SQLERRM;
END $$;
