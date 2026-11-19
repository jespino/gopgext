CREATE EXTENSION http;
DO $$
BEGIN
	BEGIN
		PERFORM http_get('http://127.0.0.1:1/');
		RAISE 'expected an error';
	EXCEPTION WHEN raise_exception THEN
		ASSERT SQLERRM LIKE 'GET http://127.0.0.1:1/: %onnect%', SQLERRM;
	END;
	BEGIN
		PERFORM http_get('file:///etc/passwd');
		RAISE 'expected an error';
	EXCEPTION WHEN raise_exception THEN
		ASSERT SQLERRM LIKE 'GET file:///etc/passwd: %rotocol%', SQLERRM;
	END;
END $$;
