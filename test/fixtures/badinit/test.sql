DO $$
BEGIN
	CREATE EXTENSION badinit;
	RAISE 'expected an error';
EXCEPTION WHEN internal_error THEN
	ASSERT SQLERRM = 'Go panic: bad configuration', SQLERRM;
END $$;
DO $$
BEGIN
	CREATE EXTENSION badinit;
	RAISE 'expected an error';
EXCEPTION WHEN object_not_in_prerequisite_state THEN
	ASSERT SQLERRM LIKE '%failed to load earlier%', SQLERRM;
END $$;
