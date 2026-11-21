CREATE EXTENSION IF NOT EXISTS types;

DO $$
DECLARE
	hint text;
BEGIN
	ASSERT add_ints(1::smallint, 2, 3) = 6;
	ASSERT add_ints((-32768)::smallint, 2147483647, 9223372036854775807 - 2147483647 + 32768) = 9223372036854775807;
	ASSERT hypot(3, 4) = 5;
	ASSERT negate(true) = false;
	ASSERT reverse('héllo') = 'olléh';
	ASSERT reverse('') = '';
	ASSERT reverse(repeat('ab', 500000)) = repeat('ba', 500000);   -- toasted input
	ASSERT xor_bytes('\x0102ff'::bytea, 1) = '\x0003fe'::bytea;
	ASSERT coalesce2(NULL, 5) = 5;
	ASSERT coalesce2(NULL, NULL) IS NULL;
	ASSERT describe(NULL, 1) = 'NULL/1';
	ASSERT describe('x', 2) = '"x"/2';
	ASSERT sum_array(ARRAY[1.5, 2.5, 3]) = 7;
	ASSERT sum_array(ARRAY[[1, 2], [3, 4]]::float8[]) = 10;
	ASSERT sum_array('{}') = 0;
	ASSERT split_words('  a bb  ccc ') = ARRAY['a', 'bb', 'ccc'];
	ASSERT split_words('') = '{}'::text[];
	ASSERT squares(ARRAY[2, -3, 65536]) = ARRAY[4, 9, 4294967296]::bigint[];
	ASSERT (SELECT array_agg(f) FROM fib(10) f) = ARRAY[0, 1, 1, 2, 3, 5, 8, 13, 21, 34]::bigint[];
	ASSERT (SELECT count(*) FROM fib(0)) = 0;
	ASSERT (SELECT array_agg(w) FROM fib(5) AS f, words('x y') AS w) IS NOT NULL;
	ASSERT (SELECT string_agg(w, ',') FROM words(' one two  three ') w) = 'one,two,three';
	ASSERT (SELECT sum(x) FROM (SELECT fib(5) AS x) s) = 7;    -- in the select list
	ASSERT safe_divide(7, 2) = 3;
	PERFORM nothing();
	ASSERT recovers() = 'recovered: boom';
	ASSERT allocate(64) > 0;

	-- NULL in a non-NULL-able parameter of a non-STRICT function
	BEGIN
		PERFORM describe('x', NULL);
		RAISE 'expected an error';
	EXCEPTION WHEN null_value_not_allowed THEN
		ASSERT SQLERRM = 'argument "n" must not be NULL', SQLERRM;
	END;
	BEGIN
		PERFORM sum_array(ARRAY[1, NULL]);
		RAISE 'expected an error';
	EXCEPTION WHEN null_value_not_allowed THEN
	END;
	BEGIN
		PERFORM safe_divide(1, 0);
		RAISE 'expected an error';
	EXCEPTION WHEN division_by_zero THEN
		GET STACKED DIAGNOSTICS hint = PG_EXCEPTION_HINT;
		ASSERT hint = 'Use a non-zero divisor.', hint;
	END;
	BEGIN
		PERFORM fail('reason');
		RAISE 'expected an error';
	EXCEPTION WHEN raise_exception THEN
		ASSERT SQLERRM = 'failed: reason', SQLERRM;
	END;
	BEGIN
		PERFORM count(*) FROM words('');
		RAISE 'expected an error';
	EXCEPTION WHEN raise_exception THEN
		ASSERT SQLERRM = 'empty input', SQLERRM;
	END;
	BEGIN
		PERFORM panics(5);
		RAISE 'expected an error';
	EXCEPTION WHEN internal_error THEN
		ASSERT SQLERRM LIKE 'Go panic: %index out of range%', SQLERRM;
	END;
	BEGIN
		PERFORM panics_nil();
		RAISE 'expected an error';
	EXCEPTION WHEN internal_error THEN
		ASSERT SQLERRM LIKE 'Go panic: %nil pointer%', SQLERRM;
	END;
	ASSERT channels(100) = 4950;
	BEGIN
		PERFORM deadlock();
		RAISE 'expected an error';
	EXCEPTION WHEN internal_error THEN
		GET STACKED DIAGNOSTICS hint = PG_EXCEPTION_HINT;
		ASSERT hint LIKE 'Extensions run Go without goroutines%', hint;
	END;
	-- the backend is still healthy after panics
	ASSERT panics(2) = 3;
END $$;

-- the GC under sustained load: 64 MiB of garbage, 20 times
SELECT sum(allocate(64)) > 0 AS ok FROM generate_series(1, 20);
