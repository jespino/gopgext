-- pg.UUID (uuid) and pg.Record (record, SETOF record).

CREATE EXTENSION IF NOT EXISTS types;
SET TIME ZONE 'UTC';

DO $$
DECLARE
	t text;
BEGIN
	ASSERT uuid_next(ARRAY['a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a11', '00000000-0000-0000-0000-0000000000ff']::uuid[], NULL)
		= ARRAY['a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a12', '00000000-0000-0000-0000-000000000000', NULL]::uuid[];
	ASSERT uuid_next('{}', '11111111-1111-1111-1111-111111111111') = ARRAY['11111111-1111-1111-1111-111111111111']::uuid[];
	ASSERT uuid_parse('{A0EEBC99-9C0B4EF8-BB6D6BB9-BD380A11}') = 'a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a11';
	ASSERT uuid_parse('a0eebc999c0b4ef8bb6d6bb9bd380a11') = 'a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a11';
	ASSERT (SELECT count(DISTINCT u) FROM uuid_series(1000) u) = 1000;
	ASSERT (SELECT u FROM uuid_series(300) u ORDER BY u DESC LIMIT 1) = '00000000-0000-0000-0000-00000000012b';
	BEGIN
		PERFORM uuid_parse('a0eebc99-9c0b');
		RAISE 'expected an error';
	EXCEPTION WHEN raise_exception THEN
	END;

	SELECT string_agg(d::text, ';') INTO t FROM result_columns_of(2) AS d(a text, "B" varchar(30), c name);
	ASSERT t = '(a:text,"B:character varying(30)",c:name);(a:text,"B:character varying(30)",c:name)', t;
	SELECT v::text INTO t FROM record_values(false) AS v(n int, b bool, i2 int2, i4 int4, i8 int8, i int8, f float8, f4 float4,
		s text, by bytea, num numeric, ts timestamptz, u uuid, conv int);
	ASSERT t = '(,t,7,-8,9,42,1.5,0.25,text,"\\x0102",12.50,"2023-11-14 22:13:20+00",00000000-0000-0000-0000-000000000001,17)', t;
	-- conversions through text
	SELECT v::text INTO t FROM record_values(false) AS v(n text, b text, i2 numeric, i4 text, i8 float8, i int2, f numeric,
		f4 text, s varchar(4), by bytea, num float8, ts date, u text, conv numeric(4,1));
	ASSERT t = '(,true,7,-8,9,42,1.5,0.25,text,"\\x0102",12.5,2023-11-14,00000000-0000-0000-0000-000000000001,17.0)', t;
	-- NULL records, records from OUT parameters
	ASSERT maybe_record(true) IS NULL;
	ASSERT (SELECT b FROM maybe_record(false) AS r(a int, b text)) = 'b';
END $$;


DO $$
DECLARE
	t text;
BEGIN
	BEGIN
		PERFORM * FROM record_values(true) AS v(n int, b bool, i2 int2, i4 int4, i8 int8, i int8, f float8, f4 float4,
			s text, by bytea, num numeric, ts timestamptz, u uuid, conv int);
		RAISE 'expected an error';
	EXCEPTION WHEN datatype_mismatch THEN
		GET STACKED DIAGNOSTICS t = PG_EXCEPTION_DETAIL;
		ASSERT t = 'Returned row contains 15 attributes, but query expects 14.', t;
	END;
	BEGIN
		PERFORM * FROM record_values(false) AS v(n int, b bool, i2 int2, i4 int4, i8 int8, i int8, f float8, f4 float4,
			s text, by text, num numeric, ts timestamptz, u uuid, conv int);
		RAISE 'expected an error';
	EXCEPTION WHEN datatype_mismatch THEN
		GET STACKED DIAGNOSTICS t = PG_EXCEPTION_DETAIL;
		ASSERT t = 'Returned type bytea at ordinal position 10, but query expects text.', t;
	END;
	BEGIN
		PERFORM * FROM record_values(false) AS v(n int, b bool, i2 int2, i4 int4, i8 int8, i int8, f float8, f4 float4,
			s varchar(2), by bytea, num numeric, ts timestamptz, u uuid, conv int);
		RAISE 'expected an error';
	EXCEPTION WHEN string_data_right_truncation THEN
	END;
	BEGIN
		PERFORM result_columns_of(1);
		RAISE 'expected an error';
	EXCEPTION WHEN feature_not_supported THEN
		GET STACKED DIAGNOSTICS t = MESSAGE_TEXT;
		ASSERT t = 'function returning record called in context that cannot accept type record', t;
	END;
END $$;

-- A declaration with fewer arguments than the Go function raises an error
-- instead of reading past the arguments.
CREATE FUNCTION uuid_next_short(uuid[]) RETURNS uuid[] AS '$libdir/types', 'gopgext_fn_UuidNext' LANGUAGE C;
DO $$
BEGIN
	PERFORM uuid_next_short('{}');
	RAISE 'expected an error';
EXCEPTION WHEN invalid_function_definition THEN
END $$;
DROP FUNCTION uuid_next_short(uuid[]);
