CREATE EXTENSION hello;

DO $$
BEGIN
	ASSERT hello('world') = 'Hello, world!';
	ASSERT hello(NULL) IS NULL;          -- STRICT
	ASSERT shout('hey') = 'HEY!';
	BEGIN
		PERFORM shout('');
		RAISE 'shout('''') should have failed';
	EXCEPTION WHEN invalid_parameter_value THEN
		ASSERT SQLERRM = 'nothing to shout', SQLERRM;
	END;
END $$;

-- many calls: exercises the Go heap and collector
SELECT count(*) FROM generate_series(1, 200000) i WHERE length(hello(i::text)) > 0;
