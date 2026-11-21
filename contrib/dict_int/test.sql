-- dict_int: lexemes, options (defGetString/defGetBoolean semantics),
-- errors, and use in a text search configuration. Checks raise errors.

CREATE EXTENSION IF NOT EXISTS dict_int;

DO $$
BEGIN
	-- The library is this port.
	ASSERT (SELECT prosrc FROM pg_proc WHERE proname = 'dintdict_init') = 'gopgext_tsearch_init';
	ASSERT ts_lexize('intdict', '511673') = '{511673}';
	ASSERT ts_lexize('intdict', '6532082986') = '{653208}';
	ASSERT ts_lexize('intdict', '-40865854') = '{-40865}';
	ASSERT ts_lexize('intdict', '') IS NULL;
END $$;

CREATE TEXT SEARCH DICTIONARY t_int (TEMPLATE = intdict_template, MAXLEN = 3, ABSVAL = on, REJECTLONG = 'true');
DO $$
BEGIN
	ASSERT ts_lexize('t_int', '-123') = '{123}';
	ASSERT ts_lexize('t_int', '+1234') = '{}', 'rejected: a stop word';
	ASSERT ts_lexize('t_int', '-') = '{""}';
	-- Options survive the catalog round trip (ALTER, a new session's cache).
	ASSERT (SELECT dictinitoption FROM pg_ts_dict WHERE dictname = 't_int') =
		'maxlen = 3, absval = ''on'', rejectlong = ''true''';
END $$;
ALTER TEXT SEARCH DICTIONARY t_int (MAXLEN = '2abc');
DO $$
BEGIN
	-- atoi: digits only; ALTER reloads the dictionary, keeping the other options.
	ASSERT ts_lexize('t_int', '12') = '{12}';
	ASSERT ts_lexize('t_int', '123') = '{}';
END $$;

-- Errors.
DO $$
DECLARE
	o text;
	msg text;
	state text;
BEGIN
	FOR o, msg, state IN VALUES
			('maxlen = 0', 'maxlen value has to be >= 1', '22023'),
			('absval = 2', 'absval requires a Boolean value', '42601'),
			('absval = yes', 'absval requires a Boolean value', '42601'),
			('absval', 'absval requires a parameter', '42601'), -- (PostgreSQL's)
			('bogus = 1', 'unrecognized intdict parameter: "bogus"', '22023') LOOP
		BEGIN
			EXECUTE format('CREATE TEXT SEARCH DICTIONARY t_bad (TEMPLATE = intdict_template, %s)', o);
			RAISE EXCEPTION 'no error for %', o;
		EXCEPTION WHEN OTHERS THEN
			ASSERT SQLERRM = msg AND SQLSTATE = state, format('%s: %s %s', o, SQLSTATE, SQLERRM);
		END;
	END LOOP;
END $$;

-- In a configuration: integers through intdict, the rest through simple.
CREATE TEXT SEARCH CONFIGURATION t_cfg (COPY = simple);
ALTER TEXT SEARCH CONFIGURATION t_cfg ALTER MAPPING FOR int, uint WITH t_int;
DO $$
BEGIN
	ASSERT to_tsvector('t_cfg', 'Call 1234567 or 42 now') = '''42'':4 ''call'':1 ''now'':5 ''or'':3', to_tsvector('t_cfg', 'Call 1234567 or 42 now');
	ASSERT to_tsvector('t_cfg', 'room 123456789') @@ to_tsquery('t_cfg', 'room'), 'long integers are dropped';
	ASSERT (SELECT count(*) FROM generate_series(1, 20000) i WHERE to_tsvector('t_cfg', i::text || ' x') @@ to_tsquery('t_cfg', 'x')) = 20000;
END $$;
DROP TEXT SEARCH CONFIGURATION t_cfg;
DROP TEXT SEARCH DICTIONARY t_int;
