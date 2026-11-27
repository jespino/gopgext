CREATE EXTENSION osapi;
DO $$
BEGIN
	ASSERT round_trip('hello') = 'hello!', round_trip('hello');
	ASSERT data_dir_files(ARRAY['PG_VERSION', 'postgresql.conf', 'no-such-file']) = 'PG_VERSION,postgresql.conf';
	ASSERT missing();
	ASSERT pid() = pg_backend_pid(), pid();
	ASSERT env('PATH') <> '';
	ASSERT env('GOPGEXT_OSAPI_UNSET') = '';
	ASSERT set_env('GOPGEXT_OSAPI', 'x') = 'x';
	ASSERT env('GOPGEXT_OSAPI') = 'x';
END $$;
