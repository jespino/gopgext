-- gopgext/tsearch: a Go parser (tokens, token types, headlines), a Go
-- dictionary template (options, a data file, variants, prefix lexemes,
-- stop words), calling dictionaries from Go, state lifetime.

CREATE EXTENSION IF NOT EXISTS textsearch;

DO $$
DECLARE
	n int;
BEGIN
	ASSERT (SELECT string_agg(alias || ':' || description, ',' ORDER BY tokid) FROM ts_token_type('tagprs')) =
		'word:Word,number:Number,tag:Hash tag,blank:Space symbols';
	ASSERT (SELECT string_agg(tokid || '=' || token, '|') FROM ts_parse('tagprs', 'Héllo #pg18 world, 42!')) =
		'1=Héllo|4= |3=#pg18|4= |1=world|4=, |2=42|4=!', (SELECT string_agg(tokid || '=' || token, '|') FROM ts_parse('tagprs', 'Héllo #pg18 world, 42!'));
	ASSERT (SELECT count(*) FROM ts_parse('tagprs', '')) = 0;

	-- Variants: the compound and its parts.
	ASSERT (SELECT array_agg(lexeme || '/' || variant ORDER BY lexeme) FROM (
		SELECT * FROM lexemes('compounds'::regdictionary, 'FootBall')) l) = '{ball/2,foot/2,football/1}';
	ASSERT ts_lexize('compounds', 'Doorbell') = '{doorbell,door,bell}';
	ASSERT ts_lexize('compounds', 'stopper') = '{}';
	ASSERT ts_lexize('compounds', 'other') IS NULL;
	ASSERT (SELECT count(*) FROM lexemes('compounds'::regdictionary, 'other')) = 0;
	ASSERT (SELECT array_agg(lexeme) FROM lexemes('simple'::regdictionary, 'Word')) = '{word}', 'any dictionary, from Go';

	ASSERT to_tsvector('tags', 'Sunflower seeds #garden 2024') =
		'''#garden'':3 ''2024'':4 ''flower'':1 ''seeds'':2 ''sun'':1 ''sunflower'':1', to_tsvector('tags', 'Sunflower seeds #garden 2024');
	ASSERT to_tsvector('tags', 'a sunflower') @@ to_tsquery('tags', 'flower');
	ASSERT to_tsvector('tags', 'stop it') = '''it'':2';
	ASSERT ts_headline('tags', 'The big doorbell rang', to_tsquery('tags', 'bell')) = 'The big <b>doorbell</b> rang';
	ASSERT char_lengths('aé€😀') = '{1,2,3,4}';

	-- Many parses: their states are dropped.
	n := live_states();
	PERFORM to_tsvector('tags', 'word ' || i) FROM generate_series(1, 5000) i;
	ASSERT live_states() <= n + 1, format('%s states alive, had %s', live_states(), n);
END $$;

-- Prefix lexemes; ALTER reloads a dictionary, and the old state is dropped.
DO $$
DECLARE
	n int;
BEGIN
	n := live_states();
	ALTER TEXT SEARCH DICTIONARY compounds (FILE = textsearch_compounds, PREFIX = true);
	ASSERT (SELECT array_agg(flags) FROM lexemes('compounds'::regdictionary, 'football')) = '{2,2,2}';
	ASSERT to_tsquery('tags', 'football') = '''football'':* | ''foot'':* & ''ball'':*', to_tsquery('tags', 'football')::text;
	ASSERT live_states() = n, format('%s states alive, had %s', live_states(), n);
END $$;
ALTER TEXT SEARCH DICTIONARY compounds (FILE = textsearch_compounds, PREFIX = false);

-- Errors: options, files, an init that isn't registered.
DO $$
BEGIN
	BEGIN
		CREATE TEXT SEARCH DICTIONARY bad (TEMPLATE = compound, FILE = nosuch);
		RAISE EXCEPTION 'no error';
	EXCEPTION WHEN OTHERS THEN
		ASSERT SQLERRM LIKE 'could not open text search data file "%/tsearch_data/nosuch.txt": No such file or directory', SQLERRM;
	END;
	BEGIN
		CREATE TEXT SEARCH DICTIONARY bad (TEMPLATE = compound, FILE = textsearch_compounds, PREFIX = maybe);
		RAISE EXCEPTION 'no error';
	EXCEPTION WHEN syntax_error THEN
		ASSERT SQLERRM = 'prefix requires a Boolean value', SQLERRM;
	END;
	CREATE FUNCTION unregistered_init(internal) RETURNS internal
		AS '$libdir/textsearch', 'gopgext_tsearch_init' LANGUAGE C STRICT;
	CREATE TEXT SEARCH TEMPLATE unregistered (INIT = unregistered_init, LEXIZE = compound_lexize);
	BEGIN
		CREATE TEXT SEARCH DICTIONARY bad (TEMPLATE = unregistered);
		RAISE EXCEPTION 'no error';
	EXCEPTION WHEN object_not_in_prerequisite_state THEN
		ASSERT SQLERRM = 'no text search dictionary template "unregistered_init"', SQLERRM;
	END;
	-- A lexize function given another template's data.
	CREATE TEXT SEARCH TEMPLATE foreign_data (INIT = dsimple_init, LEXIZE = compound_lexize);
	CREATE TEXT SEARCH DICTIONARY foreign_dict (TEMPLATE = foreign_data);
	BEGIN
		PERFORM ts_lexize('foreign_dict', 'x');
		RAISE EXCEPTION 'no error';
	EXCEPTION WHEN datatype_mismatch THEN
		ASSERT SQLERRM = 'text search dictionary data was not created by gopgext/tsearch', SQLERRM;
	END;
	DROP TEXT SEARCH DICTIONARY foreign_dict;
	DROP TEXT SEARCH TEMPLATE foreign_data;
	DROP TEXT SEARCH TEMPLATE unregistered;
	DROP FUNCTION unregistered_init(internal);
END $$;
