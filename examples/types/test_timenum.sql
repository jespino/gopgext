-- time.Time (timestamptz) and pg.Numeric (numeric).

CREATE EXTENSION IF NOT EXISTS types;

DO $$
BEGIN
	ASSERT add_hours('2024-01-02 03:04:05.123456+00', 1.5) = '2024-01-02 04:34:05.123456+00';
	ASSERT add_hours('1999-12-31 23:00:00+00', 2) = '2000-01-01 01:00:00+00';
	ASSERT add_hours('1900-06-01 00:00:00.5+00', -24) = '1900-05-31 00:00:00.5+00';
	ASSERT pg_typeof(add_hours(now(), 0))::text = 'timestamp with time zone';
	ASSERT latest(ARRAY['2020-01-01', '2024-05-05', '2010-01-01']::timestamptz[]) = '2024-05-05';
	ASSERT latest('{}') IS NULL;
	ASSERT numeric_sum(ARRAY[0.1, 0.2, NULL, 12345678901234567890.5]) = 12345678901234567890.8;
	ASSERT pg_typeof(numeric_sum('{}'))::text = 'numeric';
	ASSERT numeric_sum(ARRAY['-3'::numeric]) = -3;
END $$;

DO $$
BEGIN
	PERFORM add_hours('infinity', 1);
	RAISE 'expected an error';
EXCEPTION WHEN datetime_field_overflow THEN
END $$;

DO $$
BEGIN
	PERFORM bad_numeric();
	RAISE 'expected an error';
EXCEPTION WHEN invalid_text_representation THEN
END $$;
