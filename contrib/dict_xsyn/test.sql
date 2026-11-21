-- dict_xsyn: synonyms with the option combinations, case folding, data
-- file errors, and use in a text search configuration. Checks raise errors.

CREATE EXTENSION IF NOT EXISTS dict_xsyn;

DO $$
BEGIN
	ASSERT (SELECT prosrc FROM pg_proc WHERE proname = 'dxsyn_init') = 'gopgext_tsearch_init';
	-- Without rules, nothing matches.
	ASSERT ts_lexize('xsyn', 'supernova') IS NULL;
END $$;

CREATE TEXT SEARCH DICTIONARY t_syn (TEMPLATE = xsyn_template, RULES = 'xsyn_sample');
DO $$
BEGIN
	ASSERT ts_lexize('t_syn', 'SuperNova') = '{supernova,sn,sne,1987a}';
	ASSERT ts_lexize('t_syn', 'sn') IS NULL;
END $$;
ALTER TEXT SEARCH DICTIONARY t_syn (RULES = 'xsyn_sample', KEEPORIG = false, MATCHSYNONYMS = true);
DO $$
BEGIN
	ASSERT ts_lexize('t_syn', 'SNE') = '{sn,sne,1987a}';
	ASSERT ts_lexize('t_syn', 'grb') IS NULL;
END $$;
ALTER TEXT SEARCH DICTIONARY t_syn (RULES = 'xsyn_sample', KEEPORIG = false, KEEPSYNONYMS = false, MATCHSYNONYMS = true);
DO $$
BEGIN
	ASSERT ts_lexize('t_syn', 'sn') = '{}', 'matched, nothing returned: a stop word';
END $$;

-- Errors.
DO $$
DECLARE
	o text;
	msg text;
	state text;
BEGIN
	FOR o, msg, state IN VALUES
			('rules = nosuch', 'could not open synonym file "%/tsearch_data/nosuch.rules": No such file or directory', 'F0000'),
			('rules = ''../x''', 'invalid text search configuration file name "../x"', '22023'),
			('keeporig = maybe', 'keeporig requires a Boolean value', '42601'),
			('bogus = 1', 'unrecognized xsyn parameter: "bogus"', '22023') LOOP
		BEGIN
			EXECUTE format('CREATE TEXT SEARCH DICTIONARY t_bad (TEMPLATE = xsyn_template, %s)', o);
			RAISE EXCEPTION 'no error for %', o;
		EXCEPTION WHEN OTHERS THEN
			ASSERT SQLERRM LIKE msg AND SQLSTATE = state, format('%s: %s %s', o, SQLSTATE, SQLERRM);
		END;
	END LOOP;
END $$;

-- In a configuration: synonyms first, then simple.
CREATE TEXT SEARCH CONFIGURATION t_cfg (COPY = simple);
ALTER TEXT SEARCH DICTIONARY t_syn (RULES = 'xsyn_sample', KEEPORIG = true, KEEPSYNONYMS = true, MATCHSYNONYMS = false);
ALTER TEXT SEARCH CONFIGURATION t_cfg ALTER MAPPING FOR asciiword, word, numword WITH t_syn, simple;
DO $$
BEGIN
	ASSERT to_tsvector('t_cfg', 'A supernova!') = '''1987a'':2 ''a'':1 ''sn'':2 ''sne'':2 ''supernova'':2', to_tsvector('t_cfg', 'A supernova!');
	ASSERT to_tsvector('t_cfg', 'the supernova 1987A') @@ to_tsquery('t_cfg', 'sn & 1987a');
END $$;
DROP TEXT SEARCH CONFIGURATION t_cfg;
DROP TEXT SEARCH DICTIONARY t_syn;
